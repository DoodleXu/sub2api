package repository

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/require"
)

func TestImageLifecycleRuleCoversPrefix(t *testing.T) {
	days := int32(2)
	shortDays := int32(1)
	rootPrefix := "images/"
	exactPrefix := "images/generated/"
	narrowPrefix := "images/generated/narrow/"
	objectSize := int64(1)
	enabledRule := func() types.LifecycleRule {
		return types.LifecycleRule{
			Status:     types.ExpirationStatusEnabled,
			Expiration: &types.LifecycleExpiration{Days: &days},
		}
	}

	tests := []struct {
		name string
		rule types.LifecycleRule
		want bool
	}{
		{name: "unfiltered rule", rule: enabledRule(), want: true},
		{name: "empty filter", rule: func() types.LifecycleRule {
			rule := enabledRule()
			rule.Filter = &types.LifecycleRuleFilter{}
			return rule
		}(), want: true},
		{name: "broader prefix", rule: func() types.LifecycleRule {
			rule := enabledRule()
			rule.Filter = &types.LifecycleRuleFilter{Prefix: &rootPrefix}
			return rule
		}(), want: true},
		{name: "exact prefix", rule: func() types.LifecycleRule {
			rule := enabledRule()
			rule.Filter = &types.LifecycleRuleFilter{Prefix: &exactPrefix}
			return rule
		}(), want: true},
		{name: "narrower prefix", rule: func() types.LifecycleRule {
			rule := enabledRule()
			rule.Filter = &types.LifecycleRuleFilter{Prefix: &narrowPrefix}
			return rule
		}()},
		{name: "tag filter", rule: func() types.LifecycleRule {
			rule := enabledRule()
			rule.Filter = &types.LifecycleRuleFilter{Tag: &types.Tag{}}
			return rule
		}()},
		{name: "size filter", rule: func() types.LifecycleRule {
			rule := enabledRule()
			rule.Filter = &types.LifecycleRuleFilter{ObjectSizeGreaterThan: &objectSize}
			return rule
		}()},
		{name: "and filter", rule: func() types.LifecycleRule {
			rule := enabledRule()
			rule.Filter = &types.LifecycleRuleFilter{And: &types.LifecycleRuleAndOperator{Prefix: &rootPrefix, Tags: []types.Tag{{}}}}
			return rule
		}()},
		{name: "legacy broader prefix", rule: func() types.LifecycleRule {
			rule := enabledRule()
			//nolint:staticcheck // Exercise compatibility with deprecated Prefix responses from S3-compatible providers.
			rule.Prefix = &rootPrefix
			return rule
		}(), want: true},
		{name: "disabled", rule: func() types.LifecycleRule {
			rule := enabledRule()
			rule.Status = types.ExpirationStatusDisabled
			return rule
		}()},
		{name: "retention too short", rule: types.LifecycleRule{
			Status:     types.ExpirationStatusEnabled,
			Expiration: &types.LifecycleExpiration{Days: &shortDays},
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, imageLifecycleRuleCoversPrefix(tt.rule, exactPrefix, 2))
		})
	}
}

func TestBuildImageStorageObjectPageSortsNewestFirstAndReportsTotal(t *testing.T) {
	objects := []service.ImageStorageObject{
		{Key: "images/old.png", LastModified: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)},
		{Key: "images/new.png", LastModified: time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)},
		{Key: "images/tie-a.png", LastModified: time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)},
		{Key: "images/tie-b.png", LastModified: time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)},
	}

	first, err := buildImageStorageObjectPage(objects, "", 2)
	require.NoError(t, err)
	require.Equal(t, int64(4), first.TotalCount)
	require.True(t, first.HasMore)
	require.Equal(t, []string{"images/new.png", "images/tie-b.png"}, imageStorageObjectKeys(first.Items))

	last, err := buildImageStorageObjectPage(objects, first.NextCursor, 2)
	require.NoError(t, err)
	require.Equal(t, int64(4), last.TotalCount)
	require.False(t, last.HasMore)
	require.Empty(t, last.NextCursor)
	require.Equal(t, []string{"images/tie-a.png", "images/old.png"}, imageStorageObjectKeys(last.Items))
}

func TestBuildImageStorageObjectPageRejectsInvalidCursor(t *testing.T) {
	_, err := buildImageStorageObjectPage(nil, "not-a-valid-cursor", 60)
	require.Error(t, err)
}

func TestImageStorageObjectCacheReusesAndInvalidatesMatchingPrefixes(t *testing.T) {
	now := time.Now().UTC()
	storage := &S3ImageStorage{}
	objects := []service.ImageStorageObject{{Key: "images/generated/one.png", LastModified: now}}
	storage.storeCachedObjects("images/", objects, now)
	storage.storeCachedObjects("images/generated/", objects, now)
	storage.storeCachedObjects("other/", objects, now)

	cached, ok := storage.cachedObjectsForPrefix("images/", now.Add(time.Second))
	require.True(t, ok)
	require.Equal(t, objects, cached)

	storage.invalidateObjectCacheForKey("images/generated/two.png")
	_, ok = storage.cachedObjectsForPrefix("images/", now.Add(time.Second))
	require.False(t, ok)
	_, ok = storage.cachedObjectsForPrefix("images/generated/", now.Add(time.Second))
	require.False(t, ok)
	_, ok = storage.cachedObjectsForPrefix("other/", now.Add(time.Second))
	require.True(t, ok)
}

func TestImageStorageObjectCacheExpires(t *testing.T) {
	now := time.Now().UTC()
	storage := &S3ImageStorage{}
	storage.storeCachedObjects("images/", []service.ImageStorageObject{{Key: "images/one.png"}}, now)

	_, ok := storage.cachedObjectsForPrefix("images/", now.Add(imageStorageObjectCacheTTL))
	require.False(t, ok)
}

func imageStorageObjectKeys(objects []service.ImageStorageObject) []string {
	keys := make([]string, 0, len(objects))
	for _, object := range objects {
		keys = append(keys, object.Key)
	}
	return keys
}
