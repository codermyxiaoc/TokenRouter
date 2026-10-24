//go:build unit

package handler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	middleware "github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type compatStreamUsageUpstream struct {
	service.HTTPUpstream
	response *http.Response
	calls    int
}

func (u *compatStreamUsageUpstream) DoWithTLS(*http.Request, string, int64, int, *tlsfingerprint.Profile) (*http.Response, error) {
	u.calls++
	return u.response, nil
}

type compatStreamUsageBillingRepo struct {
	service.UsageBillingRepository
	mu       sync.Mutex
	commands []*service.UsageBillingCommand
}

func (r *compatStreamUsageBillingRepo) Apply(ctx context.Context, cmd *service.UsageBillingCommand) (*service.UsageBillingApplyResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commands = append(r.commands, cmd)
	return &service.UsageBillingApplyResult{Applied: true}, nil
}

const compatStreamUsagePartialStream = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4.5","usage":{"input_tokens":12,"output_tokens":1}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}` + "\n\n"

const compatStreamUsageCompletion = "event: content_block_stop\n" +
	`data: {"type":"content_block_stop","index":0}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

// 模拟客户端在正文首块断开，记录后续是否仍试图向下游写入。
type compatStreamDisconnectedWriter struct {
	*httptest.ResponseRecorder
	failed            bool
	writesAfterError  int
	flushesAfterError int
}

func (w *compatStreamDisconnectedWriter) Write(data []byte) (int, error) {
	if w.failed {
		w.writesAfterError++
		return 0, errors.New("client disconnected")
	}
	if strings.Contains(string(data), "partial") {
		w.failed = true
		return 0, errors.New("client disconnected")
	}
	return w.ResponseRecorder.Write(data)
}
func (w *compatStreamDisconnectedWriter) Flush() {
	if w.failed {
		w.flushesAfterError++
		return
	}
	w.ResponseRecorder.Flush()
}

type compatStreamUsageRun struct {
	disconnected *compatStreamDisconnectedWriter
	rec          *httptest.ResponseRecorder
	upstream     *compatStreamUsageUpstream
	usageRepo    *openAIWSUsageHandlerUsageLogRepoStub
	billingRepo  *compatStreamUsageBillingRepo
}

// runCompatStreamUsageHandler 经 GatewayHandler 转发一次流式请求；上游写完 upstreamStream 后，
// closeUpstream 为 false 时保持连接不再发送数据。
func runCompatStreamUsageHandler(t *testing.T, endpoint, upstreamStream string, closeUpstream bool, disconnect ...bool) compatStreamUsageRun {
	t.Helper()
	cfg := &config.Config{Gateway: config.GatewayConfig{StreamDataIntervalTimeout: 1}}
	groupID := int64(9300)
	group := &service.Group{ID: groupID, Hydrated: true, Platform: service.PlatformAnthropic, Status: service.StatusActive, RateMultiplier: 1}
	account := &service.Account{
		ID: 9301, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Concurrency: 1,
		Credentials:   map[string]any{"api_key": "test-key"},
		AccountGroups: []service.AccountGroup{{AccountID: 9301, GroupID: groupID}},
	}
	snapshot := service.NewSchedulerSnapshotService(&fakeSchedulerCache{accounts: []*service.Account{account}}, nil, nil, nil, nil)
	run := compatStreamUsageRun{
		rec:         httptest.NewRecorder(),
		usageRepo:   &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 2)},
		billingRepo: &compatStreamUsageBillingRepo{},
	}
	billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, &config.Config{RunMode: config.RunModeSimple}, nil)
	t.Cleanup(billingCache.Stop)

	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pr.Close(); _ = pw.Close() })
	go func() {
		_, _ = io.WriteString(pw, upstreamStream)
		if closeUpstream {
			_ = pw.Close()
		}
	}()
	run.upstream = &compatStreamUsageUpstream{response: &http.Response{
		StatusCode: http.StatusOK, Body: pr,
		Header: http.Header{"X-Request-Id": []string{"compat-stream-usage"}},
	}}
	gw := service.NewGatewayService(
		nil, &fakeGroupRepo{group: group}, run.usageRepo, run.billingRepo, nil, nil, nil, nil, cfg,
		snapshot, nil, service.NewBillingService(cfg, nil), nil, nil, nil, run.upstream,
		&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	h := &GatewayHandler{
		gatewayService: gw, billingCacheService: billingCache, cfg: cfg, maxAccountSwitches: 1,
		concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(&fakeConcurrencyCache{}), SSEPingFormatClaude, 0),
	}

	body := `{"model":"claude-sonnet-4.5","stream":true,"input":"hello"}`
	if endpoint == "chat/completions" {
		body = `{"model":"claude-sonnet-4.5","stream":true,"messages":[{"role":"user","content":"hello"}]}`
	}
	var writer http.ResponseWriter = run.rec
	if len(disconnect) > 0 && disconnect[0] {
		run.disconnected = &compatStreamDisconnectedWriter{ResponseRecorder: run.rec}
		writer = run.disconnected
	}
	c, _ := gin.CreateTestContext(writer)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+endpoint, bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	apiKey := &service.APIKey{ID: 9302, UserID: 9303, GroupID: &groupID, Group: group, User: &service.User{ID: 9303, Balance: 100}}
	c.Set(string(middleware.ContextKeyAPIKey), apiKey)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 9303})

	if endpoint == "chat/completions" {
		h.ChatCompletions(c)
	} else {
		h.Responses(c)
	}
	return run
}

// compatStreamUsageMarkers 返回成功收尾与终止错误的标记。
func compatStreamUsageMarkers(endpoint string) (success, failure string) {
	if endpoint == "responses" {
		return "event: response.completed", "event: response.failed"
	}
	return "[DONE]", `data: {"error":`
}

func TestGatewayCompatibilityHandlers_StreamUsageRecordedOnceWithTerminalEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []string{"chat/completions", "responses"} {
		for _, tc := range []struct {
			name       string
			completion string
			wantOutput int
		}{
			{name: "complete", completion: compatStreamUsageCompletion, wantOutput: 7},
			{name: "idle_before_message_stop", wantOutput: 1},
		} {
			t.Run(endpoint+"/"+tc.name, func(t *testing.T) {
				successMarker, failureMarker := compatStreamUsageMarkers(endpoint)
				wantMarker, denyMarker := successMarker, failureMarker
				if tc.completion == "" {
					wantMarker, denyMarker = failureMarker, successMarker
				}

				run := runCompatStreamUsageHandler(t, endpoint, compatStreamUsagePartialStream+tc.completion, tc.completion != "")

				require.Equal(t, 1, run.upstream.calls)
				out := run.rec.Body.String()
				require.Contains(t, out, "partial")
				require.Contains(t, out, wantMarker)
				require.NotContains(t, out, denyMarker)
				require.Equal(t, 1, strings.Count(out, wantMarker), "exactly one terminal event")
				require.NotContains(t, out, `data: {"type":"error"`, "the handler must not append a fallback frame")
				if tc.completion == "" {
					require.Contains(t, out, "stream_timeout")
				}

				run.billingRepo.mu.Lock()
				defer run.billingRepo.mu.Unlock()
				require.Len(t, run.billingRepo.commands, 1, "usage must be settled exactly once")
				require.Equal(t, 12, run.billingRepo.commands[0].InputTokens)
				require.Equal(t, tc.wantOutput, run.billingRepo.commands[0].OutputTokens)
				require.Len(t, run.usageRepo.created, 1)
			})
		}
	}
}

func TestGatewayCompatibilityHandlers_IdleBeforeAnyEventWritesStreamError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []string{"chat/completions", "responses"} {
		t.Run(endpoint, func(t *testing.T) {
			successMarker, failureMarker := compatStreamUsageMarkers(endpoint)

			run := runCompatStreamUsageHandler(t, endpoint, "", false)

			require.Equal(t, 1, run.upstream.calls)
			require.Equal(t, http.StatusOK, run.rec.Code)
			require.Equal(t, "text/event-stream", run.rec.Header().Get("Content-Type"))
			out := run.rec.Body.String()
			require.Equal(t, 1, strings.Count(out, failureMarker), "exactly one terminal event")
			require.NotContains(t, out, `data: {"type":"error"`, "the handler must not append a fallback frame")
			require.Contains(t, out, "stream_timeout")
			require.NotContains(t, out, successMarker)

			run.billingRepo.mu.Lock()
			defer run.billingRepo.mu.Unlock()
			require.Empty(t, run.billingRepo.commands, "no usage was observed")
			require.Empty(t, run.usageRepo.created)
		})
	}
}

func TestGatewayCompatibilityHandlers_UpstreamErrorEventAfterOutputIsNotBilled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstreamError := "event: error\n" + `data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}` + "\n\n"
	for _, endpoint := range []string{"chat/completions", "responses"} {
		t.Run(endpoint, func(t *testing.T) {
			successMarker, failureMarker := compatStreamUsageMarkers(endpoint)

			run := runCompatStreamUsageHandler(t, endpoint, compatStreamUsagePartialStream+upstreamError, true)

			require.Equal(t, 1, run.upstream.calls)
			out := run.rec.Body.String()
			require.Contains(t, out, "partial")
			require.Equal(t, 1, strings.Count(out, failureMarker), "exactly one terminal event")
			require.NotContains(t, out, `data: {"type":"error"`, "the handler must not append a fallback frame")
			require.Contains(t, out, "Overloaded")
			require.NotContains(t, out, successMarker)

			run.billingRepo.mu.Lock()
			defer run.billingRepo.mu.Unlock()
			require.Empty(t, run.billingRepo.commands, "an upstream error event is not billed while the client is online")
			require.Empty(t, run.usageRepo.created)
		})
	}
}

// 客户端断开后上游明确失败，仍只结算已观测用量一次，且不追加响应或重试。
func TestGatewayCompatibilityHandlers_DisconnectedUpstreamErrorKeepsUsageOnce(t *testing.T) {
	for _, endpoint := range []string{"chat/completions", "responses"} {
		t.Run(endpoint, func(t *testing.T) {
			upstreamError := "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"
			run := runCompatStreamUsageHandler(t, endpoint, compatStreamUsagePartialStream+upstreamError, true, true)
			require.Equal(t, 1, run.upstream.calls)
			require.True(t, run.disconnected.failed)
			require.Zero(t, run.disconnected.writesAfterError)
			require.Zero(t, run.disconnected.flushesAfterError)
			run.billingRepo.mu.Lock()
			defer run.billingRepo.mu.Unlock()
			require.Len(t, run.billingRepo.commands, 1)
			require.Equal(t, 12, run.billingRepo.commands[0].InputTokens)
			require.Equal(t, 1, run.billingRepo.commands[0].OutputTokens)
			require.Len(t, run.usageRepo.created, 1)
		})
	}
}

// 同时执行成功、在线失败和断连失败，核对各请求独立结算，失败流不得跨请求重放或重复收费。
func TestGatewayCompatibilityStability_ConcurrentSettlement(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for i := 0; i < 32; i++ {
		t.Run(fmt.Sprintf("request_%02d", i), func(t *testing.T) {
			t.Parallel()
			endpoint := "chat/completions"
			if i%2 == 1 {
				endpoint = "responses"
			}
			failure := "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"
			completion := compatStreamUsageCompletion
			disconnected := i%4 == 3
			wantBills, wantOutput := 1, 7
			if i%4 >= 2 {
				completion = failure
				wantOutput = 1
			}
			if i%4 == 2 {
				wantBills = 0
			}
			run := runCompatStreamUsageHandler(t, endpoint, compatStreamUsagePartialStream+completion, true, disconnected)
			require.Equal(t, 1, run.upstream.calls)
			run.billingRepo.mu.Lock()
			defer run.billingRepo.mu.Unlock()
			require.Len(t, run.billingRepo.commands, wantBills)
			require.Len(t, run.usageRepo.created, wantBills)
			if wantBills > 0 {
				require.Equal(t, 12, run.billingRepo.commands[0].InputTokens)
				require.Equal(t, wantOutput, run.billingRepo.commands[0].OutputTokens)
			}
			if disconnected {
				require.Zero(t, run.disconnected.writesAfterError)
				require.Zero(t, run.disconnected.flushesAfterError)
			}
		})
	}
}
