package service

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// CNQuotaTier 表示 Coding Plan 的滚动用量窗口。
type CNQuotaTier struct {
	Window      string  `json:"window"`
	UsedPercent float64 `json:"used_percent"`
	ResetAt     string  `json:"reset_at,omitempty"`
}

// parseOpenCodeGoUsageTiers 将 rolling/weekly/monthly 归一化为 5h/weekly/monthly。
// 缺失窗口可省略，已经返回的窗口若数值无效则整次查询失败，防止覆盖最近成功快照。
func parseOpenCodeGoUsageTiers(body []byte) []CNQuotaTier {
	if !gjson.ValidBytes(body) {
		return nil
	}
	usage := gjson.GetBytes(body, "usage")
	if !usage.IsObject() {
		return nil
	}
	var tiers []CNQuotaTier
	for _, item := range []struct{ key, window string }{
		{"rolling", "5h"}, {"weekly", "weekly"}, {"monthly", "monthly"},
	} {
		node := usage.Get(item.key)
		if !node.Exists() {
			continue
		}
		used, ok := cnParseF64(node.Get("percent").Value())
		if !node.IsObject() || !ok || !validNonNegativeNumber(used) {
			return nil
		}
		// 未给出恢复时间的窗口可以展示，但不能据此推断账号恢复时间。
		reset := cnNormalizeResetTime(node.Get("resetsAt").Value())
		tiers = append(tiers, CNQuotaTier{Window: item.window, UsedPercent: used, ResetAt: reset})
	}
	return tiers
}

// parseKimiUsageTiers 解析 Kimi For Coding 的 /usages 响应。
//
// Kimi 目前并存两种套餐形态：
//
//   - 老套餐（5h + 7d）：limits[].detail.{limit,remaining,resetTime} → 5h
//     （取首个 detail），顶层 usage.{limit,remaining,resetTime} → 周窗口，
//     utilization = (limit-remaining)/limit*100。
//   - 新套餐（5h + 月度）：不再有顶层 usage，改由 usages.limit_5h /
//     usages.limit_7d / usages.limit_month_total / usages.limit_month_code
//     以 used_ratio（0-1）+ reset_time 上报。月度 total 与 code 任一耗尽都会
//     限流，因此 monthly 取两者较大用量、较晚重置时间（见函数内说明）。
//
// 两者同时出现时 used_ratio 优先（比 counts 更贴近官方口径），顶层 usage 仅兜底；
// usages.limit_5h 只在 limits[] 没有 detail 时补位，避免重复产出 5h 档。
func parseKimiUsageTiers(body []byte) []CNQuotaTier {
	var tiers []CNQuotaTier

	hasFiveHour := false
	if limits := gjson.GetBytes(body, "limits"); limits.IsArray() {
		limits.ForEach(func(_, item gjson.Result) bool {
			detail := item.Get("detail")
			if !detail.Exists() {
				return true
			}
			// 字段节点存在不等于数值可信：limit/remaining 必须可解析、limit>0、
			// remaining 落在 [0, limit]，否则跳过该条（`{"detail":{}}` 或 limit=0
			// 不能产出一个「已用 0%」的伪窗口，那会掩盖真实的额度耗尽状态）。
			limit, okLimit := cnParseF64(detail.Get("limit").Value())
			remaining, okRemaining := cnParseF64(detail.Get("remaining").Value())
			if !okLimit || !okRemaining || limit <= 0 || remaining < 0 || remaining > limit {
				return true
			}
			util := (limit - remaining) / limit * 100
			tiers = append(tiers, CNQuotaTier{
				Window:      "5h",
				UsedPercent: util,
				ResetAt:     cnNormalizeResetTime(detail.Get("resetTime").Value()),
			})
			hasFiveHour = true
			return false // 取首个有效 detail 作为 5h 窗口
		})
	}
	if !hasFiveHour {
		if tier, ok := kimiUsedRatioTier(body, "limit_5h", "5h"); ok {
			tiers = append(tiers, tier)
			hasFiveHour = true
		}
	}

	// 周窗口：usages.limit_7d 优先，顶层 usage（老套餐单数）兜底。
	if tier, ok := kimiUsedRatioTier(body, "limit_7d", "weekly"); ok {
		tiers = append(tiers, tier)
	} else if usage := gjson.GetBytes(body, "usage"); usage.Exists() {
		limit, okLimit := cnParseF64(usage.Get("limit").Value())
		remaining, okRemaining := cnParseF64(usage.Get("remaining").Value())
		if okLimit && okRemaining && limit > 0 && remaining >= 0 && remaining <= limit {
			tiers = append(tiers, CNQuotaTier{
				Window:      "weekly",
				UsedPercent: (limit - remaining) / limit * 100,
				ResetAt:     cnNormalizeResetTime(usage.Get("resetTime").Value()),
			})
		}
	}

	// 月度限额（新套餐）：total 与 code 是两个**独立**的限流窗口，任一耗尽都会限流。
	// 用量取两者较大值；重置时间取**较晚**者——取较早会在另一个窗口仍耗尽时提前
	// 解除预停调（例如 total 到 11-26 才恢复、code 到 11-20 恢复）。宁可晚回池，
	// 不可提前撞 429。
	total, hasTotal := kimiUsedRatioTier(body, "limit_month_total", "monthly")
	code, hasCode := kimiUsedRatioTier(body, "limit_month_code", "monthly")
	if hasTotal || hasCode {
		monthly := total
		if !hasTotal {
			monthly = code
		} else if hasCode {
			if code.UsedPercent > monthly.UsedPercent {
				monthly.UsedPercent = code.UsedPercent
			}
			if code.ResetAt != "" && (monthly.ResetAt == "" || code.ResetAt > monthly.ResetAt) {
				monthly.ResetAt = code.ResetAt
			}
		}
		tiers = append(tiers, monthly)
	}

	return tiers
}

