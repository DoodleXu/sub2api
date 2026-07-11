package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
)

var (
	ErrImageGenerationRecordNotFound = infraerrors.NotFound("IMAGE_GENERATION_RECORD_NOT_FOUND", "image generation record not found")
	ErrWebConsoleImageTaskNotFound   = infraerrors.NotFound("WEB_CONSOLE_IMAGE_TASK_NOT_FOUND", "web console image task not found")
	ErrImageArchiveDisabled          = infraerrors.Forbidden("IMAGE_ARCHIVE_DISABLED", "image archive storage is disabled")
)

const (
	ImageArchiveMaxBytes           = 32 << 20
	ImageAssetScopeAdmin           = "admin-image-generation"
	ImageAssetScopeWebConsole      = "web-console-image-task"
	imageArchiveAssetTokenTTL      = 15 * time.Minute
	imageArchiveAssetTokenLeeway   = time.Minute
	imageArchiveAssetTokenVersion  = "v1"
	imageArchiveStableTokenVersion = "v2"
	imageArchiveTaskTimeout        = 5 * time.Minute
)

type ImageGenerationArchiveRepository interface {
	CreateRecord(ctx context.Context, record *ImageGenerationRecord) error
	UpdateRecord(ctx context.Context, record *ImageGenerationRecord) error
	GetRecordByID(ctx context.Context, id int64) (*ImageGenerationRecord, []*ImageGenerationAsset, error)
	ListRecords(ctx context.Context, params ImageGenerationRecordListParams) ([]*ImageGenerationRecord, *ImageGenerationRecordListResult, error)
	ListAllArchiveStorageRefs(ctx context.Context) (*ImageGenerationArchiveClearResult, error)
	DeleteArchiveRecordsByID(ctx context.Context, recordIDs []int64) (int64, error)
	ListDailyStats(ctx context.Context, params ImageGenerationRecordDailyStatsParams) ([]ImageGenerationDailyStat, error)
	GetStorageStats(ctx context.Context) (ImageGenerationStorageStats, error)
	CreateAsset(ctx context.Context, asset *ImageGenerationAsset) error
	GetAssetByID(ctx context.Context, id int64) (*ImageGenerationAsset, *ImageGenerationRecord, error)
	ListAssetsByRecordID(ctx context.Context, recordID int64) ([]*ImageGenerationAsset, error)
	CreateWebConsoleTask(ctx context.Context, task *WebConsoleImageTask) error
	ClaimWebConsoleTask(ctx context.Context, id int64, staleBefore time.Time) (*WebConsoleImageTask, bool, error)
	GetWebConsoleTaskByID(ctx context.Context, id int64) (*WebConsoleImageTask, error)
	ListWebConsoleTasksByUserID(ctx context.Context, userID int64, params pagination.PaginationParams) ([]*WebConsoleImageTask, *pagination.PaginationResult, error)
	MarkWebConsoleTasksUserDeletedBySessionID(ctx context.Context, userID int64, sessionID string) (int64, error)
	UpdateWebConsoleTask(ctx context.Context, task *WebConsoleImageTask) error
	CountDailyByDate(ctx context.Context, day time.Time) (int64, error)
}

type ImageArchiveStorageConfig struct {
	Enabled       bool   `json:"enabled"`
	Type          string `json:"type"`
	LocalDir      string `json:"local_dir"`
	S3Endpoint    string `json:"s3_endpoint"`
	S3Region      string `json:"s3_region"`
	S3Bucket      string `json:"s3_bucket"`
	S3AccessKey   string `json:"s3_access_key"`
	S3SecretKey   string `json:"s3_secret_key"`
	S3Prefix      string `json:"s3_prefix"`
	PublicBaseURL string `json:"public_base_url"`
	PathStyle     bool   `json:"path_style"`
}

