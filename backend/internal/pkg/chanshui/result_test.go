package chanshui

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func reportFixture() map[string]any {
	return map[string]any{
		"id": testID, "status": "done", "model": "claude-sonnet-5", "expires_at": 2000000000,
		"probes": []any{map[string]any{"id": "fingerprint", "status": "done", "ms": 123}},
		"verdict": map[string]any{
			"fingerprint": map[string]any{"basis": "numeric", "family": "claude", "top_model": "claude-sonnet-5", "used_outputs": 3, "low_confidence": false},
			"total":       map[string]any{"score": 100, "max": 100, "parts": []any{map[string]any{"key": "fingerprint", "applicable": true, "known": true, "points": 10, "max_points": 10, "coefficient": 100, "contribution": 100}}},
		},
	}
}

func parseFixture(t *testing.T, fixture map[string]any) *Result {
	t.Helper()
	body, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	client := testClient(func(*http.Request) (*http.Response, error) { return response(200, string(body)), nil })
	result, err := client.Get(context.Background(), testID)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func fingerprint(f map[string]any) map[string]any {
	return f["verdict"].(map[string]any)["fingerprint"].(map[string]any)
}
func total(f map[string]any) map[string]any {
	return f["verdict"].(map[string]any)["total"].(map[string]any)
}
func firstPart(f map[string]any) map[string]any { return total(f)["parts"].([]any)[0].(map[string]any) }

func TestReliableFingerprintVerdicts(t *testing.T) {
	for _, item := range []struct{ name, model, actual, family, want string }{
		{"same", "claude-sonnet-5", "claude-sonnet-5", "anthropic", "clean"},
		{"qualified", "anthropic/claude-sonnet-5", "claude-sonnet-5", "claude", "clean"},
		{"same_family_substitution", "claude-opus-5", "claude-sonnet-5", "claude", "adulterated"},
		{"other_family", "claude-sonnet-5", "gpt-6", "openai", "adulterated"},
		{"dated_deepseek", "deepseek-v4.1-flash", "deepseek-v4.1-flash-1017", "deepseek", "clean"},
		{"known_deepseek_alias", "deepseek-flash", "deepseek-v4.1-flash-1017", "deepseek", "clean"},
		{"unrecognized_alias", "my-special-model", "claude-sonnet-5", "claude", "unknown"},
	} {
		t.Run(item.name, func(t *testing.T) {
			f := reportFixture()
			f["model"] = item.model
			fingerprint(f)["top_model"] = item.actual
			fingerprint(f)["family"] = item.family
			r := parseFixture(t, f)
			if r.Verdict != item.want {
				t.Fatalf("verdict = %s", r.Verdict)
			}
			if item.want == "unknown" && r.ScoreComplete {
				t.Fatal("unknown identity is complete evidence")
			}
			if item.want == "adulterated" && (r.Score == nil || *r.Score != 0 || !r.ScoreComplete) {
				t.Fatal("presented score must remove fingerprint contribution")
			}
			if item.want == "clean" && (r.Score == nil || *r.Score != 100 || !r.ScoreComplete) {
				t.Fatal("complete matching fingerprint score lost")
			}
		})
	}
}

func TestMissingAndUnreliableFingerprintNeverMeansAdulterated(t *testing.T) {
	tests := map[string]func(map[string]any){
		"missing":                func(f map[string]any) { delete(f["verdict"].(map[string]any), "fingerprint") },
		"null":                   func(f map[string]any) { f["verdict"].(map[string]any)["fingerprint"] = nil },
		"missing_low_confidence": func(f map[string]any) { delete(fingerprint(f), "low_confidence") },
		"null_low_confidence":    func(f map[string]any) { fingerprint(f)["low_confidence"] = nil },
		"low_confidence":         func(f map[string]any) { fingerprint(f)["low_confidence"] = true },
		"missing_basis":          func(f map[string]any) { delete(fingerprint(f), "basis") },
		"unsupported_basis":      func(f map[string]any) { fingerprint(f)["basis"] = "self_report" },
		"missing_samples":        func(f map[string]any) { delete(fingerprint(f), "used_outputs") },
		"insufficient_samples":   func(f map[string]any) { fingerprint(f)["used_outputs"] = 2 },
		"missing_baseline":       func(f map[string]any) { fingerprint(f)["top_model"] = "" },
		"unknown_family":         func(f map[string]any) { fingerprint(f)["family"] = "unknown" },
		"inconsistent_family":    func(f map[string]any) { fingerprint(f)["family"] = "grok" },
		"error":                  func(f map[string]any) { fingerprint(f)["error"] = "sk-secret" },
		"error_object":           func(f map[string]any) { fingerprint(f)["error"] = map[string]any{"message": "sk-secret"} },
		"probe_error":            func(f map[string]any) { f["probes"].([]any)[0].(map[string]any)["error"] = "sk-secret" },
		"probe_running":          func(f map[string]any) { f["probes"].([]any)[0].(map[string]any)["status"] = "running" },
		"missing_probe":          func(f map[string]any) { f["probes"] = []any{} },
		"still_running":          func(f map[string]any) { f["status"] = "running" },
		"cancelled":              func(f map[string]any) { f["status"] = "cancelled" },
		"unknown_state":          func(f map[string]any) { f["status"] = "accepted-secret" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			f := reportFixture()
			fingerprint(f)["top_model"] = "claude-opus-5"
			mutate(f)
			r := parseFixture(t, f)
			if r.Verdict != "unknown" || r.ScoreComplete {
				t.Fatalf("unsupported evidence interpreted as %s / complete %v", r.Verdict, r.ScoreComplete)
			}
		})
	}
}

