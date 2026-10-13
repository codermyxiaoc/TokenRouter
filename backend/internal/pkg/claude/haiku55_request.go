package claude

import (
	"fmt"
	"github.com/tidwall/gjson"
	"strings"
)

// ValidateHaiku55Request 在协议转换前校验 Haiku 5.5 的采样和思考约束，保留强制工具调用。
func ValidateHaiku55Request(body []byte, model string) error {
	if !IsHaiku55Model(model) {
		return nil
	}
	for _, field := range []string{"reasoning.effort", "reasoning_effort", "output_config.effort"} {
		effort := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, field).String()))
		switch effort {
		case "", "none", "low", "medium", "high", "xhigh", "max":
		default:
			return fmt.Errorf("reasoning effort %q is not supported by claude-haiku-5-5", effort)
		}
	}
	switch gjson.GetBytes(body, "thinking.type").String() {
	case "enabled":
		return fmt.Errorf("claude-haiku-5-5 requires adaptive thinking; omit thinking or use thinking.type=adaptive")
	case "between_tools":
		return fmt.Errorf("claude-haiku-5-5 requires adaptive thinking; thinking.type=between_tools is unsupported")
	case "disabled":
		switch gjson.GetBytes(body, "output_config.effort").String() {
		case "xhigh", "max":
			return fmt.Errorf("claude-haiku-5-5 thinking.type=disabled supports only low, medium or high effort")
		}
	}
	if v := gjson.GetBytes(body, "temperature"); v.Exists() && (v.Type != gjson.Number || v.Float() != 1) {
		return fmt.Errorf("claude-haiku-5-5 does not support non-default temperature")
	}
	if v := gjson.GetBytes(body, "top_p"); v.Exists() && (v.Type != gjson.Number || v.Float() != 0.99) {
		return fmt.Errorf("claude-haiku-5-5 does not support non-default top_p")
	}
	if gjson.GetBytes(body, "top_k").Exists() {
		return fmt.Errorf("claude-haiku-5-5 does not support top_k")
	}
	if gjson.GetBytes(body, "temperature").Exists() && gjson.GetBytes(body, "top_p").Exists() {
		return fmt.Errorf("claude-haiku-5-5 does not allow temperature and top_p together; omit both")
	}
	return nil
}
