//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type legacyDeletedTaskRepo struct {
	*legacyRecoveryRepoStub
	current *LegacyVideoTask
}

func (r *legacyDeletedTaskRepo) Get(context.Context, string, string, int64, int64) (*LegacyVideoTask, error) {
	return r.current, nil
}

// 删除可能发生在领取租约之后；没有完成证据时即使内存快照陈旧也不能继续查询 404。
func TestLegacyVideoDeletedWithoutUsageStopsPolling(t *testing.T) {
	for _, stale := range []bool{false, true} {
		r, task, _, billing, calls := legacyRecoveryFixture(t, "")
		task.Source = "seedance_video"
		now := time.Now()
		if stale {
			current := *task
			current.UpstreamDeletedAt = &now
			r.repo = &legacyDeletedTaskRepo{legacyRecoveryRepoStub: r.repo.(*legacyRecoveryRepoStub), current: &current}
		} else {
			task.UpstreamDeletedAt = &now
		}
		require.ErrorContains(t, r.RecoverLegacyVideoTask(context.Background(), task), "已删除")
		require.Equal(t, "reconciliation", task.BillingStatus)
		require.NotNil(t, task.UpstreamDeletedAt)
		require.Zero(t, *calls)
		require.Zero(t, billing.calls)
		require.NotEqual(t, "not_billable", task.BillingStatus)
	}
}

// 成功删除不撤销此前确认的实际 Token 或冻结报价；账本回执仍能补齐原使用记录。
func TestLegacyVideoDeletedKeepsCompletedSettlement(t *testing.T) {
	for _, replayReceipt := range []bool{false, true} {
		r, task, logs, billing, calls := legacyRecoveryFixture(t, "")
		account := seedanceTestAccount()
		r.gateway.accountRepo = &openAIRecordUsageAccountRepoStub{account: account}
		task.Source, task.AccountID, task.Status = "seedance_video", account.ID, "completed"
		task.Pending.Model, task.Pending.BillingModel = "seedance-test", "seedance-test"
		price := 10.0
		group := task.Pending.SeedanceBilling.Group
		group.Platform = PlatformOpenAI
		group.ModelPricing = []ChannelModelPricing{{Platform: PlatformOpenAI, Models: []string{"seedance-test"}, BillingMode: BillingModeToken, OutputPrice: &price}}
		r.gateway.resolver = NewModelPricingResolver(nil, r.gateway.billingService)
		evidence := &legacyVideoEvidence{Usage: OpenAIUsage{OutputTokens: 1000}, Observation: &MediaTaskObservation{Status: "completed"}}
		raw, err := json.Marshal(evidence)
		require.NoError(t, err)
		task.SettlementResult = raw
		if replayReceipt {
			logs.err = errors.New("使用记录暂时不可写")
			require.Error(t, r.RecoverLegacyVideoTask(context.Background(), task))
			require.Equal(t, 1, billing.calls)
			logs.err = nil
			billing.result = &UsageBillingApplyResult{Applied: false, BalanceAmountUSD: 0.12}
		}
		now := time.Now()
		task.UpstreamDeletedAt = &now
		require.NoError(t, r.RecoverLegacyVideoTask(context.Background(), task))
		require.Equal(t, "settled", task.BillingStatus)
		require.Equal(t, 1000, logs.lastLog.OutputTokens)
		require.Zero(t, *calls, "已有完成证据的删除任务不能查询上游")
		if replayReceipt {
			require.Equal(t, 2, billing.calls)
			require.InDelta(t, 0.12, logs.lastLog.ActualCost, 1e-10)
		}
	}
}
