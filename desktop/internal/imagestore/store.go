// Package imagestore manages durable image files independently from the
// browser Cache Storage used by the web console.
package imagestore

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	_ "golang.org/x/image/webp"
)

// ErrAssetNotFound is returned by scoped access when an id is missing or
// belongs to another account. Keeping one not-found shape prevents the native
// binding from becoming an account-existence oracle.
var ErrAssetNotFound = errors.New("image asset not found")

const (
	DefaultMaxBytes        int64 = 32 << 20
	defaultDownloadLimit         = DefaultMaxBytes
	defaultDownloadTimeout       = 60 * time.Second
	maxDownloadRedirects         = 5
	maxDownloadURLLength         = 16 << 10
	maxImageDimension            = 16384
	maxImagePixels         int64 = 100_000_000
)

type AssetMetadata struct {
	Name     string
	MimeType string
}

type Asset struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	MimeType string `json:"mime_type"`
	Bytes    int64  `json:"bytes"`
	SHA256   string `json:"sha256"`
	// OwnerHash is an opaque local account partition. It is kept out of the
	// Wails/JSON DTO because the renderer only needs display metadata.
	OwnerHash string `json:"-"`
	// Path is intentionally excluded from JSON/Wails serialization. It is an
	// absolute local filesystem path and must stay inside the trusted Go side of
	// the desktop app; the renderer receives only opaque asset metadata.
	Path      string    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
}

// assetDiskMetadata is the private on-disk representation. Keep Path here so
// existing metadata files remain readable without ever putting the path on the
// Wails bridge through Asset's JSON representation.
type assetDiskMetadata struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	MimeType  string    `json:"mime_type"`
	Bytes     int64     `json:"bytes"`
	SHA256    string    `json:"sha256"`
	OwnerHash string    `json:"owner_hash,omitempty"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"created_at"`
}

func newAssetDiskMetadata(asset Asset) assetDiskMetadata {
	return assetDiskMetadata{
		ID: asset.ID, Name: asset.Name, MimeType: asset.MimeType, Bytes: asset.Bytes,
		SHA256: asset.SHA256, OwnerHash: asset.OwnerHash, Path: asset.Path, CreatedAt: asset.CreatedAt,
	}
}

func (metadata assetDiskMetadata) asset() Asset {
	return Asset{
		ID: metadata.ID, Name: metadata.Name, MimeType: metadata.MimeType, Bytes: metadata.Bytes,
		SHA256: metadata.SHA256, OwnerHash: metadata.OwnerHash, Path: metadata.Path, CreatedAt: metadata.CreatedAt,
	}
}

type Store interface {
	Save(ctx context.Context, source io.Reader, metadata AssetMetadata) (Asset, error)
	Download(ctx context.Context, sourceURL string, headers map[string]string, metadata AssetMetadata) (Asset, error)
	Open(ctx context.Context, id string) (io.ReadCloser, Asset, error)
	List(ctx context.Context) ([]Asset, error)
	Delete(ctx context.Context, id string) error
}

// ScopedStore is the account-isolated image-store surface used by the desktop
// application. The legacy Store methods remain available for migration and
// package compatibility, but they must not be used for current-account UI
// operations because they include unowned/other-account assets.
type ScopedStore interface {
	Store
	SaveForOwner(ctx context.Context, ownerHash string, source io.Reader, metadata AssetMetadata) (Asset, error)
	DownloadForOwner(ctx context.Context, ownerHash, sourceURL string, headers map[string]string, metadata AssetMetadata) (Asset, error)
	SaveDataURLForOwner(ctx context.Context, ownerHash, dataURL string, metadata AssetMetadata) (Asset, error)
	OpenForOwner(ctx context.Context, ownerHash, id string) (io.ReadCloser, Asset, error)
	ListForOwner(ctx context.Context, ownerHash string) ([]Asset, error)
	DeleteForOwner(ctx context.Context, ownerHash, id string) error
}

type FileStore struct {
	root       string
	maxBytes   int64
	httpClient *http.Client
	mu         sync.Mutex
}

var _ ScopedStore = (*FileStore)(nil)