// kimiUsedRatioTier 解析 usages.<key>.{used_ratio,reset_time} 为窗口档位。
// used_ratio 是 0-1 比例，输出换算为百分比；键缺失、不可解析或越界返回 false。
func kimiUsedRatioTier(body []byte, key, window string) (CNQuotaTier, bool) {
	node := gjson.GetBytes(body, "usages."+key)
	if !node.Exists() {
		return CNQuotaTier{}, false
	}
	ratioNode := node.Get("used_ratio")
	if !ratioNode.Exists() {
		return CNQuotaTier{}, false
	}
	ratio, ok := cnParseF64(ratioNode.Value())
	if !ok {
		return CNQuotaTier{}, false
	}
	// 必须落在上游声明区间 [0,1]：越界值（例如把百分数 65 直接塞进 used_ratio）
	// 会算出 6500%，NaN/Inf 同理不可信。
	if ratio < 0 || ratio > 1 {
		return CNQuotaTier{}, false
	}
	return CNQuotaTier{
		Window:      window,
		UsedPercent: ratio * 100,
		ResetAt:     cnNormalizeResetTime(node.Get("reset_time").Value()),
	}, true
}

// parseMiniMaxUsageTiers 只读取 general 编程套餐，把剩余百分比转为已用百分比。
// 周窗口仅在 current_weekly_status=1 时存在；异常数值使整份响应失效，不能伪装成额度恢复。
func parseMiniMaxUsageTiers(body []byte) []CNQuotaTier {
	remains := gjson.GetBytes(body, "model_remains")
	if !remains.IsArray() {
		return nil
	}
	var general gjson.Result
	remains.ForEach(func(_, item gjson.Result) bool {
		if strings.EqualFold(strings.TrimSpace(item.Get("model_name").String()), "general") {
			general = item
			return false
		}
		return true
	})
	if !general.Exists() {
		return nil
	}
	parseTier := func(window, remainingField, resetField string) (CNQuotaTier, bool) {
		remaining, ok := cnParseF64(general.Get(remainingField).Value())
		if !ok || !validFiniteNumber(remaining) || remaining < 0 || remaining > 100 {
			return CNQuotaTier{}, false
		}
		return CNQuotaTier{
			Window:      window,
			UsedPercent: 100 - remaining,
			ResetAt:     cnNormalizeResetTime(general.Get(resetField).Value()),
		}, true
	}
	fiveHour, ok := parseTier("5h", "current_interval_remaining_percent", "end_time")
	if !ok {
		return nil
	}
	tiers := []CNQuotaTier{fiveHour}
	if general.Get("current_weekly_status").Int() == 1 {
		weekly, ok := parseTier("weekly", "current_weekly_remaining_percent", "weekly_end_time")
		if !ok {
			return nil
		}
		tiers = append(tiers, weekly)
	}
	return tiers
}

