package repository

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/servertiming"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// S3ImageStorage 用 S3 兼容对象存储实现 service.ImageStorage。
type S3ImageStorage struct {
	client        *s3.Client
	bucket        string
	publicBaseURL string
	presignExpiry time.Duration
}

var _ service.ImageStorage = (*S3ImageStorage)(nil)
var _ service.ImageStorageBrowser = (*S3ImageStorage)(nil)

// NewS3ImageStorage 依据配置构造 S3 图片存储（调用方应先确认 cfg.Active()）。
func NewS3ImageStorage(ctx context.Context, cfg *config.ImageStorageConfig) (*S3ImageStorage, error) {
	client, err := newS3Client(ctx, s3ClientParams{
		Endpoint:        cfg.Endpoint,
		Region:          cfg.Region,
		AccessKeyID:     cfg.AccessKeyID,
		SecretAccessKey: cfg.SecretAccessKey,
		ForcePathStyle:  cfg.ForcePathStyle,
	})
	if err != nil {
		return nil, err
	}

	expiry := time.Duration(cfg.PresignExpiry) * time.Hour
	if expiry <= 0 {
		expiry = 24 * time.Hour
	}
	retentionDays := cfg.LifecycleExpirationDays
	if retentionDays <= 0 {
		retentionDays = 2
	}
	if err := requireImageLifecycle(ctx, client, cfg.Bucket, cfg.Prefix, retentionDays); err != nil {
		return nil, err
	}

	return &S3ImageStorage{
		client:        client,
		bucket:        cfg.Bucket,
		publicBaseURL: strings.TrimRight(cfg.PublicBaseURL, "/"),
		presignExpiry: expiry,
	}, nil
}

// requireImageLifecycle prevents the async image feature from silently creating
// permanent public objects. We only validate here and never overwrite existing
// bucket rules, so operators retain control of provider-specific lifecycle policy.
func requireImageLifecycle(ctx context.Context, client *s3.Client, bucket, prefix string, minimumDays int) error {
	result, err := client.GetBucketLifecycleConfiguration(ctx, &s3.GetBucketLifecycleConfigurationInput{Bucket: &bucket})
	if err != nil {
		return fmt.Errorf("image bucket lifecycle configuration is required: %w", err)
	}
	for _, rule := range result.Rules {
		if imageLifecycleRuleCoversPrefix(rule, prefix, minimumDays) {
			return nil
		}
	}
	return fmt.Errorf("image bucket lifecycle has no enabled expiration rule for prefix %q with at least %d days", prefix, minimumDays)
}

func imageLifecycleRuleCoversPrefix(rule types.LifecycleRule, prefix string, minimumDays int) bool {
	if rule.Status != types.ExpirationStatusEnabled || rule.Expiration == nil || rule.Expiration.Days == nil || int(*rule.Expiration.Days) < minimumDays {
		return false
	}
	if rule.Filter == nil {
		//nolint:staticcheck // S3-compatible providers may still return the deprecated top-level Prefix response field.
		if rule.Prefix == nil {
			return true
		}
		//nolint:staticcheck // See the compatibility note above.
		return strings.HasPrefix(prefix, *rule.Prefix)
	}
	filter := rule.Filter
	if filter.Prefix != nil {
		return strings.HasPrefix(prefix, *filter.Prefix)
	}
	// An empty Filter applies to the whole bucket. Tag, size, and And filters
	// cannot guarantee coverage because generated objects carry no tags and have
	// variable sizes.
	return filter.And == nil && filter.Tag == nil && filter.ObjectSizeGreaterThan == nil && filter.ObjectSizeLessThan == nil
}

// Save 上传图片字节，返回可访问 URL：配了 public_base_url 则返回公开直链，否则返回 presigned 临时链接。
func (s *S3ImageStorage) Save(ctx context.Context, key, contentType string, data []byte) (string, error) {
	finish := servertiming.ObserveDependency(ctx, "s3")
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &s.bucket,
		Key:         &key,
		Body:        bytes.NewReader(data),
		ContentType: &contentType,
	})
	finish()
	if err != nil {
		putErr := fmt.Errorf("S3 PutObject: %w", err)
		// The server may have committed the object before a timeout/disconnect was
		// observed. Delay and repeat compensation so an immediate successful
		// DeleteObject cannot race ahead of a late server-side commit. Async image
		// tasks also persist this unique key before PutObject for durable retries.
		if deleteErr := s.cleanupAmbiguousPut(key); deleteErr != nil {
			return "", errors.Join(putErr, fmt.Errorf("cleanup object after ambiguous put: %w", deleteErr))
		}
		return "", putErr
	}

	if s.publicBaseURL != "" {
		return s.publicBaseURL + "/" + strings.TrimLeft(key, "/"), nil
	}

	presignClient := s3.NewPresignClient(s.client)
	result, err := presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
	}, s3.WithPresignExpires(s.presignExpiry))
	if err != nil {
		presignErr := fmt.Errorf("presign url: %w", err)
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, deleteErr := s.client.DeleteObject(cleanupCtx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: &key}); deleteErr != nil {
			return "", errors.Join(presignErr, fmt.Errorf("cleanup object after presign failure: %w", deleteErr))
		}
		return "", presignErr
	}
	return result.URL, nil
}

