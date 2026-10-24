package service

import (
	"context"
	"encoding/json"
)

type AccountQualitySummary struct {
	State          string   `json:"state"`
	Samples        int      `json:"samples"`
	ErrorRate      *float64 `json:"error_rate,omitempty"`
	LatencySamples int      `json:"latency_samples"`
	P50MS          *int64   `json:"p50_ms,omitempty"`
	P95MS          *int64   `json:"p95_ms,omitempty"`
	UpdatedAt      int64    `json:"updated_at"`
	OpenUntil      int64    `json:"open_until,omitempty"`
	Window         string   `json:"window"`
}

type AccountQualityModelView struct {
	Model      string `json:"model"`
	Capability string `json:"capability"`
	Transport  string `json:"transport"`
	Effort     string `json:"effort,omitempty"`
	Stream     bool   `json:"stream"`
	AccountQualitySummary
}

type AccountQualityView struct {
	Strategy  string                    `json:"strategy"`
	AutoPause *AccountBalancePause      `json:"auto_pause,omitempty"`
	Models    []AccountQualityModelView `json:"models,omitempty"`
}

func (s *RateLimitService) AccountQualityView(ctx context.Context, a *Account, details bool) *AccountQualityView {
	if s == nil || a == nil {
		return nil
	}
	view := &AccountQualityView{Strategy: "legacy"}
	if raw := a.Extra[AccountBalancePauseKey]; raw != nil {
		bytes, _ := json.Marshal(raw)
		var pause AccountBalancePause
		if json.Unmarshal(bytes, &pause) == nil {
			view.AutoPause = &pause
		}
	}
	return view
}
