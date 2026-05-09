package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type updateServiceTestCache struct {
	data string
}

func (c *updateServiceTestCache) GetUpdateInfo(context.Context) (string, error) {
	if c.data == "" {
		return "", errUpdateServiceTestCacheMiss{}
	}
	return c.data, nil
}

func (c *updateServiceTestCache) SetUpdateInfo(_ context.Context, data string, _ time.Duration) error {
	c.data = data
	return nil
}

type errUpdateServiceTestCacheMiss struct{}

func (errUpdateServiceTestCacheMiss) Error() string {
	return "cache miss"
}

type updateServiceTestGitHubClient struct {
	repo string
}

func (c *updateServiceTestGitHubClient) FetchLatestRelease(_ context.Context, repo string) (*GitHubRelease, error) {
	c.repo = repo
	return &GitHubRelease{
		TagName:     "v9.9.9",
		Name:        "test release",
		PublishedAt: "2026-05-09T00:00:00Z",
		HTMLURL:     "https://github.com/" + repo + "/releases/tag/v9.9.9",
	}, nil
}

func (c *updateServiceTestGitHubClient) DownloadFile(context.Context, string, string, int64) error {
	return nil
}

func (c *updateServiceTestGitHubClient) FetchChecksumFile(context.Context, string) ([]byte, error) {
	return nil, nil
}

func TestUpdateServiceUsesForkRepositoryByDefault(t *testing.T) {
	cache := &updateServiceTestCache{}
	client := &updateServiceTestGitHubClient{}
	svc := NewUpdateService(cache, client, "0.0.1", "release", nil)

	info, err := svc.CheckUpdate(context.Background(), true)
	require.NoError(t, err)
	require.Equal(t, "DoodleXu/sub2api", client.repo)
	require.Equal(t, "DoodleXu/sub2api", info.Repository)
	require.Equal(t, "https://github.com/DoodleXu/sub2api/releases/tag/v9.9.9", info.ReleaseInfo.HTMLURL)
}

func TestUpdateServiceUsesConfiguredRepository(t *testing.T) {
	cache := &updateServiceTestCache{}
	client := &updateServiceTestGitHubClient{}
	svc := NewUpdateService(cache, client, "0.0.1", "release", &config.Config{
		Update: config.UpdateConfig{Repository: "example/custom-sub2api"},
	})

	info, err := svc.CheckUpdate(context.Background(), true)
	require.NoError(t, err)
	require.Equal(t, "example/custom-sub2api", client.repo)
	require.Equal(t, "example/custom-sub2api", info.Repository)
	require.Equal(t, "https://github.com/example/custom-sub2api/releases/tag/v9.9.9", info.ReleaseInfo.HTMLURL)
}
