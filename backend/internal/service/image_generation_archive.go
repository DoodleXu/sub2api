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
	"mime"
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
)

const (
	ImageArchiveMaxBytes          = 32 << 20
	ImageAssetScopeAdmin          = "admin-image-generation"
	ImageAssetScopeWebConsole     = "web-console-image-task"
	imageArchiveAssetTokenTTL     = 15 * time.Minute
	imageArchiveAssetTokenLeeway  = time.Minute
	imageArchiveAssetTokenVersion = "v1"
)

type ImageGenerationArchiveRepository interface {
	CreateRecord(ctx context.Context, record *ImageGenerationRecord) error
	UpdateRecord(ctx context.Context, record *ImageGenerationRecord) error
	GetRecordByID(ctx context.Context, id int64) (*ImageGenerationRecord, []*ImageGenerationAsset, error)
	ListRecords(ctx context.Context, params ImageGenerationRecordListParams) ([]*ImageGenerationRecord, *ImageGenerationRecordListResult, error)
	ListDailyStats(ctx context.Context, params ImageGenerationRecordDailyStatsParams) ([]ImageGenerationDailyStat, error)
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

type ImageGenerationRecordDailyStatsParams struct {
	StartDate time.Time
	EndDate   time.Time
}

type ImageGenerationStorage interface {
	Save(ctx context.Context, imageBytes []byte, meta StoredImageMeta) (*StoredImage, error)
	ResolveURL(ctx context.Context, stored *StoredImage, download bool) string
	Open(ctx context.Context, storageKey string) (io.ReadCloser, string, error)
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

func (s *s3ImageArchiveStorage) publicURLForKey(key string) string {
	if s == nil || strings.TrimSpace(s.publicBaseURL) == "" {
		return ""
	}
	return joinURLPath(s.publicBaseURL, key)
}

type ImageGenerationArchiveService struct {
	repo          ImageGenerationArchiveRepository
	settingRepo   SettingRepository
	apiKeyService APIKeyRepository
	storage       ImageGenerationStorage
	workerLimit   chan struct{}
	cfg           *config.Config
}

func NewImageGenerationArchiveService(repo ImageGenerationArchiveRepository, settingRepo SettingRepository, apiKeyService APIKeyRepository, cfg *config.Config) *ImageGenerationArchiveService {
	svc := &ImageGenerationArchiveService{
		repo:          repo,
		settingRepo:   settingRepo,
		apiKeyService: apiKeyService,
		workerLimit:   make(chan struct{}, 4),
		cfg:           cfg,
	}
	svc.storage = newLocalImageArchiveStorage(defaultImageArchiveLocalDir(cfg), "")
	return svc
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
	if strings.TrimSpace(contentType) == "" {
		contentType = resolveImageMimeType(asset.MimeType, asset.Extension)
	}
	filename := fmt.Sprintf("image-%d%s", asset.ID, ensureExt(asset.Extension, contentType))
	return &ImageGenerationAssetReader{
		Body:        body,
		ContentType: contentType,
		Size:        asset.Bytes,
		Filename:    filename,
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
		_ = s.archiveBase64Images(context.Background(), record, images)
	}()
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
	mimeType := resolveImageMimeType(image.MimeType, image.Extension)
	ext := ensureExt(image.Extension, mimeType)
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
		return strings.TrimSpace(mimeType)
	}
	if ext != "" {
		if m := mime.TypeByExtension(ext); m != "" {
			return m
		}
	}
	return "image/png"
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
