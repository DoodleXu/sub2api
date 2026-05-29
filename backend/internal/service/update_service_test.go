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

type updateServiceTestCache struct {
	data string
}

func (c *updateServiceTestCache) GetUpdateInfo(context.Context) (string, error) {
	if c.data == "" {
		return "", errors.New("cache miss")
	}
	return c.data, nil
}

func (c *updateServiceTestCache) SetUpdateInfo(_ context.Context, data string, _ time.Duration) error {
	c.data = data
	return nil
}

type updateServiceTestGitHubClient struct {
	repo    string
	release *GitHubRelease
}

func (c *updateServiceTestGitHubClient) FetchLatestRelease(_ context.Context, repo string) (*GitHubRelease, error) {
	c.repo = repo
	if c.release != nil {
		return c.release, nil
	}
	return &GitHubRelease{
		TagName:     "v9.9.9",
		Name:        "test release",
		PublishedAt: "2026-05-09T00:00:00Z",
		HTMLURL:     "https://github.com/" + repo + "/releases/tag/v9.9.9",
	}, nil
}

func (c *updateServiceTestGitHubClient) DownloadFile(context.Context, string, string, int64) error {
	panic("DownloadFile should not be called by these tests")
}

func (c *updateServiceTestGitHubClient) FetchChecksumFile(context.Context, string) ([]byte, error) {
	panic("FetchChecksumFile should not be called by these tests")
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

func TestUpdateServicePerformUpdateNoUpdateReturnsSentinel(t *testing.T) {
	svc := NewUpdateService(
		&updateServiceTestCache{},
		&updateServiceTestGitHubClient{
			release: &GitHubRelease{
				TagName: "v0.1.140",
				Name:    "v0.1.140",
			},
		},
		"0.1.140",
		"release",
		nil,
	)

	err := svc.PerformUpdate(context.Background())

	require.Error(t, err)
	require.True(t, errors.Is(err, ErrNoUpdateAvailable))
	require.ErrorIs(t, err, ErrNoUpdateAvailable)
}