func (s *S3ImageStorage) cleanupAmbiguousPut(key string) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var lastErr error
	for _, delay := range []time.Duration{250 * time.Millisecond, time.Second} {
		timer := time.NewTimer(delay)
		select {
		case <-cleanupCtx.Done():
			timer.Stop()
			return errors.Join(lastErr, cleanupCtx.Err())
		case <-timer.C:
		}
		_, lastErr = s.client.DeleteObject(cleanupCtx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: &key})
	}
	return lastErr
}

// Delete 删除未被任务终态引用的补偿对象。
func (s *S3ImageStorage) Delete(ctx context.Context, key string) error {
	finish := servertiming.ObserveDependency(ctx, "s3")
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
	})
	finish()
	if err != nil {
		return fmt.Errorf("S3 DeleteObject: %w", err)
	}
	return nil
}

type imageStorageObjectCursor struct {
	LastModified int64  `json:"last_modified"`
	Key          string `json:"key"`
}

// List exposes objects already written by upstream async image tasks. S3 only
// lists by key, so the complete prefix must be read before sorting globally by
// object write time and applying cursor pagination.
func (s *S3ImageStorage) List(ctx context.Context, prefix, cursor string, limit int) (*service.ImageStorageObjectPage, error) {
	if limit <= 0 || limit > 200 {
		limit = 60
	}
	maxKeys := int32(1000)
	input := &s3.ListObjectsV2Input{
		Bucket:  &s.bucket,
		Prefix:  &prefix,
		MaxKeys: &maxKeys,
	}
	objects := make([]service.ImageStorageObject, 0, maxKeys)
	paginator := s3.NewListObjectsV2Paginator(s.client, input)
	for paginator.HasMorePages() {
		result, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("S3 ListObjectsV2: %w", err)
		}
		for _, object := range result.Contents {
			if object.Key == nil || strings.HasSuffix(*object.Key, "/") {
				continue
			}
			item := service.ImageStorageObject{Key: *object.Key}
			if object.Size != nil {
				item.Size = *object.Size
			}
			if object.ETag != nil {
				item.ETag = strings.Trim(*object.ETag, "\"")
			}
			if object.LastModified != nil {
				item.LastModified = *object.LastModified
			}
			objects = append(objects, item)
		}
	}

	page, err := buildImageStorageObjectPage(objects, cursor, limit)
	if err != nil {
		return nil, err
	}
	presignClient := s3.NewPresignClient(s.client)
	for index := range page.Items {
		item := &page.Items[index]
		if s.publicBaseURL != "" {
			item.URL = s.publicBaseURL + "/" + strings.TrimLeft(item.Key, "/")
			continue
		}
		presigned, presignErr := presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
			Bucket: &s.bucket,
			Key:    &item.Key,
		}, s3.WithPresignExpires(s.presignExpiry))
		if presignErr != nil {
			return nil, fmt.Errorf("presign image object %q: %w", item.Key, presignErr)
		}
		item.URL = presigned.URL
	}
	return page, nil
}

func buildImageStorageObjectPage(objects []service.ImageStorageObject, cursor string, limit int) (*service.ImageStorageObjectPage, error) {
	sort.Slice(objects, func(i, j int) bool {
		leftTime := imageStorageObjectTime(objects[i])
		rightTime := imageStorageObjectTime(objects[j])
		if leftTime != rightTime {
			return leftTime > rightTime
		}
		return objects[i].Key > objects[j].Key
	})

	decodedCursor, hasCursor, err := decodeImageStorageObjectCursor(cursor)
	if err != nil {
		return nil, err
	}
	start := 0
	if hasCursor {
		start = sort.Search(len(objects), func(index int) bool {
			objectTime := imageStorageObjectTime(objects[index])
			return objectTime < decodedCursor.LastModified ||
				(objectTime == decodedCursor.LastModified && objects[index].Key < decodedCursor.Key)
		})
	}
	end := min(start+limit, len(objects))
	items := append([]service.ImageStorageObject(nil), objects[start:end]...)
	page := &service.ImageStorageObjectPage{
		Items:      items,
		HasMore:    end < len(objects),
		TotalCount: int64(len(objects)),
	}
	if page.HasMore && len(items) > 0 {
		page.NextCursor = encodeImageStorageObjectCursor(items[len(items)-1])
	}
	return page, nil
}

func imageStorageObjectTime(object service.ImageStorageObject) int64 {
	if object.LastModified.IsZero() || object.LastModified.Before(time.Unix(0, 0)) {
		return 0
	}
	return object.LastModified.UnixMilli()
}

func encodeImageStorageObjectCursor(object service.ImageStorageObject) string {
	payload, _ := json.Marshal(imageStorageObjectCursor{
		LastModified: imageStorageObjectTime(object),
		Key:          object.Key,
	})
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodeImageStorageObjectCursor(value string) (imageStorageObjectCursor, bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return imageStorageObjectCursor{}, false, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return imageStorageObjectCursor{}, false, infraerrors.BadRequest("INVALID_IMAGE_STORAGE_CURSOR", "invalid image storage cursor")
	}
	var cursor imageStorageObjectCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || cursor.LastModified < 0 || strings.TrimSpace(cursor.Key) == "" {
		return imageStorageObjectCursor{}, false, infraerrors.BadRequest("INVALID_IMAGE_STORAGE_CURSOR", "invalid image storage cursor")
	}
	return cursor, true, nil
}
