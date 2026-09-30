package claude

import "strings"

// IsOpus55Model 识别 Opus 5.5 的同型号写法，不将 Opus 5 或其它小版本当作 5.5。
// 供应商路径与版本后缀只用于能力/价格识别，不生成路由 ID 或承诺地域可用性。
func IsOpus55Model(model string) bool {
	return isClaude55Model(model, "opus")
}

// IsSonnet55Model 只识别 Sonnet 5.5 及其明确的供应商、日期和部署写法。
func IsSonnet55Model(model string) bool {
	return isClaude55Model(model, "sonnet")
}

// IsClaude55SignedThinkingModel 标识缺省开启思考、需要回传签名历史的型号。
func IsClaude55SignedThinkingModel(model string) bool {
	return IsOpus55Model(model) || IsSonnet55Model(model)
}

func isClaude55Model(model, family string) bool {
	id := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		id = id[i+1:]
	}
	for _, prefix := range []string{"us.", "eu.", "apac.", "jp.", "au.", "us-gov.", "global."} {
		id = strings.TrimPrefix(id, prefix)
	}
	id = strings.TrimPrefix(id, "anthropic.")
	for _, base := range []string{"claude-" + family + "-5-5", "claude-" + family + "-5.5"} {
		if id == base || strings.HasPrefix(id, base+"-") || strings.HasPrefix(id, base+"@") || strings.HasPrefix(id, base+":") {
			return true
		}
	}
	return false
}
