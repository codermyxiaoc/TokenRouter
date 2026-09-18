//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 合成 WS 错误和失败终态都必须带序号；保留原错误、响应身份和失败语义。
func TestResponsesSync_WSHTTPBridgeSyntheticSequence(t *testing.T) {
	failed := buildOpenAIWSHTTPBridgeFailedEvent("resp_test", "gpt-test", []byte(`{"error":{"code":"server_error","message":"temporary"}}`), "fallback")
	for _, body := range [][]byte{buildOpenAIWSHTTPBridgeErrorEvent(503, "temporary"), failed} {
		require.True(t, gjson.ValidBytes(body))
		require.True(t, gjson.GetBytes(body, "sequence_number").Exists())
		require.EqualValues(t, 0, gjson.GetBytes(body, "sequence_number").Int())
	}
	require.Equal(t, "failed", gjson.GetBytes(failed, "response.status").String())
	require.Equal(t, "resp_test", gjson.GetBytes(failed, "response.id").String())
	require.Equal(t, "temporary", gjson.GetBytes(failed, "response.error.message").String())
}

// done 中恢复的正文同样属于真实输出；随后失败仍留痕、返回错误并保留部分用量。
func TestResponsesSync_RecoveredDoneFollowedByFailureDoesNotReplay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	payload := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_recovery","status":"in_progress","model":"gpt-5.4","output":[]}}`, "",
		`data: {"type":"response.output_text.done","output_index":0,"content_index":0,"text":"partial answer"}`, "",
		`data: {"type":"response.failed","response":{"id":"resp_recovery","status":"failed","error":{"code":"server_error","message":"Service temporarily unavailable"},"output":[{"type":"message","content":[{"type":"output_text","text":"partial answer hidden tail"}]}],"usage":{"input_tokens":10,"output_tokens":3,"total_tokens":13}}}`, "", "",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(payload))}}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
	result, err := svc.ForwardAsAnthropic(context.Background(), c, rawChatCompletionsTestAccount(), body, "", "")
	require.Error(t, err)
	var failover *UpstreamFailoverError
	require.False(t, errors.As(err, &failover))
	require.Contains(t, rec.Body.String(), `"text":"partial answer"`)
	require.Contains(t, rec.Body.String(), "event: error")
	require.NotContains(t, rec.Body.String(), "hidden tail")
	require.NotContains(t, rec.Body.String(), "event: message_stop")
	facts, recorded := c.Get(OpsUpstreamErrorsKey)
	require.True(t, recorded, "客户端已收到正文不能阻止后台记录上游失败")
	require.NotEmpty(t, facts)
	require.NotNil(t, result)
	require.EqualValues(t, 10, result.Usage.InputTokens)
	require.EqualValues(t, 3, result.Usage.OutputTokens)
}
