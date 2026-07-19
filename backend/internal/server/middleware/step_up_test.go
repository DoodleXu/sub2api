package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type stubStepUpGrantChecker struct {
	granted bool
	err     error
}

func (s stubStepUpGrantChecker) HasStepUpGrant(ctx context.Context, userID int64, sessionKey string) (bool, error) {
	return s.granted, s.err
}

type stubStepUpUserReader struct {
	user *service.User
	err  error
}

func (s stubStepUpUserReader) GetByID(ctx context.Context, id int64) (*service.User, error) {
	return s.user, s.err
}

func newStepUpTestContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/sensitive", nil)
	return c, rec
}

func TestStepUpSessionKeyLegacyTokensAreIsolated(t *testing.T) {
	newContext := func(token string) *gin.Context {
		c, _ := newStepUpTestContext(t)
		if token != "" {
			c.Request.Header.Set("Authorization", "Bearer "+token)
		}
		return c
	}

	keyA := StepUpSessionKey(newContext("legacy-token-a"), 7)
	keyAAgain := StepUpSessionKey(newContext("legacy-token-a"), 7)
	keyB := StepUpSessionKey(newContext("legacy-token-b"), 7)
	require.NotEmpty(t, keyA)
	require.Equal(t, keyA, keyAAgain)
	require.NotEqual(t, keyA, keyB)
	require.Empty(t, StepUpSessionKey(newContext(""), 7))
}

func TestStepUpSessionKeyPrefersSessionID(t *testing.T) {
	c, _ := newStepUpTestContext(t)
	c.Set(ContextKeySessionID, "family-123")
	c.Request.Header.Set("Authorization", "Bearer ignored-token")
	require.Equal(t, "sid:family-123", StepUpSessionKey(c, 7))
}

func TestEnforceStepUpRejectsAdminAPIKey(t *testing.T) {
	c, rec := newStepUpTestContext(t)
	c.Set("auth_method", service.AuditAuthMethodAdminAPIKey)

	ok := enforceStepUp(c, stubStepUpGrantChecker{granted: true}, stubStepUpUserReader{user: &service.User{TotpEnabled: true}})

	require.False(t, ok)
	require.True(t, c.IsAborted())
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "STEP_UP_ADMIN_API_KEY_FORBIDDEN")
}

func TestEnforceStepUpRequiresAuthSubject(t *testing.T) {
	c, rec := newStepUpTestContext(t)

	ok := enforceStepUp(c, stubStepUpGrantChecker{granted: true}, stubStepUpUserReader{user: &service.User{TotpEnabled: true}})

	require.False(t, ok)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestEnforceStepUpRequiresTotpEnabled(t *testing.T) {
	c, rec := newStepUpTestContext(t)
	c.Set(string(ContextKeyUser), AuthSubject{UserID: 1})

	ok := enforceStepUp(c, stubStepUpGrantChecker{granted: true}, stubStepUpUserReader{user: &service.User{ID: 1, TotpEnabled: false}})

	require.False(t, ok)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "STEP_UP_TOTP_NOT_ENABLED")
}

func TestEnforceStepUpFailsClosedOnGrantError(t *testing.T) {
	c, rec := newStepUpTestContext(t)
	c.Set(string(ContextKeyUser), AuthSubject{UserID: 1})
	c.Request.Header.Set("Authorization", "Bearer legacy-access-token")

	ok := enforceStepUp(c, stubStepUpGrantChecker{err: errors.New("redis down")}, stubStepUpUserReader{user: &service.User{ID: 1, TotpEnabled: true}})

	require.False(t, ok)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "STEP_UP_UNAVAILABLE")
}

func TestEnforceStepUpRequiresGrant(t *testing.T) {
	c, rec := newStepUpTestContext(t)
	c.Set(string(ContextKeyUser), AuthSubject{UserID: 1})
	c.Request.Header.Set("Authorization", "Bearer legacy-access-token")

	ok := enforceStepUp(c, stubStepUpGrantChecker{granted: false}, stubStepUpUserReader{user: &service.User{ID: 1, TotpEnabled: true}})

	require.False(t, ok)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "STEP_UP_REQUIRED")
}

func TestEnforceStepUpPassesWithGrant(t *testing.T) {
	c, _ := newStepUpTestContext(t)
	c.Set(string(ContextKeyUser), AuthSubject{UserID: 1})
	c.Request.Header.Set("Authorization", "Bearer legacy-access-token")

	ok := enforceStepUp(c, stubStepUpGrantChecker{granted: true}, stubStepUpUserReader{user: &service.User{ID: 1, TotpEnabled: true}})

	require.True(t, ok)
	require.False(t, c.IsAborted())
}
