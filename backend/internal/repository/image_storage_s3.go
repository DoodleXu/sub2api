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
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/servertiming"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"golang.org/x/sync/singleflight"
)

const imageStorageObjectCacheTTL = 30 * time.Second

// S3ImageStorage 用 S3 兼容对象存储实现 service.ImageStorage。
type S3ImageStorage struct {
	client        *s3.Client
	bucket        string
	publicBaseURL string
	presignExpiry time.Duration

	objectCacheMu sync.Mutex
	objectCache   map[string]imageStorageObjectCacheEntry
	objectVersion uint64
	objectLoad    singleflight.Group
}

type imageStorageObjectCacheEntry struct {
	objects   []service.ImageStorageObject
	expiresAt time.Time
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
		objectCache:   make(map[string]imageStorageObjectCacheEntry),
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
	defer s.invalidateObjectCacheForKey(key)
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
	defer s.invalidateObjectCacheForKey(key)
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
// lists by key, so the complete prefix is cached briefly after a globally
// time-sorted scan. Cursor pages reuse that immutable inventory rather than
// rescanning the full prefix for every page.
func (s *S3ImageStorage) List(ctx context.Context, prefix, cursor string, limit int) (*service.ImageStorageObjectPage, error) {
	if limit <= 0 || limit > 200 {
		limit = 60
	}
	objects, err := s.cachedObjects(ctx, prefix)
	if err != nil {
		return nil, err
	}
	page, err := buildImageStorageObjectPageFromSorted(objects, cursor, limit)
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

// ResolveURLs generates current access URLs for known object keys without
// rescanning the bucket. Callers remain responsible for constraining keys to
// their configured namespace before invoking this method.
func (s *S3ImageStorage) ResolveURLs(ctx context.Context, keys []string) (map[string]string, error) {
	resolved := make(map[string]string, len(keys))
	if len(keys) == 0 {
		return resolved, nil
	}
	presignClient := s3.NewPresignClient(s.client)
	for _, rawKey := range keys {
		key := strings.TrimSpace(rawKey)
		if key == "" {
			continue
		}
		if s.publicBaseURL != "" {
			resolved[key] = s.publicBaseURL + "/" + strings.TrimLeft(key, "/")
			continue
		}
		result, err := presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
			Bucket: &s.bucket,
			Key:    &key,
		}, s3.WithPresignExpires(s.presignExpiry))
		if err != nil {
			return nil, fmt.Errorf("presign image object %q: %w", key, err)
		}
		resolved[key] = result.URL
	}
	return resolved, nil
}

func (s *S3ImageStorage) cachedObjects(ctx context.Context, prefix string) ([]service.ImageStorageObject, error) {
	if objects, ok := s.cachedObjectsForPrefix(prefix, time.Now()); ok {
		return objects, nil
	}
	loaded, err, _ := s.objectLoad.Do(prefix, func() (any, error) {
		if objects, ok := s.cachedObjectsForPrefix(prefix, time.Now()); ok {
			return objects, nil
		}
		version := s.objectCacheVersion()
		objects, err := s.listObjects(ctx, prefix)
		if err != nil {
			return nil, err
		}
		sortImageStorageObjects(objects)
		s.storeCachedObjectsIfVersion(prefix, objects, time.Now(), version)
		return objects, nil
	})
	if err != nil {
		return nil, err
	}
	objects, ok := loaded.([]service.ImageStorageObject)
	if !ok {
		return nil, fmt.Errorf("unexpected image storage object cache result type %T", loaded)
	}
	return objects, nil
}

func (s *S3ImageStorage) listObjects(ctx context.Context, prefix string) ([]service.ImageStorageObject, error) {
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

	return objects, nil
}

func (s *S3ImageStorage) cachedObjectsForPrefix(prefix string, now time.Time) ([]service.ImageStorageObject, bool) {
	s.objectCacheMu.Lock()
	defer s.objectCacheMu.Unlock()
	entry, ok := s.objectCache[prefix]
	if !ok || !now.Before(entry.expiresAt) {
		return nil, false
	}
	return entry.objects, true
}

func (s *S3ImageStorage) storeCachedObjects(prefix string, objects []service.ImageStorageObject, now time.Time) {
	s.objectCacheMu.Lock()
	defer s.objectCacheMu.Unlock()
	s.storeCachedObjectsLocked(prefix, objects, now)
}

func (s *S3ImageStorage) storeCachedObjectsIfVersion(prefix string, objects []service.ImageStorageObject, now time.Time, version uint64) {
	s.objectCacheMu.Lock()
	defer s.objectCacheMu.Unlock()
	if s.objectVersion != version {
		return
	}
	s.storeCachedObjectsLocked(prefix, objects, now)
}

func (s *S3ImageStorage) storeCachedObjectsLocked(prefix string, objects []service.ImageStorageObject, now time.Time) {
	if s.objectCache == nil {
		s.objectCache = make(map[string]imageStorageObjectCacheEntry)
	}
	for cachedPrefix, entry := range s.objectCache {
		if !now.Before(entry.expiresAt) {
			delete(s.objectCache, cachedPrefix)
		}
	}
	s.objectCache[prefix] = imageStorageObjectCacheEntry{objects: objects, expiresAt: now.Add(imageStorageObjectCacheTTL)}
}

func (s *S3ImageStorage) objectCacheVersion() uint64 {
	s.objectCacheMu.Lock()
	defer s.objectCacheMu.Unlock()
	return s.objectVersion
}

func (s *S3ImageStorage) invalidateObjectCacheForKey(key string) {
	if s == nil {
		return
	}
	s.objectCacheMu.Lock()
	defer s.objectCacheMu.Unlock()
	s.objectVersion++
	for prefix := range s.objectCache {
		if strings.HasPrefix(key, prefix) {
			delete(s.objectCache, prefix)
		}
	}
}

func buildImageStorageObjectPage(objects []service.ImageStorageObject, cursor string, limit int) (*service.ImageStorageObjectPage, error) {
	sortImageStorageObjects(objects)
	return buildImageStorageObjectPageFromSorted(objects, cursor, limit)
}

func sortImageStorageObjects(objects []service.ImageStorageObject) {
	sort.Slice(objects, func(i, j int) bool {
		leftTime := imageStorageObjectTime(objects[i])
		rightTime := imageStorageObjectTime(objects[j])
		if leftTime != rightTime {
			return leftTime > rightTime
		}
		return objects[i].Key > objects[j].Key
	})
}

func buildImageStorageObjectPageFromSorted(objects []service.ImageStorageObject, cursor string, limit int) (*service.ImageStorageObjectPage, error) {
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
