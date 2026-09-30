//go:build integration

package repository

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
)

// 实际执行原始和预聚合 SQL，防止只改排序字符串却漏掉付款主体或时间边界。
func (s *UsageLogRepoSuite) TestUserTrendMetricRawAndRollupPreservePayer() {
	owner := mustCreateUser(s.T(), s.client, &service.User{Email: "trend-owner-" + uuid.NewString() + "@example.com"})
	member := mustCreateUser(s.T(), s.client, &service.User{Email: "trend-member-" + uuid.NewString() + "@example.com"})
	tokenUser := mustCreateUser(s.T(), s.client, &service.User{Email: "trend-token-" + uuid.NewString() + "@example.com"})
	teamKey := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: owner.ID, Key: "sk-test-trend-" + uuid.NewString()})
	tokenKey := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: tokenUser.ID, Key: "sk-test-trend-" + uuid.NewString()})
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "trend-metric-account"})
	start := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	end := start.Add(4 * time.Hour)
	for _, item := range []struct {
		userID, payerID, keyID int64
		tokens                 int
		cost, actual           float64
		at                     time.Time
	}{
		{tokenUser.ID, tokenUser.ID, tokenKey.ID, 10000, 99, 0.2, start.Add(time.Minute)},
		{member.ID, owner.ID, teamKey.ID, 100, 10, 4, start.Add(2 * time.Minute)},
		{member.ID, owner.ID, teamKey.ID, 200, 20, 5, start.Add(3 * time.Minute)},
		{tokenUser.ID, tokenUser.ID, tokenKey.ID, 1, 99, 0, start.Add(4 * time.Minute)},
		// 区间外的高费用不能改变本次排行，结束边界必须保持右开。
		{tokenUser.ID, tokenUser.ID, tokenKey.ID, 100000, 1000, 1000, start.Add(-time.Second)},
		{tokenUser.ID, tokenUser.ID, tokenKey.ID, 100000, 1000, 1000, end},
	} {
		_, err := s.repo.Create(s.ctx, &service.UsageLog{
			UserID: item.userID, BillingUserID: item.payerID, APIKeyID: item.keyID, AccountID: account.ID,
			RequestID: uuid.NewString(), Model: "gpt-6.1-sol", InputTokens: item.tokens,
			TotalCost: item.cost, ActualCost: item.actual, CreatedAt: item.at,
		})
		s.Require().NoError(err)
	}
	for _, metric := range []string{"tokens", "actual_cost"} {
		raw, err := s.repo.GetUserUsageTrend(s.ctx, start, end, "hour", 1, metric)
		s.Require().NoError(err)
		s.Require().Len(raw, 1)
		if metric == "tokens" {
			s.Require().Equal(tokenUser.ID, raw[0].UserID)
			s.Require().Equal(int64(10001), raw[0].Tokens)
			s.Require().InDelta(0.2, raw[0].ActualCost, 1e-10)
		} else {
			s.Require().Equal(owner.ID, raw[0].UserID)
			s.Require().Equal(owner.Email, raw[0].Email)
			s.Require().Equal(int64(2), raw[0].Requests)
			s.Require().InDelta(9, raw[0].ActualCost, 1e-10)
			s.Require().InDelta(30, raw[0].Cost, 1e-10)
		}
		aggregator := newDashboardAggregationRepositoryWithSQL(s.tx)
		s.Require().NoError(aggregator.AggregateUsageAnalyticsRange(s.ctx, start, end))
		_, err = s.tx.ExecContext(s.ctx, `UPDATE usage_analytics_aggregation_state
			SET live_watermark = $1, coverage_start = $2, backfill_cursor = $2,
			    source_oldest_at = $2, phase = 'idle' WHERE id = 1`, end, start)
		s.Require().NoError(err)
		s.repo.preAggregation = service.NewPreAggregationSettingsService(nil, &config.Config{
			DashboardAgg: config.DashboardAggregationConfig{Enabled: true, IntervalSeconds: 60},
		})
		rollup, usedRollup, err := s.repo.getUserUsageTrendFromAnalytics(s.ctx, start, end, "hour", 1, metric)
		s.Require().NoError(err)
		s.Require().True(usedRollup, "不能静默回退原始查询掩盖预聚合问题")
		s.Require().Equal(raw, rollup, "费用/Token 排名和付款主体在两条查询路径必须完全一致")
		s.repo.preAggregation = nil
	}
}
