//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 不同付款人没有共享用户行锁；同一最终账号成本必须在真实并行资金事务中保持原子累加。
func TestVideoFailoverStressIndependentPayersShareAccount(t *testing.T) {
	videoStressConnectionPool(t)
	ctx := context.Background()
	const payers = 32
	fixtures := make([]*videoBillingFixture, payers)
	tasks := make([]*service.VideoTaskRecord, payers)
	accountRates := make([]float64, payers)
	actualBases := make([]float64, payers)
	wantUser := make([]float64, payers)
	finalAccount := newVideoRetryTarget(t)
	wantAccount := 0.0
	for i := range payers {
		f := newVideoBillingFixture(t)
		f.quota(100)
		fixtures[i] = f
		// 包含零倍率；视频免费不能免掉固定参考图费用。
		rate := []float64{0, .38, .5, 2}[i%4]
		task := tokenPrepayTestTask(t, f, rate, .3)
		task.Hold.APIKeyBillingMode = service.APIKeyBillingModeBalance
		task.Hold.DisablePlanGroupRateMultiplier = true
		require.NoError(t, f.tasks.Save(ctx, task, false))
		task = f.reserve(task)
		task.Status, task.CreateResponseStatus = "failed", 429
		task.CreateResponse = []byte(`{"error":{"code":"QuotaExceeded"}}`)
		task.RawResponse = task.CreateResponse
		require.NoError(t, f.tasks.Save(ctx, task, false))
		tasks[i] = task
		// 模拟不同任务创建时冻结的账号倍率，不能使用最后一笔任务的倍率覆盖其它任务。
		accountRates[i] = 1 + float64(i%3)*.5
		actualBases[i] = .7 + float64(i%5)*.1
		wantUser[i] = service.QuantizeUsageBillingAmount(actualBases[i]*rate + .3)
		wantAccount += service.QuantizeUsageBillingAmount((actualBases[i] + .3) * accountRates[i])
	}

	moved := make([]*service.VideoTaskRecord, payers)
	errs := videoFailoverStressParallel(payers, func(i int) error {
		var err error
		repo := NewVideoTaskRepository(integrationDB).(service.VideoTaskRetryRepository)
		moved[i], err = repo.SwitchRejected(ctx, tasks[i].ID, tasks[i].LeaseToken, videoRetryTarget(finalAccount), tasks[i].Metadata, accountRates[i], payers)
		return err
	})
	commands := make([]service.BatchImageBalanceHoldCommand, payers)
	for i, err := range errs {
		require.NoError(t, err)
		commands[i] = fixtures[i].command(moved[i], "completed", actualBases[i])
		commands[i].VideoActualFixedAmountUSD = .3
		commands[i].VideoAccountQuotaCost = (actualBases[i] + .3) * accountRates[i]
	}

	// 每个付款人的四个合法捕获和一个非法退款并发，不能由付款人锁掩盖目标账号更新竞争。
	applied := make([]bool, payers*5)
	errs = videoFailoverStressParallel(payers*5, func(i int) error {
		payer, attempt := i/5, i%5
		cmd := commands[payer]
		if attempt == 4 {
			cmd.RequestID = "video_release:" + cmd.BatchID
			_, err := fixtures[payer].repo.ReleaseBatchImageBalance(ctx, &cmd)
			if err != service.ErrVideoTaskConflict {
				return fmt.Errorf("已完成任务退款未被拒绝: %v", err)
			}
			return nil
		}
		result, err := fixtures[payer].repo.CaptureBatchImageBalance(ctx, &cmd)
		if err == nil {
			applied[i] = result.Applied
		}
		return err
	})
	for _, err := range errs {
		require.NoError(t, err)
	}
	wantTotal := 0.0
	for i, f := range fixtures {
		commits := 0
		for attempt := range 4 {
			if applied[i*5+attempt] {
				commits++
			}
		}
		require.Equal(t, 1, commits)
		f.money(1000-wantUser[i], 0, wantUser[i], 0)
		f.quotaUsage(wantUser[i], wantUser[i], wantUser[i])
		videoFailoverStressKeyWindows(t, f.keyID, wantUser[i])
		stored, err := f.tasks.Get(ctx, tasks[i].ID)
		require.NoError(t, err)
		require.Equal(t, "settled", stored.BillingStatus)
		require.Equal(t, finalAccount.ID, stored.AccountID)
		require.InDelta(t, wantUser[i], stored.BillingResult.ActualAmountUSD, 1e-8)
		for _, allocation := range stored.BillingResult.BillingAllocations {
			if allocation.Component == service.VideoImageInputBillingComponent {
				require.Equal(t, 1.0, allocation.RateMultiplier)
				require.InDelta(t, .3, allocation.AmountUSD, 1e-8)
			}
		}
		wantTotal += wantUser[i]
	}
	videoFailoverStressAccountCost(t, finalAccount.ID, wantAccount)
	var captures, settled int
	var storedTotal float64
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT count(*),sum((billing_result->>'ActualAmountUSD')::numeric) FROM video_tasks WHERE account_id=$1 AND billing_status='settled'`, finalAccount.ID).Scan(&settled, &storedTotal))
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT count(*) FROM usage_billing_dedup d JOIN video_tasks t ON t.api_key_id=d.api_key_id AND d.request_id='video_capture:'||t.id WHERE t.account_id=$1`, finalAccount.ID).Scan(&captures))
	require.Equal(t, payers, settled)
	require.Equal(t, payers, captures)
	require.InDelta(t, wantTotal, storedTotal, 1e-8)
	t.Logf("32 独立付款人，160 并发资金操作；用户扣款 %.8f，最终同账号成本 %.8f，32 个结算去重键，无重复或漏记", wantTotal, wantAccount)
}
