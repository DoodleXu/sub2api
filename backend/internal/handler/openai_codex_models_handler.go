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
// here. ChatGPT manifests are proxied verbatim; custom API key manifests receive
// provider-compatibility normalization and use a short-lived, asynchronously
// revalidated cache to tolerate canceled client requests.
func (h *OpenAIGatewayHandler) TryCodexModels(c *gin.Context) bool {
	if c.Request.Context().Err() != nil {
		return true
	}
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey.Group == nil {
		h.errorResponse(c, http.StatusUnauthorized, "invalid_request_error", "API key group is required")
		return true
	}
	if apiKey.Group.Platform != service.PlatformOpenAI && apiKey.Group.Platform != service.PlatformComposite {
		h.errorResponse(c, http.StatusNotFound, "not_found_error", "Codex models manifest is only available for OpenAI and Composite groups")
		return true
	}
	ifNoneMatch := c.GetHeader("If-None-Match")
	configuredManifest, configured, err := h.gatewayService.BuildGroupConfiguredCodexModelsManifest(
		c.Request.Context(),
		apiKey.Group,
		ifNoneMatch,
	)
	if err != nil {
		if c.Request.Context().Err() != nil {
			return true
		}
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "Failed to build Codex models manifest")
		return true
	}
	if configured {
		writeCodexModelsManifestResponse(c, configuredManifest)
		return true
	}
	maxAccountSwitches := h.maxAccountSwitches
	if maxAccountSwitches <= 0 {
		maxAccountSwitches = 3
	}
	failedAccountIDs := make(map[int64]struct{})
	switchCount := 0
	var lastUpstreamErr error
	lastUpstreamErrSuppressed := false

	for {
		account, err := h.gatewayService.SelectAccountForModelWithExclusions(c.Request.Context(), apiKey.GroupID, "", "", failedAccountIDs)
		if err != nil {
			if c.Request.Context().Err() != nil {
				return true
			}
			if lastUpstreamErr != nil {
				if lastUpstreamErrSuppressed {
					h.errorResponse(c, http.StatusBadGateway, "upstream_error", "Upstream request failed")
					return true
				}
				h.errorResponse(c, infraerrors.Code(lastUpstreamErr), "upstream_error", infraerrors.Message(lastUpstreamErr))
				return true
			}
			return false
		}
		// 让 ops 错误日志携带实际选中的上游账号，便于定位失效账号（#4544）。
		setOpsSelectedAccount(c, account.ID, account.Platform)

		// The client ETag is for the final group-specific body. Fetch the source
		// manifest without it, then apply local filtering and alias metadata.
		manifest, err := h.gatewayService.FetchCodexModelsManifest(c.Request.Context(), account, c.Query("client_version"), "")
		if err != nil {
			if c.Request.Context().Err() != nil {
				return true
			}
			fallbackEligible := codexModelsManifestFallbackEligible(err)
			suppressClientError := account.IsOpenAIContinueSchedulingAfterLimitEnabled()
			if (suppressClientError || service.IsRetryableCodexModelsManifestError(err) || fallbackEligible) && switchCount < maxAccountSwitches {
				failedAccountIDs[account.ID] = struct{}{}
				switchCount++
				if !fallbackEligible {
					lastUpstreamErr = err
					lastUpstreamErrSuppressed = suppressClientError
				}
				continue
			}
			if fallbackEligible {
				return false
			}
			if suppressClientError {
				h.errorResponse(c, http.StatusBadGateway, "upstream_error", "Upstream request failed")
				return true
			}
			h.errorResponse(c, infraerrors.Code(err), "upstream_error", infraerrors.Message(err))
			return true
		}
		if err := h.gatewayService.CompleteAPIKeyCodexModelsManifestForClient(manifest, account); err != nil {
			h.errorResponse(c, http.StatusInternalServerError, "api_error", "Failed to complete Codex models manifest")
			return true
		}
		if err := h.gatewayService.MergeGroupConfiguredCodexModels(c.Request.Context(), apiKey.Group, manifest, ifNoneMatch); err != nil {
			h.errorResponse(c, http.StatusInternalServerError, "api_error", "Failed to build Codex models manifest")
			return true
		}
		if c.Request.Context().Err() != nil {
			return true
		}
		writeCodexModelsManifestResponse(c, manifest)
		return true
	}
}

func writeCodexModelsManifestResponse(c *gin.Context, manifest *service.CodexModelsManifest) {
	if manifest == nil {
		return
	}
	if manifest.ETag != "" {
		c.Header("ETag", manifest.ETag)
	}
	if manifest.NotModified {
		c.Status(http.StatusNotModified)
		c.Writer.WriteHeaderNow()
		return
	}
	c.Data(http.StatusOK, "application/json", manifest.Body)
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
