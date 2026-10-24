package service

import (
	"context"
	"github.com/google/uuid"
)

// AccountQualitySchedulingSetting is retained only for old settings clients.
// Selection uses the upstream scheduler; this former override is always disabled.
const AccountQualitySchedulingSetting = "account_quality_scheduling_enabled"

// AccountQualityRequest preserves the forwarding context used by adapters and
// the financial safety extension, without a separate ranking or probe system.
type AccountQualityRequest struct {
	ID         string
	Model      string
	Capability string
	Transport  string
	Effort     string
	Stream     bool
}
type qualityRequestKey struct{}
type qualityAttemptKey struct{}

func WithAccountQualityRequest(ctx context.Context, request AccountQualityRequest) context.Context {
	ctx = withAccountQualityCredentials(ctx)
	if request.ID == "" {
		request.ID = uuid.NewString()
	}
	return context.WithValue(ctx, qualityRequestKey{}, request)
}

func WithAccountQualityCapability(ctx context.Context, capability string) context.Context {
	request := qualityRequest(ctx, "")
	request.Capability = capability
	if request.ID == "" {
		request.ID = uuid.NewString()
	}
	return context.WithValue(ctx, qualityRequestKey{}, request)
}

func AccountQualityRequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	q, _ := ctx.Value(qualityRequestKey{}).(AccountQualityRequest)
	return q.ID
}

func qualityRequest(ctx context.Context, model string) AccountQualityRequest {
	q, _ := ctx.Value(qualityRequestKey{}).(AccountQualityRequest)
	if effort := RequestedReasoningEffortFromContext(ctx); effort != nil && q.Effort == "" {
		q.Effort = *effort
	}
	if model != "" {
		q.Model = model
	}
	if q.Capability == "" {
		q.Capability = "generation"
	}
	if q.Transport == "" {
		q.Transport = "http"
	}
	return q
}
