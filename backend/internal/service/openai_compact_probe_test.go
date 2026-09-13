package service

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestNormalizeAccountTestMode(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "", want: AccountTestModeDefault},
		{input: "default", want: AccountTestModeDefault},
		{input: " compact ", want: AccountTestModeCompact},
		{input: "COMPACT", want: AccountTestModeCompact},
		{input: " legacy_compact ", want: AccountTestModeLegacyCompact},
		{input: "unknown", want: AccountTestModeDefault},
	}

	for _, tt := range tests {
		if got := normalizeAccountTestMode(tt.input); got != tt.want {
			t.Fatalf("normalizeAccountTestMode(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestResolveAccountTestModeAndType(t *testing.T) {
	tests := []struct {
		name      string
		mode      string
		testTypes []string
		wantMode  string
		wantType  string
		explicit  bool
	}{
		{name: "explicit image", mode: "default", testTypes: []string{"image"}, wantMode: AccountTestModeDefault, wantType: AccountTestTypeImage, explicit: true},
		{name: "explicit text", mode: "default", testTypes: []string{"text"}, wantMode: AccountTestModeDefault, wantType: AccountTestTypeText, explicit: true},
		{name: "legacy compact", mode: "compact", wantMode: AccountTestModeCompact, wantType: "", explicit: false},
		{name: "mode alias", mode: "image", wantMode: AccountTestModeDefault, wantType: AccountTestTypeImage, explicit: true},
		{name: "swapped new call", mode: "image", testTypes: []string{"compact"}, wantMode: AccountTestModeCompact, wantType: AccountTestTypeImage, explicit: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode, testType, explicit := resolveAccountTestModeAndType(tt.mode, tt.testTypes...)
			if mode != tt.wantMode || testType != tt.wantType || explicit != tt.explicit {
				t.Fatalf("resolveAccountTestModeAndType(%q, %#v) = (%q, %q, %v), want (%q, %q, %v)", tt.mode, tt.testTypes, mode, testType, explicit, tt.wantMode, tt.wantType, tt.explicit)
			}
		})
	}
}

func TestCreateOpenAICompactProbePayload_NativeV2Shape(t *testing.T) {
	payload := createOpenAICompactProbePayload("gpt-5.6-sol", true)
	if payload["stream"] != true || payload["store"] != false {
		t.Fatalf("OAuth V2 probe payload must be streaming with store:false: %#v", payload)
	}
	input, ok := payload["input"].([]any)
	if !ok || len(input) != 2 {
		t.Fatalf("expected message and compaction_trigger input items: %#v", payload["input"])
	}
	last, ok := input[len(input)-1].(map[string]any)
	if !ok || last["type"] != "compaction_trigger" {
		t.Fatalf("last input item must be compaction_trigger: %#v", input[len(input)-1])
	}

	legacy := createOpenAILegacyCompactProbePayload("gpt-5.6-sol")
	legacyInput, ok := legacy["input"].([]any)
	if !ok || len(legacyInput) != 1 {
		t.Fatalf("legacy probe must not inject a V2 trigger: %#v", legacyInput)
	}
}

func TestOpenAICompactProbeFoundCompactionItem(t *testing.T) {
	sse := []byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"compaction\",\"id\":\"cmp_1\"}}\n\n")
	if !openAICompactProbeFoundCompactionItem(sse) {
		t.Fatal("SSE compaction item should mark native V2 support")
	}
	if openAICompactProbeFoundCompactionItem([]byte(`{"output":[{"type":"message"}]}`)) {
		t.Fatal("ordinary output item must not mark native V2 support")
	}
}

func TestBuildOpenAINativeCompactionV2ProbeExtraUpdates_UsesDedicatedKeys(t *testing.T) {
	now := time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC)
	updates := buildOpenAINativeCompactionV2ProbeExtraUpdates(&http.Response{StatusCode: http.StatusOK}, nil, nil, true, now)
	if updates[openAINativeCompactionV2SupportedExtraKey] != true {
		t.Fatalf("native V2 support = %v, want true", updates[openAINativeCompactionV2SupportedExtraKey])
	}
	if _, legacyTouched := updates["openai_compact_supported"]; legacyTouched {
		t.Fatal("native V2 probe must not overwrite legacy compact state")
	}

	withoutItem := buildOpenAINativeCompactionV2ProbeExtraUpdates(&http.Response{StatusCode: http.StatusOK}, []byte(`{"output":[]}`), nil, false, now)
	if withoutItem[openAINativeCompactionV2SupportedExtraKey] != false {
		t.Fatalf("2xx without compaction item = %v, want false", withoutItem[openAINativeCompactionV2SupportedExtraKey])
	}
}

