//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 本地校验和先前上游失败都是 400 时，必须保留本地终态，不能借旧事件换组。
func TestOpenAILocalValidationKeepsFinalRequestBoundary(t *testing.T) {
	for _, scenario := range []string{"lite_tools", "spark_image", "grok_body"} {
		t.Run(scenario, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{}
			svc := newOpenAIRejectedFieldTestService(upstream)
			account := newOpenAIRejectedFieldTestAccount()
			body := []byte(`{"model":"gpt-5.3-codex-spark","input":[{"type":"input_image","file_id":"file_1"}]}`)
			c := newOpenAIRejectedFieldTestContext(body)
			switch scenario {
			case "lite_tools":
				account = newOpenAIOAuthNamespaceTestAccount()
				c.Request.Header.Set(responsesLiteHeader, "true")
				body = []byte(`{"model":"gpt-5.6-terra","tools":[{"type":"function","name":"shell"}],"parallel_tool_calls":"false"}`)
			case "grok_body":
				account.Platform = PlatformGrok
				body = []byte(`{"model":`)
			}
			history := &OpsUpstreamErrorEvent{AccountID: 1, UpstreamStatusCode: http.StatusBadRequest, Kind: "http_error", Message: "previous account failed"}
			c.Set(OpsUpstreamErrorsKey, []*OpsUpstreamErrorEvent{history})
			SetOpsUpstreamError(c, http.StatusBadRequest, history.Message, "")

			var result *OpenAIForwardResult
			var err error
			if scenario == "grok_body" {
				result, err = svc.forwardGrokResponses(context.Background(), c, account, body, "grok-4.5", false, time.Now())
			} else {
				result, err = svc.Forward(context.Background(), c, account, body)
			}

			require.Error(t, err)
			require.Nil(t, result)
			require.Equal(t, http.StatusBadRequest, c.Writer.Status())
			require.Nil(t, upstream.lastReq, "本地拒绝不能发出上游请求")
			require.True(t, HasOpsClientBusinessLimited(c))
			require.Equal(t, OpsClientBusinessLimitedReasonLocalPolicyDenied, OpsClientBusinessLimitedReason(c))
			events, _ := c.Get(OpsUpstreamErrorsKey)
			require.Equal(t, []*OpsUpstreamErrorEvent{history}, events, "已有失败历史保留用于排查")
		})
	}
}

// 明确风控警告禁止跨组，普通上游上下文超限仍保留用户要求的候选恢复资格。
func TestOpenAIContentWarningKeepsSmartRecoveryBoundary(t *testing.T) {
	for _, endpoint := range []string{"responses", "compat", "passthrough"} {
		for _, warning := range []bool{false, true} {
			name := endpoint + "/context_limit"
			message := "maximum context length exceeded"
			status := http.StatusBadRequest
			if warning {
				name = endpoint + "/cyber_warning"
				message = "This request has been flagged for potentially high-risk cyber activity."
				status = http.StatusForbidden
			}
			t.Run(name, func(t *testing.T) {
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				body := []byte(`{"error":{"message":"` + message + `"}}`)
				resp := &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(body)))}
				svc := &OpenAIGatewayService{}
				account := &Account{ID: 1, Name: "openai", Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
				var err error
				switch endpoint {
				case "responses":
					_, err = svc.handleErrorResponse(context.Background(), resp, c, account, []byte(`{"model":"gpt-5.6-terra"}`))
				case "compat":
					_, err = svc.handleCompatErrorResponse(resp, c, account,
						func(c *gin.Context, status int, errType, message string) {
							c.JSON(status, gin.H{"error": gin.H{"type": errType, "message": message}})
						}, func(c *gin.Context, status int, body []byte) { c.Data(status, "application/json", body) })
				case "passthrough":
					err = svc.handleErrorResponsePassthrough(context.Background(), resp, c, account, []byte(`{"model":"gpt-5.6-terra"}`), body)
				}

				require.Error(t, err)
				clientStatus := status
				if endpoint == "passthrough" && warning {
					// 透传路径原本将此 403 净化为 502，本次只改变换组资格。
					clientStatus = http.StatusBadGateway
				}
				require.Equal(t, clientStatus, recorder.Code)
				require.Equal(t, warning, HasOpsClientBusinessLimited(c))
				if warning {
					require.Equal(t, OpsClientBusinessLimitedReasonLocalPolicyDenied, OpsClientBusinessLimitedReason(c))
					if endpoint == "responses" {
						upstreamWarning, ok := ExtractOpenAIUpstreamWarning(err)
						require.True(t, ok, "原告警仍随错误返回")
						require.Equal(t, message, upstreamWarning.Message)
						require.Contains(t, recorder.Body.String(), message)
					}
				} else {
					require.Contains(t, recorder.Body.String(), message, "上游上下文超限保留原消息")
				}
			})
		}
	}
}
