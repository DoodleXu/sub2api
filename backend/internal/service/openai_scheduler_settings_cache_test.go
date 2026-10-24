//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAISchedulerSettingsSaveUpdatesActiveCache(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
	repo := &settingUpdateRepoStub{values: map[string]string{openAIAdvancedSchedulerSettingKey: "false"}}
	settings := NewSettingService(repo, &config.Config{})
	rl := NewRateLimitService(nil, nil, nil, nil, nil)
	rl.SetSettingService(settings)
	gateway := &OpenAIGatewayService{rateLimitService: rl}
	ctx := context.Background()
	require.False(t, gateway.isOpenAIAdvancedSchedulerEnabled(ctx))
	require.NoError(t, settings.UpdateSettings(ctx, &SystemSettings{OpenAIAdvancedSchedulerEnabled: true, OpenAIAdvancedSchedulerLBTopK: "7"}))
	current := gateway.openAIAdvancedSchedulerRuntimeSettings(ctx)
	require.True(t, current.enabled, "saving must publish into the cache actually read by scheduling")
	require.Equal(t, 7, current.lbTopKOverride)
}

type schedulerBlockedSettingsRepo struct {
	SettingRepository
	started chan struct{}
	release chan struct{}
}

func (r *schedulerBlockedSettingsRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	close(r.started)
	<-r.release
	return map[string]string{openAIAdvancedSchedulerSettingKey: "false"}, nil
}

func TestOpenAISchedulerInflightReadCannotOverwriteSavedSettings(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty cache", true: "expired cache"}[expired], func(t *testing.T) {
			resetOpenAIAdvancedSchedulerSettingCacheForTest()
			t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
			repo := &schedulerBlockedSettingsRepo{started: make(chan struct{}), release: make(chan struct{})}
			t.Cleanup(func() {
				select {
				case <-repo.release:
				default:
					close(repo.release)
				}
			})
			settings := NewSettingService(repo, &config.Config{})
			rl := NewRateLimitService(nil, nil, nil, nil, nil)
			rl.SetSettingService(settings)
			gateway := &OpenAIGatewayService{rateLimitService: rl}
			key := openAIAdvancedSchedulerSettingRepoKey(repo)
			if expired {
				openAIAdvancedSchedulerSettingCaches.Store(key, &cachedOpenAIAdvancedSchedulerSetting{repoKey: key, repoRef: repo, expiresAt: time.Now().Add(-time.Second).UnixNano()})
			}
			done := make(chan openAIAdvancedSchedulerRuntimeSettings, 1)
			go func() { done <- gateway.openAIAdvancedSchedulerRuntimeSettings(context.Background()) }()
			select {
			case <-repo.started:
			case <-time.After(3 * time.Second):
				t.Fatal("settings read did not start")
			}
			settings.refreshCachedSettings(&SystemSettings{OpenAIAdvancedSchedulerEnabled: true, OpenAIAdvancedSchedulerLBTopK: "9"})
			close(repo.release)
			select {
			case result := <-done:
				require.True(t, result.enabled, "the pending request must also use the saved settings")
				require.Equal(t, 9, result.lbTopKOverride)
			case <-time.After(3 * time.Second):
				t.Fatal("settings read did not finish")
			}
			current := gateway.openAIAdvancedSchedulerRuntimeSettings(context.Background())
			require.True(t, current.enabled)
			require.Equal(t, 9, current.lbTopKOverride, "late database reads must not replace a completed settings save")
		})
	}
}

func TestOpenAISchedulerSettingsSavePreservesOtherRepository(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
	newGateway := func(repo SettingRepository) (*SettingService, *OpenAIGatewayService) {
		settings := NewSettingService(repo, &config.Config{})
		rl := NewRateLimitService(nil, nil, nil, nil, nil)
		rl.SetSettingService(settings)
		return settings, &OpenAIGatewayService{rateLimitService: rl}
	}
	settings, first := newGateway(&settingUpdateRepoStub{values: map[string]string{openAIAdvancedSchedulerSettingKey: "false"}})
	_, second := newGateway(&settingUpdateRepoStub{values: map[string]string{openAIAdvancedSchedulerSettingKey: "false"}})
	ctx := context.Background()
	require.False(t, first.isOpenAIAdvancedSchedulerEnabled(ctx))
	require.False(t, second.isOpenAIAdvancedSchedulerEnabled(ctx))
	settings.refreshCachedSettings(&SystemSettings{OpenAIAdvancedSchedulerEnabled: true, OpenAIAdvancedSchedulerWeightLoad: "2"})
	current := first.openAIAdvancedSchedulerRuntimeSettings(ctx)
	require.True(t, current.enabled)
	require.False(t, second.isOpenAIAdvancedSchedulerEnabled(ctx))
	for key := range current.weightOverrides {
		current.weightOverrides[key] = 99
	}
	require.NotEqual(t, current.weightOverrides, first.openAIAdvancedSchedulerRuntimeSettings(ctx).weightOverrides, "request snapshots must not mutate the published settings")
}
