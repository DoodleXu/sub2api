package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// CodexModels serves the Codex models manifest for Codex clients.
//
// Codex CLI and the Codex desktop app refresh their model picker from
// GET {base_url}/models?client_version=... (custom provider mode) or
// GET /backend-api/codex/models (chatgpt_base_url mode). Both routes land
// here. The manifest is proxied verbatim from the selected account's ChatGPT
// backend or custom API key upstream. API key manifests use a short-lived,
// asynchronously revalidated cache to tolerate canceled client requests.
func (h *OpenAIGatewayHandler) TryCodexModels(c *gin.Context) bool {
	if c.Request.Context().Err() != nil {
		return true
	}
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey.Group == nil {
		h.errorResponse(c, http.StatusUnauthorized, "invalid_request_error", "API key group is required")
		return true
	}
	if apiKey.Group.Platform != service.PlatformOpenAI {
		h.errorResponse(c, http.StatusNotFound, "not_found_error", "Codex models manifest is only available for OpenAI groups")
		return true
	}
	maxAccountSwitches := h.maxAccountSwitches
	if maxAccountSwitches <= 0 {
		maxAccountSwitches = 3
	}
	failedAccountIDs := make(map[int64]struct{})
	switchCount := 0
	var lastUpstreamErr error

	for {
		account, err := h.gatewayService.SelectAccountForModelWithExclusions(c.Request.Context(), apiKey.GroupID, "", "", failedAccountIDs)
		if err != nil {
			if c.Request.Context().Err() != nil {
				return true
			}
			if lastUpstreamErr != nil {
				h.errorResponse(c, infraerrors.Code(lastUpstreamErr), "upstream_error", infraerrors.Message(lastUpstreamErr))
				return true
			}
			return false
		}

		manifest, err := h.gatewayService.FetchCodexModelsManifest(c.Request.Context(), account, c.Query("client_version"), c.GetHeader("If-None-Match"))
		if err != nil {
			if c.Request.Context().Err() != nil {
				return true
			}
			fallbackEligible := codexModelsManifestFallbackEligible(err)
			if (service.IsRetryableCodexModelsManifestError(err) || fallbackEligible) && switchCount < maxAccountSwitches {
				failedAccountIDs[account.ID] = struct{}{}
				switchCount++
				if !fallbackEligible {
					lastUpstreamErr = err
				}
				continue
			}
			if fallbackEligible {
				return false
			}
			h.errorResponse(c, infraerrors.Code(err), "upstream_error", infraerrors.Message(err))
			return true
		}
		if c.Request.Context().Err() != nil {
			return true
		}

		if manifest.ETag != "" {
			c.Header("ETag", manifest.ETag)
		}
		if manifest.NotModified {
			c.Status(http.StatusNotModified)
			return true
		}
		c.Data(http.StatusOK, "application/json", manifest.Body)
		return true
	}
}

func codexModelsManifestFallbackEligible(err error) bool {
	switch infraerrors.Reason(err) {
	case "OPENAI_CODEX_MODELS_TOKEN_MISSING",
		"OPENAI_CODEX_MODELS_API_KEY_UPSTREAM_UNSUPPORTED",
		"OPENAI_CODEX_MODELS_API_KEY_MISSING",
		"OPENAI_CODEX_MODELS_API_KEY_UPSTREAM_INVALID",
		"OPENAI_CODEX_MODELS_ACCOUNT_TYPE_UNSUPPORTED":
		return true
	default:
		return false
	}
}

// CodexModels serves direct handler users that cannot provide a local-list
// fallback. Gateway routes call TryCodexModels and fall back to the standard
// models handler so API-key clients retain their bundled model catalog.
func (h *OpenAIGatewayHandler) CodexModels(c *gin.Context) {
	if !h.TryCodexModels(c) {
		h.errorResponse(c, http.StatusServiceUnavailable, "upstream_error", service.ErrNoAvailableCodexModelsAccount.Error())
	}
}
