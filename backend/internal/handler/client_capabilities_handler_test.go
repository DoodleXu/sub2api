//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestClientCapabilitiesExposeDesktopAndImageContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewClientCapabilitiesHandler(nil, BuildInfo{Version: "test-version"})
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/client/capabilities", nil)

	h.Get(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			ClientID  string            `json:"client_id"`
			Audience  string            `json:"audience"`
			Features  map[string]bool   `json:"features"`
			Endpoints map[string]string `json:"endpoints"`
			Flow      struct {
				ExpiresIn int      `json:"expires_in_seconds"`
				TokenType string   `json:"token_type"`
				Curves    []string `json:"public_key_curves"`
				Nonce     bool     `json:"nonce_required"`
			} `json:"device_flow"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	require.Zero(t, envelope.Code)
	require.Equal(t, service.DesktopClientID, envelope.Data.ClientID)
	require.Equal(t, service.DesktopAudience, envelope.Data.Audience)
	require.True(t, envelope.Data.Features["images"])
	require.True(t, envelope.Data.Features["image_tasks"])
	require.False(t, envelope.Data.Features["web_console"])
	require.Equal(t, "/v1/images/generations", envelope.Data.Endpoints["images"])
	require.Equal(t, service.DesktopAuthorizationExpiresInSeconds, envelope.Data.Flow.ExpiresIn)
	require.Equal(t, "DPoP", envelope.Data.Flow.TokenType)
	require.Equal(t, []string{"P-256"}, envelope.Data.Flow.Curves)
	require.True(t, envelope.Data.Flow.Nonce)
}

func TestClientCapabilitiesPinsAPIBaseURLToOfficialOrigin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settings := service.NewSettingService(&settingHandlerPublicRepoStub{values: map[string]string{
		service.SettingKeyAPIBaseURL: "https://evil.example/v1",
	}}, &config.Config{})
	h := NewClientCapabilitiesHandler(settings, BuildInfo{})
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/client/capabilities", nil)

	h.Get(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	var envelope struct {
		Data struct {
			APIBaseURL string `json:"api_base_url"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	require.Equal(t, "https://ai.clol.site", envelope.Data.APIBaseURL)
}

func TestClientCapabilitiesReflectsAsyncRuntimeAvailability(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewClientCapabilitiesHandler(nil, BuildInfo{})
	h.SetImageTaskCapabilityResolver(func(context.Context) (bool, bool) {
		return false, true
	})
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/client/capabilities", nil)

	h.Get(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	var envelope struct {
		Data struct {
			Features     map[string]bool   `json:"features"`
			Availability map[string]string `json:"availability"`
			AsyncImages  struct {
				Enabled  bool   `json:"enabled"`
				Pollable bool   `json:"pollable"`
				Reason   string `json:"reason"`
			} `json:"async_images"`
			Endpoints map[string]string `json:"endpoints"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	require.False(t, envelope.Data.Features["async_images"])
	require.True(t, envelope.Data.Features["image_tasks"])
	require.Equal(t, "object_storage_unavailable", envelope.Data.Availability["async_images"])
	require.False(t, envelope.Data.AsyncImages.Enabled)
	require.True(t, envelope.Data.AsyncImages.Pollable)
	require.Equal(t, "object_storage_unavailable", envelope.Data.AsyncImages.Reason)
	require.Equal(t, "/v1/images/edits/async", envelope.Data.Endpoints["async_image_edits"])

	h.SetImageTaskCapabilityResolver(func(context.Context) (bool, bool) {
		return false, false
	})
	recorder = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/client/capabilities", nil)
	h.Get(c)
	var unavailable struct {
		Data struct {
			Features     map[string]bool   `json:"features"`
			Availability map[string]string `json:"availability"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &unavailable))
	require.False(t, unavailable.Data.Features["image_tasks"])
	require.Equal(t, "task_store_unavailable", unavailable.Data.Availability["image_tasks"])
}

func TestClientCapabilitiesReflectsBackendMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settings := service.NewSettingService(&settingHandlerPublicRepoStub{values: map[string]string{
		service.SettingKeyBackendModeEnabled: "true",
		service.SettingPaymentEnabled:        "true",
		service.SettingKeyWebConsoleEnabled:  "true",
	}}, &config.Config{})
	h := NewClientCapabilitiesHandler(settings, BuildInfo{})
	h.SetImageTaskCapabilityResolver(func(context.Context) (bool, bool) { return true, true })
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/client/capabilities", nil)

	h.Get(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	var envelope struct {
		Data struct {
			BackendMode  bool              `json:"backend_mode_enabled"`
			Features     map[string]bool   `json:"features"`
			Availability map[string]string `json:"availability"`
			AsyncImages  struct {
				Enabled bool   `json:"enabled"`
				Reason  string `json:"reason"`
			} `json:"async_images"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	require.True(t, envelope.Data.BackendMode)
	for _, feature := range []string{
		"desktop_device_authorization", "balance", "usage", "billing", "checkout_sessions",
		"checkin", "image_tasks", "async_images", "api_keys", "device_session_revocation",
		"web_console",
	} {
		require.False(t, envelope.Data.Features[feature], feature)
		require.Equal(t, "backend_mode_disabled", envelope.Data.Availability[feature], feature)
	}
	require.False(t, envelope.Data.AsyncImages.Enabled)
	require.Equal(t, "backend_mode_disabled", envelope.Data.AsyncImages.Reason)
	// The API-key gateway and cryptographic protocol metadata remain discoverable;
	// backend mode only hides account self-service operations.
	require.True(t, envelope.Data.Features["images"])
	require.True(t, envelope.Data.Features["pkce_s256"])
}

func TestClientCapabilitiesIntegrationProfilesAreCredentialFree(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewClientCapabilitiesHandler(nil, BuildInfo{})
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/client/integration-profiles", nil)

	h.IntegrationProfiles(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	body := recorder.Body.String()
	require.NotContains(t, body, "secret")
	require.NotContains(t, body, "api_key_value")
	var envelope struct {
		Data struct {
			Profiles []map[string]any `json:"profiles"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	require.Len(t, envelope.Data.Profiles, 3)
	var desktopProfile map[string]any
	var imageProfile map[string]any
	for _, profile := range envelope.Data.Profiles {
		if profile["id"] == "desktop" {
			desktopProfile = profile
		}
		if profile["id"] == "openai-images" {
			imageProfile = profile
		}
	}
	require.Equal(t, service.DesktopGrantType, desktopProfile["grant_type"])
	require.Equal(t, "refresh_token", desktopProfile["refresh_grant_type"])
	require.Contains(t, imageProfile["async_capability"], "async_images")
}

func TestClientCapabilitiesIntegrationProfilesRequireAuthenticationForKeySpecificQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewClientCapabilitiesHandler(nil, BuildInfo{})

	for _, rawQuery := range []string{"api_key_id=not-a-number", "api_key_id="} {
		t.Run(rawQuery, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/client/integration-profiles?"+rawQuery, nil)

			h.IntegrationProfiles(c)

			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Contains(t, recorder.Body.String(), "api_key_id")
			require.NotContains(t, recorder.Body.String(), "profiles")
		})
	}
	// A syntactically valid key id reaches the authenticated branch. Calling the
	// handler directly without the JWT middleware must fail closed.
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/client/integration-profiles?api_key_id=123", nil)
	h.IntegrationProfiles(c)
	require.Equal(t, http.StatusUnauthorized, recorder.Code)
}