type ImageGenerationRecord struct {
	ID            int64      `json:"id"`
	UserID        *int64     `json:"user_id,omitempty"`
	APIKeyID      *int64     `json:"api_key_id,omitempty"`
	GroupID       *int64     `json:"group_id,omitempty"`
	AccountID     *int64     `json:"account_id,omitempty"`
	RequestID     string     `json:"request_id"`
	Source        string     `json:"source"`
	Endpoint      string     `json:"endpoint"`
	Model         string     `json:"model"`
	PromptExcerpt string     `json:"prompt_excerpt"`
	ImageCount    int        `json:"image_count"`
	Status        string     `json:"status"`
	StorageType   string     `json:"storage_type"`
	ErrorMessage  string     `json:"error_message"`
	UsageLogID    *int64     `json:"usage_log_id,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
}

type ImageGenerationAsset struct {
	ID         int64     `json:"id"`
	RecordID   int64     `json:"record_id"`
	AssetIndex int       `json:"asset_index"`
	MimeType   string    `json:"mime_type"`
	Extension  string    `json:"extension"`
	Width      *int      `json:"width,omitempty"`
	Height     *int      `json:"height,omitempty"`
	Bytes      int64     `json:"bytes"`
	SHA256     string    `json:"sha256"`
	StorageKey string    `json:"storage_key"`
	PublicURL  string    `json:"public_url"`
	AdminURL   string    `json:"admin_url"`
	CreatedAt  time.Time `json:"created_at"`
}

type WebConsoleImageTask struct {
	ID            int64           `json:"id"`
	UserID        int64           `json:"user_id"`
	APIKeyID      *int64          `json:"api_key_id,omitempty"`
	SessionID     string          `json:"session_id"`
	MessageID     string          `json:"message_id"`
	Status        string          `json:"status"`
	RequestJSON   json.RawMessage `json:"request_json"`
	RecordID      *int64          `json:"record_id,omitempty"`
	ErrorMessage  string          `json:"error_message"`
	CreatedAt     time.Time       `json:"created_at"`
	StartedAt     *time.Time      `json:"started_at,omitempty"`
	CompletedAt   *time.Time      `json:"completed_at,omitempty"`
	UserDeletedAt *time.Time      `json:"user_deleted_at,omitempty"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

type ImageGenerationRecordListParams struct {
	Page     int
	PageSize int
	UserID   *int64
	APIKeyID *int64
	Model    string
	Status   string
	Source   string
	StartAt  *time.Time
	EndAt    *time.Time
}

type ImageGenerationRecordListResult struct {
	Total int64 `json:"total"`
	Page  int   `json:"page"`
	Size  int   `json:"page_size"`
	Pages int   `json:"pages"`
}

type ImageGenerationDailyStat struct {
	Date         string `json:"date"`
	RequestCount int64  `json:"request_count"`
	ImageCount   int64  `json:"image_count"`
	FailedCount  int64  `json:"failed_count"`
}

type ImageGenerationStorageStats struct {
	TotalBytes int64 `json:"total_bytes"`
}

type ImageGenerationArchiveClearResult struct {
	RecordsDeleted        int64                            `json:"records_deleted"`
	AssetsDeleted         int64                            `json:"assets_deleted"`
	StorageDeleteFailures int64                            `json:"storage_delete_failures"`
	SkippedRecords        int64                            `json:"skipped_records"`
	ActiveRecords         int64                            `json:"active_records"`
	RecordIDs             []int64                          `json:"-"`
	AssetRefs             []ImageGenerationAssetStorageRef `json:"-"`
}

type ImageGenerationAssetStorageRef struct {
	ID          int64  `json:"id"`
	StorageKey  string `json:"storage_key"`
	StorageType string `json:"storage_type"`
}

type ImageGenerationRecordDailyStatsParams struct {
	StartDate time.Time
	EndDate   time.Time
}

type ImageGenerationStorage interface {
	Save(ctx context.Context, imageBytes []byte, meta StoredImageMeta) (*StoredImage, error)
	ResolveURL(ctx context.Context, stored *StoredImage, download bool) string
	Open(ctx context.Context, storageKey string) (io.ReadCloser, string, error)
	Delete(ctx context.Context, storageKey string) error
}

type StoredImageMeta struct {
	RecordID   int64
	AssetIndex int
	MimeType   string
	Extension  string
	Width      *int
	Height     *int
}

type StoredImage struct {
	StorageType string
	StorageKey  string
	PublicURL   string
	AdminURL    string
	Bytes       int64
	SHA256      string
	MimeType    string
	Extension   string
}

type localImageArchiveStorage struct {
	baseDir       string
	publicBaseURL string
}

func newLocalImageArchiveStorage(baseDir, publicBaseURL string) *localImageArchiveStorage {
	return &localImageArchiveStorage{baseDir: baseDir, publicBaseURL: publicBaseURL}
}

func (s *localImageArchiveStorage) Save(ctx context.Context, imageBytes []byte, meta StoredImageMeta) (*StoredImage, error) {
	if len(imageBytes) == 0 {
		return nil, fmt.Errorf("image bytes are empty")
	}
	if len(imageBytes) > ImageArchiveMaxBytes {
		return nil, fmt.Errorf("image exceeds max archive size: %d bytes", len(imageBytes))
	}
	now := time.Now().UTC()
	rel := filepath.Join(
		fmt.Sprintf("%04d", now.Year()),
		fmt.Sprintf("%02d", int(now.Month())),
		fmt.Sprintf("%02d", now.Day()),
		fmt.Sprintf("%d-%s%s", meta.RecordID, uuid.NewString(), ensureExt(meta.Extension, meta.MimeType)),
	)
	abs := filepath.Join(s.baseDir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(abs, imageBytes, 0o644); err != nil {
		return nil, err
	}
	return &StoredImage{
		StorageType: "local",
		StorageKey:  rel,
		PublicURL:   joinURLPath(s.publicBaseURL, rel),
		AdminURL:    joinURLPath(s.publicBaseURL, rel),
		Bytes:       int64(len(imageBytes)),
		SHA256:      sha256Hex(imageBytes),
		MimeType:    resolveImageMimeType(meta.MimeType, meta.Extension),
		Extension:   ensureExt(meta.Extension, meta.MimeType),
	}, nil
}

func (s *localImageArchiveStorage) ResolveURL(_ context.Context, stored *StoredImage, download bool) string {
	if stored == nil {
		return ""
	}
	if download {
		return stored.AdminURL
	}
	return stored.PublicURL
}

func (s *localImageArchiveStorage) Open(_ context.Context, storageKey string) (io.ReadCloser, string, error) {
	path, err := safeLocalImageArchivePath(s.baseDir, storageKey)
	if err != nil {
		return nil, "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	return file, mime.TypeByExtension(filepath.Ext(path)), nil
}

func (s *localImageArchiveStorage) Delete(_ context.Context, storageKey string) error {
	path, err := safeLocalImageArchivePath(s.baseDir, storageKey)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

type s3ImageArchiveStorage struct {
	client        *s3.Client
	bucket        string
	prefix        string
	publicBaseURL string
}

func newS3ImageArchiveStorage(client *s3.Client, bucket, prefix, publicBaseURL string) *s3ImageArchiveStorage {
	return &s3ImageArchiveStorage{
		client:        client,
		bucket:        bucket,
		prefix:        strings.Trim(prefix, "/"),
		publicBaseURL: strings.TrimRight(publicBaseURL, "/"),
	}
}

func (s *s3ImageArchiveStorage) Save(ctx context.Context, imageBytes []byte, meta StoredImageMeta) (*StoredImage, error) {
	if len(imageBytes) == 0 {
		return nil, fmt.Errorf("image bytes are empty")
	}
	if len(imageBytes) > ImageArchiveMaxBytes {
		return nil, fmt.Errorf("image exceeds max archive size: %d bytes", len(imageBytes))
	}
	key := filepath.ToSlash(filepath.Join(
		s.prefix,
		fmt.Sprintf("%04d", time.Now().UTC().Year()),
		fmt.Sprintf("%02d", int(time.Now().UTC().Month())),
		fmt.Sprintf("%02d", time.Now().UTC().Day()),
		fmt.Sprintf("%d-%s%s", meta.RecordID, uuid.NewString(), ensureExt(meta.Extension, meta.MimeType)),
	))
	mimeType := resolveImageMimeType(meta.MimeType, meta.Extension)
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(imageBytes),
		ContentType: aws.String(mimeType),
	})
	if err != nil {
		return nil, err
	}
	return &StoredImage{
		StorageType: "s3",
		StorageKey:  key,
		PublicURL:   s.publicURLForKey(key),
		AdminURL:    s.publicURLForKey(key),
		Bytes:       int64(len(imageBytes)),
		SHA256:      sha256Hex(imageBytes),
		MimeType:    mimeType,
		Extension:   ensureExt(meta.Extension, meta.MimeType),
	}, nil
}

func (s *s3ImageArchiveStorage) ResolveURL(_ context.Context, stored *StoredImage, download bool) string {
	if stored == nil {
		return ""
	}
	if download {
		return stored.AdminURL
	}
	return stored.PublicURL
}

func (s *s3ImageArchiveStorage) Open(ctx context.Context, storageKey string) (io.ReadCloser, string, error) {
	if s == nil || s.client == nil {
		return nil, "", fmt.Errorf("s3 image archive storage is not configured")
	}
	key := strings.TrimLeft(strings.TrimSpace(storageKey), "/")
	if key == "" || strings.Contains(key, "..") {
		return nil, "", fmt.Errorf("invalid s3 image archive key")
	}
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, "", err
	}
	contentType := ""
	if out.ContentType != nil {
		contentType = strings.TrimSpace(*out.ContentType)
	}
	return out.Body, contentType, nil
}

func (s *s3ImageArchiveStorage) Delete(ctx context.Context, storageKey string) error {
	if s == nil || s.client == nil {
		return fmt.Errorf("s3 image archive storage is not configured")
	}
	key := strings.TrimLeft(strings.TrimSpace(storageKey), "/")
	if key == "" || strings.Contains(key, "..") {
		return fmt.Errorf("invalid s3 image archive key")
	}
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	return err
}

func (s *s3ImageArchiveStorage) publicURLForKey(key string) string {
	if s == nil || strings.TrimSpace(s.publicBaseURL) == "" {
		return ""
	}
	return joinURLPath(s.publicBaseURL, key)
}

type ImageGenerationArchiveService struct {
	repo            ImageGenerationArchiveRepository
	settingRepo     SettingRepository
	apiKeyService   APIKeyRepository
	storage         ImageGenerationStorage
	storageResolver func(context.Context) (ImageGenerationStorage, error)
	workerLimit     chan struct{}
	taskTimeout     time.Duration
	cfg             *config.Config
}

func NewImageGenerationArchiveService(repo ImageGenerationArchiveRepository, settingRepo SettingRepository, apiKeyService APIKeyRepository, cfg *config.Config) *ImageGenerationArchiveService {
	svc := &ImageGenerationArchiveService{
		repo:          repo,
		settingRepo:   settingRepo,
		apiKeyService: apiKeyService,
		workerLimit:   make(chan struct{}, 4),
		taskTimeout:   imageArchiveTaskTimeout,
		cfg:           cfg,
	}
	svc.storage = newLocalImageArchiveStorage(defaultImageArchiveLocalDir(cfg), "")
	return svc
}

func (s *ImageGenerationArchiveService) archiveTaskTimeoutDuration() time.Duration {
	if s != nil && s.taskTimeout > 0 {
		return s.taskTimeout
	}
	return imageArchiveTaskTimeout
}

func (s *ImageGenerationArchiveService) SetStorage(storage ImageGenerationStorage) {
	if storage != nil {
		s.storage = storage
	}
}

const imageArchiveStorageSettingKey = "image_archive_storage"

func defaultImageArchiveLocalDir(cfg *config.Config) string {
	baseDir := "./data"
	if cfg != nil && strings.TrimSpace(cfg.Pricing.DataDir) != "" {
		baseDir = strings.TrimSpace(cfg.Pricing.DataDir)
	}
	return filepath.Join(baseDir, "image-archive")
}

func DefaultImageArchiveStorageConfig(cfg *config.Config) ImageArchiveStorageConfig {
	return ImageArchiveStorageConfig{
		Enabled:  true,
		Type:     "local",
		LocalDir: defaultImageArchiveLocalDir(cfg),
	}
}

func (s *ImageGenerationArchiveService) GetStorageConfig(ctx context.Context) (ImageArchiveStorageConfig, error) {
	if s == nil {
		return DefaultImageArchiveStorageConfig(nil), nil
	}
	cfg := DefaultImageArchiveStorageConfig(s.cfg)
	if s.settingRepo == nil {
		return cfg, nil
	}
	raw, err := s.settingRepo.GetValue(ctx, imageArchiveStorageSettingKey)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return cfg, nil
		}
		return cfg, err
	}
	if strings.TrimSpace(raw) == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return DefaultImageArchiveStorageConfig(s.cfg), nil
	}
	if strings.TrimSpace(cfg.Type) == "" {
		cfg.Type = "local"
	}
	if strings.TrimSpace(cfg.LocalDir) == "" {
		cfg.LocalDir = defaultImageArchiveLocalDir(s.cfg)
	}
	return cfg, nil
}