// cnZhipuWindow 标识智谱 TOKENS_LIMIT / CREDIT_LIMIT 条目所属窗口。
type cnZhipuWindow int

const (
	cnZhipuWindowUnknown cnZhipuWindow = iota
	cnZhipuWindow5h
	cnZhipuWindowWeekly
)

// classifyZhipuWindowUnit 按 unit 字段判定窗口类型（3=5h，6=weekly）。
// unit 缺失或未识别时返回 Unknown，由调用方走 reset 时间启发式兜底。
func classifyZhipuWindowUnit(unit int64) cnZhipuWindow {
	switch unit {
	case 3:
		return cnZhipuWindow5h
	case 6:
		return cnZhipuWindowWeekly
	default:
		return cnZhipuWindowUnknown
	}
}

// parseZhipuTokenTiers 解析智谱额度响应 data.limits 为 5h + weekly 两档。
//
// 分类优先级（对齐 cc-switch parse_zhipu_token_tiers，issue #3036）：
//  1. 显式 unit 字段（3=5h / 6=weekly）——不能用 reset 排序代替，周期末尾
//     周窗口会比 5h 更早重置，时间排序必然标反。
//  2. unit 缺失/未识别：无 nextResetTime 的条目优先归 5h（0% 状态下 5h 桶可能
//     没有 reset），其余按 reset 升序依次填入仍空缺的槽位。
//
// CREDIT_LIMIT（信用额度）与 TOKENS_LIMIT（token 窗口）度量不同：两者同时返回时
// 只让 TOKENS_LIMIT 参与 5h/weekly 槽位竞争，避免信用额度百分比污染阈值停调
// 快照；仅当无任何 TOKENS_LIMIT 条目时才降级用 CREDIT_LIMIT 展示。
// 老套餐只回 1 条 TOKENS_LIMIT，自然降级为仅 5h；新套餐回 2 条。
func parseZhipuTokenTiers(data gjson.Result) []CNQuotaTier {
	type entry struct {
		resetMs    int64
		hasReset   bool
		percentage float64
		resetISO   string
		unit       int64
	}
	var (
		fiveHour     entry
		fiveHourSet  bool
		weekly       entry
		weeklySet    bool
		unclassified []entry
	)

	classify := func(e entry) {
		switch classifyZhipuWindowUnit(e.unit) {
		case cnZhipuWindow5h:
			if !fiveHourSet {
				fiveHour, fiveHourSet = e, true
			} else {
				unclassified = append(unclassified, e)
			}
		case cnZhipuWindowWeekly:
			if !weeklySet {
				weekly, weeklySet = e, true
			} else {
				unclassified = append(unclassified, e)
			}
		default:
			unclassified = append(unclassified, e)
		}
	}
	var creditFallback []entry
	hasTokensLimit := false

	data.Get("limits").ForEach(func(_, item gjson.Result) bool {
		limitType := strings.ToUpper(strings.TrimSpace(item.Get("type").String()))
		if limitType != "TOKENS_LIMIT" && limitType != "CREDIT_LIMIT" {
			return true
		}
		percentage := 0.0
		if p, ok := cnParseF64(item.Get("percentage").Value()); ok {
			percentage = p
		}
		var (
			resetMs  int64
			hasReset bool
			resetISO string
		)
		if nr := item.Get("nextResetTime"); nr.Exists() {
			switch nr.Type {
			case gjson.Number:
				resetMs = nr.Int()
				hasReset = resetMs > 0
				resetISO = cnMillisToRFC3339(resetMs)
			case gjson.String:
				resetISO = cnNormalizeResetTime(nr.String())
				hasReset = resetISO != ""
			}
		}
		e := entry{resetMs: resetMs, hasReset: hasReset, percentage: percentage, resetISO: resetISO, unit: item.Get("unit").Int()}
		if limitType == "TOKENS_LIMIT" {
			hasTokensLimit = true
			classify(e)
		} else {
			creditFallback = append(creditFallback, e)
		}
		return true
	})

	// 无任何 TOKENS_LIMIT 条目（部分套餐只报信用额度）：降级用 CREDIT_LIMIT 展示。
	// 信用窗口也必须优先按 unit 判定；周末周额度可比五小时额度更早重置。
	if !hasTokensLimit {
		for _, e := range creditFallback {
			classify(e)
		}
	}

	// 无 reset 的条目排前，再按 reset 升序，依次填入仍空缺的槽位。
	sort.SliceStable(unclassified, func(i, j int) bool {
		if unclassified[i].hasReset != unclassified[j].hasReset {
			return !unclassified[i].hasReset
		}
		return unclassified[i].resetMs < unclassified[j].resetMs
	})
	for _, e := range unclassified {
		switch {
		case !fiveHourSet:
			fiveHour, fiveHourSet = e, true
		case !weeklySet:
			weekly, weeklySet = e, true
		}
	}

	var tiers []CNQuotaTier
	if fiveHourSet {
		tiers = append(tiers, CNQuotaTier{Window: "5h", UsedPercent: fiveHour.percentage, ResetAt: fiveHour.resetISO})
	}
	if weeklySet {
		tiers = append(tiers, CNQuotaTier{Window: "weekly", UsedPercent: weekly.percentage, ResetAt: weekly.resetISO})
	}
	return tiers
}

