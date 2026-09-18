package service

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const streamBusyEvent = `{"type":"error","sequence_number":2,"error":{"code":"server_error","type":"server_error","message":"The service is busy. Please retry later."}}`
const streamBusyFailedEvent = `{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"The service is busy. Please retry later."}}}`
const streamBusySuccessEvent = `{"type":"response.completed","response":{"id":"resp_done","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":5,"output_tokens":1}}}`

func busyTestSSE(events ...string) string {
	var out strings.Builder
	for _, event := range events {
		out.WriteString("data: ")
		out.WriteString(event)
		out.WriteString("\n\n")
	}
	return out.String()
}

func runBusyTestStream(svc *OpenAIGatewayService, c *gin.Context, account *Account, passthrough bool, resp *http.Response) (*OpenAIUsage, error) {
	if passthrough {
		result, err := svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, time.Now(), "gpt-test", "gpt-test")
		if result == nil {
			return nil, err
		}
		return result.usage, err
	}
	result, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "gpt-test", "gpt-test")
	if result == nil {
		return nil, err
	}
	return result.usage, err
}

func busyTestContext() (*gin.Context, *httptest.ResponseRecorder, *OpenAIGatewayService, *Account) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	svc := &OpenAIGatewayService{cfg: &config.Config{}, toolCorrector: NewCodexToolCorrector()}
	return c, rec, svc, &Account{ID: 65735, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
}

func busyTestResponse(stream string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}, "X-Request-Id": {"upstream-busy-test"}}, Body: io.NopCloser(strings.NewReader(stream))}
}

func busyTestUpstreamEvents(c *gin.Context) []*OpsUpstreamErrorEvent {
	value, _ := c.Get(OpsUpstreamErrorsKey)
	events, _ := value.([]*OpsUpstreamErrorEvent)
	return events
}

func TestOpenAIStreamBusy_SafeEmptyPreludeAllowsFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, passthrough := range []bool{false, true} {
		for _, prelude := range []string{
			`{"type":"ping","sequence_number":0}`,
			`{"type":"heartbeat"}`,
			`{"type":"response.output_text.delta","delta":"","item_id":"item-1","output_index":0,"content_index":0}`,
			`{"type":"response.reasoning_text.delta","delta":""}`,
			`{"type":"response.reasoning_summary_text.delta","delta":""}`,
		} {
			t.Run(prelude+map[bool]string{false: "/native", true: "/passthrough"}[passthrough], func(t *testing.T) {
				c, rec, svc, account := busyTestContext()
				_, err := runBusyTestStream(svc, c, account, passthrough, busyTestResponse(busyTestSSE(prelude, streamBusyEvent)))
				var failover *UpstreamFailoverError
				require.ErrorAs(t, err, &failover)
				require.Empty(t, rec.Body.String())
				require.Len(t, busyTestUpstreamEvents(c), 1)
				_, failed := GetOpsStreamError(c)
				require.False(t, failed, "候选失败尚可恢复，不能提前标记最终请求失败")
			})
		}
	}
}

func TestOpenAIStreamBusy_ConservativePreludeRecordsFinalFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, passthrough := range []bool{false, true} {
		for _, prelude := range []string{
			`{"type":"response.output_text.delta","delta":"hello"}`,
			`{"type":"provider.unknown"}`,
			`{"type":"response.function_call_arguments.delta","delta":""}`,
			`{"type":"response.output_item.added","item":{"type":"reasoning","encrypted_content":"opaque"}}`,
			`{"type":"response.output_text.delta","delta":"","usage":{"input_tokens":1}}`,
			`{"type":"ping","future_side_effect":true}`,
		} {
			t.Run(prelude+map[bool]string{false: "/native", true: "/passthrough"}[passthrough], func(t *testing.T) {
				c, rec, svc, account := busyTestContext()
				_, err := runBusyTestStream(svc, c, account, passthrough, busyTestResponse(busyTestSSE(prelude, streamBusyEvent, streamBusyFailedEvent)))
				require.Error(t, err)
				var failover *UpstreamFailoverError
				require.False(t, errors.As(err, &failover))
				require.Contains(t, rec.Body.String(), "The service is busy")
				events := busyTestUpstreamEvents(c)
				require.Len(t, events, 1, "裸 error 和 response.failed 只登记一次尝试")
				require.Equal(t, account.ID, events[0].AccountID)
				require.Equal(t, "upstream-busy-test", events[0].UpstreamRequestID)
				streamError, failed := GetOpsStreamError(c)
				require.True(t, failed)
				require.Equal(t, "upstream_error", streamError.ErrType)
			})
		}
	}
}

func TestOpenAIStreamBusy_FailureWithUsageCannotReplay(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		c, _, svc, account := busyTestContext()
		failed := `{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"Please retry later"},"usage":{"input_tokens":7,"output_tokens":2}}}`
		usage, err := runBusyTestStream(svc, c, account, passthrough, busyTestResponse(busyTestSSE(failed)))
		require.Error(t, err)
		var failover *UpstreamFailoverError
		require.False(t, errors.As(err, &failover))
		require.Equal(t, 7, usage.InputTokens)
		require.Len(t, busyTestUpstreamEvents(c), 1)
	}
}