func (s *ImageGenerationArchiveService) UpdateStorageConfig(ctx context.Context, cfg ImageArchiveStorageConfig) (ImageArchiveStorageConfig, error) {
	if s == nil || s.settingRepo == nil {
		return cfg, fmt.Errorf("image archive service is not configured")
	}
	cfg.Type = strings.ToLower(strings.TrimSpace(cfg.Type))
	if cfg.Type == "" {
		cfg.Type = "local"
	}
	if cfg.Type != "local" && cfg.Type != "s3" {
		return cfg, fmt.Errorf("unsupported image archive storage type")
	}
	if strings.TrimSpace(cfg.LocalDir) == "" {
		cfg.LocalDir = defaultImageArchiveLocalDir(s.cfg)
	}
	storage, err := s.storageFromConfig(ctx, cfg)
	if err != nil {
		return cfg, err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return cfg, err
	}
	if err := s.settingRepo.Set(ctx, imageArchiveStorageSettingKey, string(raw)); err != nil {
		return cfg, err
	}
	s.SetStorage(storage)
	return cfg, nil
}

func (s *ImageGenerationArchiveService) IsArchiveEnabled(ctx context.Context) (bool, error) {
	cfg, err := s.GetStorageConfig(ctx)
	if err != nil {
		return false, err
	}
	return cfg.Enabled, nil
}

