package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/google/uuid"
)

const (
	codexInviteResetReferralKey       = "codex_referral_persistent_invite"
	codexInviteResetBackendAPIBaseURL = "https://chatgpt.com/backend-api"
	codexInviteResetMaxEmails         = 5
	// Codex Desktop 的邀请重置请求默认使用 Desktop UA；账号可配置专用 UA 覆盖。
	codexInviteResetDefaultUserAgent      = "Codex Desktop/0.0.0 (Linux; x86_64)"
	codexInviteResetDefaultTLSProfileName = "Built-in Default (Node.js 24.x)"
)

var codexInviteResetEmailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

type CodexInviteResetService struct {
	adminService        AdminService
	httpUpstream        HTTPUpstream
	openAITokenProvider *OpenAITokenProvider
	tlsFPProfileService *TLSFingerprintProfileService
}

func NewCodexInviteResetService(
	adminService AdminService,
	httpUpstream HTTPUpstream,
	openAITokenProvider *OpenAITokenProvider,
	tlsFPProfileService *TLSFingerprintProfileService,
) *CodexInviteResetService {
	return &CodexInviteResetService{
		adminService:        adminService,
		httpUpstream:        httpUpstream,
		openAITokenProvider: openAITokenProvider,
		tlsFPProfileService: tlsFPProfileService,
	}
}

type CodexInviteResetStatus struct {
	ReferralKey         string                   `json:"referral_key"`
	InviteEligibility   map[string]any           `json:"invite_eligibility,omitempty"`
	EligibilityRules    []string                 `json:"eligibility_rules,omitempty"`
	RequiresConsent     bool                     `json:"requires_consent"`
	AvailableCount      int                      `json:"available_count"`
	Credits             []CodexInviteResetCredit `json:"credits"`
	RawEligibilityRules map[string]any           `json:"raw_eligibility_rules,omitempty"`
	RawCredits          map[string]any           `json:"raw_credits,omitempty"`
}

type CodexInviteResetCredit struct {
	ID              string         `json:"id"`
	Status          string         `json:"status,omitempty"`
	Title           string         `json:"title,omitempty"`
	Description     string         `json:"description,omitempty"`
	ProfileUserID   string         `json:"profile_user_id,omitempty"`
	ProfileImageURL string         `json:"profile_image_url,omitempty"`
	Raw             map[string]any `json:"raw,omitempty"`
}

type CodexInviteResetInviteResult struct {
	Invites      []map[string]any `json:"invites,omitempty"`
	FailedEmails []string         `json:"failed_emails,omitempty"`
	Message      string           `json:"message,omitempty"`
	Raw          map[string]any   `json:"raw,omitempty"`
}

type CodexInviteResetConsumeResult struct {
	Code             string           `json:"code,omitempty"`
	CreditID         string           `json:"credit_id"`
	RedeemRequestID  string           `json:"redeem_request_id"`
	AvailableCount   *int             `json:"available_count,omitempty"`
	RemainingCredits []map[string]any `json:"remaining_credits,omitempty"`
	Raw              map[string]any   `json:"raw,omitempty"`
}

type codexInviteResetAccountContext struct {
	account    *Account
	token      string
	proxyURL   string
	userAgent  string
	tlsProfile *tlsfingerprint.Profile
}

func (s *CodexInviteResetService) GetStatus(ctx context.Context, accountID int64) (*CodexInviteResetStatus, error) {
	accountCtx, err := s.prepareAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}

	eligibility, err := s.getJSON(ctx, accountCtx, "/referrals/invite/eligibility", map[string]string{
		"referral_key": codexInviteResetReferralKey,
	})
	if err != nil {
		return nil, err
	}
	rules, err := s.getJSON(ctx, accountCtx, "/wham/referrals/eligibility_rules", map[string]string{
		"referral_key": codexInviteResetReferralKey,
	})
	if err != nil {
		return nil, err
	}
	creditsRaw, err := s.getJSON(ctx, accountCtx, "/wham/rate-limit-reset-credits", nil)
	if err != nil {
		return nil, err
	}

	credits := normalizeCodexInviteResetCredits(creditsRaw)
	availableCount := codexInviteResetIntFromMap(creditsRaw, "available_count")
	if availableCount == 0 {
		for _, credit := range credits {
			if strings.EqualFold(credit.Status, "available") {
				availableCount++
			}
		}
	}

	return &CodexInviteResetStatus{
		ReferralKey:         codexInviteResetReferralKey,
		InviteEligibility:   eligibility,
		EligibilityRules:    normalizeCodexInviteResetRules(rules),
		RequiresConsent:     codexInviteResetBoolFromMapDefault(eligibility, "requires_explicit_confirmation", true),
		AvailableCount:      availableCount,
		Credits:             credits,
		RawEligibilityRules: rules,
		RawCredits:          creditsRaw,
	}, nil
}

