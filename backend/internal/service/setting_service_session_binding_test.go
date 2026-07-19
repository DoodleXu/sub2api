//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func resetSessionBindingTestCache(t *testing.T) {
	t.Helper()
}

func TestRefreshCachedSettingsCannotBeOverwrittenByInflightSessionBindingRead(t *testing.T) {
	resetSessionBindingTestCache(t)
	started := make(chan struct{})
	release := make(chan struct{})
	repo := &bmRepoStub{getValueFn: func(_ context.Context, key string) (string, error) {
		require.Equal(t, SettingKeySessionBindingEnabled, key)
		close(started)
		<-release
		return "false", nil
	}}
	svc := NewSettingService(repo, &config.Config{})
	result := make(chan bool, 1)
	go func() { result <- svc.IsSessionBindingEnabled(context.Background()) }()
	<-started

	svc.refreshCachedSettings(&SystemSettings{SessionBindingEnabled: true})
	close(release)

	require.True(t, <-result)
	require.True(t, svc.IsSessionBindingEnabled(context.Background()))
}

func TestIsSessionBindingEnabledCachesResult(t *testing.T) {
	resetSessionBindingTestCache(t)
	repo := &bmRepoStub{getValueFn: func(_ context.Context, key string) (string, error) {
		require.Equal(t, SettingKeySessionBindingEnabled, key)
		return "false", nil
	}}
	svc := NewSettingService(repo, &config.Config{})

	require.False(t, svc.IsSessionBindingEnabled(context.Background()))
	require.False(t, svc.IsSessionBindingEnabled(context.Background()))
	require.Equal(t, 1, repo.calls)
}

func TestIsSessionBindingEnabledFailsSecurelyAndCachesErrorsBriefly(t *testing.T) {
	resetSessionBindingTestCache(t)
	repo := &bmRepoStub{getValueFn: func(_ context.Context, _ string) (string, error) {
		return "", errors.New("database unavailable")
	}}
	svc := NewSettingService(repo, &config.Config{})

	require.True(t, svc.IsSessionBindingEnabled(context.Background()))
	require.True(t, svc.IsSessionBindingEnabled(context.Background()))
	require.Equal(t, 1, repo.calls)
	cached, ok := svc.sessionBindingCache.Load().(*cachedSessionBinding)
	require.True(t, ok)
	require.LessOrEqual(t, cached.expiresAt, time.Now().Add(sessionBindingErrorTTL).UnixNano())
}

func TestRefreshCachedSettingsUpdatesSessionBindingImmediately(t *testing.T) {
	resetSessionBindingTestCache(t)
	svc := NewSettingService(&bmRepoStub{}, &config.Config{})
	svc.refreshCachedSettings(&SystemSettings{SessionBindingEnabled: false})

	require.False(t, svc.IsSessionBindingEnabled(context.Background()))
}
