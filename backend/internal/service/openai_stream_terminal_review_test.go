//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIPassthroughBareErrorAfterOutputNeedsFailedTerminal(t *testing.T) {
	created := `data: {"type":"response.created","response":{"id":"resp_review","created_at":1700000000}}` + "\n\n"
	partial := `data: {"type":"response.output_text.delta","delta":"partial"}` + "\n\n"
	bare := `event: error` + "\n" + `data: {"type":"error","sequence_number":2,"error":{"code":"server_is_overloaded","message":"busy"},"usage":{"input_tokens":7,"output_tokens":2}}` + "\n\n"
	for _, ending := range []string{"done", "eof", "read_error"} {
		t.Run(ending, func(t *testing.T) {
			payload := created + partial + bare
			var body io.ReadCloser = io.NopCloser(strings.NewReader(payload))
			if ending == "done" {
				body = io.NopCloser(strings.NewReader(payload + "data: [DONE]\n\n"))
			} else if ending == "read_error" {
				body = &passthroughFlushTestErrorBody{payload: []byte(payload), err: io.ErrUnexpectedEOF}
			}
			result, recorder, _, err := runPassthroughFlushTest(t, body, -1)
			require.Error(t, err)
			var failover *UpstreamFailoverError
			require.False(t, errors.As(err, &failover), "partial output must never be replayed")
			require.Equal(t, 7, result.usage.InputTokens)
			require.Equal(t, 2, result.usage.OutputTokens)
			wire := recorder.Body.String()
			require.Contains(t, wire, `"delta":"partial"`)
			require.Equal(t, 1, strings.Count(wire, `"type":"response.failed"`))
			require.NotContains(t, wire, `"type":"error"`)
			require.NotContains(t, wire, "[DONE]")
			require.Contains(t, wire, `"created_at":1700000000`)
			require.Contains(t, wire, `"sequence_number":2`)
		})
	}
}

func TestOpenAIPassthroughBareErrorAllowsAuthoritativeCompletion(t *testing.T) {
	partial := `data: {"type":"response.output_text.delta","delta":"partial"}` + "\n\n"
	bare := `data: {"type":"error","error":{"code":"transient","message":"retrying"}}` + "\n\n"
	completed := `data: {"type":"response.completed","response":{"id":"resp_recovered","usage":{"input_tokens":11,"output_tokens":4}}}` + "\n\n"
	result, recorder, _, err := runPassthroughFlushTest(t, io.NopCloser(strings.NewReader(partial+bare+completed+"data: [DONE]\n\n")), -1)
	require.NoError(t, err)
	require.Equal(t, 11, result.usage.InputTokens)
	require.Equal(t, 4, result.usage.OutputTokens)
	require.Contains(t, recorder.Body.String(), completed)
	require.NotContains(t, recorder.Body.String(), `"type":"response.failed"`)
}

func TestOpenAIPassthroughBareErrorDrainsFailedUsageAfterOutput(t *testing.T) {
	partial := `data: {"type":"response.output_text.delta","delta":"partial"}` + "\n\n"
	bare := `data: {"type":"error","error":{"code":"transient","message":"failed"},"usage":{"input_tokens":7,"output_tokens":2}}` + "\n\n"
	failed := `data: {"type":"response.failed","response":{"id":"resp_authoritative","error":{"code":"server_error","message":"failed"},"usage":{"input_tokens":11,"output_tokens":4}}}` + "\n\n"
	result, recorder, _, err := runPassthroughFlushTest(t, io.NopCloser(strings.NewReader(partial+bare+failed+"data: [DONE]\n\n")), -1)
	require.Error(t, err)
	require.Equal(t, 11, result.usage.InputTokens)
	require.Equal(t, 4, result.usage.OutputTokens)
	require.Equal(t, 1, strings.Count(recorder.Body.String(), `"type":"response.failed"`))
	require.Contains(t, recorder.Body.String(), `"id":"resp_authoritative"`)
}

