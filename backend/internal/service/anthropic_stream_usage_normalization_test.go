//go:build unit

package service

import (
	"encoding/json"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 归一化不能丢失 fork 的速度计费标记，也不能把独立输入误当成含缓存总量。
func TestMergeAnthropicUsagePreservesSpeedAndIndependentInput(t *testing.T) {
	var usage ClaudeUsage
	mergeAnthropicUsage(&usage, apicompat.AnthropicUsage{InputTokens: 308, Speed: "fast"})
	mergeAnthropicUsage(&usage, apicompat.AnthropicUsage{CacheReadInputTokens: 300, CacheCreationInputTokens: 17, OutputTokens: 49})
	mergeAnthropicUsage(&usage, apicompat.AnthropicUsage{CacheReadInputTokens: 300, CacheCreationInputTokens: 17, OutputTokens: 49})
	require.Equal(t, 308, usage.InputTokens)
	require.Equal(t, 300, usage.CacheReadInputTokens)
	require.Equal(t, 17, usage.CacheCreationInputTokens)
	require.Equal(t, 49, usage.OutputTokens)
	require.Equal(t, "fast", usage.Speed)
}

// 两条 Responses 桥的终态必须与内部结算桶一致。
func TestHandleResponsesStreamingResponse_NormalizesTerminalUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name              string
		startUsage        string
		deltaUsage        string
		wantInput         int
		wantOutput        int
		wantCached        int
		wantCacheCreation int
	}{
		{
			name:       "cache_read_input_tokens full cache",
			startUsage: `"input_tokens":173306,"prompt_tokens":173306`,
			deltaUsage: `"input_tokens":0,"output_tokens":8,"prompt_tokens":173306,"cache_read_input_tokens":173306`,
			wantOutput: 8,
			wantCached: 173306,
		},
		{
			name:       "cached_tokens full cache",
			startUsage: `"input_tokens":173306,"prompt_tokens":173306`,
			deltaUsage: `"input_tokens":0,"output_tokens":8,"prompt_tokens":173306,"cached_tokens":173306`,
			wantOutput: 8,
			wantCached: 173306,
		},
		{
			name:       "prompt_tokens_details full cache",
			startUsage: `"input_tokens":173306,"prompt_tokens":173306`,
			deltaUsage: `"input_tokens":0,"output_tokens":8,"prompt_tokens":173306,"prompt_tokens_details":{"cached_tokens":173306}`,
			wantOutput: 8,
			wantCached: 173306,
		},
		{
			name:       "DeepSeek input total with hit and miss buckets",
			startUsage: `"input_tokens":0`,
			deltaUsage: `"input_tokens":1200,"output_tokens":30,"prompt_cache_hit_tokens":800,"prompt_cache_miss_tokens":400`,
			wantInput:  400,
			wantOutput: 30,
			wantCached: 800,
		},
		{
			name:       "OpenAI prompt total with cached details",
			startUsage: `"input_tokens":0`,
			deltaUsage: `"prompt_tokens":1200,"output_tokens":30,"prompt_tokens_details":{"cached_tokens":800}`,
			wantInput:  400,
			wantOutput: 30,
			wantCached: 800,
		},
		{
			name:              "cache creation only without prompt total or miss bucket keeps independent input",
			startUsage:        `"input_tokens":1200`,
			deltaUsage:        `"input_tokens":0,"output_tokens":30,"cache_creation_input_tokens":800`,
			wantInput:         1200,
			wantOutput:        30,
			wantCacheCreation: 800,
		},
	}

	for _, tt := range tests {
		for _, terminal := range []string{"message_stop", "eof"} {
			t.Run(tt.name+"/"+terminal, func(t *testing.T) {
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				lines := []string{
					`event: message_start`,
					`data: {"type":"message_start","message":{"id":"msg_usage","type":"message","role":"assistant","content":[],"model":"k3","stop_reason":"","usage":{` + tt.startUsage + `}}}`,
					``,
					`event: message_delta`,
					`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{` + tt.deltaUsage + `}}`,
					``,
				}
				if terminal == "message_stop" {
					lines = append(lines, `event: message_stop`, `data: {"type":"message_stop"}`, ``)
				}
				resp := &http.Response{Body: io.NopCloser(strings.NewReader(strings.Join(lines, "\n")))}

				result, err := (&GatewayService{}).handleResponsesStreamingResponse(resp, c, "k3", "k3", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
				require.NoError(t, err)
				require.Equal(t, tt.wantInput, result.Usage.InputTokens)
				require.Equal(t, tt.wantCached, result.Usage.CacheReadInputTokens)
				require.Equal(t, tt.wantCacheCreation, result.Usage.CacheCreationInputTokens)

				var completed apicompat.ResponsesStreamEvent
				for _, line := range strings.Split(rec.Body.String(), "\n") {
					if !strings.HasPrefix(line, "data: ") {
						continue
					}
					var event apicompat.ResponsesStreamEvent
					require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event))
					if event.Type == "response.completed" {
						completed = event
					}
				}
				require.NotNil(t, completed.Response)
				require.NotNil(t, completed.Response.Usage)
				require.Equal(t, tt.wantInput+tt.wantCached+tt.wantCacheCreation, completed.Response.Usage.InputTokens)
				require.Equal(t, tt.wantOutput, completed.Response.Usage.OutputTokens)
				require.Equal(t, completed.Response.Usage.InputTokens+tt.wantOutput, completed.Response.Usage.TotalTokens)
				require.Equal(t, tt.wantCacheCreation, completed.Response.Usage.CacheCreationInputTokens)
				if tt.wantCached == 0 {
					require.Nil(t, completed.Response.Usage.InputTokensDetails)
				} else {
					require.Equal(t, tt.wantCached, completed.Response.Usage.InputTokensDetails.CachedTokens)
				}
			})
		}
	}
}

