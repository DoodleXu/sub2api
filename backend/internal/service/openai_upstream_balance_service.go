package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	UpstreamBalanceProviderSub2API    = "sub2api"
	UpstreamBalanceProviderNewAPI     = "new-api"
	UpstreamBalanceStatusOK           = "ok"
	UpstreamBalanceStatusError        = "error"
	openAIUpstreamBalanceHTTPTimeout  = 15 * time.Second
	openAIUpstreamBalanceErrorMaxText = 180
	newAPIQuotaPerUSD                 = 500000.0
)

type OpenAIUpstreamBalanceService struct {
	accountRepo AccountRepository
	client      *http.Client
}

func NewOpenAIUpstreamBalanceService(accountRepo AccountRepository) *OpenAIUpstreamBalanceService {
	return &OpenAIUpstreamBalanceService{
		accountRepo: accountRepo,
		client:      &http.Client{Timeout: openAIUpstreamBalanceHTTPTimeout},
	}
}

func (s *OpenAIUpstreamBalanceService) Refresh(ctx context.Context, accountID int64) (*Account, error) {
	if s == nil || s.accountRepo == nil {
		return nil, infraerrors.New(http.StatusInternalServerError, "UPSTREAM_BALANCE_NOT_CONFIGURED", "upstream balance service is not configured")
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if account == nil || !account.IsOpenAIApiKey() {
		return nil, infraerrors.BadRequest("UPSTREAM_BALANCE_INVALID_ACCOUNT", "only OpenAI API Key accounts support upstream balance")
	}
	baseURL := strings.TrimSpace(account.GetOpenAIBaseURL())
	apiKey := strings.TrimSpace(account.GetOpenAIApiKey())
	if baseURL == "" || apiKey == "" {
		return nil, infraerrors.BadRequest("UPSTREAM_BALANCE_MISSING_CREDENTIALS", "base_url and api_key are required")
	}

	updates, probeErr := s.probe(ctx, baseURL, apiKey)
	now := time.Now().UTC().Format(time.RFC3339)
	if probeErr != nil {
		updates = resetOpenAIUpstreamBalanceDerivedExtra(map[string]any{
			"upstream_balance_status":     UpstreamBalanceStatusError,
			"upstream_balance_error":      truncateText(probeErr.Error(), openAIUpstreamBalanceErrorMaxText),
			"upstream_balance_updated_at": now,
		})
	} else {
		updates = resetOpenAIUpstreamBalanceDerivedExtra(updates)
		updates["upstream_balance_status"] = UpstreamBalanceStatusOK
		updates["upstream_balance_error"] = ""
		updates["upstream_balance_updated_at"] = now
	}
	if err := s.accountRepo.UpdateExtra(ctx, account.ID, updates); err != nil {
		return nil, err
	}
	if account.Extra == nil {
		account.Extra = map[string]any{}
	}
	for k, v := range updates {
		account.Extra[k] = v
	}
	return account, nil
}

func resetOpenAIUpstreamBalanceDerivedExtra(updates map[string]any) map[string]any {
	result := map[string]any{
		"upstream_balance_provider":          nil,
		"upstream_balance_remaining":         nil,
		"upstream_balance_unit":              nil,
		"upstream_group":                     nil,
		"upstream_group_id":                  nil,
		"upstream_key_id":                    nil,
		"upstream_group_rate_multiplier":     nil,
		"upstream_effective_rate_multiplier": nil,
		"upstream_rate_source":               nil,
	}
	for key, value := range updates {
		result[key] = value
	}
	return result
}

func (s *OpenAIUpstreamBalanceService) probe(ctx context.Context, baseURL, apiKey string) (map[string]any, error) {
	if updates, err := s.probeSub2API(ctx, baseURL, apiKey); err == nil {
		return updates, nil
	} else {
		if updates, newAPIErr := s.probeNewAPI(ctx, baseURL, apiKey); newAPIErr == nil {
			return updates, nil
		}
		return nil, err
	}
}

func (s *OpenAIUpstreamBalanceService) probeSub2API(ctx context.Context, baseURL, apiKey string) (map[string]any, error) {
	var payload map[string]any
	if err := s.doGET(ctx, buildEndpointURL(baseURL, "/v1/usage"), apiKey, &payload); err != nil {
		return nil, err
	}
	remaining, ok := numberFromMap(payload, "remaining")
	if !ok {
		return nil, fmt.Errorf("sub2api response missing remaining")
	}
	updates := map[string]any{
		"upstream_balance_provider":  UpstreamBalanceProviderSub2API,
		"upstream_balance_remaining": remaining,
		"upstream_balance_unit":      strings.TrimSpace(stringFromMap(payload, "unit")),
	}
	if updates["upstream_balance_unit"] == "" {
		updates["upstream_balance_unit"] = "USD"
	}
	if groupName := upstreamGroupName(payload); groupName != "" {
		updates["upstream_group"] = groupName
	}
	if groupID, ok := upstreamGroupID(payload); ok {
		updates["upstream_group_id"] = groupID
	}
	if rate, ok := upstreamGroupRate(payload); ok {
		updates["upstream_group_rate_multiplier"] = rate
		updates["upstream_effective_rate_multiplier"] = rate
		updates["upstream_rate_source"] = "group_rate"
	}
	return updates, nil
}

func (s *OpenAIUpstreamBalanceService) probeNewAPI(ctx context.Context, baseURL, apiKey string) (map[string]any, error) {
	var payload map[string]any
	if err := s.doGET(ctx, buildEndpointURL(baseURL, "/api/usage/token/"), apiKey, &payload); err != nil {
		return nil, err
	}
	data, _ := payload["data"].(map[string]any)
	if data == nil {
		return nil, fmt.Errorf("new-api response missing data")
	}
	unit := strings.TrimSpace(stringFromMap(data, "unit"))
	convertQuotaToUSD := unit == "" || strings.EqualFold(unit, "quota")
	if convertQuotaToUSD {
		unit = "USD"
	}
	if remaining, ok := firstNumberFromMap(data, "total_available", "available_quota", "remaining_quota", "remain_quota", "quota_remaining"); ok {
		remaining = normalizeNewAPIBalance(remaining, convertQuotaToUSD)
		return map[string]any{
			"upstream_balance_provider":  UpstreamBalanceProviderNewAPI,
			"upstream_balance_remaining": nonNegative(remaining),
			"upstream_balance_unit":      unit,
		}, nil
	}
	quota, ok := numberFromMap(data, "quota")
	if !ok {
		return nil, fmt.Errorf("new-api response missing quota")
	}
	used, ok := numberFromMap(data, "used_quota")
	if !ok {
		return nil, fmt.Errorf("new-api response missing used_quota")
	}
	remaining := normalizeNewAPIBalance(quota-used, convertQuotaToUSD)
	return map[string]any{
		"upstream_balance_provider":  UpstreamBalanceProviderNewAPI,
		"upstream_balance_remaining": nonNegative(remaining),
		"upstream_balance_unit":      unit,
	}, nil
}

func normalizeNewAPIBalance(value float64, quotaToUSD bool) float64 {
	if !quotaToUSD {
		return value
	}
	return value / newAPIQuotaPerUSD
}

func (s *OpenAIUpstreamBalanceService) doGET(ctx context.Context, endpoint, apiKey string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GET %s failed: status=%d body=%s", endpoint, resp.StatusCode, truncateText(string(body), openAIUpstreamBalanceErrorMaxText))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func buildEndpointURL(baseURL, path string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(base, "/v1") && (strings.HasPrefix(path, "/v1/") || strings.HasPrefix(path, "/api/")) {
		base = strings.TrimSuffix(base, "/v1")
	}
	if u, err := url.Parse(base); err == nil && u.Path == "" && strings.HasPrefix(path, "/api/") {
		return base + path
	}
	return base + path
}

