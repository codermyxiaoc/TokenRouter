package openai_ws_v2

import (
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// 对抗性回归：图片计量不能把小数、字符串或超界数字转成可信的可结算整数。
func TestAdversarialImageUsageRejectsInvalidNumbers(t *testing.T) {
	for _, raw := range []string{`1.9`, `"999"`, `9223372036854775807`, `18446744073709551615`, `1e100`} {
		for _, source := range []string{"usage", "tool_usage"} {
			t.Run(source+"/"+raw, func(t *testing.T) {
				body := fmt.Sprintf(`{"type":"response.completed","response":{"usage":{"input_tokens":5,"output_tokens":3,"input_tokens_details":{"image_tokens":%s}}}}`, raw)
				if source == "tool_usage" {
					body = fmt.Sprintf(`{"type":"response.completed","response":{"usage":{"input_tokens":5,"output_tokens":3},"tool_usage":{"image_gen":{"input_tokens_details":{"image_tokens":%s},"output_tokens_details":{"image_tokens":%s}}}}}`, raw, raw)
				}
				var state relayState
				usage := parseUsageAndAccumulate(&state, []byte(body), "response.completed", nil)
				require.Zero(t, usage.ImageInputTokens, "畸形图片计量应拒绝或归零：%s", body)
				require.Zero(t, usage.ImageOutputTokens)
			})
		}
	}
}

// 对抗性回归：多轮图片计量聚合不可因整型溢出变成负数。
func TestAdversarialImageUsageAccumulationDoesNotOverflow(t *testing.T) {
	var state relayState
	for range 2 {
		state.turnUsage = Usage{InputTokens: 1, ImageInputTokens: math.MaxInt, ImageOutputTokens: math.MaxInt}
		finalizeRelayTurnUsage(&state)
	}
	require.GreaterOrEqual(t, state.usage.ImageInputTokens, 0)
	require.GreaterOrEqual(t, state.usage.ImageOutputTokens, 0)
}
