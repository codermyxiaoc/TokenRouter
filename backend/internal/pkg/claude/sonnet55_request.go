package claude

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
)

// ValidateSonnet55Request 在兼容转换或 OAuth 伪装前校验不受支持的原始参数。
// 型号由调用方使用最终映射结果提供，避免客户端别名绕过能力约束。
func ValidateSonnet55Request(body []byte, model string) error {
	if !IsSonnet55Model(model) {
		return nil
	}
	// 原始兼容协议在转换前校验，保证无效档位得到客户端参数错误而非转换失败。
	for _, field := range []string{"reasoning.effort", "reasoning_effort"} {
		switch effort := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, field).String())); effort {
		case "", "none", "low", "medium", "high", "xhigh", "max":
		default:
			return fmt.Errorf("reasoning effort %q is not supported by claude-sonnet-5-5", effort)
		}
	}
	switch gjson.GetBytes(body, "thinking.type").String() {
	case "disabled", "enabled":
		return fmt.Errorf("claude-sonnet-5-5 requires adaptive thinking or thinking.type=between_tools")
	case "between_tools":
		effort := gjson.GetBytes(body, "output_config.effort").String()
		if effort == "xhigh" || effort == "max" {
			return fmt.Errorf("claude-sonnet-5-5 thinking.type=between_tools supports only low, medium or high effort")
		}
		for _, field := range []string{"thinking.display", "thinking.budget_tokens", "thinking.block_binding"} {
			if gjson.GetBytes(body, field).Exists() {
				return fmt.Errorf("claude-sonnet-5-5 thinking.type=between_tools does not support %s", field)
			}
		}
	}
	if gjson.GetBytes(body, "tool_choice").String() == "required" {
		return fmt.Errorf("claude-sonnet-5-5 does not support forced tool_choice; use auto or none")
	}
	switch gjson.GetBytes(body, "tool_choice.type").String() {
	case "any", "tool", "function", "custom", "namespace":
		return fmt.Errorf("claude-sonnet-5-5 does not support forced tool_choice; use auto or none")
	}
	if v := gjson.GetBytes(body, "temperature"); v.Exists() && (v.Type != gjson.Number || v.Float() != 1) {
		return fmt.Errorf("claude-sonnet-5-5 does not support non-default temperature")
	}
	if v := gjson.GetBytes(body, "top_p"); v.Exists() && (v.Type != gjson.Number || v.Float() < 0.99 || v.Float() > 1) {
		return fmt.Errorf("claude-sonnet-5-5 does not support non-default top_p")
	}
	if gjson.GetBytes(body, "top_k").Exists() {
		return fmt.Errorf("claude-sonnet-5-5 does not support top_k")
	}
	return nil
}
