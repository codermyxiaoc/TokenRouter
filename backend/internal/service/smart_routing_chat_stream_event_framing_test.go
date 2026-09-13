//go:build unit

package service

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 按 SSE 事件边界组织的错误不能因逐 data 行解析而被吞掉；本测试只回放内存中的模拟上游。
func TestSmartRoutingChatStreamNamedAndMultilineErrorAfterOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct{ name, tail string }{
		{
			name: "multiline_data_error",
			tail: "event: error\ndata: {\ndata:   \"error\": {\ndata:     \"type\": \"server_error\",\ndata:     \"status\": 503,\ndata:     \"message\": \"local multiline upstream failure\"\ndata:   }\ndata: }\n\ndata: [DONE]\n\n",
		},
		{
			name: "named_error_flat_payload",
			tail: "event: error\ndata: {\"type\":\"server_error\",\"status\":503,\"message\":\"local named upstream failure\"}\n\ndata: [DONE]\n\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err, c, recorder := smartRoutingForwardChatStream(t, smartRoutingChatRolePrelude+smartRoutingChatContent+test.tail, false)
			assert.Error(t, err, "识别到上游错误后不能返回正常结果")
			var failover *UpstreamFailoverError
			assert.False(t, errors.As(err, &failover), "已输出文本时不能跨组重放")
			assert.NotNil(t, result, "保留部分结果供已有用量路径处理")
			assert.Contains(t, recorder.Body.String(), "partial-output")
			assert.Contains(t, recorder.Body.String(), "response.failed")
			assert.NotContains(t, recorder.Body.String(), "response.completed")
			smartRoutingAssertChatStreamOps(t, c, 503)
		})
	}
}

// ccFramingCheckedReader 在读取下一段前验证上一段已交付，不依赖计时或后台 goroutine。
type ccFramingCheckedReader struct {
	reader     io.Reader
	beforeRead func()
}

func (r *ccFramingCheckedReader) Read(p []byte) (int, error) {
	if r.beforeRead != nil {
		r.beforeRead()
		r.beforeRead = nil
	}
	return r.reader.Read(p)
}

