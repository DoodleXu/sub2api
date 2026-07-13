//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIGatewayHandlerAlphaSearch_ContentModerationBlocksSearchQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var moderationPayload []byte
	moderationServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		moderationPayload, err = io.ReadAll(r.Body)
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"category_scores":{"illicit":0.99}}]}`))
	}))
	defer moderationServer.Close()

	moderationCfg := &service.ContentModerationConfig{
		Enabled:      true,
		Mode:         service.ContentModerationModePreBlock,
		BaseURL:      moderationServer.URL,
		Model:        "omni-moderation-latest",
		APIKeys:      []string{"sk-moderation"},
		SampleRate:   100,
		AllGroups:    true,
		BlockMessage: "alpha search blocked",
	}
	rawCfg, err := json.Marshal(moderationCfg)
	require.NoError(t, err)
	moderationRepo := &contentModerationHandlerTestRepo{}
	moderationSvc := service.NewContentModerationService(
		&contentModerationHandlerSettingRepo{values: map[string]string{
			service.SettingKeyRiskControlEnabled:      "true",
			service.SettingKeyContentModerationConfig: string(rawCfg),
		}},
		moderationRepo,
		nil,
		nil,
		nil,
		nil,
		nil,
	)

	groupID := int64(7301)
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"benign wrapper"}]}],
		"commands":{"search_query":[{"q":"blocked search phrase"}]}
	}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID:      7302,
		GroupID: &groupID,
		Group:   &service.Group{ID: groupID, Name: "openai", Platform: service.PlatformOpenAI},
		User:    &service.User{ID: 7303, Status: service.StatusActive},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 7303, Concurrency: 1})
	h := &OpenAIGatewayHandler{
		gatewayService:           &service.OpenAIGatewayService{},
		billingCacheService:      &service.BillingCacheService{},
		apiKeyService:            &service.APIKeyService{},
		contentModerationService: moderationSvc,
		concurrencyHelper:        NewConcurrencyHelper(service.NewConcurrencyService(nil), SSEPingFormatNone, 0),
	}

	h.AlphaSearch(c)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "alpha search blocked")
	require.Contains(t, string(moderationPayload), "benign wrapper")
	require.Contains(t, string(moderationPayload), "blocked search phrase")
	require.Eventually(t, func() bool {
		return len(moderationRepo.logSnapshot()) == 1
	}, time.Second, 10*time.Millisecond)
}

type alphaSearchHandlerAccountRepo struct {
	service.AccountRepository
	account service.Account
}

func (r *alphaSearchHandlerAccountRepo) GetByID(_ context.Context, id int64) (*service.Account, error) {
	if id != r.account.ID {
		return nil, service.ErrNoAvailableAccounts
	}
	account := r.account
	return &account, nil
}

func (r *alphaSearchHandlerAccountRepo) ListSchedulableByGroupIDAndPlatform(_ context.Context, _ int64, platform string) ([]service.Account, error) {
	return r.accountsForPlatform(platform), nil
}

func (r *alphaSearchHandlerAccountRepo) ListSchedulableByPlatform(_ context.Context, platform string) ([]service.Account, error) {
	return r.accountsForPlatform(platform), nil
}

func (r *alphaSearchHandlerAccountRepo) ListSchedulableUngroupedByPlatform(_ context.Context, platform string) ([]service.Account, error) {
	return r.accountsForPlatform(platform), nil
}

func (r *alphaSearchHandlerAccountRepo) accountsForPlatform(platform string) []service.Account {
	if r.account.Platform != platform {
		return nil
	}
	return []service.Account{r.account}
}

type alphaSearchRetryHTTPUpstream struct {
	service.HTTPUpstream
	mu         sync.Mutex
	accountIDs []int64
}

func (u *alphaSearchRetryHTTPUpstream) Do(_ *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	u.accountIDs = append(u.accountIDs, accountID)
	call := len(u.accountIDs)
	u.mu.Unlock()
	if call == 1 {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewBufferString(`{"error":{"message":"retry same account"}}`)),
		}, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(`{"output":"search result"}`)),
	}, nil
}

func (u *alphaSearchRetryHTTPUpstream) calls() []int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]int64(nil), u.accountIDs...)
}

func TestOpenAIGatewayHandlerAlphaSearch_PoolModeRetriesSameAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)

	groupID := int64(7401)
	accountID := int64(7402)
	accountRepo := &alphaSearchHandlerAccountRepo{account: service.Account{
		ID:          accountID,
		Name:        "pool-search",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":               "sk-upstream",
			"pool_mode":             true,
			"pool_mode_retry_count": 1,
		},
	}}
	upstream := &alphaSearchRetryHTTPUpstream{}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	gatewaySvc := service.NewOpenAIGatewayService(
		accountRepo,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		cfg,
		nil,
		nil,
		service.NewBillingService(cfg, nil),
		nil,
		nil,
		upstream,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)
	billingCacheSvc := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCacheSvc.Stop)
	h := NewOpenAIGatewayHandler(
		gatewaySvc,
		service.NewConcurrencyService(nil),
		billingCacheSvc,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg),
		nil,
		nil,
		nil,
		nil,
		nil,
		cfg,
	)
	h.maxAccountSwitches = 1

	body := []byte(`{"model":"gpt-5.6-sol","input":"search latest news","commands":{"search_query":[{"q":"latest news"}]}}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID:      7403,
		GroupID: &groupID,
		Group:   &service.Group{ID: groupID, Name: "openai", Platform: service.PlatformOpenAI, Status: service.StatusActive},
		User:    &service.User{ID: 7404, Status: service.StatusActive, Balance: 100},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 7404, Concurrency: 0})

	h.AlphaSearch(c)

	require.Equal(t, []int64{accountID, accountID}, upstream.calls())
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"output":"search result"}`, rec.Body.String())
}
