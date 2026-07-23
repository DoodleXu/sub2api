package repository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/Wei-Shaw/sub2api/internal/config"
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

// List exposes objects already written by upstream async image tasks. It is
// intentionally read-only; lifecycle rules remain the owner of retention.
func (s *S3ImageStorage) List(ctx context.Context, prefix, cursor string, limit int) (*service.ImageStorageObjectPage, error) {
	if limit <= 0 || limit > 200 {
		limit = 60
	}
	maxKeys := int32(limit)
	input := &s3.ListObjectsV2Input{
		Bucket:  &s.bucket,
		Prefix:  &prefix,
		MaxKeys: &maxKeys,
	}
	if strings.TrimSpace(cursor) != "" {
		input.ContinuationToken = &cursor
	}
	result, err := s.client.ListObjectsV2(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("S3 ListObjectsV2: %w", err)
	}
	page := &service.ImageStorageObjectPage{
		Items:   make([]service.ImageStorageObject, 0, len(result.Contents)),
		HasMore: result.IsTruncated != nil && *result.IsTruncated,
	}
	if result.NextContinuationToken != nil {
		page.NextCursor = *result.NextContinuationToken
	}
	presignClient := s3.NewPresignClient(s.client)
	for _, object := range result.Contents {
		if object.Key == nil || strings.HasSuffix(*object.Key, "/") {
			continue
		}
		objectURL := ""
		if s.publicBaseURL != "" {
			objectURL = s.publicBaseURL + "/" + strings.TrimLeft(*object.Key, "/")
		} else {
			presigned, presignErr := presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
				Bucket: &s.bucket,
				Key:    object.Key,
			}, s3.WithPresignExpires(s.presignExpiry))
			if presignErr != nil {
				return nil, fmt.Errorf("presign image object %q: %w", *object.Key, presignErr)
			}
			objectURL = presigned.URL
		}
		item := service.ImageStorageObject{Key: *object.Key, URL: objectURL}
		if object.Size != nil {
			item.Size = *object.Size
		}
		if object.ETag != nil {
			item.ETag = strings.Trim(*object.ETag, "\"")
		}
		if object.LastModified != nil {
			item.LastModified = *object.LastModified
		}
		page.Items = append(page.Items, item)
	}
	return page, nil
}
