package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	defaultImageMaxDownloadBytes int64 = 32 << 20 // 32 MiB
	maxImageDownloadRedirects          = 5
)

// ImageStorage 把图片字节写入对象存储并返回可访问 URL。
//
// 这是对象存储的可插拔抽象：适配一个新的对象存储厂商，只需实现本接口
// （例如包一个厂商 SDK），无需改动任务/网关逻辑。仓库内自带一个 S3 兼容实现
// （repository.S3ImageStorage），适用于 AWS S3 / Cloudflare R2 / 阿里云 OSS / MinIO 等。
type ImageStorage interface {
	// Save 把 data 以 key 存入对象存储，返回可下载的 URL（公开直链或 presigned 临时链接）。
	// contentType 为图片 MIME 类型，如 "image/png"。
	// 实现必须响应 ctx 取消；handler 的 watchdog 只负责隔离异常实现，无法强制回收阻塞调用。
	Save(ctx context.Context, key, contentType string, data []byte) (url string, err error)
	// Delete 删除没有被任务终态引用的对象，用于失败补偿。
	Delete(ctx context.Context, key string) error
}

const imageStorageCleanupTimeout = 15 * time.Second

type imageRewriteResult struct {
	payload json.RawMessage
	keys    []string
	active  bool
}

type imagePendingObjectTracker func(keys []string) (active bool, err error)

// ImageResultUploader 是 ImageStorage 的上层编排器（与具体厂商无关）：
// 把上游生图响应里的每张图片（b64_json 解码 / url 下载）转存到对象存储，
// 并把响应结果改写为只含短链接的紧凑 JSON，从而避免大 base64 落 Redis。
type ImageResultUploader struct {
	storage          ImageStorage
	httpClient       *http.Client
	prefix           string
	maxDownloadBytes int64
}

// NewImageResultUploader 构造一个 uploader；storage 为 nil 时 Rewrite 直接透传。
func NewImageResultUploader(storage ImageStorage, prefix string, maxDownloadBytes int64, httpClient *http.Client) *ImageResultUploader {
	if httpClient == nil {
		httpClient = defaultImageDownloadHTTPClient()
	}
	if maxDownloadBytes <= 0 {
		maxDownloadBytes = defaultImageMaxDownloadBytes
	}
	return &ImageResultUploader{
		storage:          storage,
		httpClient:       httpClient,
		prefix:           prefix,
		maxDownloadBytes: maxDownloadBytes,
	}
}

func defaultImageDownloadHTTPClient() *http.Client {
	return imageDownloadHTTPClientFrom(http.DefaultTransport)
}

func imageDownloadHTTPClientFrom(base http.RoundTripper) *http.Client {
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	transport := imageDownloadTransportFrom(base)
	// 图片地址来自不受信任的上游响应。禁用环境代理，并在真正建连前解析、
	// 校验并固定公网 IP，避免 DNS rebinding 绕过 URL 层检查。
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("parse image download address: %w", err)
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("resolve image download host: %w", err)
		}
		if len(ips) == 0 {
			return nil, errors.New("image download host has no addresses")
		}
		for _, ip := range ips {
			if !isPublicImageDownloadIP(ip) {
				return nil, fmt.Errorf("image download host resolves to a non-public address: %s", ip)
			}
		}
		var dialErr error
		for _, ip := range ips {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			dialErr = err
		}
		return nil, fmt.Errorf("connect to image download host: %w", dialErr)
	}
	return &http.Client{
		Transport: transport,
		Timeout:   60 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxImageDownloadRedirects {
				return errors.New("too many image download redirects")
			}
			return validateImageDownloadURL(req.URL)
		},
	}
}

func imageDownloadTransportFrom(base http.RoundTripper) *http.Transport {
	if transport, ok := base.(*http.Transport); ok && transport != nil {
		return transport.Clone()
	}
	// Keep a complete, explicit fallback instead of inheriting an unknown
	// RoundTripper. The caller still installs the SSRF-validating DialContext
	// and clears Proxy before this transport can be used.
	return &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}

