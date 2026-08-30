//go:build unit

package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestWriteDesktopTokenErrorMapsAudienceMismatchToInvalidGrant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/desktop/token", nil)

	writeDesktopTokenError(c, service.ErrDesktopAudienceInvalid)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.JSONEq(t, `{"error":"invalid_grant","error_description":"the token audience does not match this desktop client"}`, recorder.Body.String())
}

func TestWriteDesktopTokenErrorMapsDeviceKeyConflictsToClientError(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "active", err: service.ErrDesktopDeviceAlreadyActive},
		{name: "owned", err: service.ErrDesktopDeviceKeyOwned},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/desktop/token", nil)

			writeDesktopTokenError(c, test.err)

			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Contains(t, recorder.Body.String(), `"error":"invalid_grant"`)
		})
	}
}

func TestDesktopTokenRejectsUnknownOrMixedGrantRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewDesktopDeviceHandler(&service.DesktopDeviceService{})
	tests := []struct {
		name string
		body map[string]any
	}{
		{name: "missing grant type", body: map[string]any{"device_code": "d", "code_verifier": "v"}},
		{name: "unknown grant type", body: map[string]any{"grant_type": "password", "device_code": "d", "code_verifier": "v"}},
		{name: "mixed refresh fields", body: map[string]any{"grant_type": "refresh_token", "refresh_token": "r", "device_code": "d"}},
		{name: "device fields missing", body: map[string]any{"grant_type": service.DesktopGrantType, "device_code": "d"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw, err := json.Marshal(test.body)
			require.NoError(t, err)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/desktop/token", bytes.NewReader(raw))
			h.Token(c)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Contains(t, recorder.Body.String(), `"error":"invalid_request"`)
		})
	}
}
