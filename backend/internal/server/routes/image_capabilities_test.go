//go:build unit

package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestImageCapabilitiesPayloadTracksAsyncStorageState(t *testing.T) {
	tests := []struct {
		name           string
		runtime        imageCapabilitiesRuntime
		wantOperations []string
		wantReason     string
	}{
		{
			name: "enabled and pollable",
			runtime: imageCapabilitiesRuntime{
				RuntimeKnown:  true,
				AsyncEnabled:  true,
				AsyncPollable: true,
			},
			wantOperations: []string{"generations", "edits", "generations/async", "edits/async", "tasks"},
		},
		{
			name: "storage unavailable",
			runtime: imageCapabilitiesRuntime{
				RuntimeKnown:  true,
				AsyncEnabled:  false,
				AsyncPollable: true,
			},
			wantOperations: []string{"generations", "edits", "tasks"},
			wantReason:     "object_storage_unavailable",
		},
		{
			name: "task store unavailable",
			runtime: imageCapabilitiesRuntime{
				RuntimeKnown:  true,
				AsyncEnabled:  false,
				AsyncPollable: false,
			},
			wantOperations: []string{"generations", "edits"},
			wantReason:     "task_store_unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := imageCapabilitiesPayload(tt.runtime)
			require.Equal(t, tt.wantOperations, payload.Operations)
			require.Equal(t, tt.runtime.AsyncEnabled, payload.Async.Enabled)
			require.Equal(t, tt.runtime.AsyncPollable, payload.Async.Pollable)
			require.Equal(t, tt.wantReason, payload.Async.Reason)
			for _, model := range payload.Models {
				require.Equal(t, tt.runtime.AsyncEnabled, containsString(model.Operations, "generations/async"))
				require.Equal(t, tt.runtime.AsyncEnabled, containsString(model.Operations, "edits/async"))
			}
			require.Equal(t, int64(32<<20), payload.Limits.MaxDownloadBytes)
		})
	}
}

func TestImageCapabilitiesPayloadUsesEffectiveDownloadLimit(t *testing.T) {
	payload := imageCapabilitiesPayload(imageCapabilitiesRuntime{MaxDownloadBytes: 7 << 20})
	require.Equal(t, int64(7<<20), payload.Limits.MaxDownloadBytes)
}

func TestImageCapabilitiesRouteUsesRuntimeResolver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	seenContext := false
	registerImageCapabilitiesRoute(router, func(ctx context.Context) imageCapabilitiesRuntime {
		seenContext = ctx != nil
		return imageCapabilitiesRuntime{
			RuntimeKnown:       true,
			AsyncEnabled:       false,
			AsyncPollable:      true,
			BackendModeEnabled: true,
		}
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/images/capabilities", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.True(t, seenContext)

	var envelope struct {
		Code int `json:"code"`
		Data struct {
			Operations  []string `json:"operations"`
			BackendMode bool     `json:"backend_mode_enabled"`
			Async       struct {
				Enabled  bool   `json:"enabled"`
				Pollable bool   `json:"pollable"`
				Reason   string `json:"reason"`
			} `json:"async"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	require.Zero(t, envelope.Code)
	require.Equal(t, []string{"generations", "edits", "tasks"}, envelope.Data.Operations)
	require.True(t, envelope.Data.BackendMode)
	require.False(t, envelope.Data.Async.Enabled)
	require.True(t, envelope.Data.Async.Pollable)
	require.Equal(t, "object_storage_unavailable", envelope.Data.Async.Reason)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
