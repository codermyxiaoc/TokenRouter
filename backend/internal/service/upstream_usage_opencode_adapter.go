package service

import (
	"context"
	"time"

	"github.com/tidwall/gjson"
)

// OpenCode GO 只查询订阅窗口，不把已用百分比解释为钱包或本地账本余额。
type openCodeGoUsageAdapter struct{}

func (*openCodeGoUsageAdapter) Name() string { return UpstreamUsageAdapterOpenCodeGo }

func (*openCodeGoUsageAdapter) Query(ctx context.Context, client *upstreamUsageHTTPClient) (*UpstreamUsageInfo, error) {
	if client == nil || client.account == nil || !client.account.IsOpenCodeGoPlan() {
		return nil, ErrUpstreamUsageUnsupported
	}
	// /usage 紧接账号的协议根路径；保留中继主机和路径，禁止改发官方站点。
	body, status, err := client.getURL(ctx, openCodeGoQuotaURL(client.baseURL), true)
	if err != nil {
		return nil, err
	}
	if err := validateCNUsageStatus(status); err != nil {
		return nil, err
	}
	if failure := gjson.GetBytes(body, "error"); failure.Exists() && failure.Type != gjson.Null {
		return nil, ErrUpstreamUsageInvalidResponse
	}
	return cnUsageLimits(PlatformOpenCodeGo, parseOpenCodeGoUsageTiers(body))
}

// 三窗口只消费身份匹配且带合法观测时间的统一快照，Zen 与其它平台不能借用 GO 额度。
func openCodeGoThresholdCandidates(account *Account, now time.Time) []*accountSchedulingThresholdCandidate {
	if account == nil || account.Type != AccountTypeAPIKey || !account.IsOpenCodeGoPlan() {
		return nil
	}
	snapshot := validCNUsageMonitorSnapshot(account)
	if snapshot == nil || snapshot.Provider != PlatformOpenCodeGo || snapshot.Mode != "limits" ||
		snapshot.Unit != "PERCENT" || snapshot.ObservedAt == nil || snapshot.ObservedAt.IsZero() || snapshot.ObservedAt.After(now) {
		return nil
	}
	candidates := make([]*accountSchedulingThresholdCandidate, 0, len(snapshot.Limits))
	for _, limit := range snapshot.Limits {
		if limit.Name != "5h" && limit.Name != "weekly" && limit.Name != "monthly" {
			continue
		}
		if limit.Used == nil || !validNonNegativeNumber(*limit.Used) || limit.ResetAt == nil || !limit.ResetAt.After(now) {
			continue
		}
		candidates = append(candidates, &accountSchedulingThresholdCandidate{
			window: limit.Name, scope: PlatformOpenCodeGo, usedPercent: *limit.Used, until: cloneTimePtr(limit.ResetAt),
		})
	}
	return candidates
}