func numberFromMap(payload map[string]any, key string) (float64, bool) {
	switch v := payload[key].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func firstNumberFromMap(payload map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		if v, ok := numberFromMap(payload, key); ok {
			return v, true
		}
	}
	return 0, false
}

func stringFromMap(payload map[string]any, key string) string {
	if v, ok := payload[key].(string); ok {
		return v
	}
	return ""
}

func upstreamGroupName(payload map[string]any) string {
	if group, _ := payload["group"].(map[string]any); group != nil {
		return strings.TrimSpace(stringFromMap(group, "name"))
	}
	return strings.TrimSpace(stringFromMap(payload, "group"))
}

func upstreamGroupID(payload map[string]any) (int64, bool) {
	if v, ok := numberFromMap(payload, "group_id"); ok {
		return int64(v), true
	}
	if group, _ := payload["group"].(map[string]any); group != nil {
		if v, ok := numberFromMap(group, "id"); ok {
			return int64(v), true
		}
	}
	return 0, false
}

func upstreamGroupRate(payload map[string]any) (float64, bool) {
	if group, _ := payload["group"].(map[string]any); group != nil {
		return numberFromMap(group, "rate_multiplier")
	}
	return 0, false
}

func truncateText(text string, max int) string {
	text = strings.TrimSpace(text)
	if max <= 0 || len(text) <= max {
		return text
	}
	return text[:max]
}

func nonNegative(value float64) float64 {
	if value < 0 {
		return 0
	}
	return value
}
