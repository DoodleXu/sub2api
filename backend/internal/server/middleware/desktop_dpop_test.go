//go:build unit

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type desktopProofVerifierStub struct {
	err   error
	nonce string
}

func (s desktopProofVerifierStub) IsDeviceSessionRevoked(_ context.Context, _ string) (bool, error) {
	return false, nil
}

func (s desktopProofVerifierStub) VerifyDeviceProof(_ context.Context, _, _, _, _, _ string) (string, error) {
	return s.nonce, s.err
}

func TestRequestURLForDPoPUsesCanonicalRequestTarget(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "http://internal.example.test/api/v1/user/profile?secret=omitted", nil)
	r.RemoteAddr = "192.0.2.1:1234"
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "api.example.test")
	policy := NewDesktopTransportPolicy([]string{"192.0.2.1/32"}, "")
	require.Equal(t, "https://api.example.test/api/v1/user/profile", RequestURLForDPoPWithPolicy(r, policy))
}

func TestRequestURLForDPoPRejectsCleartext(t *testing.T) {
	tests := []struct {
		name      string
		target    string
		forwarded string
	}{
		{name: "direct-http", target: "http://api.example.test/api/v1/user/profile"},
		{name: "forwarded-http", target: "http://internal.example.test/api/v1/user/profile", forwarded: "http"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tc.target, nil)
			if tc.forwarded != "" {
				r.Header.Set("X-Forwarded-Proto", tc.forwarded)
			}
			require.Empty(t, requestURLForDPoP(r))
		})
	}
}

func TestRequireHTTPSRejectsCleartextAndAcceptsTLSOrTrustedForwardedTLS(t *testing.T) {
	tests := []struct {
		name       string
		target     string
		forwarded  string
		wantStatus int
	}{
		{name: "direct-http", target: "http://api.example.test/device", wantStatus: http.StatusBadRequest},
		{name: "forwarded-http", target: "http://internal.example.test/device", forwarded: "http", wantStatus: http.StatusBadRequest},
		{name: "tls", target: "https://api.example.test/device", wantStatus: http.StatusNoContent},
		{name: "forwarded-tls", target: "http://internal.example.test/device", forwarded: "https", wantStatus: http.StatusNoContent},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			r := gin.New()
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tc.target, nil)
			if tc.forwarded != "" {
				req.RemoteAddr = "192.0.2.1:1234"
			}
			if tc.forwarded != "" {
				req.Header.Set("X-Forwarded-Proto", tc.forwarded)
			}
			policy := NewDesktopTransportPolicy([]string{"192.0.2.1/32"}, "")
			r.Use(RequireHTTPS(policy))
			r.GET("/device", func(c *gin.Context) { c.Status(http.StatusNoContent) })
			r.ServeHTTP(recorder, req)
			require.Equal(t, tc.wantStatus, recorder.Code)
		})
	}
}

func TestRequireHTTPSRejectsForwardedTLSFromUntrustedPeer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequireHTTPS(NewDesktopTransportPolicy([]string{"192.0.2.1/32"}, "")))
	r.GET("/device", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://internal.example.test/device", nil)
	req.RemoteAddr = "198.51.100.5:1234"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "api.example.test")
	r.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestRequestURLForDPoPRejectsUnnormalizedForwardedHeaders(t *testing.T) {
	tests := []struct {
		name   string
		header string
		value  string
	}{
		{name: "proto list", header: "X-Forwarded-Proto", value: "https, http"},
		{name: "host list", header: "X-Forwarded-Host", value: "api.example.test, evil.example.test"},
		{name: "host userinfo", header: "X-Forwarded-Host", value: "attacker@example.test"},
		{name: "host control", header: "X-Forwarded-Host", value: "api.example.test\r\nX-Evil: 1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "https://internal.example.test/api/v1/user/profile", nil)
			r.Header.Set(tc.header, tc.value)
			require.Empty(t, requestURLForDPoP(r))
		})
	}
}

func TestRequestURLForDPoPRejectsDuplicateForwardedHeaderValues(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "https://internal.example.test/api/v1/user/profile", nil)
	r.Header["X-Forwarded-Host"] = []string{"api.example.test", "api.example.test"}
	require.Empty(t, requestURLForDPoP(r))
}

func TestDesktopTransportPolicyNormalizesFrontendPathAndKeepsHostPinned(t *testing.T) {
	policy := DesktopTransportPolicyForConfig(nil, "https://example.test/console")
	require.Equal(t, "https://example.test", policy.PublicOrigin)

	allowed := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/desktop/token", nil)
	require.Equal(t, "https://example.test/api/v1/desktop/token", RequestURLForDPoPWithPolicy(allowed, policy))

	forged := httptest.NewRequest(http.MethodPost, "https://evil.example.test/api/v1/desktop/token", nil)
	require.Empty(t, RequestURLForDPoPWithPolicy(forged, policy))
}

func TestDesktopTransportPolicyMalformedConfiguredOriginFallsBackToPinnedDefault(t *testing.T) {
	policy := DesktopTransportPolicyForConfig(nil, "https://user:pass@example.test/path?redirect=evil")
	require.Equal(t, DefaultDesktopPublicOrigin, policy.PublicOrigin)
	forged := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/desktop/token", nil)
	require.Empty(t, RequestURLForDPoPWithPolicy(forged, policy))
}

func TestVerifyDesktopDPoPMapsNonceAndInvalidProof(t *testing.T) {
	gin.SetMode(gin.TestMode)
	claims := &service.JWTClaims{DeviceID: "device-1"}
	for name, proofErr := range map[string]error{
		"valid":   nil,
		"invalid": service.ErrDesktopProofInvalid,
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "https://api.example.test/api/v1/user/profile", nil)
			checker := desktopProofVerifierStub{err: proofErr, nonce: "nonce-1"}
			ok := verifyDesktopDPoP(c, claims, "access-token", checker)
			if proofErr == nil {
				require.True(t, ok)
				require.Equal(t, http.StatusOK, recorder.Code)
				require.Equal(t, "nonce-1", recorder.Header().Get("DPoP-Nonce"))
			} else {
				require.False(t, ok)
				require.Equal(t, http.StatusUnauthorized, recorder.Code)
				require.Contains(t, recorder.Body.String(), "DPOP_INVALID")
			}
		})
	}
}