func (s *ImageGenerationArchiveService) storageFromConfig(ctx context.Context, cfg ImageArchiveStorageConfig) (ImageGenerationStorage, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Type)) {
	case "", "local":
		return newLocalImageArchiveStorage(cfg.LocalDir, cfg.PublicBaseURL), nil
	case "s3":
		if strings.TrimSpace(cfg.S3Bucket) == "" {
			return nil, fmt.Errorf("s3 bucket is required")
		}
		region := strings.TrimSpace(cfg.S3Region)
		if region == "" {
			region = "auto"
		}
		opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}
		if strings.TrimSpace(cfg.S3AccessKey) != "" || strings.TrimSpace(cfg.S3SecretKey) != "" {
			opts = append(opts, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.S3AccessKey, cfg.S3SecretKey, "")))
		}
		if endpoint := strings.TrimSpace(cfg.S3Endpoint); endpoint != "" {
			opts = append(opts, awsconfig.WithBaseEndpoint(endpoint))
		}
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
		if err != nil {
			return nil, err
		}
		client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
			o.UsePathStyle = cfg.PathStyle
		})
		return newS3ImageArchiveStorage(client, cfg.S3Bucket, cfg.S3Prefix, cfg.PublicBaseURL), nil
	default:
		return nil, fmt.Errorf("unsupported image archive storage type")
	}
}

func (s *ImageGenerationArchiveService) CreateRecord(ctx context.Context, record *ImageGenerationRecord) error {
	if record == nil {
		return nil
	}
	record.Source = defaultString(record.Source, "gateway")
	record.Status = defaultString(record.Status, "pending")
	record.StorageType = defaultString(record.StorageType, "local")
	record.PromptExcerpt = truncateRunes(record.PromptExcerpt, 500)
	return s.repo.CreateRecord(ctx, record)
}

func (s *ImageGenerationArchiveService) ListRecords(ctx context.Context, params ImageGenerationRecordListParams) ([]*ImageGenerationRecord, *ImageGenerationRecordListResult, error) {
	return s.repo.ListRecords(ctx, params)
}

func (s *ImageGenerationArchiveService) GetRecordByID(ctx context.Context, id int64) (*ImageGenerationRecord, []*ImageGenerationAsset, error) {
	return s.repo.GetRecordByID(ctx, id)
}

func (s *ImageGenerationArchiveService) ListAssetsByRecordID(ctx context.Context, recordID int64) ([]*ImageGenerationAsset, error) {
	return s.repo.ListAssetsByRecordID(ctx, recordID)
}

func (s *ImageGenerationArchiveService) ListDailyStats(ctx context.Context, params ImageGenerationRecordDailyStatsParams) ([]ImageGenerationDailyStat, error) {
	return s.repo.ListDailyStats(ctx, params)
}

func (s *ImageGenerationArchiveService) GetStorageStats(ctx context.Context) (ImageGenerationStorageStats, error) {
	return s.repo.GetStorageStats(ctx)
}

func (s *ImageGenerationArchiveService) ClearAllArchives(ctx context.Context) (*ImageGenerationArchiveClearResult, error) {
	plan, err := s.repo.ListAllArchiveStorageRefs(ctx)
	if err != nil {
		return nil, err
	}
	if plan == nil {
		return &ImageGenerationArchiveClearResult{}, nil
	}
	result := &ImageGenerationArchiveClearResult{
		SkippedRecords: plan.SkippedRecords,
		ActiveRecords:  plan.ActiveRecords,
	}
	for _, ref := range plan.AssetRefs {
		if strings.TrimSpace(ref.StorageKey) == "" {
			continue
		}
		storage, err := s.storageForType(ctx, ref.StorageType)
		if err != nil {
			result.StorageDeleteFailures++
			continue
		}
		if err := storage.Delete(ctx, ref.StorageKey); err != nil {
			result.StorageDeleteFailures++
			continue
		}
		result.AssetsDeleted++
	}
	if result.StorageDeleteFailures > 0 {
		return result, nil
	}
	recordsDeleted, err := s.repo.DeleteArchiveRecordsByID(ctx, plan.RecordIDs)
	if err != nil {
		return nil, err
	}
	result.RecordsDeleted = recordsDeleted
	result.AssetsDeleted = plan.AssetsDeleted
	return result, nil
}

