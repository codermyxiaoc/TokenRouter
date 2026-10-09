package openai_ws_v2

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

// 逐字段验证拒绝小数、负数、字符串、溢出和舍入陷阱，不能只保护图片字段。
func TestRelayUsageRejectsInvalidKnownCounters(t *testing.T) {
	for _, field := range []string{"input_tokens", "output_tokens", "total_tokens", "cache_creation_input_tokens", "input_tokens_details.cached_tokens", "input_tokens_details.image_tokens", "output_tokens_details.image_tokens", "output_tokens_details.reasoning_tokens"} {
		for _, raw := range []string{`-1`, `1.9`, `"999"`, `9223372036854775807`, `1e100`, `1099511627775.99999`, `null`, `true`} {
			t.Run(field+"/"+raw, func(t *testing.T) {
				counter := fmt.Sprintf(`"%s":%s`, field, raw)
				if prefix, suffix, found := strings.Cut(field, "."); found {
					counter = fmt.Sprintf(`"%s":{"%s":%s}`, prefix, suffix, raw)
				}
				usage := `"input_tokens":5,"output_tokens":3,` + counter
				if field == "input_tokens" {
					usage = `"output_tokens":3,` + counter
				}
				if field == "output_tokens" {
					usage = `"input_tokens":5,` + counter
				}
				state := &relayState{}
				got := parseUsageAndAccumulate(state, []byte(`{"type":"response.completed","usage":{`+usage+`}}`), "response.completed", nil)
				require.Zero(t, got)
				require.Error(t, state.usageError)
				require.Zero(t, state.turnUsage)
			})
		}
	}
}

// 重复父节点、计量字段与转义同名字段均不能选择性读取零值。
func TestRelayUsageRejectsAmbiguousObjects(t *testing.T) {
	for _, body := range []string{
		`{"usage":{"input_tokens":0,"input_tokens":9,"output_tokens":1}}`,
		`{"usage":{"input_tokens":0,"Input_Tokens":9,"output_tokens":1}}`,
		`{"usage":{"input_tokens":0,"input_\u0074okens":9,"output_tokens":1}}`,
		`{"usage":{"input_tokens":0,"output_tokens":1},"usage":{"input_tokens":9,"output_tokens":1}}`,
		`{"response":{},"response":{"usage":{"input_tokens":9,"output_tokens":1}}}`,
		`{"usage":{"input_tokens":5,"output_tokens":3,"input_tokens_details":{"image_tokens":0,"image_tokens":9}}}`,
		`{"usage":{"input_tokens":5,"output_tokens":3,"input_tokens_details":false}}`,
		`{"usage":{"input_tokens":5,"output_tokens":3},"tool_usage":{"image_gen":{"input_tokens_details":{"image_tokens":"9"}}}}`,
		`{"tool_usage":{"image_gen":{"output_tokens_details":{"image_tokens":1.9}}}}`,
	} {
		state := &relayState{}
		require.Zero(t, parseUsageAndAccumulate(state, []byte(body), "response.completed", nil), body)
		require.Error(t, state.usageError, body)
	}
}

// 标准计量显式为零不能被工具计数覆盖；精确的整数指数仍合法。
func TestRelayUsagePreservesExplicitZeroAndExactExponent(t *testing.T) {
	state := &relayState{}
	got := parseUsageAndAccumulate(state, []byte(`{"usage":{"input_tokens":4.2e1,"output_tokens":3.0,"input_tokens_details":{"image_tokens":0},"output_tokens_details":{"image_tokens":0}},"tool_usage":{"image_gen":{"input_tokens_details":{"image_tokens":9},"output_tokens_details":{"image_tokens":8}}}}`), "response.completed", nil)
	require.NoError(t, state.usageError)
	require.Equal(t, Usage{InputTokens: 42, OutputTokens: 3}, got)
}

// 每个聚合字段都先查溢出，失败不能损坏此前已经完成回合的统计。
func TestRelayUsageAggregationIsAtomicOnOverflow(t *testing.T) {
	for field := range 6 {
		previous := Usage{InputTokens: 10, OutputTokens: 20}
		next := Usage{InputTokens: 1, OutputTokens: 2}
		fields := func(usage *Usage) []*int {
			return []*int{&usage.InputTokens, &usage.OutputTokens, &usage.ImageInputTokens, &usage.ImageOutputTokens, &usage.CacheCreationInputTokens, &usage.CacheReadInputTokens}
		}
		*fields(&previous)[field] = math.MaxInt - 1
		*fields(&next)[field] = 2
		state := &relayState{usage: previous, turnUsage: next}
		require.Zero(t, finalizeRelayTurnUsage(state))
		require.Error(t, state.usageError)
		require.Equal(t, previous, state.usage)
	}
}