func NewFileStore(root string, maxBytes int64) (*FileStore, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("image store directory is required")
	}
	root, err := cleanStoreRoot(root)
	if err != nil {
		return nil, err
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	// Create and validate the complete private directory tree up front.  Using
	// MkdirAll directly would follow a user-controlled symlink in an
	// intermediate component and could redirect generated images and metadata
	// outside the application data directory.
	if err := ensureSafeStoreRoot(root); err != nil {
		return nil, fmt.Errorf("initialize image store: %w", err)
	}
	return &FileStore{root: root, maxBytes: maxBytes}, nil
}

func (s *FileStore) Save(ctx context.Context, source io.Reader, metadata AssetMetadata) (Asset, error) {
	return s.save(ctx, "", source, metadata)
}

// SaveForOwner stores an image in the supplied opaque account partition. The
// owner is validated before any bytes are written; an empty or malformed hash
// cannot silently fall back to the shared legacy namespace.
func (s *FileStore) SaveForOwner(ctx context.Context, ownerHash string, source io.Reader, metadata AssetMetadata) (Asset, error) {
	ownerHash, err := normalizeOwnerHash(ownerHash)
	if err != nil {
		return Asset{}, err
	}
	return s.save(ctx, ownerHash, source, metadata)
}

func (s *FileStore) save(ctx context.Context, ownerHash string, source io.Reader, metadata AssetMetadata) (Asset, error) {
	if s == nil {
		return Asset{}, errors.New("image store is unavailable")
	}
	if source == nil {
		return Asset{}, errors.New("image source is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	name := safeName(metadata.Name)
	mimeType := strings.TrimSpace(metadata.MimeType)
	if mimeType == "" {
		mimeType = mime.TypeByExtension(filepath.Ext(name))
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	id, err := randomID()
	if err != nil {
		return Asset{}, err
	}
	ext := filepath.Ext(name)
	if ext == "" {
		ext = extensionForMime(mimeType)
	}
	assetPath := filepath.Join(s.root, id+ext)
	metaPath := filepath.Join(s.root, id+".json")

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ensureSafeStoreRoot(s.root); err != nil {
		return Asset{}, fmt.Errorf("validate image store: %w", err)
	}
	tmp, err := os.CreateTemp(s.root, ".asset-*.tmp")
	if err != nil {
		return Asset{}, fmt.Errorf("create image temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return Asset{}, fmt.Errorf("set image permissions: %w", err)
	}
	hasher := sha256.New()
	limited := io.LimitReader(contextReader{ctx: ctx, reader: source}, s.maxBytesLimit()+1)
	bytesWritten, err := io.Copy(io.MultiWriter(tmp, hasher), limited)
	if err != nil {
		_ = tmp.Close()
		return Asset{}, fmt.Errorf("write image: %w", err)
	}
	if bytesWritten > s.maxBytesLimit() {
		_ = tmp.Close()
		return Asset{}, fmt.Errorf("image exceeds %d byte limit", s.maxBytesLimit())
	}
	if err := contextErr(ctx); err != nil {
		_ = tmp.Close()
		return Asset{}, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return Asset{}, fmt.Errorf("sync image: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return Asset{}, fmt.Errorf("close image: %w", err)
	}
	if _, err := inspectStoreTarget(assetPath, true); err != nil {
		return Asset{}, fmt.Errorf("validate image destination: %w", err)
	}
	if err := os.Rename(tmpName, assetPath); err != nil {
		return Asset{}, fmt.Errorf("commit image: %w", err)
	}

	asset := Asset{
		ID:        id,
		Name:      name,
		MimeType:  mimeType,
		Bytes:     bytesWritten,
		SHA256:    hex.EncodeToString(hasher.Sum(nil)),
		OwnerHash: ownerHash,
		Path:      assetPath,
		CreatedAt: time.Now().UTC(),
	}
	metaBytes, err := json.MarshalIndent(newAssetDiskMetadata(asset), "", "  ")
	if err != nil {
		_ = os.Remove(assetPath)
		return Asset{}, fmt.Errorf("encode image metadata: %w", err)
	}
	if err := writeAtomicFile(metaPath, append(metaBytes, '\n'), 0o600); err != nil {
		_ = os.Remove(assetPath)
		return Asset{}, fmt.Errorf("write image metadata: %w", err)
	}
	return asset, nil
}

func (s *FileStore) Download(ctx context.Context, sourceURL string, headers map[string]string, metadata AssetMetadata) (Asset, error) {
	return s.download(ctx, "", sourceURL, headers, metadata)
}

// DownloadForOwner validates and stores a remote image in one account
// partition. URL validation and response checks are shared with Download.
func (s *FileStore) DownloadForOwner(ctx context.Context, ownerHash, sourceURL string, headers map[string]string, metadata AssetMetadata) (Asset, error) {
	ownerHash, err := normalizeOwnerHash(ownerHash)
	if err != nil {
		return Asset{}, err
	}
	return s.download(ctx, ownerHash, sourceURL, headers, metadata)
}

func (s *FileStore) download(ctx context.Context, ownerHash, sourceURL string, headers map[string]string, metadata AssetMetadata) (Asset, error) {
	if s == nil {
		return Asset{}, errors.New("image store is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	rawURL := strings.TrimSpace(sourceURL)
	if rawURL == "" {
		return Asset{}, errors.New("image download URL is required")
	}
	if len(rawURL) > maxDownloadURLLength {
		return Asset{}, fmt.Errorf("image download URL exceeds %d bytes", maxDownloadURLLength)
	}
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return Asset{}, fmt.Errorf("parse image download URL: %w", err)
	}
	if err := validateImageDownloadURL(parsedURL); err != nil {
		return Asset{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return Asset{}, fmt.Errorf("create image download request: %w", err)
	}
	for key, value := range headers {
		if strings.TrimSpace(key) != "" {
			request.Header.Set(key, value)
		}
	}
	response, err := s.downloadHTTPClient().Do(request)
	if err != nil {
		return Asset{}, fmt.Errorf("download image: %w", err)
	}
	defer response.Body.Close()
	finalURL := request.URL
	if response.Request != nil && response.Request.URL != nil {
		finalURL = response.Request.URL
	}
	if err := validateImageDownloadURL(finalURL); err != nil {
		return Asset{}, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return Asset{}, fmt.Errorf("download image: HTTP %d", response.StatusCode)
	}
	maxBytes := s.maxBytesLimit()
	if response.ContentLength > maxBytes {
		return Asset{}, fmt.Errorf("downloaded image exceeds %d byte limit", maxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return Asset{}, fmt.Errorf("read image response: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return Asset{}, fmt.Errorf("downloaded image exceeds %d byte limit", maxBytes)
	}
	detectedType, err := validateDownloadedImage(data, response.Header.Get("Content-Type"), metadata.MimeType)
	if err != nil {
		return Asset{}, err
	}
	metadata = normalizeDownloadedMetadata(metadata, finalURL, detectedType)
	return s.save(ctx, ownerHash, bytes.NewReader(data), metadata)
}

// SaveDataURL stores an image returned inline by an Images API. It follows the
// same magic-byte, MIME, dimension and pixel-count validation as Download, but
// avoids treating a potentially multi-megabyte data URL as a network URL.
func (s *FileStore) SaveDataURL(ctx context.Context, dataURL string, metadata AssetMetadata) (Asset, error) {
	return s.saveDataURL(ctx, "", dataURL, metadata)
}

// SaveDataURLForOwner stores an inline image in one account partition after
// applying the same strict MIME, magic-byte and dimension validation.
func (s *FileStore) SaveDataURLForOwner(ctx context.Context, ownerHash, dataURL string, metadata AssetMetadata) (Asset, error) {
	ownerHash, err := normalizeOwnerHash(ownerHash)
	if err != nil {
		return Asset{}, err
	}
	return s.saveDataURL(ctx, ownerHash, dataURL, metadata)
}

func (s *FileStore) saveDataURL(ctx context.Context, ownerHash, dataURL string, metadata AssetMetadata) (Asset, error) {
	if s == nil {
		return Asset{}, errors.New("image store is unavailable")
	}
	raw := strings.TrimSpace(dataURL)
	if !strings.HasPrefix(strings.ToLower(raw), "data:") {
		return Asset{}, errors.New("image data URL is required")
	}
	comma := strings.IndexByte(raw, ',')
	if comma <= len("data:") {
		return Asset{}, errors.New("invalid image data URL")
	}
	header := strings.Split(raw[len("data:"):comma], ";")
	declared, err := normalizeDeclaredImageMIME(header[0])
	if err != nil || !isSupportedImageMIME(declared) {
		return Asset{}, errors.New("image data URL MIME type is not supported")
	}
	base64Encoded := false
	for _, item := range header[1:] {
		if strings.EqualFold(strings.TrimSpace(item), "base64") {
			base64Encoded = true
			break
		}
	}
	if !base64Encoded {
		return Asset{}, errors.New("image data URL must use base64 encoding")
	}
	encoded := strings.TrimSpace(raw[comma+1:])
	// Bound the encoded payload before decoding so an untrusted renderer cannot
	// force an oversized allocation merely by passing a very large data URL.
	maxEncoded := (s.maxBytesLimit()/3)*4 + 8
	if int64(len(encoded)) > maxEncoded {
		return Asset{}, fmt.Errorf("image data URL exceeds %d byte limit", s.maxBytesLimit())
	}
	data, decodeErr := base64.StdEncoding.Strict().DecodeString(encoded)
	if decodeErr != nil {
		data, decodeErr = base64.RawStdEncoding.Strict().DecodeString(encoded)
	}
	if decodeErr != nil {
		return Asset{}, errors.New("invalid image data URL payload")
	}
	detected, validationErr := validateDownloadedImage(data, declared, metadata.MimeType)
	if validationErr != nil {
		return Asset{}, validationErr
	}
	metadata = normalizeDownloadedMetadata(metadata, nil, detected)
	return s.save(ctx, ownerHash, bytes.NewReader(data), metadata)
}

func (s *FileStore) maxBytesLimit() int64 {
	if s == nil || s.maxBytes <= 0 {
		return defaultDownloadLimit
	}
	// Leave room for the limit+1 sentinel used by both Save and Download.
	if s.maxBytes >= (1<<63)-1 {
		return (1 << 63) - 2
	}
	return s.maxBytes
}

// downloadHTTPClient returns a per-call clone so redirect policy and transport
// hardening cannot mutate a process-global client. The default transport pins
// every DNS result to a public IP before connecting; custom transports are
// still wrapped with URL validation, which keeps deterministic tests possible
// without weakening the production default.
func (s *FileStore) downloadHTTPClient() *http.Client {
	var base *http.Client
	if s != nil {
		base = s.httpClient
	}
	if base == nil {
		base = &http.Client{}
	}
	client := *base
	if client.Timeout <= 0 {
		client.Timeout = defaultDownloadTimeout
	}
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	client.Transport = secureImageRoundTripper(transport)
	previousRedirect := client.CheckRedirect
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= maxDownloadRedirects {
			return errors.New("too many image download redirects")
		}
		if err := validateImageDownloadURL(next.URL); err != nil {
			return err
		}
		// A signed source URL must not become a Referer on a redirected request.
		// Drop all non-basic headers on every redirect (including same-origin): a
		// redirect target can be an attacker-controlled path, and callers may have
		// supplied Authorization/X-API-Key in the headers map.
		next.Header.Del("Referer")
		for key := range next.Header {
			if !safeRedirectHeader(key) {
				next.Header.Del(key)
			}
		}
		if previousRedirect != nil {
			return previousRedirect(next, via)
		}
		return nil
	}
	return &client
}

type validatedImageRoundTripper struct {
	base http.RoundTripper
}

func (t validatedImageRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil {
		return nil, errors.New("image download request URL is missing")
	}
	if err := validateImageDownloadURL(request.URL); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(request)
}

func secureImageRoundTripper(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if transport, ok := base.(*http.Transport); ok && transport != nil {
		clone := transport.Clone()
		clone.Proxy = nil
		// A custom DialTLS hook could bypass DialContext entirely. Clear both
		// hooks so all HTTPS connections use the public-IP pinning dialer below.
		clone.DialTLS = nil
		clone.DialTLSContext = nil
		clone.DialContext = publicImageDialContext
		base = clone
	}
	return validatedImageRoundTripper{base: base}
}

func publicImageDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("parse image download address: %w", err)
	}
	host = strings.TrimSuffix(strings.TrimSpace(host), ".")
	if host == "" {
		return nil, errors.New("image download host is missing")
	}
	if err := validateImageDownloadHostName(host); err != nil {
		return nil, err
	}
	addresses := make([]netip.Addr, 0, 4)
	if literal, parseErr := netip.ParseAddr(strings.Trim(host, "[]")); parseErr == nil {
		addresses = append(addresses, literal)
	} else {
		resolved, lookupErr := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if lookupErr != nil {
			return nil, fmt.Errorf("resolve image download host: %w", lookupErr)
		}
		addresses = append(addresses, resolved...)
	}
	if len(addresses) == 0 {
		return nil, errors.New("image download host has no addresses")
	}
	for _, address := range addresses {
		if !isPublicImageIP(address) {
			return nil, fmt.Errorf("image download host resolves to a non-public address: %s", address)
		}
	}
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	var lastErr error
	for _, address := range addresses {
		conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(address.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	return nil, fmt.Errorf("connect to image download host: %w", lastErr)
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
	if strings.TrimSpace(parsed.Opaque) != "" {
		return errors.New("image download URL must use a hierarchical form")
	}
	host := strings.TrimSpace(parsed.Hostname())
	if host == "" {
		return errors.New("image download URL host is missing")
	}
	return validateImageDownloadHostName(host)
}

func validateImageDownloadHostName(host string) error {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "" {
		return errors.New("image download URL host is missing")
	}
	if strings.Contains(host, "%") {
		return errors.New("image download URL must not contain an IPv6 zone")
	}
	if literal, err := netip.ParseAddr(host); err == nil {
		if !isPublicImageIP(literal) {
			return fmt.Errorf("image download URL uses a non-public address: %s", literal)
		}
		return nil
	}
	// These names are either loopback/link-local aliases or reserved service
	// discovery suffixes. They are rejected before DNS so a local resolver
	// cannot turn them into an SSRF target.
	for _, blocked := range []string{
		"localhost",
		"metadata.google.internal",
		"instance-data.ec2.internal",
		"local",
		"internal",
		"lan",
		"home.arpa",
	} {
		if host == blocked || strings.HasSuffix(host, "."+blocked) {
			return errors.New("image download URL host is not allowed")
		}
	}
	return nil
}

func isPublicImageIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, prefix := range []netip.Prefix{
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("2001:db8::/32"),
		netip.MustParsePrefix("2001:2::/48"),
	} {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func safeRedirectHeader(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "accept", "accept-encoding", "user-agent":
		return true
	default:
		return false
	}
}

func validateDownloadedImage(data []byte, responseType, metadataType string) (string, error) {
	if len(data) == 0 {
		return "", errors.New("download image: response body is empty")
	}
	detectedType, ok := detectImageMagic(data)
	if !ok {
		return "", errors.New("download image: response body is not a supported PNG, JPEG, WebP, or GIF")
	}
	for label, declared := range map[string]string{"response": responseType, "metadata": metadataType} {
		if strings.TrimSpace(declared) == "" {
			continue
		}
		normalized, err := normalizeDeclaredImageMIME(declared)
		if err != nil {
			return "", fmt.Errorf("download image: invalid %s content type: %w", label, err)
		}
		if normalized != "" && normalized != "application/octet-stream" && normalized != detectedType {
			return "", fmt.Errorf("download image: %s content type %q does not match image bytes (%s)", label, normalized, detectedType)
		}
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("download image: decode image header: %w", err)
	}
	if !decodedFormatMatchesMIME(format, detectedType) {
		return "", fmt.Errorf("download image: decoded format %q does not match %s", format, detectedType)
	}
	if config.Width <= 0 || config.Height <= 0 {
		return "", errors.New("download image: image dimensions must be positive")
	}
	if config.Width > maxImageDimension || config.Height > maxImageDimension {
		return "", fmt.Errorf("download image: dimensions exceed %dx%d", maxImageDimension, maxImageDimension)
	}
	if int64(config.Width) > maxImagePixels/int64(config.Height) {
		return "", fmt.Errorf("download image: pixel count exceeds %d", maxImagePixels)
	}
	return detectedType, nil
}

func detectImageMagic(data []byte) (string, bool) {
	switch {
	case len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}):
		return "image/png", true
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		return "image/jpeg", true
	case len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a"):
		return "image/gif", true
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp", true
	default:
		return "", false
	}
}

func normalizeDeclaredImageMIME(value string) (string, error) {
	parsed, _, err := mime.ParseMediaType(strings.TrimSpace(value))
	if err != nil {
		return "", err
	}
	parsed = strings.ToLower(strings.TrimSpace(parsed))
	if parsed == "image/jpg" || parsed == "image/pjpeg" {
		parsed = "image/jpeg"
	}
	return parsed, nil
}

func isSupportedImageMIME(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
		return true
	default:
		return false
	}
}

func decodedFormatMatchesMIME(format, mimeType string) bool {
	switch mimeType {
	case "image/png":
		return strings.EqualFold(format, "png")
	case "image/jpeg":
		return strings.EqualFold(format, "jpeg")
	case "image/gif":
		return strings.EqualFold(format, "gif")
	case "image/webp":
		return strings.EqualFold(format, "webp")
	default:
		return false
	}
}

func normalizeDownloadedMetadata(metadata AssetMetadata, sourceURL *url.URL, detectedType string) AssetMetadata {
	name := strings.TrimSpace(metadata.Name)
	if name == "" && sourceURL != nil {
		name = filepath.Base(sourceURL.Path)
	}
	name = safeName(name)
	ext := strings.ToLower(filepath.Ext(name))
	if !imageExtensionMatchesMIME(ext, detectedType) {
		base := strings.TrimSuffix(name, filepath.Ext(name))
		if strings.TrimSpace(base) == "" || base == "." {
			base = "image"
		}
		name = base + extensionForImageMIME(detectedType)
	}
	metadata.Name = name
	metadata.MimeType = detectedType
	return metadata
}

func imageExtensionMatchesMIME(ext, mimeType string) bool {
	ext = strings.ToLower(strings.TrimSpace(ext))
	switch mimeType {
	case "image/png":
		return ext == ".png"
	case "image/jpeg":
		return ext == ".jpg" || ext == ".jpeg"
	case "image/gif":
		return ext == ".gif"
	case "image/webp":
		return ext == ".webp"
	default:
		return false
	}
}

func extensionForImageMIME(mimeType string) string {
	switch mimeType {
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ".png"
	}
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := contextErr(r.ctx); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

func writeAtomicFile(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	if err := ensureExistingSafeStoreRoot(directory); err != nil {
		return err
	}
	if _, err := inspectStoreTarget(path, true); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(directory, ".atomic-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := ensureExistingSafeStoreRoot(directory); err != nil {
		return err
	}
	if _, err := inspectStoreTarget(path, true); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// cleanStoreRoot resolves an absolute, non-root path and normalizes the
// system-owned /var and /tmp aliases on macOS.  The normalization keeps the
// component walk below from mistaking Apple's compatibility symlinks for a
// user-controlled redirect.
func cleanStoreRoot(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("image store directory is required")
	}
	abs, err := filepath.Abs(filepath.Clean(value))
	if err != nil {
		return "", fmt.Errorf("resolve image store directory: %w", err)
	}
	abs = normalizeStoreSystemPath(abs)
	volume := filepath.VolumeName(abs)
	root := volume + string(os.PathSeparator)
	if volume == "" {
		root = string(os.PathSeparator)
	}
	if abs == root || abs == "." {
		return "", errors.New("image store directory must not be a filesystem root")
	}
	return abs, nil
}

func normalizeStoreSystemPath(path string) string {
	if runtime.GOOS != "darwin" {
		return path
	}
	for _, alias := range []string{"/var", "/tmp"} {
		if path == alias || strings.HasPrefix(path, alias+string(os.PathSeparator)) {
			return "/private" + path
		}
	}
	return path
}

// ensureSafeStoreRoot creates missing components one at a time and rejects
// symlinks or non-directories in every existing component.  This is stricter
// than filepath.MkdirAll and is the key boundary protecting private image
// bytes from a redirected application-data path.
func ensureSafeStoreRoot(path string) error {
	path, err := cleanStoreRoot(path)
	if err != nil {
		return err
	}
	return walkStoreDirectory(path, true)
}

func ensureExistingSafeStoreRoot(path string) error {
	path, err := cleanStoreRoot(path)
	if err != nil {
		return err
	}
	return walkStoreDirectory(path, false)
}

func walkStoreDirectory(path string, createMissing bool) error {
	absPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return err
	}
	absPath = normalizeStoreSystemPath(absPath)
	volume := filepath.VolumeName(absPath)
	root := volume + string(os.PathSeparator)
	if volume == "" {
		root = string(os.PathSeparator)
	}
	relative, err := filepath.Rel(root, absPath)
	if err != nil {
		return err
	}
	if relative == "." || relative == "" {
		return errors.New("image store directory must not be a filesystem root")
	}
	current := root
	for _, component := range strings.Split(relative, string(os.PathSeparator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, fs.ErrNotExist) && createMissing {
			if mkdirErr := os.Mkdir(current, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, fs.ErrExist) {
				return mkdirErr
			}
			info, statErr = os.Lstat(current)
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("image store component %s is not a real directory", current)
		}
	}
	// Only the application-owned leaf is tightened.  Changing permissions on
	// existing ancestors such as /private, /var, or a user's home directory
	// would be both surprising and potentially destructive.
	if err := os.Chmod(absPath, 0o700); err != nil {
		return err
	}
	return nil
}

// inspectStoreTarget checks a direct child without following symlinks.  It is
// used immediately before an atomic metadata replacement to close the common
// check-then-swap window for a malicious local process.
func inspectStoreTarget(path string, allowMissing bool) (bool, error) {
	if err := ensureExistingSafeStoreRoot(filepath.Dir(path)); err != nil {
		return false, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		if allowMissing {
			return false, nil
		}
		return false, fs.ErrNotExist
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, errors.New("image store target is not a regular file")
	}
	return true, nil
}

func (s *FileStore) Open(ctx context.Context, id string) (io.ReadCloser, Asset, error) {
	return s.open(ctx, "", id, false)
}

// OpenForOwner opens an asset only when its metadata belongs to ownerHash.
// Cross-owner and legacy rows are reported as not found to avoid existence
// disclosure through the native binding.
func (s *FileStore) OpenForOwner(ctx context.Context, ownerHash, id string) (io.ReadCloser, Asset, error) {
	ownerHash, err := normalizeOwnerHash(ownerHash)
	if err != nil {
		return nil, Asset{}, err
	}
	return s.open(ctx, ownerHash, id, true)
}

func (s *FileStore) open(ctx context.Context, ownerHash, id string, scoped bool) (io.ReadCloser, Asset, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, Asset{}, ctx.Err()
	default:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ensureExistingSafeStoreRoot(s.root); err != nil {
		return nil, Asset{}, fmt.Errorf("validate image store: %w", err)
	}
	asset, err := s.readAsset(id)
	if err != nil {
		if scoped && errors.Is(err, fs.ErrNotExist) {
			return nil, Asset{}, fmt.Errorf("%w: %q", ErrAssetNotFound, id)
		}
		return nil, Asset{}, err
	}
	if scoped && asset.OwnerHash != ownerHash {
		return nil, Asset{}, fmt.Errorf("%w: %q", ErrAssetNotFound, id)
	}
	// Perform a size check before opening a renderer-requested local file. The
	// metadata is untrusted (it can be edited by another process), so enforce
	// the actual on-disk size and reject stale/inflated metadata rather than
	// allowing an oversized file to be streamed through the Wails binding.
	info, err := os.Lstat(asset.Path)
	if err != nil {
		return nil, Asset{}, fmt.Errorf("inspect image before open: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, Asset{}, errors.New("image file is not a regular file")
	}
	limit := s.maxBytesLimit()
	if info.Size() < 0 || info.Size() > limit {
		return nil, Asset{}, fmt.Errorf("local image exceeds %d byte limit", limit)
	}
	if asset.Bytes != info.Size() {
		return nil, Asset{}, errors.New("local image metadata size does not match file")
	}
	file, err := os.Open(asset.Path)
	if err != nil {
		return nil, Asset{}, fmt.Errorf("open image: %w", err)
	}
	return file, asset, nil
}

func (s *FileStore) List(ctx context.Context) ([]Asset, error) {
	return s.list(ctx, "", false)
}

// ListForOwner returns only assets explicitly written for ownerHash. Legacy
// metadata without an owner is intentionally excluded rather than guessed to
// belong to the currently signed-in account.
func (s *FileStore) ListForOwner(ctx context.Context, ownerHash string) ([]Asset, error) {
	ownerHash, err := normalizeOwnerHash(ownerHash)
	if err != nil {
		return nil, err
	}
	return s.list(ctx, ownerHash, true)
}

func (s *FileStore) list(ctx context.Context, ownerHash string, scoped bool) ([]Asset, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ensureExistingSafeStoreRoot(s.root); err != nil {
		return nil, fmt.Errorf("validate image store: %w", err)
	}
	entries, err := os.ReadDir(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return []Asset{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list image store: %w", err)
	}
	assets := make([]Asset, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		asset, err := s.readAsset(id)
		if err != nil {
			continue
		}
		if scoped && asset.OwnerHash != ownerHash {
			continue
		}
		assets = append(assets, asset)
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].CreatedAt.After(assets[j].CreatedAt) })
	return assets, nil
}

func (s *FileStore) Delete(ctx context.Context, id string) error {
	return s.delete(ctx, "", id, false)
}

// DeleteForOwner removes an asset only when its metadata belongs to the
// requested account. A mismatch is intentionally indistinguishable from a
// missing id to the renderer.
func (s *FileStore) DeleteForOwner(ctx context.Context, ownerHash, id string) error {
	ownerHash, err := normalizeOwnerHash(ownerHash)
	if err != nil {
		return err
	}
	return s.delete(ctx, ownerHash, id, true)
}

func (s *FileStore) delete(ctx context.Context, ownerHash, id string, scoped bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ensureExistingSafeStoreRoot(s.root); err != nil {
		return fmt.Errorf("validate image store: %w", err)
	}
	asset, err := s.readAsset(id)
	if err != nil {
		if scoped && errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: %q", ErrAssetNotFound, id)
		}
		return err
	}
	if scoped && asset.OwnerHash != ownerHash {
		return fmt.Errorf("%w: %q", ErrAssetNotFound, id)
	}
	if err := os.Remove(asset.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove image: %w", err)
	}
	if err := os.Remove(filepath.Join(s.root, asset.ID+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove image metadata: %w", err)
	}
	return nil
}

func (s *FileStore) readAsset(id string) (Asset, error) {
	if !validID(id) {
		return Asset{}, errors.New("invalid image id")
	}
	metadataPath := filepath.Join(s.root, id+".json")
	if err := validateLocalRegularFile(metadataPath, s.root); err != nil {
		return Asset{}, fmt.Errorf("invalid image metadata file: %w", err)
	}
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return Asset{}, fmt.Errorf("read image metadata: %w", err)
	}
	var diskMetadata assetDiskMetadata
	if err := json.Unmarshal(data, &diskMetadata); err != nil {
		return Asset{}, fmt.Errorf("decode image metadata: %w", err)
	}
	asset := diskMetadata.asset()
	if asset.OwnerHash != "" {
		normalizedOwner, ownerErr := normalizeOwnerHash(asset.OwnerHash)
		if ownerErr != nil {
			return Asset{}, fmt.Errorf("decode image metadata owner: %w", ownerErr)
		}
		asset.OwnerHash = normalizedOwner
	}
	asset.Path, err = validateLocalAssetPath(s.root, asset.Path)
	if err != nil {
		return Asset{}, err
	}
	return asset, nil
}

// validateLocalAssetPath keeps metadata-controlled paths inside the store and
// rejects symlinks/non-regular files. Metadata is persisted locally and may be
// tampered with by another process; following a symlink here could expose an
// arbitrary file through the Open Wails method or delete one on cleanup.
func validateLocalAssetPath(root, rawPath string) (string, error) {
	rootAbs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", fmt.Errorf("resolve image store root: %w", err)
	}
	base := filepath.Base(strings.TrimSpace(rawPath))
	if base == "." || base == "" || base == string(filepath.Separator) {
		return "", errors.New("image metadata path is empty")
	}
	path := filepath.Join(rootAbs, base)
	if filepath.Dir(path) != rootAbs {
		return "", errors.New("image metadata path escapes store")
	}
	if err := validateLocalRegularFile(path, rootAbs); err != nil {
		return "", fmt.Errorf("image metadata path is not a regular file: %w", err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", fmt.Errorf("resolve image store root: %w", err)
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve image metadata path: %w", err)
	}
	relative, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("image metadata path escapes store")
	}
	return path, nil
}

func validateLocalRegularFile(path, root string) error {
	rootAbs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return err
	}
	pathAbs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return err
	}
	if filepath.Dir(pathAbs) != rootAbs && pathAbs != rootAbs {
		return errors.New("path is outside image store")
	}
	info, err := os.Lstat(pathAbs)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("symlink is not allowed")
	}
	if !info.Mode().IsRegular() {
		return errors.New("file is not regular")
	}
	return nil
}

func randomID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("generate image id: %w", err)
	}
	return hex.EncodeToString(bytes[:]), nil
}

func validID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, character := range id {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func safeName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "." || name == "" || name == string(filepath.Separator) {
		return "image"
	}
	return name
}

func extensionForMime(mimeType string) string {
	if extensions, _ := mime.ExtensionsByType(mimeType); len(extensions) > 0 {
		return extensions[0]
	}
	return ".bin"
}