func (s *ImageGenerationArchiveService) GetAssetByID(ctx context.Context, id int64) (*ImageGenerationAsset, *ImageGenerationRecord, error) {
	return s.repo.GetAssetByID(ctx, id)
}

func (s *ImageGenerationArchiveService) GetAssetPath(asset *ImageGenerationAsset) string {
	if asset == nil || strings.TrimSpace(asset.StorageKey) == "" {
		return ""
	}
	cfg, _ := s.GetStorageConfig(context.Background())
	if strings.ToLower(cfg.Type) != "local" {
		return ""
	}
	baseDir := strings.TrimSpace(cfg.LocalDir)
	if baseDir == "" {
		baseDir = defaultImageArchiveLocalDir(s.cfg)
	}
	path, err := safeLocalImageArchivePath(baseDir, asset.StorageKey)
	if err != nil {
		return ""
	}
	return path
}

type ImageGenerationAssetReader struct {
	Body        io.ReadCloser
	ContentType string
	Size        int64
	Filename    string
	Inline      bool
}

func (s *ImageGenerationArchiveService) OpenAsset(ctx context.Context, asset *ImageGenerationAsset) (*ImageGenerationAssetReader, error) {
	if s == nil || asset == nil || strings.TrimSpace(asset.StorageKey) == "" {
		return nil, fmt.Errorf("image asset is not available")
	}
	storage, err := s.storageForCurrentConfig(ctx)
	if err != nil {
		return nil, err
	}
	body, contentType, err := storage.Open(ctx, asset.StorageKey)
	if err != nil {
		return nil, err
	}
	sniffed, restoredBody, err := sniffImageAssetBody(body)
	if err != nil {
		return nil, err
	}
	body = restoredBody
	if IsSafeImageContentType(sniffed) {
		contentType = sniffed
	} else if strings.TrimSpace(contentType) == "" {
		contentType = resolveImageMimeType(asset.MimeType, asset.Extension)
	}
	if !IsSafeImageContentType(sniffed) {
		contentType = "application/octet-stream"
	}
	contentType, inline := SafeImageAssetContentType(contentType)
	filename := fmt.Sprintf("image-%d%s", asset.ID, ensureSafeImageExt(asset.Extension, contentType, inline))
	return &ImageGenerationAssetReader{
		Body:        body,
		ContentType: contentType,
		Size:        asset.Bytes,
		Filename:    filename,
		Inline:      inline,
	}, nil
}

type imageAssetReadCloser struct {
	io.Reader
	closer io.Closer
}

func (r *imageAssetReadCloser) Close() error {
	if r == nil || r.closer == nil {
		return nil
	}
	return r.closer.Close()
}

func sniffImageAssetBody(body io.ReadCloser) (string, io.ReadCloser, error) {
	if body == nil {
		return "", nil, fmt.Errorf("image asset body is not available")
	}
	buf := make([]byte, 512)
	n, err := io.ReadFull(body, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		_ = body.Close()
		return "", nil, err
	}
	buf = buf[:n]
	return detectArchivedImageMimeType(buf), &imageAssetReadCloser{
		Reader: io.MultiReader(bytes.NewReader(buf), body),
		closer: body,
	}, nil
}

func (s *ImageGenerationArchiveService) SignAssetURLPath(path string, assetID int64, scope string, now time.Time) string {
	if strings.TrimSpace(path) == "" || assetID <= 0 {
		return path
	}
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = "asset"
	}
	expires := now.UTC().Add(imageArchiveAssetTokenTTL).Unix()
	sig := s.signAssetToken(assetID, scope, expires)
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return fmt.Sprintf("%s%sexpires=%d&scope=%s&sig=%s", path, sep, expires, scope, sig)
}

func (s *ImageGenerationArchiveService) SignStableAssetURLPath(path string, assetID int64, scope, version string) string {
	if strings.TrimSpace(path) == "" || assetID <= 0 {
		return path
	}
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = "asset"
	}
	version = strings.TrimSpace(version)
	if version == "" {
		return s.SignAssetURLPath(path, assetID, scope, time.Now().UTC())
	}
	expires := time.Now().UTC().Add(imageArchiveAssetTokenTTL).Unix()
	sig := s.signStableAssetToken(assetID, scope, version, expires)
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return fmt.Sprintf("%s%sv=%s&expires=%d&scope=%s&sig=%s", path, sep, url.QueryEscape(version), expires, scope, sig)
}

func (s *ImageGenerationArchiveService) VerifyAssetToken(assetID int64, scope, expiresRaw, sig string, now time.Time) bool {
	if assetID <= 0 || strings.TrimSpace(sig) == "" {
		return false
	}
	expires, err := strconv.ParseInt(strings.TrimSpace(expiresRaw), 10, 64)
	if err != nil || expires <= 0 {
		return false
	}
	if now.UTC().After(time.Unix(expires, 0).Add(imageArchiveAssetTokenLeeway)) {
		return false
	}
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return false
	}
	expected := s.signAssetToken(assetID, scope, expires)
	return hmac.Equal([]byte(expected), []byte(strings.TrimSpace(sig)))
}

func (s *ImageGenerationArchiveService) VerifyStableAssetToken(assetID int64, scope, version, expiresRaw, sig string, now time.Time) bool {
	if assetID <= 0 || strings.TrimSpace(sig) == "" {
		return false
	}
	expires, err := strconv.ParseInt(strings.TrimSpace(expiresRaw), 10, 64)
	if err != nil || expires <= 0 {
		return false
	}
	if now.UTC().After(time.Unix(expires, 0).Add(imageArchiveAssetTokenLeeway)) {
		return false
	}
	scope = strings.TrimSpace(scope)
	version = strings.TrimSpace(version)
	if scope == "" || version == "" {
		return false
	}
	expected := s.signStableAssetToken(assetID, scope, version, expires)
	return hmac.Equal([]byte(expected), []byte(strings.TrimSpace(sig)))
}

