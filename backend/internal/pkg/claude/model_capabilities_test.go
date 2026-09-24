package claude

import "testing"

// 新小版本必须保持独立身份，不能误匹配旧款或相邻版本。
func TestIsOpus55Model(t *testing.T) {
	for _, model := range []string{"claude-opus-5-5", " CLAUDE-OPUS-5.5 ", "anthropic/claude-opus-5-5", "us.anthropic.claude-opus-5-5", "projects/p/locations/global/publishers/anthropic/models/claude-opus-5-5", "claude-opus-5-5-high", "claude-opus-5-5@20260922"} {
		if !IsOpus55Model(model) {
			t.Errorf("未识别 Opus 5.5：%s", model)
		}
	}
	for _, model := range []string{"", "claude-opus-5", "claude-opus-5-50", "claude-opus-5.50", "claude-opus-5-6", "claude-sonnet-5-5", "not-claude-opus-5-5", "claude-opus-5-5x"} {
		if IsOpus55Model(model) {
			t.Errorf("错误识别其它型号：%s", model)
		}
	}
}

func TestDefaultModelsContainsOpus55(t *testing.T) {
	for _, model := range DefaultModels {
		if model.ID == "claude-opus-5-5" {
			if model.DisplayName != "Claude Opus 5.5" || model.CreatedAt != "2026-09-22T00:00:00Z" {
				t.Fatalf("型号元数据错误：%+v", model)
			}
			if NormalizeModelID(model.ID) != model.ID || DenormalizeModelID(model.ID) != model.ID {
				t.Fatal("不带日期的固定型号不应重写为旧版本")
			}
			return
		}
	}
	t.Fatal("默认模型目录缺少 Opus 5.5")
}