func (s *CodexInviteResetService) SendInvite(ctx context.Context, accountID int64, emails []string) (*CodexInviteResetInviteResult, error) {
	accountCtx, err := s.prepareAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	normalized, err := normalizeCodexInviteResetEmails(emails)
	if err != nil {
		return nil, err
	}

	raw, err := s.postJSON(ctx, accountCtx, "/wham/referrals/invite", map[string]any{
		"referral_key": codexInviteResetReferralKey,
		"emails":       normalized,
	})
	if err != nil {
		return nil, err
	}

	return &CodexInviteResetInviteResult{
		Invites:      codexInviteResetMapSliceFromMap(raw, "invites"),
		FailedEmails: codexInviteResetStringSliceFromMap(raw, "failed_emails"),
		Message:      codexInviteResetStringFromMap(raw, "message"),
		Raw:          raw,
	}, nil
}

func (s *CodexInviteResetService) Consume(ctx context.Context, accountID int64, creditID string) (*CodexInviteResetConsumeResult, error) {
	accountCtx, err := s.prepareAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	creditID = strings.TrimSpace(creditID)
	if creditID == "" {
		return nil, infraerrors.BadRequest("CODEX_INVITE_RESET_CREDIT_ID_REQUIRED", "credit_id is required")
	}
	redeemRequestID := uuid.NewString()

	raw, err := s.postJSON(ctx, accountCtx, "/wham/rate-limit-reset-credits/consume", map[string]any{
		"credit_id":         creditID,
		"redeem_request_id": redeemRequestID,
	})
	if err != nil {
		return nil, err
	}

	var availableCount *int
	if _, ok := raw["available_count"]; ok {
		v := codexInviteResetIntFromMap(raw, "available_count")
		availableCount = &v
	}
	return &CodexInviteResetConsumeResult{
		Code:             codexInviteResetStringFromMap(raw, "code"),
		CreditID:         creditID,
		RedeemRequestID:  redeemRequestID,
		AvailableCount:   availableCount,
		RemainingCredits: codexInviteResetMapSliceFromMap(raw, "credits"),
		Raw:              raw,
	}, nil
}

func (s *CodexInviteResetService) prepareAccount(ctx context.Context, accountID int64) (*codexInviteResetAccountContext, error) {
	if s == nil || s.adminService == nil {
		return nil, infraerrors.InternalServer("CODEX_INVITE_RESET_SERVICE_NOT_CONFIGURED", "codex invite reset service is not configured")
	}
	account, err := s.adminService.GetAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, infraerrors.NotFound("ACCOUNT_NOT_FOUND", "account not found")
	}
	if !account.IsOpenAIOAuth() {
		return nil, infraerrors.BadRequest("CODEX_INVITE_RESET_UNSUPPORTED_ACCOUNT", "only OpenAI OAuth accounts support Codex official quota reset")
	}

	token := ""
	if s.openAITokenProvider != nil {
		token, err = s.openAITokenProvider.GetAccessToken(ctx, account)
		if err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(token) == "" {
		token = account.GetOpenAIAccessToken()
	}
	if strings.TrimSpace(token) == "" {
		return nil, infraerrors.BadRequest("CODEX_INVITE_RESET_MISSING_TOKEN", "missing OpenAI OAuth access token")
	}

	proxyURL := ""
	if account.ProxyID != nil {
		proxy, proxyErr := s.adminService.GetProxy(ctx, *account.ProxyID)
		if proxyErr != nil {
			return nil, proxyErr
		}
		if proxy != nil {
			proxyURL = proxy.URL()
		}
	}

	return &codexInviteResetAccountContext{
		account:    account,
		token:      token,
		proxyURL:   proxyURL,
		userAgent:  s.resolveUserAgent(account),
		tlsProfile: s.resolveTLSProfile(account),
	}, nil
}