func TestScorePresenceRangeAndCompleteness(t *testing.T) {
	for _, item := range []struct {
		name   string
		mutate func(map[string]any)
		score  bool
	}{
		{"null_total", func(f map[string]any) { f["verdict"].(map[string]any)["total"] = nil }, false},
		{"missing_score", func(f map[string]any) { delete(total(f), "score") }, false},
		{"null_score", func(f map[string]any) { total(f)["score"] = nil }, false},
		{"negative", func(f map[string]any) { total(f)["score"] = -1 }, false},
		{"too_large", func(f map[string]any) { total(f)["score"] = 101 }, false},
		{"missing_max", func(f map[string]any) { delete(total(f), "max") }, false},
		{"different_scale", func(f map[string]any) { total(f)["max"] = 10 }, false},
		{"partial", func(f map[string]any) { f["status"] = "partial" }, true},
		{"parts_missing", func(f map[string]any) { delete(total(f), "parts") }, true},
		{"unknown_part", func(f map[string]any) { firstPart(f)["key"] = "new_probe" }, true},
		{"unknown_result", func(f map[string]any) { firstPart(f)["known"] = false }, true},
		{"invalid_points", func(f map[string]any) { firstPart(f)["points"] = 11 }, true},
		{"missing_applicability", func(f map[string]any) { delete(firstPart(f), "applicable") }, true},
	} {
		t.Run(item.name, func(t *testing.T) {
			f := reportFixture()
			item.mutate(f)
			r := parseFixture(t, f)
			if (r.Score != nil) != item.score || r.ScoreComplete {
				t.Fatalf("score existence %v complete %v", r.Score != nil, r.ScoreComplete)
			}
		})
	}
	f := reportFixture()
	total(f)["score"] = 0
	firstPart(f)["points"] = 0
	firstPart(f)["contribution"] = 0
	r := parseFixture(t, f)
	if r.Score == nil || *r.Score != 0 || !r.ScoreComplete {
		t.Fatal("real zero must not be treated as missing score")
	}
}

func TestCapabilityErrorsAndUnsupportedCannotTriggerLowScore(t *testing.T) {
	for _, item := range []struct {
		status      string
		pass        any
		remoteError any
		complete    bool
	}{
		{"pass", true, "", true}, {"fail", false, "", true},
		{"unknown", nil, "", false}, {"unsupported", nil, "", false}, {"error", nil, "sk-secret", false},
		{"fail", false, "sk-secret", false}, {"fail", nil, "", false},
	} {
		f := reportFixture()
		f["probes"] = []any{map[string]any{"id": "tools", "status": "done"}}
		v := f["verdict"].(map[string]any)
		v["fingerprint"] = nil
		v["tools"] = map[string]any{"status": item.status, "pass": item.pass, "error": item.remoteError}
		firstPart(f)["key"] = "tools"
		firstPart(f)["points"] = 0
		firstPart(f)["max_points"] = 1
		firstPart(f)["contribution"] = 0
		total(f)["score"] = 0
		r := parseFixture(t, f)
		if r.ScoreComplete != item.complete || r.Verdict != "unknown" {
			t.Fatalf("status %s complete %v", item.status, r.ScoreComplete)
		}
	}
}

