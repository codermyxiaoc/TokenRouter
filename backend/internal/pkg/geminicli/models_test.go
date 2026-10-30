package geminicli

import (
	"strings"
	"testing"
)

func TestDefaultModels_ContainsImageModels(t *testing.T) {
	t.Parallel()

	byID := make(map[string]Model, len(DefaultModels))
	for _, model := range DefaultModels {
		byID[model.ID] = model
	}

	required := []string{
		"gemini-3.1-flash-lite-image",
		"gemini-3.1-flash-image",
	}

	for _, id := range required {
		if _, ok := byID[id]; !ok {
			t.Fatalf("expected curated Gemini model %q to exist", id)
		}
	}
}

func TestGoogleOneModels_ExcludeUnsupportedNewAndImageModels(t *testing.T) {
	t.Parallel()

	mapping := GoogleOneModelMapping()
	for _, id := range []string{"gemini-2.0-flash", "gemini-2.5-flash", "gemini-2.5-pro"} {
		if mapping[id] != id {
			t.Fatalf("expected Google One model %q to map to itself", id)
		}
	}
	for _, id := range []string{"gemini-3.1-flash-lite-image", "gemini-3.1-flash-image", "gemini-3.5-flash"} {
		if _, ok := mapping[id]; ok {
			t.Fatalf("did not expect unsupported Google One model %q", id)
		}
	}

	mapping["gemini-2.5-flash"] = "mutated"
	if GoogleOneModelMapping()["gemini-2.5-flash"] != "gemini-2.5-flash" {
		t.Fatal("GoogleOneModelMapping must return a defensive copy")
	}
}

// 默认目录不再列出已停用的 2.x，也不列出价格目录中没有官方价的新型号，避免按同族价格误计费。
func TestDefaultModels_ExcludeRetiredAndUnpricedModels(t *testing.T) {
	for _, model := range DefaultModels {
		if strings.HasPrefix(model.ID, "gemini-2.") {
			t.Fatalf("retired Gemini model %q must not be in the default catalog", model.ID)
		}
		switch model.ID {
		case "gemini-3-pro-preview", "gemini-3.5-flash-lite", "gemini-3.7-flash", "gemini-3.8-flash":
			t.Fatalf("model %q must not be in the default catalog", model.ID)
		}
	}
	if DefaultTestModel != "gemini-3.5-flash" {
		t.Fatalf("unexpected default test model %q", DefaultTestModel)
	}
}
