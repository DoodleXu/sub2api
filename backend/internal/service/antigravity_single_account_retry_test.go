//go:build unit

package service

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

// Retained only for compatibility fixtures. Production failover never installs
// this flag after a failure and never resets the request exclusion set.
func ctxWithSingleAccountRetry() context.Context {
	return context.WithValue(context.Background(), ctxkey.SingleAccountRetry, true)
}
