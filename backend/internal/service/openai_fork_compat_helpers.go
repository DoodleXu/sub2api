package service

import (
	"strings"
)

//nolint:unused

const OpenAIHTTPContinuationUnsupportedReason = GatewayFailureReason("openai_http_continuation_unsupported")

const openAIHTTPResponseOwnerContextKeyCompat = "openai_http_response_owner"

func ResolveOpenAIAccountUpstreamModelForRequest(account *Account, requestedModel string, requireCompact bool) string {
	return resolveOpenAIAccountUpstreamModelForRequest(account, requestedModel, requireCompact)
}

func resolveOpenAIForwardMappedModels(account *Account, requestedModel string, requireCompact bool) (billingModel, upstreamModel string) {
	requestedModel = strings.TrimSpace(requestedModel)
	if account != nil && account.IsOpenAIPassthroughEnabled() {
		billingModel = requestedModel
	} else if account != nil {
		billingModel = strings.TrimSpace(account.GetMappedModel(requestedModel))
	}
	if billingModel == "" {
		billingModel = requestedModel
	}
	upstreamModel = resolveOpenAIAccountUpstreamModelForRequest(account, requestedModel, requireCompact)
	if strings.TrimSpace(upstreamModel) == "" {
		upstreamModel = billingModel
	}
	return billingModel, upstreamModel
}

func resolveOpenAIErrorSchedulingModel(billingModel, upstreamModel string) string {
	if strings.TrimSpace(upstreamModel) != "" {
		return strings.TrimSpace(upstreamModel)
	}
	return strings.TrimSpace(billingModel)
}
