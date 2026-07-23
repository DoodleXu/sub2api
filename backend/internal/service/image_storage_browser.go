package service

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// ImageStorageObject is an object already produced by the upstream async image task flow.
type ImageStorageObject struct {
	Key          string    `json:"key"`
	Size         int64     `json:"size"`
	ETag         string    `json:"etag,omitempty"`
	LastModified time.Time `json:"last_modified"`
	URL          string    `json:"url"`
}

type ImageStorageObjectPage struct {
	Items      []ImageStorageObject `json:"items"`
	NextCursor string               `json:"next_cursor,omitempty"`
	HasMore    bool                 `json:"has_more"`
}

type ImageStorageBrowser interface {
	List(ctx context.Context, prefix, cursor string, limit int) (*ImageStorageObjectPage, error)
}

type ImageStorageBrowserFactory func(ctx context.Context, cfg *config.ImageStorageConfig) (ImageStorageBrowser, error)

// BrowserConfig returns the same effective object-store binding used by async tasks.
func (s *ImageStorageSettingService) BrowserConfig(ctx context.Context) (*config.ImageStorageConfig, error) {
	cfg, err := s.effectiveConfig(ctx)
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled || !cfg.IsConfigured() {
		return nil, ErrImageStorageIncomplete
	}
	return cfg, nil
}
