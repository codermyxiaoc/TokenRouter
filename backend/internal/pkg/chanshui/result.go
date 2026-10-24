package chanshui

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
)

var sectionNames = map[string]string{
	"fingerprint": "模型指纹归因", "injection": "注入大小", "cache": "缓存读写",
	"tools": "工具调用", "web": "联网检测", "knowledge": "知识截止日期", "thinking": "思维链回传", "iq": "推理表现",
}

// Result 仅包含白名单结构与本地摘要；ID 是查询凭证，不随报告序列化。
type Result struct {
	ID            string       `json:"-"`
	Status        string       `json:"status"`
	ExpiresAt     int64        `json:"expires_at,omitempty"`
	Sections      []string     `json:"sections,omitempty"`
	Skipped       []Skipped    `json:"skipped,omitempty"`
	Score         *float64     `json:"score"`
	ScoreComplete bool         `json:"score_complete"`
	Verdict       string       `json:"verdict"`
	Summary       string       `json:"summary"`
	Probes        []Probe      `json:"probes,omitempty"`
	Fingerprint   *Fingerprint `json:"fingerprint,omitempty"`
	Parts         []ScorePart  `json:"parts,omitempty"`
}

func (Result) String() string     { return "chanshui.Result{task_and_content:redacted}" }
func (r Result) GoString() string { return r.String() }

// Terminal 未知状态不冒充成功，业务层仍需独立观察期限。
func (r *Result) Terminal() bool {
	return r != nil && (r.Status == "done" || r.Status == "partial" || r.Status == "cancelled" || r.Status == "interrupted" || r.Status == "error" || r.Status == "failed")
}

type Skipped struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// Probe 的名称与错误摘要均由本地生成，不保留供应商可能回显的凭据。
type Probe struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	MS      int64  `json:"ms"`
	Summary string `json:"summary,omitempty"`
}

// Fingerprint 只暴露家族与判断证据，不保存远端任意模型字符串。
type Fingerprint struct {
	RequestedFamily string `json:"requested_family"`
	Family          string `json:"family"`
	Samples         int    `json:"samples"`
	LowConfidence   bool   `json:"low_confidence"`
	Matches         *bool  `json:"matches"`
}

// ScorePart 保留原接口的分数组成；未知项不能参与自动低分处置。
type ScorePart struct {
	Key          string  `json:"key"`
	Name         string  `json:"name"`
	Applicable   bool    `json:"applicable"`
	Known        bool    `json:"known"`
	Points       float64 `json:"points"`
	MaxPoints    float64 `json:"max_points"`
	Coefficient  float64 `json:"coefficient"`
	Contribution float64 `json:"contribution"`
}

type wireResult struct {
	ID        string   `json:"id"`
	Status    string   `json:"status"`
	Model     string   `json:"model"`
	ExpiresAt int64    `json:"expires_at"`
	Sections  []string `json:"sections"`
	Skipped   []struct {
		ID string `json:"id"`
	} `json:"skipped"`
	Probes []struct {
		ID     string          `json:"id"`
		Status string          `json:"status"`
		MS     int64           `json:"ms"`
		Error  json.RawMessage `json:"error"`
	} `json:"probes"`
	Verdict struct {
		Total *struct {
			Score *float64   `json:"score"`
			Max   *float64   `json:"max"`
			Parts []wirePart `json:"parts"`
		} `json:"total"`
		Fingerprint *struct {
			Basis         string          `json:"basis"`
			Family        string          `json:"family"`
			TopModel      string          `json:"top_model"`
			UsedOutputs   int             `json:"used_outputs"`
			LowConfidence *bool           `json:"low_confidence"`
			Error         json.RawMessage `json:"error"`
		} `json:"fingerprint"`
		Injection *wireEvidence `json:"injection"`
		Tools     *wireEvidence `json:"tools"`
		Web       *wireEvidence `json:"web"`
		Cache     *wireEvidence `json:"cache"`
		Thinking  *wireEvidence `json:"thinking"`
		IQ        *wireEvidence `json:"iq"`
	} `json:"verdict"`
}

type wireEvidence struct {
	Status   string          `json:"status"`
	Error    json.RawMessage `json:"error"`
	Pass     *bool           `json:"pass"`
	Score    *float64        `json:"score"`
	Reliable *bool           `json:"reliable"`
	Complete *bool           `json:"complete"`
}

type wirePart struct {
	Key          string   `json:"key"`
	Applicable   *bool    `json:"applicable"`
	Known        *bool    `json:"known"`
	Points       *float64 `json:"points"`
	MaxPoints    *float64 `json:"max_points"`
	Coefficient  *float64 `json:"coefficient"`
	Contribution *float64 `json:"contribution"`
}