func (s *ImageGenerationArchiveService) signAssetToken(assetID int64, scope string, expires int64) string {
	secret := "sub2api-image-archive-dev-secret"
	if s != nil && s.cfg != nil && strings.TrimSpace(s.cfg.JWT.Secret) != "" {
		secret = strings.TrimSpace(s.cfg.JWT.Secret)
	}
	message := fmt.Sprintf("%s:%d:%s:%d", imageArchiveAssetTokenVersion, assetID, strings.TrimSpace(scope), expires)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *ImageGenerationArchiveService) signStableAssetToken(assetID int64, scope, version string, expires int64) string {
	secret := "sub2api-image-archive-dev-secret"
	if s != nil && s.cfg != nil && strings.TrimSpace(s.cfg.JWT.Secret) != "" {
		secret = strings.TrimSpace(s.cfg.JWT.Secret)
	}
	message := fmt.Sprintf("%s:%d:%s:%s:%d", imageArchiveStableTokenVersion, assetID, strings.TrimSpace(scope), strings.TrimSpace(version), expires)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *ImageGenerationArchiveService) ArchiveBase64Images(ctx context.Context, record *ImageGenerationRecord, images []ArchivedImageInput) {
	if s == nil || s.repo == nil || record == nil || len(images) == 0 {
		return
	}
	select {
	case s.workerLimit <- struct{}{}:
	default:
		_ = s.updateRecordStatus(ctx, record, "skipped", "archive queue full")
		return
	}
	go func() {
		defer func() { <-s.workerLimit }()
		archiveCtx, cancel := context.WithTimeout(context.Background(), s.archiveTaskTimeoutDuration())
		defer cancel()
		_ = s.archiveBase64Images(archiveCtx, record, images)
	}()
}

// SubmitBase64Archive 将启用检查、记录创建和资产归档作为一个有界后台任务提交。
// 返回 false 表示服务不可用或队列已满，调用方不得再创建无界 goroutine 重试。
func (s *ImageGenerationArchiveService) SubmitBase64Archive(record *ImageGenerationRecord, images []ArchivedImageInput) bool {
	if s == nil || s.repo == nil || record == nil || len(images) == 0 {
		return false
	}
	select {
	case s.workerLimit <- struct{}{}:
	default:
		return false
	}

	go func() {
		defer func() { <-s.workerLimit }()
		ctx, cancel := context.WithTimeout(context.Background(), s.archiveTaskTimeoutDuration())
		defer cancel()
		defer func() {
			if !errors.Is(ctx.Err(), context.DeadlineExceeded) || record.ID <= 0 || record.Status == "completed" {
				return
			}
			// 主任务 context 已过期，终态写入必须使用独立的短 context，
			// 否则数据库会拒绝 UpdateRecord，记录将永久停在 pending/running。
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cleanupCancel()
			if err := s.updateRecordStatus(cleanupCtx, record, "failed", "image archive task timed out"); err != nil {
				slog.Error("image archive timeout status update failed", "record_id", record.ID, "error", err)
			}
		}()

		enabled, err := s.IsArchiveEnabled(ctx)
		if err != nil {
			slog.Warn("image archive enabled check failed", "error", err)
			return
		}
		if !enabled {
			return
		}
		if err := s.CreateRecord(ctx, record); err != nil {
			slog.Warn("image archive record creation failed", "error", err)
			return
		}
		if err := s.archiveBase64Images(ctx, record, images); err != nil {
			slog.Warn("image archive task failed", "record_id", record.ID, "error", err)
		}
	}()
	return true
}

func (s *ImageGenerationArchiveService) ArchiveBase64ImagesSync(ctx context.Context, record *ImageGenerationRecord, images []ArchivedImageInput) error {
	if s == nil || s.repo == nil || record == nil || len(images) == 0 {
		return nil
	}
	byteImages := make([]ArchivedImageBytesInput, 0, len(images))
	for _, image := range images {
		imageBytes, mimeType, ext, err := decodeArchivedImage(image)
		if err != nil {
			return err
		}
		byteImages = append(byteImages, ArchivedImageBytesInput{
			Index:     image.Index,
			Bytes:     imageBytes,
			MimeType:  mimeType,
			Extension: ext,
			Width:     image.Width,
			Height:    image.Height,
		})
	}
	return s.ArchiveImageBytesSync(ctx, record, byteImages)
}

func (s *ImageGenerationArchiveService) ArchiveImageBytesSync(ctx context.Context, record *ImageGenerationRecord, images []ArchivedImageBytesInput) error {
	if s == nil || s.repo == nil || record == nil || len(images) == 0 {
		return nil
	}
	return s.archiveImageBytes(ctx, record, images)
}

func (s *ImageGenerationArchiveService) storageForCurrentConfig(ctx context.Context) (ImageGenerationStorage, error) {
	if s == nil {
		return nil, fmt.Errorf("image archive service is not configured")
	}
	if s.storageResolver != nil {
		return s.storageResolver(ctx)
	}
	cfg, err := s.GetStorageConfig(ctx)
	if err != nil {
		return nil, err
	}
	storage, err := s.storageFromConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return storage, nil
}

func (s *ImageGenerationArchiveService) storageForType(ctx context.Context, storageType string) (ImageGenerationStorage, error) {
	if s == nil {
		return nil, fmt.Errorf("image archive service is not configured")
	}
	cfg, err := s.GetStorageConfig(ctx)
	if err != nil {
		return nil, err
	}
	requestedType := strings.ToLower(strings.TrimSpace(storageType))
	if requestedType == "" {
		requestedType = "local"
	}
	currentType := strings.ToLower(strings.TrimSpace(cfg.Type))
	if currentType == "" {
		currentType = "local"
	}
	if s.storage != nil && requestedType == currentType {
		return s.storage, nil
	}
	cfg.Type = requestedType
	return s.storageFromConfig(ctx, cfg)
}

