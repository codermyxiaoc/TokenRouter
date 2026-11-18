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
	for field := range 8 {
		previous := Usage{InputTokens: 10, OutputTokens: 20}
		next := Usage{InputTokens: 1, OutputTokens: 2}
		fields := func(usage *Usage) []*int {
			return []*int{&usage.InputTokens, &usage.OutputTokens, &usage.ImageInputTokens, &usage.ImageOutputTokens, &usage.CacheCreationInputTokens, &usage.CacheReadInputTokens, &usage.CacheCreation30mInputTokens, &usage.CacheCreation12hInputTokens}
		}
		*fields(&previous)[field] = math.MaxInt - 1
		*fields(&next)[field] = 2
		state := &relayState{usage: previous, turnUsage: next}
		require.Zero(t, finalizeRelayTurnUsage(state))
		require.Error(t, state.usageError)
		require.Equal(t, previous, state.usage)
	}
}

// 期限只随同源总量更新，跨轮汇总遇到缺失明细时仍可使用总写入量结算。
func TestRelayCacheWriteTTLTracksUsageSnapshots(t *testing.T) {
	state := &relayState{}
	observe := func(eventType, details string) {
		message := []byte(`{"usage":{"input_tokens":10,"output_tokens":1,"prompt_tokens_details":` + details + `}}`)
		parseUsageAndAccumulate(state, message, eventType, nil)
		require.NoError(t, state.usageError)
	}
	observe("response.completed", `{"cache_write_tokens":10,"cache_write_tokens_details":{"ttl_30m_tokens":1,"ttl_12h_tokens":9}}`)
	first := finalizeRelayTurnUsage(state)
	require.Equal(t, 1, first.CacheCreation30mInputTokens)
	require.Equal(t, 9, first.CacheCreation12hInputTokens)
	observe("response.completed", `{"cache_write_tokens":10,"cache_write_tokens_details":{"ttl_30m_tokens":0,"ttl_12h_tokens":10}}`)
	finalizeRelayTurnUsage(state)
	require.Equal(t, 20, state.usage.CacheCreationInputTokens)
	require.Equal(t, 1, state.usage.CacheCreation30mInputTokens)
	require.Equal(t, 19, state.usage.CacheCreation12hInputTokens)

	observe("response.in_progress", `{"cache_write_tokens":5,"cache_write_tokens_details":{"ttl_30m_tokens":2,"ttl_12h_tokens":3}}`)
	observe("response.in_progress", `{"cache_write_tokens":7,"cache_write_tokens_details":{"ttl_12h_tokens":3}}`)
	require.Zero(t, state.turnUsage.CacheCreation30mInputTokens)
	require.Zero(t, state.turnUsage.CacheCreation12hInputTokens)
	observe("response.completed", `{"cache_write_tokens":7,"cache_write_tokens_details":{"ttl_30m_tokens":2,"ttl_12h_tokens":3}}`)
	turn := finalizeRelayTurnUsage(state)
	require.Equal(t, 7, turn.CacheCreationInputTokens)
	require.Zero(t, turn.CacheCreation30mInputTokens)
	require.Zero(t, turn.CacheCreation12hInputTokens)
	require.Equal(t, 27, state.usage.CacheCreationInputTokens)
	require.Zero(t, state.usage.CacheCreation30mInputTokens)
	require.Zero(t, state.usage.CacheCreation12hInputTokens)
}

// 上游提供了可信总量时，可选明细损坏只影响期限折算，成功回合仍保留全部用量。
func TestRelayCacheWriteInvalidTTLRetainsAggregate(t *testing.T) {
	for _, details := range []string{
		`null`,
		`"unsupported"`,
		`{"ttl_30m_tokens":2,"ttl_12h_tokens":"8"}`,
		`{"ttl_30m_tokens":1.5,"ttl_12h_tokens":8.5}`,
		`{"ttl_30m_tokens":2,"ttl_12h_tokens":8,"ttl_12h_tokens":0}`,
	} {
		state := &relayState{}
		message := []byte(`{"usage":{"input_tokens":10,"output_tokens":3,"input_tokens_details":{"cache_write_tokens":10,"cache_write_tokens_details":` + details + `}}}`)
		parseUsageAndAccumulate(state, message, "response.completed", nil)
		require.NoError(t, state.usageError, details)
		require.Equal(t, Usage{InputTokens: 10, OutputTokens: 3, CacheCreationInputTokens: 10}, finalizeRelayTurnUsage(state), details)
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
