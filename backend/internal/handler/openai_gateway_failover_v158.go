package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const maxOpenAIFirstOutputTimeoutSwitches = 1

func openAIForwardSucceededForScheduling(result *service.OpenAIForwardResult) bool {
	return result.SucceededForScheduling()
}

func seedOpenAIForwardImageIntentHint(c *gin.Context, channelMapped bool, imageIntent bool) {
	if channelMapped {
		// 渠道映射改变了规范请求，保持 unknown，由 Forward 按映射后的 model/body 初始化。
		return
	}
	service.SetOpenAIImageIntentHint(c, imageIntent)
}

func openAIForwardMayFailover(c *gin.Context, writerSizeBeforeForward int, failoverErr *service.UpstreamFailoverError) bool {
	if c == nil || c.Writer == nil {
		return false
	}
	if service.OpenAICompactKeepaliveAdjustedWrittenSize(c) == writerSizeBeforeForward {
		return true
	}
	return failoverErr != nil && failoverErr.SafeToFailoverAfterWrite
}

func openAIRequestAllowsFailoverReplay(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	return !failoverClientGone(c)
}

func openAIFirstOutputFailoverExhausted(failoverErr *service.UpstreamFailoverError, switchCount *int) bool {
	if failoverErr == nil || !failoverErr.SafeToFailoverAfterWrite || switchCount == nil {
		return false
	}
	if *switchCount >= maxOpenAIFirstOutputTimeoutSwitches {
		return true
	}
	*switchCount = *switchCount + 1
	return false
}
