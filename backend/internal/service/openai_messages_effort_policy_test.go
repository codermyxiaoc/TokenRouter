//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 缺省 effort 的 Messages 请求：桥接器补出的默认强度不受上限约束，只保留 from=none 映射。
func TestWithOpenAIReasoningEffortPolicyForMessagesDefaultEffort(t *testing.T) {
	mappings := []ReasoningEffortMapping{{From: "none", To: "low"}, {From: "high", To: "medium"}}
	ctx := WithOpenAIReasoningEffortPolicyForMessages(context.Background(), []byte(`{"model":"gpt-5.5"}`), "low", mappings, ReasoningEffortOverLimitDowngrade, "gpt-5.5")
	out, _, err := ApplyOpenAIReasoningEffortPolicyFromContext(ctx, []byte(`{"reasoning":{"effort":"high"}}`))
	require.NoError(t, err)
	require.Equal(t, "high", gjson.GetBytes(out, "reasoning.effort").String(), "补出的默认强度不应被上限或非 none 映射改写")

	out, _, err = ApplyOpenAIReasoningEffortPolicyFromContext(ctx, []byte(`{"reasoning":{"effort":"none"}}`))
	require.NoError(t, err)
	require.Equal(t, "low", gjson.GetBytes(out, "reasoning.effort").String(), "none 映射仍生效")
}

// 显式关闭思考时保留上限；显式 effort 时与普通策略一致。
func TestWithOpenAIReasoningEffortPolicyForMessagesKeepsCeiling(t *testing.T) {
	ctx := WithOpenAIReasoningEffortPolicyForMessages(context.Background(), []byte(`{"thinking":{"type":"disabled"}}`), "low", nil, ReasoningEffortOverLimitDowngrade, "gpt-5.5")
	out, _, err := ApplyOpenAIReasoningEffortPolicyFromContext(ctx, []byte(`{"reasoning":{"effort":"high"}}`))
	require.NoError(t, err)
	require.Equal(t, "low", gjson.GetBytes(out, "reasoning.effort").String())

	ctx = WithOpenAIReasoningEffortPolicyForMessages(context.Background(), []byte(`{"output_config":{"effort":"high"}}`), "low", nil, ReasoningEffortOverLimitDowngrade, "gpt-5.5")
	out, _, err = ApplyOpenAIReasoningEffortPolicyFromContext(ctx, []byte(`{"reasoning":{"effort":"high"}}`))
	require.NoError(t, err)
	require.Equal(t, "low", gjson.GetBytes(out, "reasoning.effort").String())
}

// GLM 上游把分组上限换算为合法档位：非 5.3 没有 low，上限 medium 时降为 high 以下最高合法档不存在则拒绝。
func TestApplyOpenAIReasoningEffortPolicyForGLM(t *testing.T) {
	ctx := WithOpenAIReasoningEffortPolicyForModel(context.Background(), "medium", nil, ReasoningEffortOverLimitDowngrade, "glm-5.3")
	out, _, err := applyOpenAIReasoningEffortPolicyForGLMFromContext(ctx, []byte(`{"reasoning_effort":"max"}`), "glm-5.3")
	require.NoError(t, err)
	require.Equal(t, "low", gjson.GetBytes(out, "reasoning_effort").String(), "GLM-5.3 在 medium 上限下取合法档 low")

	ctx = WithOpenAIReasoningEffortPolicyForModel(context.Background(), "medium", nil, ReasoningEffortOverLimitDowngrade, "glm-5.2")
	_, _, err = applyOpenAIReasoningEffortPolicyForGLMFromContext(ctx, []byte(`{"reasoning_effort":"max"}`), "glm-5.2")
	require.Error(t, err, "非 5.3 的 GLM 没有不高于 medium 的合法档位，应拒绝")

	// 非 GLM 模型沿用普通策略。
	ctx = WithOpenAIReasoningEffortPolicyForModel(context.Background(), "medium", nil, ReasoningEffortOverLimitDowngrade, "gpt-5.5")
	out, _, err = applyOpenAIReasoningEffortPolicyForGLMFromContext(ctx, []byte(`{"reasoning_effort":"high"}`), "gpt-5.5")
	require.NoError(t, err)
	require.Equal(t, "medium", gjson.GetBytes(out, "reasoning_effort").String())
}
