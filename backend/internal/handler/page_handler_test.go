package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type pageSettingRepoStub struct {
	values map[string]string
}

func requireSameResolvedPath(t *testing.T, got, want string) {
	t.Helper()
	resolvedGot, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatalf("resolve got path %q: %v", got, err)
	}
	resolvedWant, err := filepath.EvalSymlinks(want)
	if err != nil {
		t.Fatalf("resolve want path %q: %v", want, err)
	}
	if resolvedGot != resolvedWant {
		t.Fatalf("path = %q, want %q", got, want)
	}
}

func (s *pageSettingRepoStub) Get(context.Context, string) (*service.Setting, error) {
	panic("unexpected Get call")
}

func (s *pageSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	return s.values[key], nil
}

func (s *pageSettingRepoStub) Set(context.Context, string, string) error {
	panic("unexpected Set call")
}

func (s *pageSettingRepoStub) GetMultiple(context.Context, []string) (map[string]string, error) {
	panic("unexpected GetMultiple call")
}

func (s *pageSettingRepoStub) SetMultiple(context.Context, map[string]string) error {
	panic("unexpected SetMultiple call")
}

func (s *pageSettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}

func (s *pageSettingRepoStub) Delete(context.Context, string) error {
	panic("unexpected Delete call")
}

func TestCleanPageImageRelativePath(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{name: "single filename", in: "logo.png", want: "logo.png", ok: true},
		{name: "nested path", in: "images/logo.png", want: filepath.Join("images", "logo.png"), ok: true},
		{name: "dot prefix", in: "./logo.png", want: "logo.png", ok: true},
		{name: "url escaped slash", in: "images%2Flogo.png", want: filepath.Join("images", "logo.png"), ok: true},
		{name: "parent traversal", in: "../secret.png", ok: false},
		{name: "encoded parent traversal", in: "%2e%2e/secret.png", ok: false},
		{name: "backslash traversal", in: `images\secret.png`, ok: false},
		{name: "absolute path", in: "/etc/passwd", ok: false},
		{name: "encoded absolute path", in: "%2fetc/passwd", ok: false},
		{name: "encoded nul byte", in: "logo.png%00", ok: false},
		{name: "invalid escape", in: "logo.png%zz", ok: false},
		{name: "empty path", in: "", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := cleanPageImageRelativePath(tt.in)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("path = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolvePageImagePath(t *testing.T) {
	root := t.TempDir()
	pagesDir := filepath.Join(root, "pages")
	base := filepath.Join(pagesDir, "guide")
	if err := os.MkdirAll(filepath.Join(base, "images"), 0755); err != nil {
		t.Fatalf("create images dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(base, "logo.png"), []byte("fake"), 0644); err != nil {
		t.Fatalf("create direct image: %v", err)
	}
	if err := os.WriteFile(filepath.Join(base, "images", "logo.png"), []byte("fake"), 0644); err != nil {
		t.Fatalf("create image: %v", err)
	}

	got, ok := resolvePageImagePath(pagesDir, base, "logo.png")
	if !ok {
		t.Fatal("expected direct image path to be accepted")
	}
	want := filepath.Join(base, "logo.png")
	requireSameResolvedPath(t, got, want)

	got, ok = resolvePageImagePath(pagesDir, base, "images/logo.png")
	if !ok {
		t.Fatal("expected nested image path to be accepted")
	}
	want = filepath.Join(base, "images", "logo.png")
	requireSameResolvedPath(t, got, want)

	if got, ok := resolvePageImagePath(pagesDir, base, "../guide.md"); ok {
		t.Fatalf("expected traversal to be rejected, got %q", got)
	}
}

func TestResolvePageImagePathRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	pagesDir := filepath.Join(root, "pages")
	base := filepath.Join(pagesDir, "guide")
	outside := filepath.Join(root, "outside")

	if err := os.MkdirAll(base, 0755); err != nil {
		t.Fatalf("create page dir: %v", err)
	}
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatalf("create outside dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.png"), []byte("secret"), 0644); err != nil {
		t.Fatalf("create outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "images")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	if got, ok := resolvePageImagePath(pagesDir, base, "images/secret.png"); ok {
		t.Fatalf("expected symlink escape to be rejected, got %q", got)
	}
}

func newPageRoutesTestRouter(t *testing.T, customMenuItems string) (*gin.Engine, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dataDir := t.TempDir()
	pagesDir := filepath.Join(dataDir, "pages")
	require.NoError(t, os.MkdirAll(pagesDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(pagesDir, "docs.md"), []byte("# Public docs"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(pagesDir, "ops.md"), []byte("# Internal ops"), 0644))

	settingSvc := service.NewSettingService(&pageSettingRepoStub{
		values: map[string]string{
			service.SettingKeyCustomMenuItems: customMenuItems,
		},
	}, &config.Config{})

	router := gin.New()
	v1 := router.Group("/api/v1")
	RegisterPageRoutes(
		v1,
		dataDir,
		func(c *gin.Context) {
			if c.GetHeader("Authorization") != "Bearer admin" {
				middleware.AbortWithError(c, http.StatusUnauthorized, "UNAUTHORIZED", "invalid token")
				return
			}
			c.Set(string(middleware.ContextKeyUserRole), "admin")
			c.Next()
		},
		func(c *gin.Context) { c.Next() },
		settingSvc,
	)
	return router, dataDir
}

func TestPageRoutes_PublicMarkdownContentDoesNotRequireAuth(t *testing.T) {
	router, _ := newPageRoutesTestRouter(t, `[
		{"id":"docs","label":"Docs","url":"md:docs","visibility":"user"},
		{"id":"ops","label":"Ops","url":"md:ops","visibility":"admin"}
	]`)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/pages/docs", nil)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "# Public docs")
}

func TestPageRoutes_AdminMarkdownContentStillRequiresAdminAuth(t *testing.T) {
	router, _ := newPageRoutesTestRouter(t, `[
		{"id":"docs","label":"Docs","url":"md:docs","visibility":"user"},
		{"id":"ops","label":"Ops","url":"md:ops","visibility":"admin"}
	]`)

	unauth := httptest.NewRecorder()
	unauthReq := httptest.NewRequest(http.MethodGet, "/api/v1/pages/ops", nil)
	router.ServeHTTP(unauth, unauthReq)
	require.Equal(t, http.StatusNotFound, unauth.Code)

	authed := httptest.NewRecorder()
	authedReq := httptest.NewRequest(http.MethodGet, "/api/v1/pages/ops", nil)
	authedReq.Header.Set("Authorization", "Bearer admin")
	router.ServeHTTP(authed, authedReq)
	require.Equal(t, http.StatusOK, authed.Code)
	require.Contains(t, authed.Body.String(), "# Internal ops")
}
