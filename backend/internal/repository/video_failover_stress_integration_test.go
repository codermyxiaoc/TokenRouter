//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 同一屏障同时放行所有调用，所有错误都回主测试协程断言，避免并发测试中的 Fatal 遗漏。
func videoFailoverStressParallel(count int, action func(int) error) []error {
	start := make(chan struct{})
	errs := make([]error, count)
	var workers sync.WaitGroup
	for i := range count {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			<-start
			errs[i] = action(i)
		}(i)
	}
	close(start)
	workers.Wait()
	return errs
}

// 构造尚未落库的独立请求，模拟不同实例已经选到不同账号后竞争同一幂等键。
func videoFailoverStressUncreated(f *videoBillingFixture, accountID int64, key, hash string) *service.VideoTaskRecord {
	id, lease := "vid_"+uuid.NewString(), uuid.NewString()
	now := time.Now().UTC().Truncate(time.Microsecond)
	return &service.VideoTaskRecord{
		ID: id, UserID: f.userID, APIKeyID: f.keyID, GroupID: f.groupID, AccountID: accountID,
		IdempotencyKey: key, PayloadHash: hash, Status: "prepared", BillingStatus: "pending",
		CreatedAt: now, NextPollAt: now, LeaseToken: lease, LeaseUntil: now.Add(3 * time.Minute),
		Target: service.VideoUpstreamTarget{Version: 1, Endpoint: service.VideoEndpointSeedance, AccountID: accountID},
		Hold: service.BatchImageBalanceHoldCommand{
			RequestID: "video_hold:" + id, BatchID: id, UserID: f.userID, ActorUserID: f.userID,
			APIKeyID: f.keyID, GroupID: &f.groupID, APIKeyBillingMode: service.APIKeyBillingModeBalance,
			PricingSnapshotVersion: 3, BaseAmountUSD: 2.7, SubscriptionRateMultiplier: 1,
			SubscriptionRateMultiplierScale: 1, BalanceRateMultiplier: 1, SettlementRateScale: 1,
			ReservedAt: now, VideoEntity: true, VideoAccountID: accountID, VideoLeaseToken: lease,
		},
	}
}

func videoFailoverStressAccountCost(t *testing.T, accountID int64, want float64) {
	t.Helper()
	var cost float64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(),
		`SELECT COALESCE((extra->>'quota_used')::numeric,0) FROM accounts WHERE id=$1`, accountID).Scan(&cost))
	require.InDelta(t, want, cost, 1e-8)
}

func videoFailoverStressKeyWindows(t *testing.T, keyID int64, want float64) {
	t.Helper()
	var fiveHour, day, week float64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(),
		`SELECT usage_5h,usage_1d,usage_7d FROM api_keys WHERE id=$1`, keyID).Scan(&fiveHour, &day, &week))
	for _, used := range []float64{fiveHour, day, week} {
		require.InDelta(t, want, used, 1e-8)
	}
}

// 三轮六十四并发跨账号幂等创建，不允许产生第二个任务、第二笔冻结或漏记平台预留。
func TestVideoFailoverStressIdempotentCrossAccountCreate(t *testing.T) {
	videoStressConnectionPool(t)
	for round := range 3 {
		t.Run(fmt.Sprintf("round_%d", round), func(t *testing.T) {
			f := newVideoBillingFixture(t)
			f.quota(1000)
			accounts := []int64{f.accountID}
			for range 3 {
				accounts = append(accounts, newVideoRetryTarget(t).ID)
			}
			const count = 64
			key, hash := uuid.NewString(), uuid.NewString()
			created := make([]bool, count)
			ids := make([]string, count)
			errs := videoFailoverStressParallel(count, func(i int) error {
				repo := NewVideoTaskRepository(integrationDB)
				request := videoFailoverStressUncreated(f, accounts[i%len(accounts)], key, hash)
				record, fresh, err := repo.Create(context.Background(), request, 100)
				if err != nil {
					return err
				}
				created[i], ids[i] = fresh, record.ID
				if !fresh {
					return nil
				}
				result, err := f.repo.ReserveBatchImageBalance(context.Background(), &record.Hold)
				if err == nil && !result.Applied {
					return fmt.Errorf("首次创建没有建立唯一预扣")
				}
				return err
			})
			fresh := 0
			for i, err := range errs {
				require.NoError(t, err)
				require.Equal(t, ids[0], ids[i])
				if created[i] {
					fresh++
				}
			}
			require.Equal(t, 1, fresh)
			f.money(997.3, 2.7, 2.7, 0)
			f.quotaUsage(2.7, 2.7, 2.7)
			videoFailoverStressKeyWindows(t, f.keyID, 2.7)
			var tasks, holds int
			require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT count(*) FROM video_tasks WHERE api_key_id=$1`, f.keyID).Scan(&tasks))
			// 去重审计表不随用户清理；请求 ID 限定本轮，避免复用隔离库和重置 Key ID 计入旧测试。
			require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1 AND request_id=$2`, f.keyID, "video_hold:"+ids[0]).Scan(&holds))
			require.Equal(t, 1, tasks)
			require.Equal(t, 1, holds)
			t.Logf("64 个并发创建跨 4 账号：新任务 %d，重复请求复用 %d，冻结与平台预占均为 2.70", fresh, count-fresh)
		})
	}
}