func (s *CodexInviteResetService) resolveUserAgent(account *Account) string {
	if userAgent := account.GetCodexInviteResetUserAgent(); userAgent != "" {
		return userAgent
	}
	return codexInviteResetDefaultUserAgent
}

func (s *CodexInviteResetService) resolveTLSProfile(account *Account) *tlsfingerprint.Profile {
	if s == nil || s.tlsFPProfileService == nil {
		return codexInviteResetDefaultTLSProfile()
	}
	if profileID := account.GetCodexInviteResetTLSFingerprintProfileID(); profileID != nil {
		if profile, ok := s.tlsFPProfileService.ResolveTokenTLSProfileByID(*profileID); ok {
			return profile
		}
	}
	if profile := s.tlsFPProfileService.ResolveTLSProfile(account); profile != nil {
		return profile
	}
	return codexInviteResetDefaultTLSProfile()
}

func codexInviteResetDefaultTLSProfile() *tlsfingerprint.Profile {
	return &tlsfingerprint.Profile{Name: codexInviteResetDefaultTLSProfileName}
}

func (s *CodexInviteResetService) getJSON(ctx context.Context, accountCtx *codexInviteResetAccountContext, path string, query map[string]string) (map[string]any, error) {
	target, err := buildCodexInviteResetURL(path, query)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	s.applyHeaders(req, accountCtx)
	return s.doJSON(req, accountCtx)
}

func (s *CodexInviteResetService) postJSON(ctx context.Context, accountCtx *codexInviteResetAccountContext, path string, body map[string]any) (map[string]any, error) {
	target, err := buildCodexInviteResetURL(path, nil)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	s.applyHeaders(req, accountCtx)
	return s.doJSON(req, accountCtx)
}

func (s *CodexInviteResetService) applyHeaders(req *http.Request, accountCtx *codexInviteResetAccountContext) {
	*req = *req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
	req.Host = "chatgpt.com"
	req.Header.Set("Authorization", "Bearer "+accountCtx.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("OAI-Language", "zh-CN")
	req.Header.Set("originator", "Codex Desktop")
	req.Header.Set("X-OpenAI-Attach-Auth", "1")
	req.Header.Set("X-OpenAI-Attach-Integrity-State", "1")
	req.Header.Set("User-Agent", accountCtx.userAgent)
	if chatgptAccountID := accountCtx.account.GetChatGPTAccountID(); chatgptAccountID != "" {
		req.Header.Set("chatgpt-account-id", chatgptAccountID)
	}
}

func (s *CodexInviteResetService) doJSON(req *http.Request, accountCtx *codexInviteResetAccountContext) (map[string]any, error) {
	if s.httpUpstream == nil {
		return nil, infraerrors.InternalServer("HTTP_UPSTREAM_NOT_CONFIGURED", "http upstream is not configured")
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
	resp, err := s.httpUpstream.DoWithTLS(req, accountCtx.proxyURL, accountCtx.account.ID, accountCtx.account.Concurrency, accountCtx.tlsProfile)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, openAIUpstreamErrorBodyReadLimit))
	if readErr != nil {
		return nil, readErr
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		return nil, infraerrors.Newf(resp.StatusCode, "CODEX_INVITE_RESET_UPSTREAM_ERROR", "codex invite reset upstream returned %d: %s", resp.StatusCode, message)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return map[string]any{}, nil
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode codex invite reset response: %w", err)
	}
	return result, nil
}

func buildCodexInviteResetURL(path string, query map[string]string) (string, error) {
	base, err := url.Parse(codexInviteResetBackendAPIBaseURL)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	base.Path = strings.TrimRight(base.Path, "/") + path
	if len(query) > 0 {
		values := base.Query()
		for key, value := range query {
			values.Set(key, value)
		}
		base.RawQuery = values.Encode()
	}
	return base.String(), nil
}