func (s *ImageGenerationArchiveService) CreateWebConsoleTask(ctx context.Context, task *WebConsoleImageTask) error {
	if task == nil {
		return nil
	}
	task.Status = defaultString(task.Status, "pending")
	return s.repo.CreateWebConsoleTask(ctx, task)
}

func (s *ImageGenerationArchiveService) ClaimWebConsoleTask(ctx context.Context, id int64, staleBefore time.Time) (*WebConsoleImageTask, bool, error) {
	if s == nil || s.repo == nil || id <= 0 {
		return nil, false, nil
	}
	return s.repo.ClaimWebConsoleTask(ctx, id, staleBefore)
}

func (s *ImageGenerationArchiveService) GetWebConsoleTaskByID(ctx context.Context, id int64) (*WebConsoleImageTask, error) {
	return s.repo.GetWebConsoleTaskByID(ctx, id)
}

func (s *ImageGenerationArchiveService) MarkWebConsoleTasksUserDeletedBySessionID(ctx context.Context, userID int64, sessionID string) (int64, error) {
	if s == nil || s.repo == nil || userID <= 0 || strings.TrimSpace(sessionID) == "" {
		return 0, nil
	}
	return s.repo.MarkWebConsoleTasksUserDeletedBySessionID(ctx, userID, strings.TrimSpace(sessionID))
}

func (s *ImageGenerationArchiveService) UpdateWebConsoleTask(ctx context.Context, task *WebConsoleImageTask) error {
	return s.repo.UpdateWebConsoleTask(ctx, task)
}

type ArchivedImageInput struct {
	Index       int
	B64JSON     string
	DownloadURL string
	MimeType    string
	Extension   string
	Width       *int
	Height      *int
}

type ArchivedImageBytesInput struct {
	Index     int
	Bytes     []byte
	MimeType  string
	Extension string
	Width     *int
	Height    *int
}

func (s *ImageGenerationArchiveService) archiveBase64Images(ctx context.Context, record *ImageGenerationRecord, images []ArchivedImageInput) error {
	byteImages := make([]ArchivedImageBytesInput, 0, len(images))
	for _, image := range images {
		imageBytes, mimeType, ext, err := decodeArchivedImage(image)
		if err != nil {
			_ = s.appendError(ctx, record, err)
			continue
		}
		byteImages = append(byteImages, ArchivedImageBytesInput{
			Index:     image.Index,
			Bytes:     imageBytes,
			MimeType:  mimeType,
			Extension: ext,
			Width:     image.Width,
			Height:    image.Height,
		})
	}
	return s.archiveImageBytes(ctx, record, byteImages)
}

func (s *ImageGenerationArchiveService) archiveImageBytes(ctx context.Context, record *ImageGenerationRecord, images []ArchivedImageBytesInput) error {
	if len(images) == 0 {
		return nil
	}
	enabled, err := s.IsArchiveEnabled(ctx)
	if err != nil {
		_ = s.appendError(ctx, record, err)
		return err
	}
	if !enabled {
		return ErrImageArchiveDisabled
	}
	storage, err := s.storageForCurrentConfig(ctx)
	if err != nil {
		_ = s.appendError(ctx, record, err)
		return err
	}
	record.Status = "running"
	_ = s.repo.UpdateRecord(ctx, record)
	storedCount := 0
	for _, image := range images {
		if len(image.Bytes) == 0 {
			_ = s.appendError(ctx, record, fmt.Errorf("image bytes are empty"))
			continue
		}
		mimeType := resolveImageMimeType(image.MimeType, image.Extension)
		ext := ensureExt(image.Extension, mimeType)
		if len(image.Bytes) > ImageArchiveMaxBytes {
			_ = s.appendError(ctx, record, fmt.Errorf("image exceeds max archive size: %d bytes", len(image.Bytes)))
			continue
		}
		normalizedMimeType, normalizedExt, err := NormalizeArchivedImageBytes(image.Bytes, mimeType, ext)
		if err != nil {
			_ = s.appendError(ctx, record, err)
			continue
		}
		mimeType = normalizedMimeType
		ext = normalizedExt
		stored, err := storage.Save(ctx, image.Bytes, StoredImageMeta{
			RecordID:   record.ID,
			AssetIndex: image.Index,
			MimeType:   mimeType,
			Extension:  ext,
			Width:      image.Width,
			Height:     image.Height,
		})
		if err != nil {
			_ = s.appendError(ctx, record, err)
			continue
		}
		asset := &ImageGenerationAsset{
			RecordID:   record.ID,
			AssetIndex: image.Index,
			MimeType:   stored.MimeType,
			Extension:  stored.Extension,
			Width:      image.Width,
			Height:     image.Height,
			Bytes:      stored.Bytes,
			SHA256:     stored.SHA256,
			StorageKey: stored.StorageKey,
			PublicURL:  stored.PublicURL,
			AdminURL:   stored.AdminURL,
		}
		if err := s.repo.CreateAsset(ctx, asset); err != nil {
			_ = s.appendError(ctx, record, err)
			continue
		}
		storedCount++
	}
	record.ImageCount = storedCount
	var archiveErr error
	if storedCount == 0 {
		record.Status = "failed"
		if strings.TrimSpace(record.ErrorMessage) == "" {
			record.ErrorMessage = "no images were archived"
		}
		archiveErr = errors.New(record.ErrorMessage)
	} else {
		record.Status = "completed"
	}
	now := time.Now().UTC()
	record.CompletedAt = &now
	record.StorageType = storageTypeOrDefault(storage)
	if err := s.repo.UpdateRecord(ctx, record); err != nil {
		return err
	}
	return archiveErr
}