// 事件合并必须有界，并保留已有无空行 Chat 流的首段延迟和成功语义。
func TestSmartRoutingChatStreamFramingCompatibilityAndBounds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	scan := func(reader io.Reader, limit int, emit func(*apicompat.ChatCompletionsChunk)) ccStreamScanState {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		cfg := rawChatCompletionsTestConfig()
		cfg.Gateway.MaxLineSize = limit
		svc := &OpenAIGatewayService{cfg: cfg}
		return svc.scanCCStream(c, &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(reader)}, "local framing", "local framing", time.Now(), emit)
	}
	t.Run("complete_data_delivered_before_next_read_without_blank", func(t *testing.T) {
		emitted := false
		first := strings.TrimSuffix(smartRoutingChatContent, "\n")
		tail := &ccFramingCheckedReader{reader: strings.NewReader("data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\ndata: [DONE]\n"), beforeRead: func() { require.True(t, emitted, "完整首段不得等待下一行或空行") }}
		state := scan(io.MultiReader(strings.NewReader(first), tail), 0, func(chunk *apicompat.ChatCompletionsChunk) {
			if chatChunkStartsResponsesOutput(chunk) {
				emitted = true
			}
		})
		require.NoError(t, state.Err)
		require.True(t, state.SawDone)
		require.Equal(t, "stop", state.FinishReason)
	})
	t.Run("multiline_event_obeys_total_size_limit", func(t *testing.T) {
		body := "event: error\ndata: {\n" + strings.Repeat("data:   \"padding\": \"012345678901234567890123456789\",\n", 6)
		state := scan(strings.NewReader(body), 128, func(*apicompat.ChatCompletionsChunk) { t.Error("超限的未闭合事件不得交给转换器") })
		require.ErrorContains(t, state.Err, "size limit")
		require.False(t, state.SawDone)
	})
	t.Run("normal_multiline_json_completion", func(t *testing.T) {
		body := "event: message\ndata: {\ndata:   \"id\": \"chatcmpl_multiline\",\ndata:   \"choices\": [{\"index\":0,\"delta\":{\"content\":\"normal-multiline-output\"},\"finish_reason\":\"stop\"}],\ndata:   \"usage\": {\"prompt_tokens\":4,\"completion_tokens\":2,\"total_tokens\":6}\ndata: }\n\n"
		result, err, _, recorder := smartRoutingForwardChatStream(t, body, false)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Equal(t, 4, result.Usage.InputTokens)
		require.Equal(t, 2, result.Usage.OutputTokens)
		require.Contains(t, recorder.Body.String(), "normal-multiline-output")
		require.Contains(t, recorder.Body.String(), "response.completed")
		require.NotContains(t, recorder.Body.String(), "response.failed")
	})
	t.Run("named_error_without_data", func(t *testing.T) {
		state := scan(strings.NewReader("event: error\n\n"), 0, func(*apicompat.ChatCompletionsChunk) { t.Error("无正文错误事件也不能转成成功chunk") })
		require.Error(t, state.Err)
		require.Equal(t, "server_error", gjson.GetBytes(state.FailurePayload, "error.type").String())
	})
	t.Run("error_words_in_regular_content_are_not_errors", func(t *testing.T) {
		body := strings.ReplaceAll(smartRoutingChatContent, "partial-output", "event: error status 503 server_error") + "data: [DONE]\n\n"
		state := scan(strings.NewReader(body), 0, func(*apicompat.ChatCompletionsChunk) {})
		require.NoError(t, state.Err)
		require.Empty(t, state.FailurePayload)
		require.True(t, state.SawOutput)
	})
	t.Run("multiline_error_at_eof_without_blank", func(t *testing.T) {
		body := "event: error\ndata: {\ndata: \"status\": 503,\ndata: \"message\": \"local EOF error\"\ndata: }"
		state := scan(strings.NewReader(body), 0, func(*apicompat.ChatCompletionsChunk) { t.Error("错误对象不得交给转换器") })
		require.Error(t, state.Err)
		require.Equal(t, int64(503), gjson.GetBytes(state.FailurePayload, "error.status").Int())
	})
}

// 标准错误对象与 choices 同帧、或出现在 finish_reason 后，都应先识别错误再合成完成事件。
// 同时核实 response.failed 的真实 wire 字段，避免把序列化猜测当成已证实故障。
func TestSmartRoutingChatStreamRootErrorWireRetainsStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct{ name, tail string }{
		{
			name: "root_error_with_finish_reason",
			tail: "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"error\":{\"type\":\"server_error\",\"status\":503,\"message\":\"local root error\"}}\n\ndata: [DONE]\n\n",
		},
		{
			name: "root_error_after_finish_reason",
			tail: "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" + smartRoutingChat503Error + "data: [DONE]\n\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err, c, recorder := smartRoutingForwardChatStream(t, smartRoutingChatRolePrelude+smartRoutingChatContent+test.tail, false)
			require.Error(t, err)
			require.NotContains(t, recorder.Body.String(), "response.completed")
			smartRoutingAssertChatStreamOps(t, c, 503)
			var createdID string
			var failed gjson.Result
			failedCount := 0
			for _, line := range strings.Split(recorder.Body.String(), "\n") {
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				payload := gjson.Parse(strings.TrimPrefix(line, "data: "))
				switch payload.Get("type").String() {
				case "response.created":
					createdID = payload.Get("response.id").String()
				case "response.failed":
					failed, failedCount = payload, failedCount+1
				}
			}
			require.Equal(t, 1, failedCount)
			require.Equal(t, createdID, failed.Get("response.id").String())
			require.Equal(t, "response", failed.Get("response.object").String())
			require.Positive(t, failed.Get("response.created_at").Int())
			require.Equal(t, "failed", failed.Get("response.status").String())
			require.Equal(t, int64(503), failed.Get("response.error.status_code").Int())
			require.NotEmpty(t, failed.Get("response.error.code").String())
			require.NotEmpty(t, failed.Get("response.error.message").String())
			require.True(t, failed.Get("response.output").IsArray())
		})
	}
}