// 所有任务共用同一个目标账号；换号容量检查、一次预扣、费用来源和终态幂等同时承受并发。
func TestVideoFailoverStressCapacityFundingAndFinalAccount(t *testing.T) {
	videoStressConnectionPool(t)
	for _, scenario := range []struct {
		name, funding string
		independent   bool
		exhausted     bool
		wantRate      float64
	}{
		{"balance_plan_ignored", service.APIKeyBillingModeBalance, false, false, 3},
		{"subscription_plan_override", service.APIKeyBillingModeSubscription, false, false, .5},
		{"auto_subscription", service.APIKeyBillingModeAuto, false, false, .5},
		{"auto_exhausted_falls_back", service.APIKeyBillingModeAuto, false, true, 3},
		{"subscription_independent", service.APIKeyBillingModeSubscription, true, false, .38},
		{"balance_independent", service.APIKeyBillingModeBalance, true, false, .38},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.Background()
			f := newVideoBillingFixture(t)
			f.quota(1000)
			sub := fixedVideoSubscription(t, f, 500, .5)
			if scenario.exhausted {
				_, err := integrationDB.ExecContext(ctx, `UPDATE user_subscriptions SET daily_limit_usd=1,weekly_limit_usd=1,monthly_limit_usd=1,daily_usage_usd=1,weekly_usage_usd=1,monthly_usage_usd=1 WHERE id=$1`, sub.ID)
				require.NoError(t, err)
			}
			rate := 3.0
			if scenario.independent {
				rate = .38
			}
			const count, capacity = 64, 7
			tasks := make([]*service.VideoTaskRecord, count)
			for i := range tasks {
				task := tokenPrepayTestTask(t, f, rate, .3)
				task.Hold.APIKeyBillingMode = scenario.funding
				task.Hold.DisablePlanGroupRateMultiplier = scenario.independent
				if scenario.funding == service.APIKeyBillingModeSubscription {
					task.Hold.PreferredSubscriptionID = &sub.ID
				}
				// 真实的七张参考图，前五张免费；附加费在任何倍率下都只能是 0.30。
				images, imagePrice := 7, .15
				task.Quote.ReferenceImageCount = &images
				task.Quote.ImageInputPricing = &service.VideoImageInputPricing{FreeImages: 5, Price: &imagePrice}
				require.NoError(t, f.tasks.Save(ctx, task, false))
				task = f.reserve(task)
				require.InDelta(t, 2.7, task.Hold.HoldAmount, 1e-8)
				task.Status, task.CreateResponseStatus = "failed", 429
				task.CreateResponse = []byte(`{"error":{"code":"QuotaExceeded"}}`)
				task.RawResponse = task.CreateResponse
				require.NoError(t, f.tasks.Save(ctx, task, false))
				tasks[i] = task
			}
			next := newVideoRetryTarget(t)
			moved := make([]*service.VideoTaskRecord, count)
			errs := videoFailoverStressParallel(count, func(i int) error {
				repo := NewVideoTaskRepository(integrationDB).(service.VideoTaskRetryRepository)
				var err error
				moved[i], err = repo.SwitchRejected(ctx, tasks[i].ID, tasks[i].LeaseToken, videoRetryTarget(next), tasks[i].Metadata, 1.7, capacity)
				return err
			})
			admitted := 0
			for i, err := range errs {
				if err == nil {
					admitted++
					require.Equal(t, tasks[i].ID, moved[i].ID)
					require.Equal(t, tasks[i].Hold.RequestID, moved[i].Hold.RequestID)
					require.Equal(t, tasks[i].Hold.HoldAmount, moved[i].Hold.HoldAmount)
				} else {
					require.ErrorIs(t, err, service.ErrVideoTaskBusy)
				}
			}
			require.Equal(t, capacity, admitted)
			commands := make([]service.BatchImageBalanceHoldCommand, count)
			for i := range tasks {
				if moved[i] != nil {
					commands[i] = f.command(moved[i], "completed", 4)
					commands[i].VideoActualFixedAmountUSD = .3
					commands[i].VideoAccountQuotaCost = 4.3 * moved[i].AccountRateMultiplier
					// 旧账号即使拿到任务完成通知，也不能在换号后抢先结算。
					stale := commands[i]
					stale.VideoAccountID = f.accountID
					_, err := f.repo.CaptureBatchImageBalance(ctx, &stale)
					require.ErrorIs(t, err, service.ErrVideoTaskConflict)
				} else {
					commands[i] = f.command(tasks[i], "failed", 0)
				}
			}
			// 每个任务四个结算恢复者竞争，另有一个相反操作，合法命令只能生效一次。
			applied := make([]bool, count*5)
			errs = videoFailoverStressParallel(count*5, func(i int) error {
				index, attempt := i/5, i%5
				cmd := commands[index]
				capture := moved[index] != nil
				if attempt == 4 {
					capture = !capture
				}
				operation := f.repo.ReleaseBatchImageBalance
				cmd.RequestID = "video_release:" + cmd.BatchID
				if capture {
					operation, cmd.RequestID = f.repo.CaptureBatchImageBalance, "video_capture:"+cmd.BatchID
				}
				result, err := operation(ctx, &cmd)
				if attempt == 4 {
					if err != service.ErrVideoTaskConflict {
						return fmt.Errorf("相反终态资金操作必须拒绝: %v", err)
					}
					return nil
				}
				if err == nil {
					applied[i] = result.Applied
				}
				return err
			})
			for _, err := range errs {
				require.NoError(t, err)
			}
			for index := range count {
				commits := 0
				for attempt := range 4 {
					if applied[index*5+attempt] {
						commits++
					}
				}
				require.Equal(t, 1, commits)
			}
			want := float64(capacity) * (4*scenario.wantRate + .3)
			balance := 0.0
			if scenario.funding == service.APIKeyBillingModeBalance || scenario.exhausted {
				balance = want
			}
			subscription := want - balance
			if scenario.exhausted {
				subscription++
			}
			f.money(1000-balance, 0, want, 0)
			f.quotaUsage(balance, balance, balance)
			fixedVideoAssertSubscription(t, sub.ID, subscription)
			videoFailoverStressKeyWindows(t, f.keyID, want)
			videoFailoverStressAccountCost(t, next.ID, float64(capacity)*4.3*1.7)
			var settled, released, holds, captures, releases int
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FILTER(WHERE billing_status='settled'),count(*) FILTER(WHERE billing_status='released') FROM video_tasks WHERE api_key_id=$1`, f.keyID).Scan(&settled, &released))
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FILTER(WHERE d.request_id='video_hold:'||t.id),count(*) FILTER(WHERE d.request_id='video_capture:'||t.id),count(*) FILTER(WHERE d.request_id='video_release:'||t.id) FROM usage_billing_dedup d JOIN video_tasks t ON t.api_key_id=d.api_key_id AND d.request_id IN ('video_hold:'||t.id,'video_capture:'||t.id,'video_release:'||t.id) WHERE t.api_key_id=$1`, f.keyID).Scan(&holds, &captures, &releases))
			require.Equal(t, capacity, settled)
			require.Equal(t, count-capacity, released)
			require.Equal(t, count, holds)
			require.Equal(t, capacity, captures)
			require.Equal(t, count-capacity, releases)
			t.Logf("64 换号竞争容量 %d；320 并发资金操作只提交 64 次；实收 %.8f，余额 %.8f，最终账号成本 %.8f", capacity, want, balance, float64(capacity)*4.3*1.7)
		})
	}
}

