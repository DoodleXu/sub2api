package handler

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReleaseOpenAIWSFailoverSlotsReleasesImageGateBeforeAccountSwitch(t *testing.T) {
	limiter := &imageConcurrencyLimiter{}
	imageRelease, acquired := limiter.TryAcquire(true, 1)
	require.True(t, acquired)

	accountReleaseCalls := 0
	accountRelease := func() { accountReleaseCalls++ }
	releaseOpenAIWSFailoverSlots(&accountRelease, &imageRelease)

	require.Nil(t, accountRelease)
	require.Nil(t, imageRelease)
	require.Equal(t, 1, accountReleaseCalls)

	secondRelease, acquired := limiter.Acquire(context.Background(), true, 1, false, 0, 0)
	require.True(t, acquired, "failover must not inherit the previous account's image slot")
	secondRelease()

	// 清理函数必须幂等，避免 defer 在连接退出时重复释放。
	releaseOpenAIWSFailoverSlots(&accountRelease, &imageRelease)
	require.Equal(t, 1, accountReleaseCalls)
}