func TestOnlyWhitelistFieldsPersist(t *testing.T) {
	f := reportFixture()
	f["base_url"], f["host"], f["model"], f["api_key"] = "https://secret.example", "secret.example", "sk-secret-model", "sk-secret-key"
	f["poll_url"] = "https://secret.example/" + testID
	f["skipped"] = []any{map[string]any{"id": "thinking", "reason": "sk-secret-reason"}, map[string]any{"id": "sk-secret-key", "reason": "raw"}}
	f["probes"] = append(f["probes"].([]any), map[string]any{"id": "sk-secret-key", "name": "sk-secret-name", "error": "sk-secret-error", "status": "sk-secret-status"})
	fingerprint(f)["top_model"] = "sk-secret-top"
	fingerprint(f)["note"] = "sk-secret-note"
	fingerprint(f)["evidence"] = []any{map[string]any{"answer": "sk-secret-answer"}}
	r := parseFixture(t, f)
	body, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"secret", testID, "poll_url", "base_url", "api_key", "top_model", "evidence"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("unsafe report field %s", forbidden)
		}
	}
	if len(r.Skipped) != 1 || r.Probes[1].ID != "unknown" || r.Probes[1].Status != "error" {
		t.Fatal("unsafe strings were not normalized")
	}
}

func TestTerminalStatuses(t *testing.T) {
	for _, status := range []string{"done", "partial", "cancelled", "interrupted", "error", "failed"} {
		if !(&Result{Status: status}).Terminal() {
			t.Fatalf("not terminal: %s", status)
		}
	}
	for _, status := range []string{"", "queued", "running", "unknown", "future_status"} {
		if (&Result{Status: status}).Terminal() {
			t.Fatalf("unexpected terminal: %s", status)
		}
	}
	var r *Result
	if r.Terminal() {
		t.Fatal("nil is terminal")
	}
}

func TestDuplicateProbesCannotSupplyEvidence(t *testing.T) {
	f := reportFixture()
	f["probes"] = append(f["probes"].([]any), f["probes"].([]any)[0])
	r := parseFixture(t, f)
	if r.Verdict != "unknown" || r.ScoreComplete {
		t.Fatal("duplicate identity evidence accepted")
	}
}

func TestSelectedSectionCompleteness(t *testing.T) {
	// 所有计分板块都保留有效零分，缺少证据则禁止自动低分处置。
	for _, key := range []string{"injection", "cache", "tools", "web", "thinking", "iq"} {
		t.Run(key, func(t *testing.T) {
			f := reportFixture()
			v := f["verdict"].(map[string]any)
			v["fingerprint"] = nil
			max := 1
			evidence := map[string]any{"status": "fail", "pass": false}
			probes := []any{map[string]any{"id": key, "status": "done"}}
			switch key {
			case "injection":
				max = 10
				evidence = map[string]any{"score": 0, "reliable": true}
			case "thinking":
				max = 5
				evidence = map[string]any{"score": 0}
			case "iq":
				max = 100
				evidence = map[string]any{"score": 0, "complete": true}
				probes = nil
				for _, id := range []string{"Q1", "Q2", "Q3", "Q4", "Q5", "Q6", "Q7", "Q8"} {
					probes = append(probes, map[string]any{"id": id, "status": "done"})
				}
			}
			f["probes"], v[key] = probes, evidence
			firstPart(f)["key"], firstPart(f)["points"], firstPart(f)["max_points"], firstPart(f)["contribution"] = key, 0, max, 0
			total(f)["score"] = 0
			r := parseFixture(t, f)
			if r.Score == nil || *r.Score != 0 || !r.ScoreComplete || r.Verdict != "unknown" {
				t.Fatalf("valid zero score was lost for %s", key)
			}
			evidence["error"] = "request failed with sk-secret"
			if parseFixture(t, f).ScoreComplete {
				t.Fatalf("error is not valid score evidence for %s", key)
			}
		})
	}
}