func normalizeCodexInviteResetEmails(emails []string) ([]string, error) {
	result := make([]string, 0, len(emails))
	seen := make(map[string]struct{}, len(emails))
	for _, raw := range emails {
		for _, part := range splitCodexInviteResetEmailInput(raw) {
			email := strings.TrimSpace(part)
			if email == "" {
				continue
			}
			key := strings.ToLower(email)
			if _, exists := seen[key]; exists {
				continue
			}
			if !codexInviteResetEmailPattern.MatchString(email) {
				return nil, infraerrors.BadRequest("CODEX_INVITE_RESET_INVALID_EMAIL", fmt.Sprintf("invalid email: %s", email))
			}
			seen[key] = struct{}{}
			result = append(result, email)
			if len(result) > codexInviteResetMaxEmails {
				return nil, infraerrors.BadRequest("CODEX_INVITE_RESET_EMAIL_LIMIT", fmt.Sprintf("最多一次邀请 %d 个邮箱", codexInviteResetMaxEmails))
			}
		}
	}
	if len(result) == 0 {
		return nil, infraerrors.BadRequest("CODEX_INVITE_RESET_EMAILS_REQUIRED", "emails are required")
	}
	return result, nil
}

func splitCodexInviteResetEmailInput(input string) []string {
	return strings.FieldsFunc(input, func(r rune) bool {
		switch r {
		case ',', ';', '\n', '\r', '\t', ' ':
			return true
		default:
			return false
		}
	})
}

func normalizeCodexInviteResetCredits(raw map[string]any) []CodexInviteResetCredit {
	items := codexInviteResetMapSliceFromMap(raw, "credits")
	credits := make([]CodexInviteResetCredit, 0, len(items))
	for _, item := range items {
		id := codexInviteResetStringFromMap(item, "id")
		if id == "" {
			continue
		}
		credits = append(credits, CodexInviteResetCredit{
			ID:              id,
			Status:          codexInviteResetStringFromMap(item, "status"),
			Title:           codexInviteResetStringFromMap(item, "title"),
			Description:     codexInviteResetStringFromMap(item, "description"),
			ProfileUserID:   codexInviteResetStringFromMap(item, "profile_user_id"),
			ProfileImageURL: codexInviteResetStringFromMap(item, "profile_image_url"),
			Raw:             item,
		})
	}
	return credits
}

func normalizeCodexInviteResetRules(raw map[string]any) []string {
	rulesRaw, ok := raw["rules"].([]any)
	if !ok {
		return nil
	}
	rules := make([]string, 0, len(rulesRaw))
	for _, item := range rulesRaw {
		switch value := item.(type) {
		case string:
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				rules = append(rules, trimmed)
			}
		case map[string]any:
			for _, key := range []string{"text", "description", "message", "title"} {
				if text := codexInviteResetStringFromMap(value, key); text != "" {
					rules = append(rules, text)
					break
				}
			}
		}
	}
	return rules
}

func codexInviteResetStringFromMap(raw map[string]any, key string) string {
	if raw == nil {
		return ""
	}
	value, ok := raw[key]
	if !ok || value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func codexInviteResetIntFromMap(raw map[string]any, key string) int {
	if raw == nil {
		return 0
	}
	switch value := raw[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		i, _ := value.Int64()
		return int(i)
	default:
		return 0
	}
}

func codexInviteResetBoolFromMapDefault(raw map[string]any, key string, fallback bool) bool {
	if raw == nil {
		return fallback
	}
	value, ok := raw[key]
	if !ok || value == nil {
		return fallback
	}
	if b, ok := value.(bool); ok {
		return b
	}
	return fallback
}

func codexInviteResetStringSliceFromMap(raw map[string]any, key string) []string {
	values, ok := raw[key].([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if s := strings.TrimSpace(fmt.Sprint(value)); s != "" {
			result = append(result, s)
		}
	}
	return result
}

func codexInviteResetMapSliceFromMap(raw map[string]any, key string) []map[string]any {
	values, ok := raw[key].([]any)
	if !ok {
		return nil
	}
	result := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if item, ok := value.(map[string]any); ok {
			result = append(result, item)
		}
	}
	return result
}
