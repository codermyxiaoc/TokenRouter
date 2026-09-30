package claude

import "testing"

// 校验所有入口共用的模型边界及参数限制，避免别名或采样参数被静默丢弃。
func TestSonnet55RequestCapabilities(t *testing.T) {
	for _, model := range []string{"claude-sonnet-5-5", "anthropic/claude-sonnet-5.5", "global.anthropic.claude-sonnet-5-5", "claude-sonnet-5-5@20260928"} {
		if !IsSonnet55Model(model) {
			t.Errorf("model not recognized: %s", model)
		}
	}
	for _, model := range []string{"claude-sonnet-5", "claude-sonnet-5-50", "claude-sonnet-5-5x", "claude-opus-5-5"} {
		if IsSonnet55Model(model) {
			t.Errorf("unexpected model: %s", model)
		}
	}
	for _, body := range []string{`{}`, `{"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`, `{"thinking":{"type":"between_tools"},"output_config":{"effort":"high"}}`, `{"temperature":1,"top_p":0.99,"tool_choice":{"type":"auto"}}`} {
		if err := ValidateSonnet55Request([]byte(body), "claude-sonnet-5-5"); err != nil {
			t.Errorf("valid request: %s: %v", body, err)
		}
	}
	for _, body := range []string{`{"thinking":{"type":"disabled"}}`, `{"thinking":{"type":"enabled"}}`, `{"thinking":{"type":"between_tools","budget_tokens":1}}`, `{"thinking":{"type":"between_tools"},"output_config":{"effort":"max"}}`, `{"temperature":0.7}`, `{"top_p":0.98}`, `{"top_k":10}`, `{"tool_choice":"required"}`, `{"tool_choice":{"type":"tool","name":"f"}}`} {
		if err := ValidateSonnet55Request([]byte(body), "claude-sonnet-5-5"); err == nil {
			t.Errorf("invalid request accepted: %s", body)
		}
		if err := ValidateSonnet55Request([]byte(body), "claude-sonnet-5"); err != nil {
			t.Errorf("legacy request changed: %v", err)
		}
	}
}