func TestOpenAIPassthroughFailureDoneDoesNotWaitForEOF(t *testing.T) {
	for _, paired := range []bool{false, true} {
		name := "bare"
		if paired {
			name = "paired"
		}
		t.Run(name, func(t *testing.T) {
			payload := `data: {"type":"response.output_text.delta","delta":"partial"}` + "\n\n" +
				`data: {"type":"error","error":{"code":"server_error","message":"failed"},"usage":{"input_tokens":7,"output_tokens":2}}` + "\n\n"
			if paired {
				payload += `data: {"type":"response.failed","response":{"id":"resp_final","error":{"code":"server_error","message":"failed"},"usage":{"input_tokens":7,"output_tokens":2}}}` + "\n\n"
			}
			payload += "data: [DONE]\n\n"
			reader := &hangingOpenAISSEAfterTerminal{payload: []byte(payload), release: make(chan struct{})}
			t.Cleanup(func() { _ = reader.Close() })
			finished := make(chan error, 1)
			go func() {
				_, _, _, err := runPassthroughFlushTest(t, reader, -1)
				finished <- err
			}()
			select {
			case err := <-finished:
				require.Error(t, err)
			case <-time.After(3 * time.Second):
				t.Fatal("failed terminal was received but the stream still waits for EOF")
			}
		})
	}
}

func TestOpenAIBareCyberErrorKeepsObservedUsage(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, partial := range []bool{false, true} {
			name := "native"
			if passthrough {
				name = "passthrough"
			}
			if partial {
				name += "_after_output"
			}
			t.Run(name, func(t *testing.T) {
				repo := &balanceSafetyRepo{current: Account{ID: 95, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Extra: map[string]any{}}}
				svc := &OpenAIGatewayService{rateLimitService: NewRateLimitService(repo, nil, nil, nil, nil), cfg: &config.Config{}, toolCorrector: NewCodexToolCorrector()}
				payload := `data: {"type":"error","error":{"code":"cyber_policy","message":"blocked"},"usage":{"input_tokens":7,"output_tokens":2}}` + "\n\n"
				if partial {
					payload = `data: {"type":"response.output_text.delta","delta":"partial"}` + "\n\n" + payload
				}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				account, err := repo.GetByID(context.Background(), 95)
				require.NoError(t, err)
				resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(payload))}
				if passthrough {
					_, err = svc.handleStreamingResponsePassthrough(context.Background(), resp, c, account, time.Now(), "gpt-6.1-sol", "gpt-6.1-sol")
				} else {
					_, err = svc.handleStreamingResponse(context.Background(), resp, c, account, time.Now(), "gpt-6.1-sol", "gpt-6.1-sol")
				}
				require.Error(t, err)
				mark := GetOpsCyberPolicy(c)
				require.NotNil(t, mark)
				require.Equal(t, 7, mark.UpstreamInTok)
				require.Equal(t, 2, mark.UpstreamOutTok)
				require.Zero(t, repo.pauses, "content rejection is not account debt")
			})
		}
	}
}

func TestOpenAIBareErrorTerminalKeepsFinancialPause(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		name := "native"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			repo := &balanceSafetyRepo{current: Account{ID: 94, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Extra: map[string]any{}}}
			svc := &OpenAIGatewayService{rateLimitService: NewRateLimitService(repo, nil, nil, nil, nil), cfg: &config.Config{}, toolCorrector: NewCodexToolCorrector()}
			payload := `data: {"type":"response.output_text.delta","delta":"partial"}` + "\n\n" +
				`data: {"type":"error","error":{"code":"insufficient_balance","message":"Insufficient account balance"},"usage":{"input_tokens":7,"output_tokens":2}}` + "\n\n"
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			account, err := repo.GetByID(context.Background(), 94)
			require.NoError(t, err)
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(payload))}
			if passthrough {
				_, err = svc.handleStreamingResponsePassthrough(context.Background(), resp, c, account, time.Now(), "gpt-6.1-sol", "gpt-6.1-sol")
			} else {
				_, err = svc.handleStreamingResponse(context.Background(), resp, c, account, time.Now(), "gpt-6.1-sol", "gpt-6.1-sol")
			}
			require.Error(t, err)
			require.Equal(t, 1, repo.pauses)
			require.False(t, repo.current.Schedulable)
		})
	}
}
