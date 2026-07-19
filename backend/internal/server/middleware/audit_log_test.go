package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type auditMiddlewareTestRepo struct {
	mu   sync.Mutex
	logs []*service.AuditLog
}

func (r *auditMiddlewareTestRepo) BatchInsert(_ context.Context, logs []*service.AuditLog) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, logs...)
	return int64(len(logs)), nil
}

func (r *auditMiddlewareTestRepo) Insert(context.Context, *service.AuditLog) error { return nil }
func (r *auditMiddlewareTestRepo) List(context.Context, *service.AuditLogFilter) (*service.AuditLogList, error) {
	return &service.AuditLogList{}, nil
}
func (r *auditMiddlewareTestRepo) GetByID(context.Context, int64) (*service.AuditLog, error) {
	return nil, service.ErrAuditLogNotFound
}
func (r *auditMiddlewareTestRepo) ClearAll(context.Context, *service.AuditLog) (int64, error) {
	return 0, nil
}
func (r *auditMiddlewareTestRepo) DeleteBefore(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}

func (r *auditMiddlewareTestRepo) snapshot() []*service.AuditLog {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*service.AuditLog(nil), r.logs...)
}

func TestDeriveAuditAction(t *testing.T) {
	cases := []struct {
		method string
		path   string
		want   string
	}{
		{"PUT", "/api/v1/admin/accounts/:id", "admin.accounts.update"},
		{"POST", "/api/v1/admin/accounts", "admin.accounts.create"},
		{"DELETE", "/api/v1/admin/backups/:id", "admin.backups.delete"},
		{"GET", "/api/v1/admin/users/:id/api-keys", "admin.users.api_keys.read"},
		{"POST", "/api/v1/admin/redeem-codes/batch", "admin.redeem_codes.batch.create"},
	}
	for _, tc := range cases {
		if got := deriveAuditAction(tc.method, tc.path); got != tc.want {
			t.Fatalf("deriveAuditAction(%q, %q) = %q, want %q", tc.method, tc.path, got, tc.want)
		}
	}
}

func TestOperationsExportIsAuditedAsSensitiveRead(t *testing.T) {
	action, ok := auditSensitiveReads["GET /api/v1/admin/operations/export"]
	if !ok {
		t.Fatal("operations export must be included in sensitive read audit coverage")
	}
	if action != "admin.operations.export" {
		t.Fatalf("unexpected operations export audit action: %q", action)
	}
}

func TestLinuxDoOAuthCallbackAuditOmitsQuery(t *testing.T) {
	route := "GET /api/v1/auth/oauth/linuxdo/callback"
	if action := auditSensitiveReads[route]; action != "auth.oauth.linuxdo.callback" {
		t.Fatalf("unexpected LinuxDo callback audit action: %q", action)
	}
	if _, ok := auditQueryOmittedRoutes[route]; !ok {
		t.Fatal("LinuxDo callback audit must omit code/state query parameters")
	}
}

func TestOAuthCallbackAuditCoverageIncludesAllLoginProviders(t *testing.T) {
	routes := map[string]string{
		"GET /api/v1/auth/oauth/github/callback":   "auth.oauth.github.callback",
		"GET /api/v1/auth/oauth/google/callback":   "auth.oauth.google.callback",
		"GET /api/v1/auth/oauth/linuxdo/callback":  "auth.oauth.linuxdo.callback",
		"GET /api/v1/auth/oauth/oidc/callback":     "auth.oauth.oidc.callback",
		"GET /api/v1/auth/oauth/wechat/callback":   "auth.oauth.wechat.callback",
		"GET /api/v1/auth/oauth/dingtalk/callback": "auth.oauth.dingtalk.callback",
	}
	for route, wantAction := range routes {
		if got := auditSensitiveReads[route]; got != wantAction {
			t.Errorf("%s action = %q, want %q", route, got, wantAction)
		}
		if _, ok := auditQueryOmittedRoutes[route]; !ok {
			t.Errorf("%s must omit query parameters", route)
		}
	}
	if _, ok := auditSensitiveReads["GET /api/v1/auth/oauth/wechat/payment/callback"]; ok {
		t.Error("payment callback must not be treated as a login audit route")
	}
}

func TestLinuxDoOAuthCallbackAuditDoesNotPersistCodeOrState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &auditMiddlewareTestRepo{}
	auditService := service.NewAuditLogService(repo, nil)
	auditService.Start()
	t.Cleanup(auditService.Stop)

	router := gin.New()
	router.Use(gin.HandlerFunc(NewAuditLogMiddleware(auditService)))
	router.GET("/api/v1/auth/oauth/linuxdo/callback", func(c *gin.Context) {
		SetAuditActor(c, 42, "oauth@example.com")
		c.Set(string(ContextKeyUserRole), service.RoleUser)
		c.Set("auth_method", service.AuditAuthMethodOAuth)
		c.Redirect(http.StatusFound, "/auth/linuxdo/callback#access_token=redacted")
	})

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/linuxdo/callback?code=secret-code&state=secret-state", nil)
	router.ServeHTTP(recorder, req)
	requireStatus := recorder.Code
	if requireStatus != http.StatusFound {
		t.Fatalf("callback status = %d, want %d", requireStatus, http.StatusFound)
	}

	// Stop 会排空异步队列，避免依赖 flush ticker。
	auditService.Stop()
	logs := repo.snapshot()
	if len(logs) != 1 {
		t.Fatalf("audit log count = %d, want 1", len(logs))
	}
	entry := logs[0]
	if entry.ActorUserID == nil || *entry.ActorUserID != 42 {
		t.Fatalf("unexpected actor: %#v", entry.ActorUserID)
	}
	if entry.AuthMethod != service.AuditAuthMethodOAuth {
		t.Fatalf("auth method = %q, want oauth", entry.AuthMethod)
	}
	if _, exists := entry.Extra["query"]; exists {
		t.Fatalf("callback query must be omitted, got extra=%#v", entry.Extra)
	}
}