func TestBuildOpenAICompactProbeExtraUpdates_SuccessMarksSupported(t *testing.T) {
	now := time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC)
	updates := buildOpenAICompactProbeExtraUpdates(&http.Response{StatusCode: http.StatusOK}, []byte(`{"id":"cmp_1"}`), nil, now)

	if got := updates["openai_compact_supported"]; got != true {
		t.Fatalf("openai_compact_supported = %v, want true", got)
	}
	if got := updates["openai_compact_last_status"]; got != http.StatusOK {
		t.Fatalf("openai_compact_last_status = %v, want %d", got, http.StatusOK)
	}
	if got := updates["openai_compact_last_error"]; got != "" {
		t.Fatalf("openai_compact_last_error = %v, want empty string", got)
	}
	if got := updates["openai_compact_checked_at"]; got != now.Format(time.RFC3339) {
		t.Fatalf("openai_compact_checked_at = %v, want %s", got, now.Format(time.RFC3339))
	}
}

func TestBuildOpenAICompactProbeExtraUpdates_404MarksUnsupported(t *testing.T) {
	now := time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC)
	body := []byte(`404 page not found`)
	updates := buildOpenAICompactProbeExtraUpdates(&http.Response{StatusCode: http.StatusNotFound}, body, nil, now)

	if got := updates["openai_compact_supported"]; got != false {
		t.Fatalf("openai_compact_supported = %v, want false", got)
	}
	if got := updates["openai_compact_last_status"]; got != http.StatusNotFound {
		t.Fatalf("openai_compact_last_status = %v, want %d", got, http.StatusNotFound)
	}
}

func TestBuildOpenAICompactProbeExtraUpdates_502DoesNotMarkUnsupported(t *testing.T) {
	now := time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC)
	updates := buildOpenAICompactProbeExtraUpdates(&http.Response{StatusCode: http.StatusBadGateway}, []byte(`Upstream request failed`), nil, now)

	if _, exists := updates["openai_compact_supported"]; exists {
		t.Fatalf("did not expect openai_compact_supported for 502 response")
	}
	if got := updates["openai_compact_last_status"]; got != http.StatusBadGateway {
		t.Fatalf("openai_compact_last_status = %v, want %d", got, http.StatusBadGateway)
	}
}

func TestBuildOpenAICompactProbeExtraUpdates_RequestErrorDoesNotMarkUnsupported(t *testing.T) {
	now := time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC)
	updates := buildOpenAICompactProbeExtraUpdates(nil, nil, errors.New("dial tcp timeout"), now)

	if _, exists := updates["openai_compact_supported"]; exists {
		t.Fatalf("did not expect openai_compact_supported for request error")
	}
	if got, exists := updates["openai_compact_last_status"]; !exists || got != nil {
		t.Fatalf("openai_compact_last_status = %v, want nil key", got)
	}
	if got := updates["openai_compact_last_error"]; got == "" {
		t.Fatalf("expected openai_compact_last_error to be populated")
	}
}

func TestBuildOpenAICompactProbeExtraUpdates_NoResponseClearsLastStatus(t *testing.T) {
	now := time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC)
	updates := buildOpenAICompactProbeExtraUpdates(nil, nil, nil, now)

	if got, exists := updates["openai_compact_last_status"]; !exists || got != nil {
		t.Fatalf("openai_compact_last_status = %v, want nil key", got)
	}
	if got := updates["openai_compact_last_error"]; got != "compact probe failed" {
		t.Fatalf("openai_compact_last_error = %v, want compact probe failed", got)
	}
}

func TestBuildOpenAICompactProbeExtraUpdates_UnknownModelDoesNotMarkUnsupported(t *testing.T) {
	now := time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC)
	body := []byte(`{"error":{"message":"unknown model gpt-5.4-openai-compact"}}`)
	updates := buildOpenAICompactProbeExtraUpdates(&http.Response{StatusCode: http.StatusBadRequest}, body, nil, now)

	if _, exists := updates["openai_compact_supported"]; exists {
		t.Fatalf("did not expect openai_compact_supported for unknown-model diagnostics")
	}
	if got := updates["openai_compact_last_status"]; got != http.StatusBadRequest {
		t.Fatalf("openai_compact_last_status = %v, want %d", got, http.StatusBadRequest)
	}
}

func TestBuildOpenAICompactProbeExtraUpdates_EmptyFailureBodyFallsBackToHTTPStatus(t *testing.T) {
	now := time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC)
	updates := buildOpenAICompactProbeExtraUpdates(&http.Response{StatusCode: http.StatusServiceUnavailable}, nil, nil, now)

	if got := updates["openai_compact_last_status"]; got != http.StatusServiceUnavailable {
		t.Fatalf("openai_compact_last_status = %v, want %d", got, http.StatusServiceUnavailable)
	}
	if got := updates["openai_compact_last_error"]; got != "HTTP 503" {
		t.Fatalf("openai_compact_last_error = %v, want HTTP 503", got)
	}
}