// 不同目标账号持有不同容量锁，最终仍须靠同一任务行互斥，不能出现多个迁移成功结果。
func TestVideoFailoverStressSingleTaskMigrationCAS(t *testing.T) {
	videoStressConnectionPool(t)
	for round := range 3 {
		t.Run(fmt.Sprintf("round_%d", round), func(t *testing.T) {
			ctx := context.Background()
			f := newVideoBillingFixture(t)
			f.quota(100)
			task := newRejectedVideoForRetry(t, f)
			targets := make([]*service.Account, 4)
			for i := range targets {
				targets[i] = newVideoRetryTarget(t)
			}
			const count = 64
			moved := make([]*service.VideoTaskRecord, count)
			errs := videoFailoverStressParallel(count, func(i int) error {
				var err error
				repo := NewVideoTaskRepository(integrationDB).(service.VideoTaskRetryRepository)
				moved[i], err = repo.SwitchRejected(ctx, task.ID, task.LeaseToken, videoRetryTarget(targets[i%len(targets)]), task.Metadata, 2, 100)
				return err
			})
			var winner *service.VideoTaskRecord
			for i, err := range errs {
				if err == nil {
					require.Nil(t, winner)
					winner = moved[i]
				} else {
					require.ErrorIs(t, err, service.ErrVideoTaskConflict)
				}
			}
			require.NotNil(t, winner)
			stored, err := f.tasks.Get(ctx, task.ID)
			require.NoError(t, err)
			require.Equal(t, winner.AccountID, stored.AccountID)
			require.Len(t, stored.RejectedAttempts, 1)
			f.money(990, 10, 10, 0)
			f.quotaUsage(10, 10, 10)
			cmd := f.command(stored, "failed", 0)
			_, err = f.repo.ReleaseBatchImageBalance(ctx, &cmd)
			require.NoError(t, err)
			f.money(1000, 0, 0, 0)
			f.quotaUsage(0, 0, 0)
			for _, account := range targets {
				videoFailoverStressAccountCost(t, account.ID, 0)
			}
			t.Log("64 迁移者竞争同一任务、4 个目标账号：只有一个迁移成功，原预算只释放一次")
		})
	}
}