func TestResponsesStreamingFromNativeAnthropic_NormalizesTerminalUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name              string
		startUsage        string
		deltaUsage        string
		repeatDelta       bool
		wantInput         int
		wantOutput        int
		wantCached        int
		wantCacheCreation int
	}{
		{name: "full cache", startUsage: `"input_tokens":1200,"prompt_tokens":1200`, deltaUsage: `"input_tokens":0,"output_tokens":30,"prompt_tokens":1200,"cache_read_input_tokens":1200`, wantOutput: 30, wantCached: 1200},
		{name: "partial cache", startUsage: `"input_tokens":1200,"prompt_tokens":1200`, deltaUsage: `"input_tokens":0,"output_tokens":30,"prompt_tokens":1200,"cache_read_input_tokens":800`, wantInput: 400, wantOutput: 30, wantCached: 800},
		{name: "cache creation", startUsage: `"input_tokens":1200,"prompt_tokens":1200`, deltaUsage: `"input_tokens":0,"output_tokens":30,"prompt_tokens":1200,"cache_creation_input_tokens":800`, wantInput: 400, wantOutput: 30, wantCacheCreation: 800},
		{name: "repeated cumulative cache bucket", startUsage: `"input_tokens":1200,"prompt_tokens":1200`, deltaUsage: `"input_tokens":0,"output_tokens":30,"prompt_tokens":1200,"cache_read_input_tokens":800`, repeatDelta: true, wantInput: 400, wantOutput: 30, wantCached: 800},
	}

	for _, tt := range tests {
		for _, terminal := range []string{"message_stop", "eof"} {
			t.Run(tt.name+"/"+terminal, func(t *testing.T) {
				lines := []string{
					`event: message_start`,
					`data: {"type":"message_start","message":{"id":"msg_usage","type":"message","role":"assistant","content":[],"model":"k3","stop_reason":"","usage":{` + tt.startUsage + `}}}`,
					``,
					`event: message_delta`,
					`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{` + tt.deltaUsage + `}}`,
					``,
				}
				if tt.repeatDelta {
					lines = append(lines, `event: message_delta`, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{`+tt.deltaUsage+`}}`, ``)
				}
				if terminal == "message_stop" {
					lines = append(lines, `event: message_stop`, `data: {"type":"message_stop"}`, ``)
				}

				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
				resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(strings.Join(lines, "\n")))}

				result, err := (&OpenAIGatewayService{}).handleResponsesStreamingFromNativeAnthropic(
					resp, c, "k3", "k3", "k3", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
				require.NoError(t, err)
				require.Equal(t, tt.wantInput+tt.wantCached+tt.wantCacheCreation, result.Usage.InputTokens)
				require.Equal(t, tt.wantOutput, result.Usage.OutputTokens)
				require.Equal(t, tt.wantCached, result.Usage.CacheReadInputTokens)
				require.Equal(t, tt.wantCacheCreation, result.Usage.CacheCreationInputTokens)

				var completed apicompat.ResponsesStreamEvent
				for _, line := range strings.Split(rec.Body.String(), "\n") {
					if !strings.HasPrefix(line, "data: ") {
						continue
					}
					var event apicompat.ResponsesStreamEvent
					require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event))
					if event.Type == "response.completed" {
						completed = event
					}
				}
				require.NotNil(t, completed.Response)
				require.NotNil(t, completed.Response.Usage)
				require.Equal(t, tt.wantInput+tt.wantCached+tt.wantCacheCreation, completed.Response.Usage.InputTokens)
				require.Equal(t, tt.wantOutput, completed.Response.Usage.OutputTokens)
				require.Equal(t, completed.Response.Usage.InputTokens+tt.wantOutput, completed.Response.Usage.TotalTokens)
				require.Equal(t, tt.wantCacheCreation, completed.Response.Usage.CacheCreationInputTokens)
				if tt.wantCached == 0 {
					require.Nil(t, completed.Response.Usage.InputTokensDetails)
				} else {
					require.Equal(t, tt.wantCached, completed.Response.Usage.InputTokensDetails.CachedTokens)
				}
			})
		}
	}
}
