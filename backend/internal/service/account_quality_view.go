package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

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
type accountQualityViewStore interface {
	ReadAccountQualityAccount(context.Context, int64, int64) (map[string]AccountQualityState, error)
}

func (s *RateLimitService) AccountQualityView(ctx context.Context, a *Account, details bool) *AccountQualityView {
	if s == nil || a == nil {
		return nil
	}
	view := &AccountQualityView{Strategy: "legacy"}
	if s.QualitySchedulingEnabled(ctx) {
		view.Strategy = "health_first"
	}
	if raw := a.Extra[AccountBalancePauseKey]; raw != nil {
		bytes, _ := json.Marshal(raw)
		var pause AccountBalancePause
		if json.Unmarshal(bytes, &pause) == nil {
			view.AutoPause = &pause
		}
	}
	if !details || s.quality == nil {
		return view
	}
	states := map[string]AccountQualityState{}
	if store, ok := s.quality.store.(accountQualityViewStore); ok {
		readCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		defer cancel()
		if read, err := store.ReadAccountQualityAccount(readCtx, a.ID, AccountSchedulingGeneration(a)); err == nil {
			states = read
		}
	}
	if len(states) == 0 {
		s.quality.mu.Lock()
		prefix := fmt.Sprintf("account_quality:{%d}:%d:", a.ID, AccountSchedulingGeneration(a))
		for k, v := range s.quality.states {
			if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
				states[k] = v.state
			}
		}
		s.quality.mu.Unlock()
	}
	for _, state := range states {
		if len(state.Samples) == 0 {
			continue
		}
		dim := state.Samples[len(state.Samples)-1].Dimension
		view.Models = append(view.Models, AccountQualityModelView{Model: dim.Model, Capability: dim.Capability, Transport: dim.Transport, Effort: dim.Effort, Stream: dim.Stream, AccountQualitySummary: SummarizeAccountQuality(state, time.Now())})
	}
	sort.Slice(view.Models, func(i, j int) bool { return view.Models[i].UpdatedAt > view.Models[j].UpdatedAt })
	return view
}
