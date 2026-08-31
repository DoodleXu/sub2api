package routes

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

// imageCapabilities is intentionally a public, credential-free description of
// the Images API. Clients can use it before selecting a key, while
// the actual generation/edit endpoints continue to enforce API-key, group and
// provider policy.  Keep limits here in sync with the shared OpenAI image
// parser; accepting a request here never bypasses those runtime validators.
type imageCapabilities struct {
	ProtocolVersion string                    `json:"protocol_version"`
	Endpoint        string                    `json:"endpoint"`
	RequiresAPIKey  bool                      `json:"requires_api_key"`
	Operations      []string                  `json:"operations"`
	Models          []imageModelCapability    `json:"models"`
	Defaults        imageDefaults             `json:"defaults"`
	Limits          imageLimits               `json:"limits"`
	Security        imageSecurityCapabilities `json:"security"`
	Async           imageAsyncCapability      `json:"async"`
	BackendMode     bool                      `json:"backend_mode_enabled"`
	ServerTime      string                    `json:"server_time"`
}

type imageModelCapability struct {
	ID         string   `json:"id"`
	Operations []string `json:"operations"`
	Enabled    bool     `json:"enabled"`
}

type imageDefaults struct {
	Model        string `json:"model"`
	N            int    `json:"n"`
	Size         string `json:"size"`
	Quality      string `json:"quality"`
	OutputFormat string `json:"output_format"`
	Background   string `json:"background"`
	PollAfterSec int    `json:"poll_after_seconds"`
}

type imageLimits struct {
	MaxImages           int   `json:"max_images"`
	MaxReferenceImages  int   `json:"max_reference_images"`
	MaxUploadsWithMask  int   `json:"max_uploads_with_mask"`
	MaxUploadPartBytes  int64 `json:"max_upload_part_bytes"`
	MaxUploadTotalBytes int64 `json:"max_upload_total_bytes"`
	MaxImageDimension   int   `json:"max_image_dimension"`
	MaxImagePixels      int64 `json:"max_image_pixels"`
	MaxDownloadBytes    int64 `json:"max_download_bytes"`
}

type imageSecurityCapabilities struct {
	AllowedUploadMIMEs  []string `json:"allowed_upload_mimes"`
	MagicBytesRequired  bool     `json:"magic_bytes_required"`
	DecodeDimensions    bool     `json:"decode_dimensions"`
	HTTPSRemoteURLOnly  bool     `json:"https_remote_url_only"`
	PublicRemoteURLOnly bool     `json:"public_remote_url_only"`
	RedirectsValidated  bool     `json:"redirects_validated"`
}

// imageAsyncCapability makes the object-storage gate explicit.  Older clients
// can continue to inspect Operations; newer clients should use this block to
// distinguish "new task admission" from polling records accepted earlier.
type imageAsyncCapability struct {
	Enabled  bool   `json:"enabled"`
	Pollable bool   `json:"pollable"`
	Reason   string `json:"reason,omitempty"`
}

type imageCapabilitiesRuntime struct {
	AsyncEnabled       bool
	AsyncPollable      bool
	BackendModeEnabled bool
	RuntimeKnown       bool
	MaxDownloadBytes   int64
}

// RegisterImageCapabilitiesRoute installs the stable public endpoint. It is
// kept as a small route helper so the gateway registration remains readable
// and the response can be unit-tested without constructing the entire server.
func RegisterImageCapabilitiesRoute(r *gin.Engine, resolvers ...func(context.Context) imageCapabilitiesRuntime) {
	if r == nil {
		return
	}
	// Gateway routes are registered on the engine root (the OpenAI-compatible
	// surface uses /v1 rather than /api/v1), therefore keep the public contract
	// at /v1/images/capabilities.
	var resolver func(context.Context) imageCapabilitiesRuntime
	if len(resolvers) > 0 {
		resolver = resolvers[0]
	}
	r.GET("/v1/images/capabilities", func(c *gin.Context) {
		// Direct embedders that register only this helper retain the historical
		// advertised surface. Production always injects a resolver below.
		runtime := imageCapabilitiesRuntime{AsyncEnabled: true, AsyncPollable: true}
		if resolver != nil {
			ctx := context.Background()
			if c != nil && c.Request != nil {
				ctx = c.Request.Context()
			}
			runtime = resolver(ctx)
		}
		response.Success(c, imageCapabilitiesPayload(runtime))
	})
}

// ImageCapabilitiesHandler is exported for focused route tests and for
// embedders that register gateway routes themselves.
func ImageCapabilitiesHandler(c *gin.Context) {
	// Preserve the historical exported handler contract for embedders that call
	// it directly. RegisterGatewayRoutes always installs the runtime-aware
	// closure below, which is the production path and reports the real storage
	// gate instead of this compatibility default.
	response.Success(c, imageCapabilitiesPayload(imageCapabilitiesRuntime{AsyncEnabled: true, AsyncPollable: true}))
}

func imageCapabilitiesPayload(runtime imageCapabilitiesRuntime) imageCapabilities {
	// Synchronous image routes do not depend on the async result store. The
	// async operations are appended only when the exact admission gate reports
	// usable storage, preventing the desktop UI from offering a guaranteed 404.
	operations := []string{"generations", "edits"}
	asyncOperations := []string(nil)
	if runtime.AsyncEnabled {
		asyncOperations = []string{"generations/async", "edits/async"}
		operations = append(operations, asyncOperations...)
	}
	if runtime.AsyncPollable {
		operations = append(operations, "tasks")
	}
	asyncReason := ""
	if runtime.RuntimeKnown {
		if !runtime.AsyncPollable {
			asyncReason = "task_store_unavailable"
		} else if !runtime.AsyncEnabled {
			asyncReason = "object_storage_unavailable"
		}
	}
	modelOperations := func() []string {
		result := []string{"generations", "edits"}
		return append(result, asyncOperations...)
	}
	maxDownloadBytes := runtime.MaxDownloadBytes
	if maxDownloadBytes <= 0 {
		maxDownloadBytes = 32 << 20
	}
	return imageCapabilities{
		ProtocolVersion: "1", Endpoint: "/v1/images", RequiresAPIKey: true,
		Operations: operations,
		Models: []imageModelCapability{
			{ID: "gpt-image-2", Operations: modelOperations(), Enabled: true},
			{ID: "gpt-image-1", Operations: modelOperations(), Enabled: true},
			{ID: "grok-imagine-image", Operations: modelOperations(), Enabled: true},
		},
		Defaults:    imageDefaults{Model: "gpt-image-2", N: 1, Size: "auto", Quality: "auto", OutputFormat: "png", Background: "auto", PollAfterSec: 3},
		Limits:      imageLimits{MaxImages: 4, MaxReferenceImages: 8, MaxUploadsWithMask: 9, MaxUploadPartBytes: 20 << 20, MaxUploadTotalBytes: 80 << 20, MaxImageDimension: 16384, MaxImagePixels: 100_000_000, MaxDownloadBytes: maxDownloadBytes},
		Security:    imageSecurityCapabilities{AllowedUploadMIMEs: []string{"image/png", "image/jpeg", "image/webp", "image/gif"}, MagicBytesRequired: true, DecodeDimensions: true, HTTPSRemoteURLOnly: true, PublicRemoteURLOnly: true, RedirectsValidated: true},
		Async:       imageAsyncCapability{Enabled: runtime.AsyncEnabled, Pollable: runtime.AsyncPollable, Reason: asyncReason},
		BackendMode: runtime.BackendModeEnabled,
		ServerTime:  time.Now().UTC().Format(time.RFC3339),
	}
}