// 坏终态只终止当前回合，前一回合正常结算一次，并保留本轮前段可信消耗供失败结算。
func TestRelayInvalidUsageKeepsCompletedTurnsAndTrustedPendingUsage(t *testing.T) {
	client := newPassthroughTestFrameConn(nil, false)
	upstream := newPassthroughTestFrameConn([]passthroughTestFrame{
		{msgType: coderws.MessageText, payload: []byte(`{"type":"response.completed","response":{"id":"good","usage":{"input_tokens":10,"output_tokens":4}}}`)},
		{msgType: coderws.MessageText, payload: []byte(`{"type":"response.in_progress","response":{"id":"bad","usage":{"input_tokens":7,"output_tokens":2}}}`)},
		{msgType: coderws.MessageText, payload: []byte(`{"type":"response.completed","response":{"id":"bad","usage":{"input_tokens":99,"output_tokens":3,"input_tokens_details":{"image_tokens":"bad-counter"}}}}`)},
	}, true)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	var turns []RelayTurnResult
	result, exit := Relay(ctx, client, upstream, []byte(`{"type":"response.create","model":"m"}`), RelayOptions{OnTurnComplete: func(turn RelayTurnResult) { turns = append(turns, turn) }})
	require.NotNil(t, exit)
	require.Equal(t, "upstream_usage", exit.Stage)
	require.Len(t, turns, 1)
	require.Equal(t, Usage{InputTokens: 10, OutputTokens: 4}, result.Usage)
	require.Equal(t, Usage{InputTokens: 7, OutputTokens: 2}, result.PendingUsage)
	require.Equal(t, "bad", result.PendingRequestID)
	require.Len(t, client.Writes(), 2)
}

// 协议判别字段与会话模型不可有重复键，防止两端采用不同的 JSON 解释。
func TestRelayClientRoutingRejectsAmbiguousTypeBeforeWrite(t *testing.T) {
	client := newPassthroughTestFrameConn(nil, false)
	upstream := newPassthroughTestFrameConn(nil, false)
	body := []byte(`{"type":"noop","type":"session.update","session":{"model":"allowed","model":"blocked"}}`)
	_, exit := Relay(t.Context(), client, upstream, body, RelayOptions{})
	require.NotNil(t, exit)
	require.Equal(t, "relay_init", exit.Stage)
	require.Empty(t, upstream.Writes())
	require.NoError(t, validateRelayClientRouting([]byte(`{"type":"session.update","session":{"model":"allowed"}}`)))
}

// 首包会话标识的重复键必须在任何上游写入前失败，包含大小写与转义同名键。
func TestRelayClientRoutingRejectsAmbiguousSessionControls(t *testing.T) {
	for _, fields := range []string{
		`"previous_response_id":null,"previous_response_id":null`,
		`"previous_response_id":null,"previous_response_id":"resp_foreign"`,
		`"previous_response_id":"resp_allowed","Previous_Response_Id":"resp_foreign"`,
		`"previous_response_id":"resp_allowed","previous_response_\u0069d":"resp_foreign"`,
		`"prompt_cache_key":"allowed","prompt_cache_key":"foreign"`,
		`"prompt_cache_key":"allowed","Prompt_Cache_Key":"foreign"`,
		`"prompt_cache_key":"allowed","prompt_cache_\u006bey":"foreign"`,
	} {
		client := newPassthroughTestFrameConn(nil, false)
		upstream := newPassthroughTestFrameConn(nil, false)
		_, exit := Relay(t.Context(), client, upstream, []byte(`{"type":"response.create","model":"m",`+fields+`}`), RelayOptions{})
		require.NotNil(t, exit, fields)
		require.Equal(t, "relay_init", exit.Stage)
		require.Empty(t, upstream.Writes())
	}
	require.NoError(t, validateRelayClientRouting([]byte(`{"type":"response.create","previous_response_id":"resp_allowed","prompt_cache_key":"allowed"}`)))
}
