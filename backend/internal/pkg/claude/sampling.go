package claude

import "strings"

// RejectsTemperature 同时识别直接型号与 Bedrock、Vertex 的供应商前后缀。
func RejectsTemperature(model string) bool {
	if IsOpus55Model(model) || IsClaude52Model(model) {
		return true
	}
	id := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		id = id[i+1:]
	}
	if i := strings.Index(id, "anthropic."); i >= 0 {
		id = id[i+len("anthropic."):]
	}
	for _, family := range []string{"claude-opus-4-7", "claude-opus-4-8", "claude-opus-4.7", "claude-opus-4.8"} {
		if id == family || strings.HasPrefix(id, family+"-") || strings.HasPrefix(id, family+"@") || strings.HasPrefix(id, family+":") {
			return true
		}
	}
	return false
}