func normalizeResult(raw wireResult) *Result {
	r := &Result{ID: raw.ID, Status: safeStatus(raw.Status), Verdict: "unknown", Summary: "尚无足够证据判断模型是否掺水"}
	if raw.ExpiresAt > 0 {
		r.ExpiresAt = raw.ExpiresAt
	}
	sections := map[string]bool{}
	for _, id := range raw.Sections {
		if ValidSection(id) && !sections[id] {
			r.Sections = append(r.Sections, id)
			sections[id] = true
		}
	}
	skipped := map[string]bool{}
	for _, item := range raw.Skipped {
		if ValidSection(item.ID) && !skipped[item.ID] {
			r.Skipped = append(r.Skipped, Skipped{ID: item.ID, Reason: "检测服务跳过此板块，未参与判断"})
			skipped[item.ID] = true
		}
	}
	probesDone := map[string]bool{}
	probeSeen := map[string]bool{}
	allProbesDone := len(raw.Probes) > 0 && len(raw.Probes) <= 64
	for i, item := range raw.Probes {
		if i >= 64 {
			break
		}
		id, name := safeProbe(item.ID)
		status := safeStatus(item.Status)
		if hasError(item.Error) {
			status = "error"
		}
		if status != "done" || id == "unknown" || probeSeen[id] {
			allProbesDone = false
		}
		if probeSeen[id] {
			probesDone[id] = false
		} else {
			probesDone[id] = status == "done"
		}
		probeSeen[id] = true
		ms := item.MS
		if ms < 0 || ms > 24*60*60*1000 {
			ms = 0
		}
		probe := Probe{ID: id, Name: name, Status: status, MS: ms}
		if status == "error" {
			probe.Summary = "检测请求失败，不能据此判为掺水"
		} else if r.Terminal() && status == "queued" {
			probe.Summary = "检测服务已结束任务，但此项尚未执行"
		} else if r.Terminal() && status == "running" {
			probe.Summary = "检测服务已结束任务，但此项尚未完成"
		}
		r.Probes = append(r.Probes, probe)
	}
	if f := raw.Verdict.Fingerprint; f != nil {
		family, want := fingerprintFamily(f.Family), modelFamily(raw.Model)
		samples := f.UsedOutputs
		if samples < 0 || samples > 1000 {
			samples = 0
		}
		r.Fingerprint = &Fingerprint{RequestedFamily: want, Family: family, Samples: samples, LowConfidence: f.LowConfidence == nil || *f.LowConfidence}
		// 官方没有明确布尔结论；沿其指纹比较语义，但缺字段、缺基线和低置信度一律未知。
		if r.Terminal() && (r.Status == "done" || r.Status == "partial") && probesDone["fingerprint"] && !hasError(f.Error) &&
			(f.Basis == "numeric" || f.Basis == "behavioral") && samples >= 3 && f.LowConfidence != nil && !*f.LowConfidence &&
			family != "" && want != "" && modelFamily(f.TopModel) == family {
			matches := sameModel(raw.Model, f.TopModel)
			r.Fingerprint.Matches = &matches
			if matches {
				r.Verdict, r.Summary = "clean", "指纹归因与所测模型一致"
			} else {
				r.Verdict, r.Summary = "adulterated", "指纹归因与所测模型不一致，检测提示掺水"
			}
		}
	}
	if total := raw.Verdict.Total; total != nil {
		if total.Max != nil && *total.Max == 100 && inRange(total.Score, 0, 100) {
			r.Score = total.Score
		}
		complete := r.Status == "done" && r.Score != nil && len(total.Parts) > 0 && len(total.Parts) <= 8 && allProbesDone
		seen := map[string]bool{}
		applicable := 0
		for i, item := range total.Parts {
			if i >= 8 {
				break
			}
			if !ValidSection(item.Key) || item.Key == "knowledge" || seen[item.Key] {
				complete = false
				continue
			}
			seen[item.Key] = true
			part := ScorePart{Key: item.Key, Name: sectionNames[item.Key]}
			part.Applicable = item.Applicable != nil && *item.Applicable
			part.Known = item.Known != nil && *item.Known
			if item.Applicable == nil || item.Known == nil || !inRange(item.MaxPoints, 1, 100) || !inRange(item.Points, 0, value(item.MaxPoints)) ||
				!inRange(item.Coefficient, 0, 100) || !inRange(item.Contribution, 0, value(item.Coefficient)) {
				part.Known, complete = false, false
			} else {
				part.Points, part.MaxPoints = *item.Points, *item.MaxPoints
				part.Coefficient, part.Contribution = *item.Coefficient, *item.Contribution
			}
			if part.Applicable {
				applicable++
				if !part.Known || !evidenceComplete(raw, item.Key, r, probesDone) {
					complete = false
				}
			}
			r.Parts = append(r.Parts, part)
		}
		// 官方网页会扣掉掺水指纹贡献，避免与原始总分显示不一致。
		if r.Verdict == "adulterated" && r.Score != nil {
			for i := range r.Parts {
				if r.Parts[i].Key == "fingerprint" && r.Parts[i].Contribution > 0 {
					score := math.Max(0, *r.Score-r.Parts[i].Contribution)
					r.Score = &score
					r.Parts[i].Contribution = 0
				}
			}
		}
		r.ScoreComplete = complete && applicable > 0
		if r.Terminal() && !r.ScoreComplete {
			r.Summary += "；评分不完整：" + incompleteReasons(raw, r, probesDone) + "，该总分不用于自动低分处置"
		}
	}
	return r
}

