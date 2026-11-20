package service

import (
	"context"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/pkg/claude"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// prepareRoutedClaudeRequest 根据本次尝试的最终模型转换旧式思考模式，然后执行原生参数校验。
// @project-doc docs/interfaces/anthropic_upstream.md#anthropic_model_policy
func prepareRoutedClaudeRequest(ctx context.Context, account *Account, parsed *ParsedRequest) error {
	target := resolveAccountUpstreamModel(ctx, account, parsed.Model)
	if !account.IsBedrock() {
		if body, changed := adaptMappedClaudeThinking(ctx, account, parsed.Body.Bytes(), parsed.Model, target); changed {
			if err := parsed.ReplaceBody(body); err != nil {
				return err
			}
		}
	}
	return ValidateSonnet55Request(parsed.Body.Bytes(), target)
}

// adaptMappedClaudeThinking 比较模型映射两端；剥离分组前缀、上下文选择器或规范化同一型号均不改变思考模式。
func adaptMappedClaudeThinking(ctx context.Context, account *Account, body []byte, source, target string) ([]byte, bool) {
	source = gatewaySchedulingModel(ctx, source)
	if trace, ok := APIKeyModelRedirectTraceFromContext(ctx); ok {
		// SourceModel 已剥离复合 Key 的选组前缀，能与真实模型比较。
		source = trace.SourceModel
	}
	if source == target || resolveAnthropicAccountUpstreamModel(account, normalizeClaudeCodeLongContextModel(source)) == target {
		return body, false
	}
	if !claude.IsSonnet55Model(target) && !claude.IsOpus55Model(target) && !claude.IsHaiku55Model(target) {
		return body, false
	}
	return adaptClaude55LegacyThinking(body, target)
}

// adaptClaude55LegacyThinking 只替换目标 5.5 型号不接受的旧模式；其它思考参数交由目标型号校验。
func adaptClaude55LegacyThinking(body []byte, model string) ([]byte, bool) {
	switch gjson.GetBytes(body, "thinking.type").String() {
	case "enabled":
		body, _ = sjson.SetBytes(body, "thinking.type", "adaptive")
		body, _ = sjson.DeleteBytes(body, "thinking.budget_tokens")
		return body, true
	case "disabled":
		if claude.IsSonnet55Model(model) {
			body, _ = sjson.SetBytes(body, "thinking.type", "between_tools")
			return body, true
		}
	}
	return body, false
}

// ValidateSonnet55Request 供各协议桥共用映射后的 Sonnet 5.5 参数边界。
func ValidateSonnet55Request(body []byte, model string) error {
	// Fable/Mythos 5.2 与 Haiku 5.5 同样锁定采样参数，共用同一入口校验。
	if err := claude.ValidateClaude52Request(body, model); err != nil {
		return err
	}
	if err := claude.ValidateHaiku55Request(body, model); err != nil {
		return err
	}
	return claude.ValidateSonnet55Request(body, model)
}

// filterSonnet55ToolsetBeta 移除与稳定 computer/browser 工具集不兼容的旧流式 beta。
// 每个工具自己的 eager_input_streaming 字段仍保持不变。
func filterSonnet55ToolsetBeta(header string, body []byte, model string) string {
	if !claude.IsSonnet55Model(model) && !claude.IsHaiku55Model(model) {
		return header
	}
	for _, tool := range gjson.GetBytes(body, "tools").Array() {
		switch tool.Get("type").String() {
		case "computer_toolset_20260801", "browser_toolset_20260801":
			return stripBetaTokensWithSet(header, map[string]struct{}{claude.BetaFineGrainedToolStreaming: {}})
		}
	}
	return header
}

// filterSonnet55ToolsetBetaHeader 在管理员覆写后再次约束协议不支持的 beta 组合。
func filterSonnet55ToolsetBetaHeader(headers http.Header, body []byte, model string) {
	header := getHeaderRaw(headers, "anthropic-beta")
	filtered := filterSonnet55ToolsetBeta(header, body, model)
	if filtered == header {
		return
	}
	deleteHeaderAllForms(headers, "anthropic-beta")
	if filtered != "" {
		setHeaderRaw(headers, "anthropic-beta", filtered)
	}
}
