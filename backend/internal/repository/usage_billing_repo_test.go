package repository

import (
	"database/sql"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

// 事务计费与请求准入共用时间规则，不再阻止到期前不足完整周期的尾段刷新。
func TestNormalizeUsageBillingSubscriptionRow_UpstreamWindowRules(t *testing.T) {
	base := time.Date(2026, 9, 2, 10, 11, 0, 0, timezone.Location())
	limit := sql.NullFloat64{Float64: 70, Valid: true}
	for _, tt := range []struct {
		name, kind                           string
		startsAt, expiresAt, now, wantAnchor time.Time
		anchor                               *time.Time
		wantUsed                             float64
	}{
		{name: "周卡到期不足第二完整周仍刷新", kind: "weekly", startsAt: base,
			anchor:     usageBillingTestTime(time.Date(2026, 9, 23, 0, 0, 0, 0, timezone.Location())),
			expiresAt:  time.Date(2026, 10, 5, 10, 18, 0, 0, timezone.Location()),
			now:        time.Date(2026, 9, 30, 10, 0, 0, 0, timezone.Location()),
			wantAnchor: time.Date(2026, 9, 30, 0, 0, 0, 0, timezone.Location())},
		{name: "月卡最后一天也可进入新周期", kind: "monthly", startsAt: base, anchor: &base,
			expiresAt: base.Add(31 * 24 * time.Hour), now: base.Add(30*24*time.Hour + time.Minute), wantAnchor: base.Add(30 * 24 * time.Hour)},
		{name: "恰好到期不再发放", kind: "monthly", startsAt: base, anchor: &base,
			expiresAt: base.Add(30 * 24 * time.Hour), now: base.Add(30 * 24 * time.Hour), wantAnchor: base, wantUsed: 70},
		{name: "旧开通日午夜锚点不能提前刷新", kind: "monthly", startsAt: base, anchor: usageBillingTestTime(timezone.StartOfDay(base)),
			expiresAt: base.Add(30 * 24 * time.Hour), now: timezone.StartOfDay(base).Add(30*24*time.Hour + time.Minute), wantAnchor: timezone.StartOfDay(base), wantUsed: 70},
		{name: "周窗口跨多周期不漂移", kind: "weekly", startsAt: base.Add(-24 * time.Hour), anchor: &base,
			expiresAt: base.Add(100 * 24 * time.Hour), now: base.Add(36 * 24 * time.Hour), wantAnchor: base.Add(35 * 24 * time.Hour)},
		{name: "月窗口跨多周期不漂移", kind: "monthly", startsAt: base.Add(-24 * time.Hour), anchor: &base,
			expiresAt: base.Add(100 * 24 * time.Hour), now: base.Add(65 * 24 * time.Hour), wantAnchor: base.Add(60 * 24 * time.Hour)},
		{name: "延迟首次激活保留历史已用额度", kind: "weekly", startsAt: base,
			expiresAt: base.Add(10 * 24 * time.Hour), now: base.Add(9 * 24 * time.Hour), wantAnchor: base.Add(9 * 24 * time.Hour), wantUsed: 70},
		{name: "多日卡尾段按午夜刷新", kind: "daily", startsAt: base, anchor: usageBillingTestTime(timezone.StartOfDay(base).AddDate(0, 0, 1)),
			expiresAt: base.Add(2 * 24 * time.Hour), now: timezone.StartOfDay(base).AddDate(0, 0, 2).Add(time.Minute), wantAnchor: timezone.StartOfDay(base).AddDate(0, 0, 2)},
		{name: "单日卡跨午夜仍是一次性额度", kind: "daily", startsAt: base, anchor: usageBillingTestTime(timezone.StartOfDay(base)),
			expiresAt: base.AddDate(0, 0, 1), now: timezone.StartOfDay(base).AddDate(0, 0, 1).Add(time.Minute), wantAnchor: timezone.StartOfDay(base), wantUsed: 70},
	} {
		t.Run(tt.name, func(t *testing.T) {
			row := usageBillingSubscriptionRow{ID: 1, PlanID: 2, StartsAt: tt.startsAt, ExpiresAt: tt.expiresAt, ResetCountedAt: tt.now}
			switch tt.kind {
			case "daily":
				row.DailyLimitUSD, row.DailyWindowStart, row.DailyUsageUSD = limit, nullTimePtr(tt.anchor), 70
			case "weekly":
				row.WeeklyLimitUSD, row.WeeklyWindowStart, row.WeeklyUsageUSD = limit, nullTimePtr(tt.anchor), 70
			default:
				row.MonthlyLimitUSD, row.MonthlyWindowStart, row.MonthlyUsageUSD = limit, nullTimePtr(tt.anchor), 70
			}
			got := normalizeUsageBillingSubscriptionRow(row, tt.now)
			var start sql.NullTime
			var used float64
			switch tt.kind {
			case "daily":
				start, used = got.DailyWindowStart, got.DailyUsageUSD
			case "weekly":
				start, used = got.WeeklyWindowStart, got.WeeklyUsageUSD
			default:
				start, used = got.MonthlyWindowStart, got.MonthlyUsageUSD
			}
			require.True(t, start.Valid)
			require.True(t, start.Time.Equal(tt.wantAnchor), "实际 %s，期望 %s", start.Time, tt.wantAnchor)
			require.Equal(t, tt.wantUsed, used)
			require.Equal(t, row.ExpiresAt, got.ExpiresAt)
			require.Equal(t, got, normalizeUsageBillingSubscriptionRow(got, tt.now), "重复归一化不能重复发放额度")
		})
	}
}

// 先结算原时间表再推进窗口，保证停机跨周期时的次数不会因新锚点丢失。
func TestNormalizeUsageBillingSubscriptionRow_CountsBeforeAdvancingWindows(t *testing.T) {
	anchor := time.Date(2026, 9, 2, 10, 11, 0, 0, timezone.Location())
	now := anchor.Add(22 * 24 * time.Hour)
	row := usageBillingSubscriptionRow{StartsAt: anchor, ExpiresAt: anchor.Add(24 * 24 * time.Hour),
		WeeklyWindowStart: nullTimePtr(&anchor), WeeklyLimitUSD: sql.NullFloat64{Float64: 70, Valid: true},
		WeeklyUsageUSD: 70, WeeklyResetCount: 4, ResetCountedAt: anchor}
	got := normalizeUsageBillingSubscriptionRow(row, now)
	require.Equal(t, int64(7), got.WeeklyResetCount)
	require.Equal(t, now, got.ResetCountedAt)
	require.True(t, got.WeeklyWindowStart.Time.Equal(anchor.Add(21*24*time.Hour)))
	require.Zero(t, got.WeeklyUsageUSD)
	require.Equal(t, got, normalizeUsageBillingSubscriptionRow(got, now))
}

// 无限额度及尚未生效的窗口不能被归一化为新的收费周期。
func TestNormalizeUsageBillingSubscriptionRow_IneligibleWindowsRemainUntouched(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, timezone.Location())
	anchor := now.Add(-40 * 24 * time.Hour)
	for _, limit := range []sql.NullFloat64{{}, {Float64: 0, Valid: true}, {Float64: -1, Valid: true}} {
		row := usageBillingSubscriptionRow{StartsAt: anchor, ExpiresAt: now.Add(time.Hour), ResetCountedAt: now,
			WeeklyLimitUSD: limit, WeeklyWindowStart: nullTimePtr(&anchor), WeeklyUsageUSD: 70}
		require.Equal(t, row, normalizeUsageBillingSubscriptionRow(row, now))
	}
	row := usageBillingSubscriptionRow{StartsAt: now.Add(time.Hour), ExpiresAt: now.Add(72 * time.Hour), ResetCountedAt: now,
		WeeklyLimitUSD: sql.NullFloat64{Float64: 70, Valid: true}, WeeklyUsageUSD: 12}
	require.Equal(t, row, normalizeUsageBillingSubscriptionRow(row, now))
}

// 返回独立时间指针，避免表驱动用例共享可变窗口。
func usageBillingTestTime(value time.Time) *time.Time { return &value }
