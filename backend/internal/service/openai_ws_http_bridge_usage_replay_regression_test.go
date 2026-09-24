package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 已观测到的用量代表上游已经执行；即便客户端尚未看到业务输出，也不能再次执行该请求。
func TestOpenAIWSHTTPBridgeObservedUsageNeverReplays(t *testing.T) {
	gin.SetMode(gin.TestMode)
	usagePreamble := "data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_usage_guard\",\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}}\n\n"
	failed := "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_usage_guard\",\"status\":\"failed\",\"error\":{\"code\":\"server_is_overloaded\",\"message\":\"overloaded\"},\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}}\n\n"
	bareError := "data: {\"type\":\"error\",\"error\":{\"code\":\"server_is_overloaded\",\"message\":\"overloaded\"},\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}\n\n"
	completed := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_usage_guard\",\"status\":\"completed\",\"usage\":{\"input_tokens\":8,\"output_tokens\":4}}}\n\n"
	// 使用多个合法的小帧超过暂存上限，避免只触发扫描器的单行大小限制。
	largePreamble := "data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_usage_guard\",\"metadata\":\"" + strings.Repeat("x", 64*1024) + "\"}}\n\n"
	overflowPreamble := strings.Repeat(largePreamble, int(openAIFirstOutputStageMaxBytes)/len(largePreamble)+2)
	for _, accountType := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
		for _, tc := range []struct {
			name         string
			body         string
			readErr      error
			disconnect   bool
			writeErr     error
			wantInput    int
			wantOutput   int
			wantError    bool
			wantTerminal string
			wantReplay   bool
			wantNil      bool
		}{
			{name: "failed carries usage", body: failed, wantInput: 3, wantOutput: 2, wantTerminal: "response.failed"},
			{name: "preamble usage then EOF", body: usagePreamble, wantInput: 3, wantOutput: 2, wantError: true},
			{name: "preamble usage then read failure", body: usagePreamble, readErr: io.ErrUnexpectedEOF, wantInput: 3, wantOutput: 2, wantError: true},
			{name: "bare error carries usage", body: bareError, wantInput: 3, wantOutput: 2, wantError: true, wantTerminal: "response.failed"},
			{name: "preamble usage then staging overflow", body: usagePreamble + overflowPreamble, wantInput: 3, wantOutput: 2, wantError: true},
			{name: "disconnect then final usage", body: "data: {\"type\":\"keepalive\"}\n\n" + usagePreamble + completed, disconnect: true, wantInput: 8, wantOutput: 4, wantTerminal: "response.completed"},
			{name: "writer failure retains observed usage", body: usagePreamble, writeErr: errors.New("unexpected writer failure"), wantInput: 3, wantOutput: 2, wantError: true},
			// 尚未执行的失败继续允许换号；无用量的客户端写入故障保持原有不可重试错误。
			{name: "failed without usage remains retryable", body: strings.ReplaceAll(failed, `,"usage":{"input_tokens":3,"output_tokens":2}`, ""), wantError: true, wantReplay: true, wantNil: true},
			{name: "preamble without usage then EOF remains retryable", body: strings.ReplaceAll(usagePreamble, `,"usage":{"input_tokens":3,"output_tokens":2}`, ""), wantError: true, wantReplay: true, wantNil: true},
			{name: "writer failure without usage remains nil result", body: "data: {\"type\":\"keepalive\"}\n\n", writeErr: errors.New("unexpected writer failure"), wantError: true, wantNil: true},
		} {
			t.Run(accountType+"/"+tc.name, func(t *testing.T) {
				var reader io.Reader = strings.NewReader(tc.body)
				if tc.readErr != nil {
					reader = io.MultiReader(reader, iotest.ErrReader(tc.readErr))
				}
				upstream := &httpUpstreamRecorder{resp: &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body:       io.NopCloser(reader),
				}}
				svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
				account := &Account{ID: 1901, Platform: PlatformOpenAI, Type: accountType, Concurrency: 1}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
				payload := []byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`)
				writes := 0
				result, err := svc.proxyOpenAIWSHTTPBridgeTurn(context.Background(), c, account, "test-token", payload, len(payload),
					"gpt-5", "", "", "", "", "", 1, func(message []byte) error {
						writes++
						if tc.disconnect && gjson.GetBytes(message, "type").String() == "keepalive" {
							return io.EOF
						}
						return tc.writeErr
					})
				var failoverErr *UpstreamFailoverError
				require.Equal(t, tc.wantReplay, errors.As(err, &failoverErr), "有用量的请求禁止重放，无用量保持原有恢复规则：%v", err)
				if tc.wantError {
					require.Error(t, err)
				}
				if tc.writeErr != nil {
					require.ErrorIs(t, err, tc.writeErr)
				}
				if tc.wantNil {
					require.Nil(t, result)
					return
				}
				require.NotNil(t, result, "已观测的用量必须交给后续结算")
				require.Equal(t, tc.wantInput, result.Usage.InputTokens)
				require.Equal(t, tc.wantOutput, result.Usage.OutputTokens)
				if tc.wantTerminal != "" {
					require.Equal(t, tc.wantTerminal, result.UpstreamTerminalEvent)
				}
				if tc.disconnect {
					require.Equal(t, 1, writes, "断连后只排空上游，不得继续向客户端写入")
				}
			})
		}
	}
}
