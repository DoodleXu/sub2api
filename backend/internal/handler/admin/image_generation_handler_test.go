//go:build unit

package admin

import (
	"net/url"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestImageGenerationAdminAssetResponsesUseAuthenticatedAdminURL(t *testing.T) {
	h := &ImageGenerationHandler{}

	assets := h.adminAssetResponses([]*service.ImageGenerationAsset{
		{ID: 7, RecordID: 42, AssetIndex: 0, MimeType: "image/png", Extension: ".png"},
	})

	require.Len(t, assets, 1)
	rawURL, ok := assets[0]["url"].(string)
	require.True(t, ok)
	parsed, err := url.Parse(rawURL)
	require.NoError(t, err)
	require.Equal(t, "/api/v1/admin/image-generations/assets/7", parsed.Path)
	require.Empty(t, parsed.RawQuery)
	require.Equal(t, rawURL, assets[0]["admin_url"])
}