// 有用量的限流终态不能重放，但仍须按既有账号策略进入冷却。
func TestOpenAIStreamBusy_FailureWithUsageRetainsAccountCooldown(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(map[bool]string{false: "native", true: "passthrough"}[passthrough], func(t *testing.T) {
			c, rec, svc, account := busyTestContext()
			repo := &openAIWSRateLimitSignalRepo{}
			svc.rateLimitService = NewRateLimitService(repo, nil, svc.cfg, nil, nil)
			failed := `{"type":"response.failed","response":{"status":"failed","error":{"code":"rate_limit_exceeded","message":"rate limit reached"},"usage":{"input_tokens":7,"output_tokens":2}}}`
			usage, err := runBusyTestStream(svc, c, account, passthrough, busyTestResponse(busyTestSSE(failed)))
			require.Error(t, err)
			var failover *UpstreamFailoverError
			require.False(t, errors.As(err, &failover))
			require.Equal(t, 7, usage.InputTokens)
			require.Contains(t, rec.Body.String(), "response.failed")
			require.Len(t, repo.rateLimitCalls, 1)
			require.True(t, repo.rateLimitCalls[0].After(time.Now()))
			require.Len(t, busyTestUpstreamEvents(c), 1)
			_, failedStream := GetOpsStreamError(c)
			require.True(t, failedStream)
		})
	}
}

func TestOpenAIStreamBusy_EmptyEventRejectsAmbiguousMetadata(t *testing.T) {
	for _, payload := range []string{
		`{"type":"response.output_text.delta","delta":"","delta":"hidden"}`,
		`{"type":"response.output_text.delta","delta":"hidden","delta":""}`,
		`{"type":"response.output_text.delta","delta":"","item_id":{"text":"hidden"}}`,
		`{"type":"response.output_text.delta","delta":"","content_index":-1}`,
		`{"type":"response.output_text.delta","delta":"","content_index":0.5}`,
		`{"type":"response.output_text.delta","delta":"","sequence_number":[]}`,
		`{"type":"response.output_text.delta","delta":"","usage":{}}`,
	} {
		require.False(t, openAIStreamSafeEmptyEvent(payload, "response.output_text.delta"), payload)
	}
	require.Equal(t, "unrecognized", openAIStreamDiagnosticEventName("sk-private-token-value"))
	require.Equal(t, "unrecognized", openAIStreamDiagnosticEventName("provider.private_event"))
	require.Equal(t, "response.output_item.added", openAIStreamDiagnosticEventName("response.output_item.added"))
}

func TestOpenAIStreamBusy_RecoveredBareErrorKeepsUpstreamFact(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		c, rec, svc, account := busyTestContext()
		account.Type = AccountTypeOAuth
		recoverable := `{"type":"error","error":{"code":"transient","message":"retrying"}}`
		usage, err := runBusyTestStream(svc, c, account, passthrough, busyTestResponse(busyTestSSE(recoverable, streamBusySuccessEvent)))
		require.NoError(t, err)
		require.Equal(t, 5, usage.InputTokens)
		require.NotContains(t, rec.Body.String(), `"type":"error"`)
		require.NotContains(t, rec.Body.String(), "response.failed")
		events := busyTestUpstreamEvents(c)
		require.Len(t, events, 1)
		require.Equal(t, "stream_error_recovered", events[0].Kind)
		_, failed := GetOpsStreamError(c)
		require.False(t, failed)
	}
}

func TestOpenAIStreamBusy_APIKeyPublishedErrorIsNotRecovered(t *testing.T) {
	c, rec, svc, account := busyTestContext()
	providerError := `{"type":"error","error":{"code":"provider_error","message":"provider failed"}}`
	_, err := runBusyTestStream(svc, c, account, false, busyTestResponse(busyTestSSE(providerError, streamBusySuccessEvent)))
	require.Error(t, err)
	require.Contains(t, rec.Body.String(), `"type":"error"`)
	require.Len(t, busyTestUpstreamEvents(c), 1)
	_, failed := GetOpsStreamError(c)
	require.True(t, failed)
}

// 六十个并发请求只访问本机假上游，验证尝试之间不串错误标记或用量。
func TestOpenAIStreamBusy_SixtyConcurrentFakeUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ready := make(chan struct{}, 60)
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if r.URL.Path == "/busy" {
			ready <- struct{}{}
			<-release
			_, _ = io.WriteString(w, busyTestSSE(`{"type":"response.output_text.delta","delta":""}`, streamBusyEvent))
			return
		}
		_, _ = io.WriteString(w, busyTestSSE(streamBusySuccessEvent))
	}))
	defer upstream.Close()
	var wg sync.WaitGroup
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func(passthrough bool) {
			defer wg.Done()
			c, rec, svc, account := busyTestContext()
			first, err := upstream.Client().Get(upstream.URL + "/busy")
			if !assertBusyTestNoError(t, err) {
				return
			}
			_, err = runBusyTestStream(svc, c, account, passthrough, first)
			_ = first.Body.Close()
			var failover *UpstreamFailoverError
			if !errors.As(err, &failover) {
				t.Errorf("expected first attempt failover, got %v", err)
				return
			}
			second, err := upstream.Client().Get(upstream.URL + "/success")
			if !assertBusyTestNoError(t, err) {
				return
			}
			usage, err := runBusyTestStream(svc, c, account, passthrough, second)
			_ = second.Body.Close()
			if err != nil || usage == nil || usage.InputTokens != 5 || strings.Contains(rec.Body.String(), "busy") {
				t.Errorf("recovery mismatch: usage=%+v err=%v", usage, err)
			}
			if _, failed := GetOpsStreamError(c); failed || len(busyTestUpstreamEvents(c)) != 1 {
				t.Errorf("attempt state leaked: failed=%v events=%d", failed, len(busyTestUpstreamEvents(c)))
			}
		}(i%2 == 0)
	}
	for i := 0; i < 60; i++ {
		select {
		case <-ready:
		case <-time.After(10 * time.Second):
			close(release)
			wg.Wait()
			t.Fatal("local upstream did not receive all concurrent requests")
		}
	}
	close(release)
	wg.Wait()
}

func assertBusyTestNoError(t *testing.T, err error) bool {
	t.Helper()
	if err != nil {
		t.Errorf("local upstream: %v", err)
		return false
	}
	return true
}