// 迁移已经提交但调用方没有拿到结果，重启只能恢复退款，不得凭旧租约继续迁移或重新预扣。
func TestVideoFailoverStressCommittedMigrationRecovery(t *testing.T) {
	videoStressConnectionPool(t)
	for round := range 3 {
		t.Run(fmt.Sprintf("round_%d", round), func(t *testing.T) {
			ctx := context.Background()
			f := newVideoBillingFixture(t)
			f.quota(100)
			task := newRejectedVideoForRetry(t, f)
			next := newVideoRetryTarget(t)
			// 丢弃成功返回值，模拟事务已提交但进程丢失响应；恢复只读数据库。
			_, err := f.tasks.(service.VideoTaskRetryRepository).SwitchRejected(ctx, task.ID, task.LeaseToken, videoRetryTarget(next), task.Metadata, 2, 1)
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET lease_until=NOW()-INTERVAL '1 second' WHERE id=$1`, task.ID)
			require.NoError(t, err)
			const workers = 64
			claimed := make([]*service.VideoTaskRecord, workers)
			errs := videoFailoverStressParallel(workers, func(i int) error {
				var err error
				claimed[i], err = NewVideoTaskRepository(integrationDB).Claim(ctx, task.ID, 3*time.Minute)
				return err
			})
			var owner *service.VideoTaskRecord
			for i, err := range errs {
				if err == nil {
					require.Nil(t, owner, "同一任务只能有一个恢复者获得租约")
					owner = claimed[i]
				} else {
					require.ErrorIs(t, err, service.ErrVideoTaskNotFound)
				}
			}
			require.NotNil(t, owner)
			require.Equal(t, "prepared", owner.Status)
			require.Equal(t, next.ID, owner.AccountID)
			require.Equal(t, task.Hold.HoldAmount, owner.Hold.HoldAmount)
			third := newVideoRetryTarget(t)
			errs = videoFailoverStressParallel(workers, func(i int) error {
				repo := NewVideoTaskRepository(integrationDB)
				if i%2 == 0 {
					stale := *task
					stale.Status = "submitting"
					return repo.Save(ctx, &stale, false)
				}
				_, err := repo.(service.VideoTaskRetryRepository).SwitchRejected(ctx, task.ID, task.LeaseToken, videoRetryTarget(third), task.Metadata, 3, 100)
				return err
			})
			for _, err := range errs {
				require.ErrorIs(t, err, service.ErrVideoTaskConflict)
			}
			cmd := owner.Hold
			cmd.RequestID, cmd.RequestFingerprint = "video_release:"+owner.ID, ""
			applied := make([]bool, workers)
			errs = videoFailoverStressParallel(workers, func(i int) error {
				local := cmd
				result, err := f.repo.ReleaseBatchImageBalance(ctx, &local)
				if err == nil {
					applied[i] = result.Applied
				}
				return err
			})
			commits := 0
			for i, err := range errs {
				require.NoError(t, err)
				if applied[i] {
					commits++
				}
			}
			require.Equal(t, 1, commits)
			f.money(1000, 0, 0, 0)
			f.quotaUsage(0, 0, 0)
			videoFailoverStressKeyWindows(t, f.keyID, 0)
			videoFailoverStressAccountCost(t, next.ID, 0)
			videoFailoverStressAccountCost(t, third.ID, 0)
			stored, err := f.tasks.Get(ctx, task.ID)
			require.NoError(t, err)
			require.Equal(t, "released", stored.BillingStatus)
			require.Equal(t, next.ID, stored.AccountID)
			require.Len(t, stored.RejectedAttempts, 1)
			t.Log("64 恢复者仅一个获得租约；64 旧执行者全部拒绝；64 退款只提交一次")
		})
	}
}

// 已扣款后使用记录尚未保存时，不同实例重复补写只能产生一条记录，且不能改变任何资金统计。
func TestVideoFailoverStressUsageProjectionAfterSettlement(t *testing.T) {
	videoStressConnectionPool(t)
	ctx := context.Background()
	f := newVideoBillingFixture(t)
	f.quota(100)
	task := newRejectedVideoForRetry(t, f)
	next := newVideoRetryTarget(t)
	moved, err := f.tasks.(service.VideoTaskRetryRepository).SwitchRejected(ctx, task.ID, task.LeaseToken, videoRetryTarget(next), task.Metadata, 2, 10)
	require.NoError(t, err)
	cmd := f.command(moved, "completed", 4)
	cmd.VideoAccountQuotaCost = 8
	result, err := f.repo.CaptureBatchImageBalance(ctx, &cmd)
	require.NoError(t, err)
	require.True(t, result.Applied)
	const workers = 64
	logs := []service.UsageLogRepository{
		NewUsageLogRepository(integrationEntClient, integrationDB, nil),
		NewUsageLogRepository(integrationEntClient, integrationDB, nil),
		NewUsageLogRepository(integrationEntClient, integrationDB, nil),
		NewUsageLogRepository(integrationEntClient, integrationDB, nil),
	}
	inserted := make([]bool, workers)
	errs := videoFailoverStressParallel(workers, func(i int) error {
		// 重启后的财务重放先命中账本，再使用原任务 ID 补使用记录，不应再次收费。
		local := cmd
		replayed, err := f.repo.CaptureBatchImageBalance(ctx, &local)
		if err != nil {
			return err
		}
		if replayed.Applied {
			return fmt.Errorf("已结算任务被重复扣费")
		}
		mode, media, resolution, rate := "video_token", "video", "720p", 2.0
		log := &service.UsageLog{
			UserID: f.userID, BillingUserID: f.userID, APIKeyID: f.keyID, GroupID: &f.groupID,
			AccountID: next.ID, RequestID: cmd.RequestID, Model: "failover-stress-model", RequestedModel: "failover-stress-model",
			BillingMode: &mode, MediaType: &media, VideoCount: 1, VideoResolution: &resolution,
			OutputTokens: 100000, OutputCost: 4, TotalCost: 4, ActualCost: replayed.ActualAmountUSD,
			BalanceAmountUSD: replayed.BalanceAmountUSD, BillingAllocations: replayed.BillingAllocations,
			RateMultiplier: 1, AccountRateMultiplier: &rate, CreatedAt: moved.CreatedAt,
		}
		inserted[i], err = logs[i%len(logs)].Create(ctx, log)
		return err
	})
	created := 0
	for i, err := range errs {
		require.NoError(t, err)
		if inserted[i] {
			created++
		}
	}
	require.Equal(t, 1, created)
	var count int
	var accountID int64
	var actual, balance, base, accountRate float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*),min(account_id),sum(actual_cost),sum(balance_amount_usd),sum(total_cost),min(account_rate_multiplier) FROM usage_logs WHERE api_key_id=$1 AND request_id=$2`, f.keyID, cmd.RequestID).Scan(&count, &accountID, &actual, &balance, &base, &accountRate))
	require.Equal(t, 1, count)
	require.Equal(t, next.ID, accountID)
	require.Equal(t, 4.0, actual)
	require.Equal(t, 4.0, balance)
	require.Equal(t, 4.0, base)
	require.Equal(t, 2.0, accountRate)
	f.money(996, 0, 4, 0)
	f.quotaUsage(4, 4, 4)
	videoFailoverStressAccountCost(t, next.ID, 8)
	t.Log("4 个使用记录仓储实例、64 并发结算重放/补记录：账务一次，使用记录一条，最终账号成本一次")
}
