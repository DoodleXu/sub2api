package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/model"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

type codexInviteResetAdminServiceStub struct {
	AdminService
	account *Account
	proxy   *Proxy
}

func (s codexInviteResetAdminServiceStub) GetAccount(context.Context, int64) (*Account, error) {
	return s.account, nil
}

func (s codexInviteResetAdminServiceStub) GetProxy(context.Context, int64) (*Proxy, error) {
	return s.proxy, nil
}

type codexInviteResetHTTPUpstreamStub struct {
	responses []*http.Response
	requests  []*http.Request
	bodies    []string
	profiles  []*tlsfingerprint.Profile
}

func (s *codexInviteResetHTTPUpstreamStub) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	return s.DoWithTLS(req, proxyURL, accountID, accountConcurrency, nil)
}

func (s *codexInviteResetHTTPUpstreamStub) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	body := ""
	if req.Body != nil {
		payload, _ := io.ReadAll(req.Body)
		body = string(payload)
		req.Body = io.NopCloser(strings.NewReader(body))
	}
	s.requests = append(s.requests, req)
	s.bodies = append(s.bodies, body)
	s.profiles = append(s.profiles, profile)
	if len(s.responses) == 0 {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	}
	resp := s.responses[0]
	s.responses = s.responses[1:]
	return resp, nil
}

func TestCodexInviteResetServiceGetStatusAggregatesDesktopEndpoints(t *testing.T) {
	account := &Account{
		ID:          42,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Concurrency: 3,
		Credentials: map[string]any{
			"access_token":       "oauth-token",
			"chatgpt_account_id": "chatgpt-acc",
		},
	}
	upstream := &codexInviteResetHTTPUpstreamStub{responses: []*http.Response{
		codexInviteResetJSONResponse(`{"requires_explicit_confirmation":true}`),
		codexInviteResetJSONResponse(`{"rules":[{"text":"friend must send first Codex message"}]}`),
		codexInviteResetJSONResponse(`{"available_count":2,"credits":[{"id":"credit-1","status":"available","title":"Reset"},{"id":"credit-2","status":"available"}]}`),
	}}
	svc := NewCodexInviteResetService(codexInviteResetAdminServiceStub{account: account}, upstream, nil, nil)

	status, err := svc.GetStatus(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, codexInviteResetReferralKey, status.ReferralKey)
	require.Equal(t, 2, status.AvailableCount)
	require.True(t, status.RequiresConsent)
	require.Len(t, status.Credits, 2)
	require.Equal(t, "friend must send first Codex message", status.EligibilityRules[0])

	require.Len(t, upstream.requests, 3)
	require.Equal(t, "/backend-api/referrals/invite/eligibility", upstream.requests[0].URL.Path)
	require.Equal(t, codexInviteResetReferralKey, upstream.requests[0].URL.Query().Get("referral_key"))
	require.Equal(t, "/backend-api/wham/referrals/eligibility_rules", upstream.requests[1].URL.Path)
	require.Equal(t, "/backend-api/wham/rate-limit-reset-credits", upstream.requests[2].URL.Path)
	require.Equal(t, "Bearer oauth-token", upstream.requests[0].Header.Get("Authorization"))
	require.Equal(t, "Codex Desktop", upstream.requests[0].Header.Get("originator"))
	require.Equal(t, codexInviteResetDefaultUserAgent, upstream.requests[0].Header.Get("User-Agent"))
	require.Equal(t, "1", upstream.requests[0].Header.Get("X-OpenAI-Attach-Auth"))
	require.Equal(t, "1", upstream.requests[0].Header.Get("X-OpenAI-Attach-Integrity-State"))
	require.Equal(t, "chatgpt-acc", upstream.requests[0].Header.Get("chatgpt-account-id"))
	require.Equal(t, HTTPUpstreamProfileOpenAI, HTTPUpstreamProfileFromContext(upstream.requests[0].Context()))
}

func TestCodexInviteResetServiceSendInviteNormalizesEmails(t *testing.T) {
	account := &Account{
		ID:          7,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "oauth-token"},
	}
	upstream := &codexInviteResetHTTPUpstreamStub{responses: []*http.Response{
		codexInviteResetJSONResponse(`{"invites":[{"email":"a@example.com"}],"message":"ok"}`),
	}}
	svc := NewCodexInviteResetService(codexInviteResetAdminServiceStub{account: account}, upstream, nil, nil)

	result, err := svc.SendInvite(context.Background(), account.ID, []string{"a@example.com, b@example.com", "A@example.com"})
	require.NoError(t, err)
	require.Equal(t, "ok", result.Message)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "/backend-api/wham/referrals/invite", upstream.requests[0].URL.Path)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(upstream.bodies[0]), &payload))
	require.Equal(t, codexInviteResetReferralKey, payload["referral_key"])
	require.Equal(t, []any{"a@example.com", "b@example.com"}, payload["emails"])
}