func (s *ImageGenerationArchiveService) appendError(ctx context.Context, record *ImageGenerationRecord, err error) error {
	if record == nil || err == nil {
		return nil
	}
	record.Status = "failed"
	record.ErrorMessage = strings.TrimSpace(err.Error())
	now := time.Now().UTC()
	record.CompletedAt = &now
	return s.repo.UpdateRecord(ctx, record)
}

func (s *ImageGenerationArchiveService) updateRecordStatus(ctx context.Context, record *ImageGenerationRecord, status, msg string) error {
	if record == nil {
		return nil
	}
	record.Status = status
	record.ErrorMessage = msg
	now := time.Now().UTC()
	record.CompletedAt = &now
	return s.repo.UpdateRecord(ctx, record)
}

func storageTypeOrDefault(storage ImageGenerationStorage) string {
	if storage == nil {
		return "local"
	}
	switch storage.(type) {
	case *s3ImageArchiveStorage:
		return "s3"
	default:
		return "local"
	}
}

func decodeArchivedImage(image ArchivedImageInput) ([]byte, string, string, error) {
	raw := strings.TrimSpace(image.B64JSON)
	if raw == "" {
		return nil, "", "", fmt.Errorf("image payload is empty")
	}
	if strings.HasPrefix(strings.ToLower(raw), "data:") {
		if idx := strings.Index(raw, ","); idx >= 0 {
			raw = raw[idx+1:]
		}
	}
	raw = normalizeBase64Padding(raw)
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, "", "", err
	}
	if len(b) > ImageArchiveMaxBytes {
		return nil, "", "", fmt.Errorf("image exceeds max archive size: %d bytes", len(b))
	}
	mimeType, ext, err := NormalizeArchivedImageBytes(b, image.MimeType, image.Extension)
	if err != nil {
		return nil, "", "", err
	}
	return b, mimeType, ext, nil
}

func normalizeBase64Padding(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimRight(raw, "=")
	if raw == "" {
		return ""
	}
	return raw + strings.Repeat("=", (4-len(raw)%4)%4)
}

func resolveImageMimeType(mimeType, ext string) string {
	if strings.TrimSpace(mimeType) != "" {
		return normalizeMediaType(mimeType)
	}
	if ext != "" {
		if m := mime.TypeByExtension(ext); m != "" {
			return m
		}
	}
	return "image/png"
}

func NormalizeArchivedImageBytes(imageBytes []byte, mimeType, ext string) (string, string, error) {
	detected := detectArchivedImageMimeType(imageBytes)
	if !IsSafeImageContentType(detected) {
		return "", "", fmt.Errorf("archived image content type is not a supported image: %s", detected)
	}
	// Trust bytes over metadata; upstream-compatible APIs sometimes send a generic
	// MIME, but executable or mismatched metadata must not control the response.
	return detected, ensureSafeImageExt(ext, detected, true), nil
}

func SafeImageAssetContentType(contentType string) (string, bool) {
	normalized := normalizeMediaType(contentType)
	if IsSafeImageContentType(normalized) {
		return normalized, true
	}
	return "application/octet-stream", false
}

func IsSafeImageContentType(contentType string) bool {
	switch normalizeMediaType(contentType) {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

func normalizeMediaType(contentType string) string {
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		return ""
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err == nil {
		return strings.ToLower(strings.TrimSpace(mediaType))
	}
	if idx := strings.Index(contentType, ";"); idx >= 0 {
		contentType = contentType[:idx]
	}
	return strings.ToLower(strings.TrimSpace(contentType))
}

func detectArchivedImageMimeType(imageBytes []byte) string {
	if len(imageBytes) >= 12 && string(imageBytes[0:4]) == "RIFF" && string(imageBytes[8:12]) == "WEBP" {
		return "image/webp"
	}
	return normalizeMediaType(http.DetectContentType(imageBytes))
}

func ensureSafeImageExt(ext, mimeType string, inline bool) string {
	if !inline {
		return ".bin"
	}
	return ensureExt("", mimeType)
}

func ensureExt(ext, mimeType string) string {
	ext = strings.TrimSpace(ext)
	if ext != "" {
		if !strings.HasPrefix(ext, ".") {
			return "." + ext
		}
		return ext
	}
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
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

func joinURLPath(base, rel string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	rel = strings.TrimLeft(filepath.ToSlash(rel), "/")
	if base == "" {
		return ""
	}
	return base + "/" + rel
}

func safeLocalImageArchivePath(baseDir, storageKey string) (string, error) {
	baseDir = strings.TrimSpace(baseDir)
	storageKey = strings.TrimSpace(storageKey)
	if baseDir == "" || storageKey == "" {
		return "", fmt.Errorf("image archive path is empty")
	}
	cleanKey := filepath.Clean(storageKey)
	if filepath.IsAbs(cleanKey) || cleanKey == "." || cleanKey == ".." || strings.HasPrefix(cleanKey, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid image archive storage key")
	}
	baseAbs, err := filepath.Abs(baseDir)
	if err != nil {
		return "", err
	}
	pathAbs, err := filepath.Abs(filepath.Join(baseAbs, cleanKey))
	if err != nil {
		return "", err
	}
	if pathAbs != baseAbs && !strings.HasPrefix(pathAbs, baseAbs+string(filepath.Separator)) {
		return "", fmt.Errorf("image archive storage key escapes base directory")
	}
	return pathAbs, nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func defaultString(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return strings.TrimSpace(v)
}

func truncateRunes(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}
