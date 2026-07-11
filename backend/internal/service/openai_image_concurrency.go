package service

import (
	"context"
	"errors"
)

// ErrOpenAIImageGenerationConcurrencyLimited indicates that the final,
// account-normalized Responses payload could not acquire the image gate.
var ErrOpenAIImageGenerationConcurrencyLimited = errors.New("OpenAI image generation concurrency limit exceeded")

// OpenAIImageGenerationSlotAcquirer is installed by the HTTP handler so the
// service can acquire the process-local image gate after all account-specific
// bridge and normalization steps have produced the final request semantics.
type OpenAIImageGenerationSlotAcquirer func(ctx context.Context) (release func(), err error)

type openAIImageGenerationSlotAcquirerContextKey struct{}

func WithOpenAIImageGenerationSlotAcquirer(ctx context.Context, acquire OpenAIImageGenerationSlotAcquirer) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if acquire == nil {
		return ctx
	}
	return context.WithValue(ctx, openAIImageGenerationSlotAcquirerContextKey{}, acquire)
}

func acquireOpenAIImageGenerationSlot(ctx context.Context) (func(), error) {
	if ctx == nil {
		return nil, nil
	}
	acquire, _ := ctx.Value(openAIImageGenerationSlotAcquirerContextKey{}).(OpenAIImageGenerationSlotAcquirer)
	if acquire == nil {
		return nil, nil
	}
	return acquire(ctx)
}
