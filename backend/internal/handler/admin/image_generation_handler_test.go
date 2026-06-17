//go:build unit

package admin

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestImageGenerationAdminAssetResponsesUseSignedPreviewURL(t *testing.T) {
	imageService := service.NewImageGenerationArchiveService(nil, nil, nil, &config.Config{})
	h := &ImageGenerationHandler{imageService: imageService}

	assets := h.adminAssetResponses([]*service.ImageGenerationAsset{
		{ID: 7, RecordID: 42, AssetIndex: 0, MimeType: "image/png", Extension: ".png"},
	})

	require.Len(t, assets, 1)
	rawURL, ok := assets[0]["url"].(string)
	require.True(t, ok)
	parsed, err := url.Parse(rawURL)
	require.NoError(t, err)
	require.Equal(t, "/api/v1/image-assets/7", parsed.Path)
	require.Equal(t, service.ImageAssetScopeAdmin, parsed.Query().Get("scope"))
	require.NotEmpty(t, parsed.Query().Get("sig"))
	require.True(t, imageService.VerifyAssetToken(7, parsed.Query().Get("scope"), parsed.Query().Get("expires"), parsed.Query().Get("sig"), time.Now().UTC()))
	require.Equal(t, "/api/v1/admin/image-generations/assets/7", assets[0]["admin_url"])
}

func TestWriteAdminImageAssetReaderStreamsInlineWithPrivateCache(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	writeAdminImageAssetReader(c, &service.ImageGenerationAssetReader{
		Body:        io.NopCloser(strings.NewReader("png")),
		ContentType: "image/png",
		Size:        3,
		Filename:    "image-7.png",
	})

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "image/png", rec.Header().Get("Content-Type"))
	require.Equal(t, `inline; filename="image-7.png"`, rec.Header().Get("Content-Disposition"))
	require.Equal(t, "private, max-age=300", rec.Header().Get("Cache-Control"))
	require.Equal(t, "png", rec.Body.String())
}