func TestCompletedRemoteTaskWithQueuedIQExplainsPartialScore(t *testing.T) {
	// 对应真实 72 分样本：计分服务已结束，但八道推理题仍排队；不能当作模型答错。
	f := reportFixture()
	f["model"] = "claude-opus-5-5"
	fingerprint(f)["top_model"] = "claude-opus-5-5"
	v := f["verdict"].(map[string]any)
	v["iq"] = map[string]any{"score": nil, "complete": false}
	v["injection"] = map[string]any{"score": 10, "reliable": true}
	v["tools"] = map[string]any{"pass": true}
	v["web"] = map[string]any{"pass": true}
	v["thinking"] = map[string]any{"score": 5}
	v["cache"] = map[string]any{"pass": true}
	parts := []any{}
	for _, item := range []struct {
		key                         string
		points, max, weight, earned int
	}{
		{"fingerprint", 10, 10, 33, 33}, {"iq", 0, 100, 28, 0}, {"injection", 10, 10, 13, 13},
		{"tools", 1, 1, 9, 9}, {"web", 1, 1, 7, 7}, {"thinking", 5, 5, 6, 6}, {"cache", 1, 1, 4, 4},
	} {
		parts = append(parts, map[string]any{"key": item.key, "applicable": true, "known": true, "points": item.points, "max_points": item.max, "coefficient": item.weight, "contribution": item.earned})
		if item.key != "fingerprint" && item.key != "iq" {
			f["probes"] = append(f["probes"].([]any), map[string]any{"id": item.key, "status": "done"})
		}
	}
	total(f)["score"], total(f)["parts"] = 72, parts
	for _, id := range []string{"Q1", "Q2", "Q3", "Q4", "Q5", "Q6", "Q7", "Q8"} {
		f["probes"] = append(f["probes"].([]any), map[string]any{"id": id, "status": "queued", "ms": 0})
	}
	r := parseFixture(t, f)
	if r.Score == nil || *r.Score != 72 || r.ScoreComplete || r.Verdict != "clean" {
		t.Fatalf("partial score must preserve separate fingerprint verdict: score=%v complete=%v verdict=%s", r.Score, r.ScoreComplete, r.Verdict)
	}
	if !strings.Contains(r.Summary, "推理题未完成（8 道）") || !strings.Contains(r.Summary, "不用于自动低分处置") {
		t.Fatalf("missing actionable completion reason: %s", r.Summary)
	}
	for _, probe := range r.Probes {
		if probe.Status == "queued" && probe.Summary != "检测服务已结束任务，但此项尚未执行" {
			t.Fatalf("queued probe lacks terminal explanation: %+v", probe)
		}
	}
	// 远端总体或 IQ complete 标志不能盖过仍未执行的题目。
	v["iq"] = map[string]any{"score": 0, "complete": true}
	if parseFixture(t, f).ScoreComplete {
		t.Fatal("queued questions were treated as a completed zero score")
	}
}

func TestIncompleteSummaryUsesLocalNamesAndOnlyTerminalState(t *testing.T) {
	f := reportFixture()
	f["probes"].([]any)[0].(map[string]any)["status"] = "running"
	f["status"] = "running"
	r := parseFixture(t, f)
	if strings.Contains(r.Summary, "评分不完整") || r.Probes[0].Summary != "" {
		t.Fatal("active work was described as a terminal failure")
	}
	f["status"] = "done"
	r = parseFixture(t, f)
	if !strings.Contains(r.Summary, "模型指纹归因未完成") || r.Probes[0].Summary != "检测服务已结束任务，但此项尚未完成" {
		t.Fatal("unfinished terminal probe was not explained")
	}
	f["probes"].([]any)[0].(map[string]any)["status"] = "done"
	fingerprint(f)["error"] = "sk-secret <script>raw upstream error</script>"
	r = parseFixture(t, f)
	if !strings.Contains(r.Summary, "模型指纹归因证据不足") || strings.Contains(r.Summary, "secret") || strings.Contains(r.Summary, "<script>") {
		t.Fatal("incomplete evidence reason must be local and safe")
	}
}