// cnParseF64 把 JSON 数值或字符串解析为 float64（兼容 "100" 与 100）。
func cnParseF64(raw any) (float64, bool) {
	var value float64
	switch v := raw.(type) {
	case float64:
		value = v
	case float32:
		value = float64(v)
	case int:
		value = float64(v)
	case int64:
		value = float64(v)
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return 0, false
		}
		value = f
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0, false
		}
		value = f
	default:
		return 0, false
	}
	// strconv.ParseFloat 接受 "NaN"/"Inf"，JSON 的 1e999 也会得到 +Inf；
	// 这类值参与百分比与阈值比较会让停调判定失效，一律视为不可解析。
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return value, true
}

// cnNormalizeResetTime 把上游重置时间（ISO8601 字符串 / 秒级 / 毫秒级数字）归一化为
// RFC3339 字符串；无法识别或非正时间戳返回空串。
func cnNormalizeResetTime(raw any) string {
	switch v := raw.(type) {
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return ""
		}
		if ts, err := parseSchedulingTime(s); err == nil {
			return ts.UTC().Format(time.RFC3339)
		}
		return ""
	case float64:
		return cnMillisToRFC3339(int64(v))
	case int:
		return cnMillisToRFC3339(int64(v))
	case int64:
		return cnMillisToRFC3339(v)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return cnMillisToRFC3339(n)
		}
		return ""
	default:
		return ""
	}
}

// cnMillisToRFC3339 把秒级（<1e12）或毫秒级时间戳转为 RFC3339 字符串；非正返回空串。
func cnMillisToRFC3339(n int64) string {
	if n <= 0 {
		return ""
	}
	var ms int64
	if n < 1_000_000_000_000 {
		ms = n * 1000
	} else {
		ms = n
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}