// Rewrite 将 result（上游生图响应 JSON）里的每张图片转存到对象存储，
// 返回改写后的紧凑结果（data[i].url 指向对象存储，b64_json 被移除）。
// 任一图片转存失败即返回 error（调用方据此将任务标记为失败，绝不把大 blob 落 Redis）。
func (u *ImageResultUploader) Rewrite(ctx context.Context, taskID string, result json.RawMessage) (json.RawMessage, error) {
	rewritten, err := u.rewriteTracked(ctx, taskID, result, nil)
	if err != nil {
		return nil, err
	}
	return rewritten.payload, nil
}

func (u *ImageResultUploader) rewriteTracked(ctx context.Context, taskID string, result json.RawMessage, track imagePendingObjectTracker) (_ *imageRewriteResult, retErr error) {
	if u == nil || u.storage == nil {
		return &imageRewriteResult{payload: result, active: true}, nil
	}
	savedKeys := make([]string, 0)
	defer func() {
		if retErr == nil || len(savedKeys) == 0 || track != nil {
			return
		}
		if cleanupErr := u.cleanup(savedKeys); cleanupErr != nil {
			retErr = errors.Join(retErr, cleanupErr)
		}
	}()
	var top map[string]json.RawMessage
	if err := json.Unmarshal(result, &top); err != nil {
		return nil, fmt.Errorf("parse image response: %w", err)
	}
	rawData, ok := top["data"]
	if !ok {
		// 没有 data 数组（结构不符合预期），保持原样返回，交由上层决定。
		return &imageRewriteResult{payload: result, active: true}, nil
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(rawData, &items); err != nil {
		return nil, fmt.Errorf("parse image response data: %w", err)
	}
	if len(items) == 0 {
		return &imageRewriteResult{payload: result, active: true}, nil
	}
	type preparedImage struct {
		data        []byte
		contentType string
		key         string
	}
	prepared := make([]preparedImage, len(items))
	keys := make([]string, len(items))
	attemptID := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	for i, item := range items {
		data, contentType, err := u.fetchImageBytes(ctx, item)
		if err != nil {
			return nil, fmt.Errorf("image %d: %w", i, err)
		}
		key := u.buildKey(taskID+"-"+attemptID, i, contentType)
		prepared[i] = preparedImage{data: data, contentType: contentType, key: key}
		keys[i] = key
	}
	// Persist the complete object manifest before the first upload. This closes
	// the crash window where an object could exist without a durable cleanup key.
	if track != nil {
		active, err := track(append([]string(nil), keys...))
		if err != nil {
			return nil, fmt.Errorf("track pending image objects: %w", err)
		}
		if !active {
			return &imageRewriteResult{payload: result}, nil
		}
	}
	savedKeys = append(savedKeys, keys...)
	for i, item := range items {
		image := prepared[i]
		url, err := u.storage.Save(ctx, image.key, image.contentType, image.data)
		if err != nil {
			return nil, fmt.Errorf("image %d: upload to object storage: %w", i, err)
		}
		urlRaw, err := json.Marshal(url)
		if err != nil {
			return nil, fmt.Errorf("image %d: encode url: %w", i, err)
		}
		item["url"] = urlRaw
		delete(item, "b64_json")
		items[i] = item
	}
	newData, err := json.Marshal(items)
	if err != nil {
		return nil, fmt.Errorf("encode image response data: %w", err)
	}
	top["data"] = newData
	out, err := json.Marshal(top)
	if err != nil {
		return nil, fmt.Errorf("encode image response: %w", err)
	}
	return &imageRewriteResult{payload: out, keys: savedKeys, active: true}, nil
}

func (u *ImageResultUploader) cleanup(keys []string) error {
	if u == nil || u.storage == nil || len(keys) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), imageStorageCleanupTimeout)
	defer cancel()
	var cleanupErr error
	for _, key := range keys {
		if err := u.storage.Delete(ctx, key); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("delete image object %q: %w", key, err))
		}
	}
	return cleanupErr
}