func TestCodexInviteResetServiceConsumeSendsRedeemRequestID(t *testing.T) {
	account := &Account{
		ID:          9,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "oauth-token"},
	}
	upstream := &codexInviteResetHTTPUpstreamStub{responses: []*http.Response{
		codexInviteResetJSONResponse(`{"code":"reset","available_count":0}`),
	}}
	svc := NewCodexInviteResetService(codexInviteResetAdminServiceStub{account: account}, upstream, nil, nil)

	result, err := svc.Consume(context.Background(), account.ID, "credit-1")
	require.NoError(t, err)
	require.Equal(t, "reset", result.Code)
	require.Equal(t, "credit-1", result.CreditID)
	require.NotEmpty(t, result.RedeemRequestID)
	require.NotNil(t, result.AvailableCount)
	require.Equal(t, 0, *result.AvailableCount)

	var payload map[string]string
	require.NoError(t, json.Unmarshal([]byte(upstream.bodies[0]), &payload))
	require.Equal(t, "credit-1", payload["credit_id"])
	require.Equal(t, result.RedeemRequestID, payload["redeem_request_id"])
}

func TestCodexInviteResetServiceUsesConfiguredInviteResetUserAgent(t *testing.T) {
	account := &Account{
		ID:          43,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "oauth-token"},
		Extra: map[string]any{
			"codex_invite_reset_user_agent": " Codex Desktop/0.135.0-alpha.1 (Windows 10.0.26200; x86_64) ",
		},
	}
	upstream := &codexInviteResetHTTPUpstreamStub{responses: []*http.Response{
		codexInviteResetJSONResponse(`{"requires_explicit_confirmation":true}`),
		codexInviteResetJSONResponse(`{"rules":[]}`),
		codexInviteResetJSONResponse(`{"available_count":0,"credits":[]}`),
	}}
	svc := NewCodexInviteResetService(codexInviteResetAdminServiceStub{account: account}, upstream, nil, nil)

	_, err := svc.GetStatus(context.Background(), account.ID)
	require.NoError(t, err)
	require.Len(t, upstream.requests, 3)
	require.Equal(t, "Codex Desktop/0.135.0-alpha.1 (Windows 10.0.26200; x86_64)", upstream.requests[0].Header.Get("User-Agent"))
}

func TestCodexInviteResetServiceDoesNotReuseUnrelatedUserAgent(t *testing.T) {
	account := &Account{
		ID:          45,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "oauth-token"},
		Extra: map[string]any{
			"chatgpt_oauth_token_user_agent": "codex-tui/0.135.0 (Windows 10.0.26200; x86_64)",
		},
	}
	upstream := &codexInviteResetHTTPUpstreamStub{responses: []*http.Response{
		codexInviteResetJSONResponse(`{"requires_explicit_confirmation":true}`),
		codexInviteResetJSONResponse(`{"rules":[]}`),
		codexInviteResetJSONResponse(`{"available_count":0,"credits":[]}`),
	}}
	svc := NewCodexInviteResetService(codexInviteResetAdminServiceStub{account: account}, upstream, nil, nil)

	_, err := svc.GetStatus(context.Background(), account.ID)
	require.NoError(t, err)
	require.Len(t, upstream.requests, 3)
	require.Equal(t, codexInviteResetDefaultUserAgent, upstream.requests[0].Header.Get("User-Agent"))
}

func TestCodexInviteResetServiceUsesConfiguredInviteResetTLSProfile(t *testing.T) {
	inviteResetProfileID := int64(20)
	account := &Account{
		ID:          44,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "oauth-token"},
		Extra: map[string]any{
			"codex_invite_reset_tls_fingerprint_profile_id": inviteResetProfileID,
		},
	}
	upstream := &codexInviteResetHTTPUpstreamStub{responses: []*http.Response{
		codexInviteResetJSONResponse(`{"requires_explicit_confirmation":true}`),
		codexInviteResetJSONResponse(`{"rules":[]}`),
		codexInviteResetJSONResponse(`{"available_count":0,"credits":[]}`),
	}}
	profileService := &TLSFingerprintProfileService{
		localCache: map[int64]*model.TLSFingerprintProfile{
			inviteResetProfileID: {ID: inviteResetProfileID, Name: "Codex Desktop Test"},
		},
	}
	svc := NewCodexInviteResetService(codexInviteResetAdminServiceStub{account: account}, upstream, nil, profileService)

	_, err := svc.GetStatus(context.Background(), account.ID)
	require.NoError(t, err)
	require.Len(t, upstream.profiles, 3)
	require.NotNil(t, upstream.profiles[0])
	require.Equal(t, "Codex Desktop Test", upstream.profiles[0].Name)
}

func TestNormalizeCodexInviteResetEmailsRejectsInvalidAndTooMany(t *testing.T) {
	_, err := normalizeCodexInviteResetEmails([]string{"bad-email"})
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))

	_, err = normalizeCodexInviteResetEmails([]string{"a@x.com,b@x.com,c@x.com,d@x.com,e@x.com,f@x.com"})
	require.Error(t, err)
	require.Equal(t, "CODEX_INVITE_RESET_EMAIL_LIMIT", infraerrors.Reason(err))
}

func codexInviteResetJSONResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
