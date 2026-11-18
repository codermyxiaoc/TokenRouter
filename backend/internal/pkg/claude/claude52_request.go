package claude

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
)

// IsFable52Model 识别 Claude Fable 5.2 的供应商、日期和部署写法。
func IsFable52Model(model string) bool {
	return isClaude52Model(model, "fable")
}

// IsMythos52Model 识别受限访问的 Claude Mythos 5.2。
func IsMythos52Model(model string) bool {
	return isClaude52Model(model, "mythos")
}

// IsClaude52Model 标识 Fable/Mythos 5.2：只支持自适应思考、采样参数锁定且默认 high 推理。
func IsClaude52Model(model string) bool {
	return IsFable52Model(model) || IsMythos52Model(model)
}

func isClaude52Model(model, family string) bool {
	id := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		id = id[i+1:]
	}
	for _, prefix := range []string{"us.", "eu.", "apac.", "jp.", "au.", "us-gov.", "global."} {
		id = strings.TrimPrefix(id, prefix)
	}
	id = strings.TrimPrefix(id, "anthropic.")
	for _, base := range []string{"claude-" + family + "-5-2", "claude-" + family + "-5.2"} {
		if id == base || strings.HasPrefix(id, base+"-") || strings.HasPrefix(id, base+"@") || strings.HasPrefix(id, base+":") {
			return true
		}
	}
	return false
}

// ValidateClaude52Request 在协议转换前校验 Fable/Mythos 5.2 的思考与采样约束：
// 只接受自适应思考；temperature 只能为 1、top_p 只能为 0.99 且二者不能同时出现；
// 不支持 top_k 与强制工具调用。
func ValidateClaude52Request(body []byte, model string) error {
	if !IsClaude52Model(model) {
		return nil
	}
	name := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	switch gjson.GetBytes(body, "thinking.type").String() {
	case "enabled", "disabled":
		return fmt.Errorf("%s requires adaptive thinking; omit thinking or use thinking.type=adaptive and output_config.effort", name)
	case "between_tools":
		return fmt.Errorf("%s requires adaptive thinking; thinking.type=between_tools is unsupported", name)
	}
	if v := gjson.GetBytes(body, "temperature"); v.Exists() && (v.Type != gjson.Number || v.Float() != 1) {
		return fmt.Errorf("%s does not support non-default temperature", name)
	}
	if v := gjson.GetBytes(body, "top_p"); v.Exists() && (v.Type != gjson.Number || v.Float() != 0.99) {
		return fmt.Errorf("%s does not support non-default top_p", name)
	}
	if gjson.GetBytes(body, "top_k").Exists() {
		return fmt.Errorf("%s does not support top_k", name)
	}
	if gjson.GetBytes(body, "temperature").Exists() && gjson.GetBytes(body, "top_p").Exists() {
		return fmt.Errorf("%s does not allow temperature and top_p together; omit both", name)
	}
	switch gjson.GetBytes(body, "tool_choice.type").String() {
	case "any", "tool":
		return fmt.Errorf("%s does not support forced tool_choice; use auto or none", name)
	}
	return nil
}
