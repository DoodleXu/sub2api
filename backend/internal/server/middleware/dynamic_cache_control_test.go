package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestShouldNoStoreDynamicPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path string
		want bool
	}{
		{path: "/api/v1/auth/me", want: true},
		{path: "/api/event_logging/batch", want: true},
		{path: "/v1/responses", want: true},
		{path: "/v1beta/models/gemini-2.5-pro:generateContent", want: true},
		{path: "/backend-api/codex/responses", want: true},
		{path: "/images/tasks/task-1", want: true},
		{path: "/antigravity/v1/models", want: true},
		{path: "/health", want: false},
		{path: "/", want: false},
		{path: "/static/app.js", want: false},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			require.Equal(t, test.want, ShouldNoStoreDynamicPath(test.path))
		})
	}
}

func TestDynamicResponseNoStoreSetsHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(DynamicResponseNoStore())
	router.GET("/api/v1/auth/me", func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=3600")
		c.Header("Vary", "Origin")
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "no-store, no-cache, must-revalidate, proxy-revalidate", recorder.Header().Get("Cache-Control"))
	require.Equal(t, "no-cache", recorder.Header().Get("Pragma"))
	require.Equal(t, "no-store", recorder.Header().Get("Surrogate-Control"))
	require.Equal(t, "Origin, Authorization, Cookie", recorder.Header().Get("Vary"))
}
