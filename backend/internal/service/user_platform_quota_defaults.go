package service

import (
	"context"

	dbent "github.com/TokenFlux/TokenRouter/ent"
)

// 新用户的限额从创建时的配置复制，保留明确配置的零额度。
func snapshotUserPlatformQuotaDefaults(ctx context.Context, repo UserPlatformQuotaRepository, userID int64, quotas map[string]*DefaultPlatformQuotaSetting) error {
	if repo == nil || userID <= 0 || len(quotas) == 0 {
		return nil
	}
	// 三个窗口均未配置时不写入记录。
	records := make([]UserPlatformQuotaRecord, 0, len(quotas))
	for platform, quota := range quotas {
		if quota == nil || (quota.DailyLimitUSD == nil && quota.WeeklyLimitUSD == nil && quota.MonthlyLimitUSD == nil) {
			continue
		}
		records = append(records, UserPlatformQuotaRecord{
			UserID:          userID,
			Platform:        platform,
			DailyLimitUSD:   quota.DailyLimitUSD,
			WeeklyLimitUSD:  quota.WeeklyLimitUSD,
			MonthlyLimitUSD: quota.MonthlyLimitUSD,
		})
	}
	if len(records) == 0 {
		return nil
	}
	// 隔离可选快照写入，避免失败中断创建用户的事务。
	return repo.BulkInsertInitial(dbent.WithoutTx(ctx), records)
}
