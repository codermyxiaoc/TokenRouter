package service

// Ollama Cloud 用量窗口恢复：复用现有抓取流程，异步探测只延长已确认的账号限流。

import (
	"math"
	"time"
)

// 仅接受成功且新鲜的快照；全部耗尽窗口都有未来重置点时取最晚值，不执行额度清零。
func ollamaCloudUsageExhaustionResetAt(
	snapshot *OllamaCloudUsageSnapshot,
	now time.Time,
	minFetchedAt time.Time,
) (reset time.Time, ok bool) {
	if snapshot == nil || snapshot.Status != OllamaCloudUsageStatusOK || snapshot.Data == nil {
		return time.Time{}, false
	}
	if snapshot.FetchedAt == nil || snapshot.FetchedAt.IsZero() || snapshot.FetchedAt.Before(minFetchedAt) || snapshot.FetchedAt.After(now) {
		return time.Time{}, false
	}

	var latest time.Time
	for _, window := range []*OllamaCloudUsageWindow{
		snapshot.Data.FiveHour,
		snapshot.Data.SevenDay,
	} {
		if window != nil && (math.IsNaN(window.UsedPercent) || math.IsInf(window.UsedPercent, 0)) {
			return time.Time{}, false
		}
		if window == nil || window.UsedPercent < 100 {
			continue
		}
		if window.ResetAt == nil || window.ResetAt.IsZero() || !window.ResetAt.After(now) {
			return time.Time{}, false
		}
		candidate := window.ResetAt.UTC()
		if latest.IsZero() || candidate.After(latest) {
			latest = candidate
		}
	}
	if latest.IsZero() {
		return time.Time{}, false
	}
	return latest, true
}