// incompleteReasons 仅根据白名单探针与证据生成原因，不回显远端错误或模型回答。
func incompleteReasons(raw wireResult, result *Result, probesDone map[string]bool) string {
	reasons := make([]string, 0, 8)
	seen := make(map[string]bool)
	iqPending := 0
	for _, probe := range result.Probes {
		if probe.Status == "done" {
			continue
		}
		if len(probe.ID) == 2 && probe.ID[0] == 'Q' {
			iqPending++
			continue
		}
		if !seen[probe.ID] {
			reasons = append(reasons, probe.Name+"未完成")
			seen[probe.ID] = true
		}
	}
	if iqPending > 0 {
		reasons = append(reasons, fmt.Sprintf("推理题未完成（%d 道）", iqPending))
		seen["iq"] = true
	}
	for _, part := range result.Parts {
		if part.Applicable && !seen[part.Key] && (!part.Known || !evidenceComplete(raw, part.Key, result, probesDone)) {
			reasons = append(reasons, part.Name+"证据不足")
			seen[part.Key] = true
		}
	}
	if len(reasons) == 0 {
		return "总分或分项数据不完整"
	}
	return strings.Join(reasons, "、")
}

// evidenceComplete 不把官方总分中按零计入的错误、未知或不支持项当作有效低分。
func evidenceComplete(raw wireResult, key string, result *Result, probesDone map[string]bool) bool {
	if key == "fingerprint" {
		return result.Verdict != "unknown"
	}
	if key == "iq" {
		for _, id := range []string{"Q1", "Q2", "Q3", "Q4", "Q5", "Q6", "Q7", "Q8"} {
			if !probesDone[id] {
				return false
			}
		}
		e := raw.Verdict.IQ
		return e != nil && !hasError(e.Error) && e.Complete != nil && *e.Complete && inRange(e.Score, 0, 100)
	}
	if !probesDone[key] {
		return false
	}
	e := map[string]*wireEvidence{"injection": raw.Verdict.Injection, "cache": raw.Verdict.Cache, "tools": raw.Verdict.Tools, "web": raw.Verdict.Web, "thinking": raw.Verdict.Thinking}[key]
	if e == nil || hasError(e.Error) {
		return false
	}
	if e.Status != "" && e.Status != "pass" && e.Status != "fail" && e.Status != "passed" && e.Status != "failed" && e.Status != "supported" && e.Status != "done" {
		return false
	}
	switch key {
	case "injection":
		return e.Reliable != nil && *e.Reliable && inRange(e.Score, 0, 10)
	case "thinking":
		return inRange(e.Score, 0, 5)
	default:
		return e.Pass != nil
	}
}

func safeStatus(status string) string {
	switch status {
	case "queued", "running", "done", "partial", "cancelled", "interrupted", "error", "failed":
		return status
	default:
		return "unknown"
	}
}

func safeProbe(id string) (string, string) {
	if name, ok := sectionNames[id]; ok {
		return id, name
	}
	if len(id) == 2 && id[0] == 'Q' && id[1] >= '1' && id[1] <= '8' {
		return id, "推理题 " + string(id[1])
	}
	return "unknown", "未知检测项"
}

func hasError(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "null" && string(raw) != `""` && string(raw) != "false"
}

func inRange(v *float64, min, max float64) bool {
	return v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0) && *v >= min && *v <= max
}
func value(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

var oFamily = regexp.MustCompile(`^o[134](?:[.-]|$)`)

func unqualifiedModel(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	for _, prefix := range []string{"openai/", "anthropic/", "moonshotai/"} {
		m = strings.TrimPrefix(m, prefix)
	}
	return m
}

func modelFamily(model string) string {
	m := unqualifiedModel(model)
	for _, family := range []string{"claude", "gpt", "gemini", "deepseek", "grok", "kimi", "glm"} {
		if strings.HasPrefix(m, family) {
			return family
		}
	}
	if oFamily.MatchString(m) || strings.Contains(m, "chatgpt") {
		return "gpt"
	}
	return ""
}

func fingerprintFamily(family string) string {
	f := strings.ToLower(strings.TrimSpace(family))
	switch f {
	case "openai", "chatgpt":
		return "gpt"
	case "anthropic":
		return "claude"
	case "google":
		return "gemini"
	case "xai", "x-ai":
		return "grok"
	case "zhipu", "z-ai":
		return "glm"
	case "claude", "gpt", "gemini", "deepseek", "grok", "kimi", "glm":
		return f
	default:
		return ""
	}
}

func canonModel(model string) string {
	m := unqualifiedModel(model)
	if m == "deepseek-flash" {
		m = "deepseek-v4.1-flash-1017"
	}
	var b strings.Builder
	for _, r := range m {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func sameModel(requested, actual string) bool {
	want, got := canonModel(requested), canonModel(actual)
	return want != "" && (want == got || want == "deepseekv41flash" && strings.HasPrefix(got, "deepseekv41flash"))
}