func (u *ImageResultUploader) fetchImageBytes(ctx context.Context, item map[string]json.RawMessage) ([]byte, string, error) {
	if raw, ok := item["b64_json"]; ok {
		var b64 string
		if err := json.Unmarshal(raw, &b64); err == nil {
			if b64 = strings.TrimSpace(b64); b64 != "" {
				data, err := base64.StdEncoding.DecodeString(b64)
				if err != nil {
					return nil, "", fmt.Errorf("decode b64_json: %w", err)
				}
				contentType, ok := detectImageContentType(data)
				if !ok {
					return nil, "", errors.New("b64_json is not a supported image")
				}
				return data, contentType, nil
			}
		}
	}
	if raw, ok := item["url"]; ok {
		var rawURL string
		if err := json.Unmarshal(raw, &rawURL); err == nil {
			if rawURL = strings.TrimSpace(rawURL); rawURL != "" {
				return u.download(ctx, rawURL)
			}
		}
	}
	return nil, "", errors.New("image item has neither b64_json nor url")
}

func (u *ImageResultUploader) download(ctx context.Context, rawURL string) ([]byte, string, error) {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return nil, "", fmt.Errorf("parse download URL: %w", err)
	}
	if err := validateImageDownloadURL(parsedURL); err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return nil, "", fmt.Errorf("build download request: %w", err)
	}
	resp, err := u.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download image: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.Request == nil || resp.Request.URL == nil {
		return nil, "", errors.New("download image: response URL is missing")
	}
	if err := validateImageDownloadURL(resp.Request.URL); err != nil {
		return nil, "", err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, "", fmt.Errorf("download image: unexpected status %d", resp.StatusCode)
	}
	limit := u.maxDownloadBytes
	if limit <= 0 {
		limit = defaultImageMaxDownloadBytes
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, "", fmt.Errorf("read image body: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, "", fmt.Errorf("downloaded image exceeds %d bytes", limit)
	}
	declaredContentType := normalizeImageContentType(resp.Header.Get("Content-Type"))
	if declaredContentType != "" && declaredContentType != "application/octet-stream" && !isSupportedImageContentType(declaredContentType) {
		return nil, "", fmt.Errorf("download image: unsupported content type %q", declaredContentType)
	}
	detectedContentType, ok := detectImageContentType(data)
	if !ok {
		return nil, "", errors.New("download image: response body is not a supported image")
	}
	return data, detectedContentType, nil
}

func (u *ImageResultUploader) buildKey(taskID string, index int, contentType string) string {
	return u.prefix + taskID + "-" + strconv.Itoa(index) + extensionForContentType(contentType)
}

func detectImageContentType(data []byte) (string, bool) {
	ct := normalizeImageContentType(http.DetectContentType(data))
	return ct, isSupportedImageContentType(ct)
}

func normalizeImageContentType(contentType string) string {
	return strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
}

func isSupportedImageContentType(contentType string) bool {
	switch normalizeImageContentType(contentType) {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
		return true
	default:
		return false
	}
}

func validateImageDownloadURL(parsed *url.URL) error {
	if parsed == nil {
		return errors.New("image download URL is missing")
	}
	if !strings.EqualFold(strings.TrimSpace(parsed.Scheme), "https") {
		return errors.New("image download URL must use HTTPS")
	}
	if parsed.User != nil {
		return errors.New("image download URL must not contain user info")
	}
	host := strings.TrimSpace(parsed.Hostname())
	if host == "" {
		return errors.New("image download URL host is missing")
	}
	if ip, err := netip.ParseAddr(strings.TrimSuffix(host, ".")); err == nil && !isPublicImageDownloadIP(ip) {
		return fmt.Errorf("image download URL uses a non-public address: %s", ip)
	}
	return nil
}

func isPublicImageDownloadIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	// CGNAT 与基准测试网段不属于 netip.IsPrivate，但可能被基础设施用于
	// 内部服务；下载器同样禁止访问。
	for _, prefix := range []netip.Prefix{
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("198.18.0.0/15"),
	} {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func extensionForContentType(ct string) string {
	switch {
	case strings.Contains(ct, "png"):
		return ".png"
	case strings.Contains(ct, "jpeg"), strings.Contains(ct, "jpg"):
		return ".jpg"
	case strings.Contains(ct, "webp"):
		return ".webp"
	case strings.Contains(ct, "gif"):
		return ".gif"
	default:
		return ".png"
	}
}
