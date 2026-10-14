//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 测试使用隔离数据库中的虚构归属，不连接视频供应商。
type legacyVideoFixture struct {
	repo                      service.LegacyVideoTaskRepository
	user, key, group, account int64
}

func newLegacyVideoFixture(t *testing.T) *legacyVideoFixture {
	t.Helper()
	client := testEntClient(t)
	u := mustCreateUser(t, client, &service.User{Email: "legacy-video-" + uuid.NewString() + "@example.test", Balance: 100})
	g := mustCreateGroup(t, client, &service.Group{Name: "legacy-video-" + uuid.NewString(), Platform: service.PlatformGrok, RateMultiplier: 1})
	k := mustCreateApiKey(t, client, &service.APIKey{UserID: u.ID, GroupID: &g.ID, Key: "sk-test-" + uuid.NewString(), Name: "legacy-video"})
	a := mustCreateAccount(t, client, &service.Account{Name: "legacy-video-" + uuid.NewString(), Platform: service.PlatformGrok, Type: service.AccountTypeAPIKey})
	return &legacyVideoFixture{NewLegacyVideoTaskRepository(integrationDB), u.ID, k.ID, g.ID, a.ID}
}

func (f *legacyVideoFixture) task() *service.LegacyVideoTask {
	id := uuid.NewString()
	return &service.LegacyVideoTask{Source: "grok_video", TaskID: id, UserID: f.user, BillingUserID: f.user,
		APIKeyID: f.key, GroupID: f.group, AccountID: f.account, Model: "grok-imagine-video",
		RequestID: service.StableGrokVideoBillingRequestID(id), Status: "queued", BillingStatus: "pending",
		CreatedAt: time.Now().UTC().Add(-time.Hour), NextPollAt: time.Now().UTC().Add(-time.Minute),
		Pending: &service.GrokVideoPendingBilling{Model: "grok-imagine-video", VideoResolution: "720p", VideoDurationSeconds: 8}}
}

func TestLegacyVideoRepositorySaveIdentitySnapshotAndAccess(t *testing.T) {
	f := newLegacyVideoFixture(t)
	ctx := context.Background()
	task := f.task()
	require.NoError(t, f.repo.Save(ctx, task))
	originalID, originalCreated := task.ID, task.CreatedAt
	duplicate := *task
	duplicate.Status, duplicate.BillingStatus = "completed", "settled"
	duplicate.CreatedAt = time.Now()
	duplicate.Pending = &service.GrokVideoPendingBilling{Model: "incorrect-replacement", VideoDurationSeconds: 99}
	require.NoError(t, f.repo.Save(ctx, &duplicate))
	require.Equal(t, originalID, duplicate.ID)
	require.Equal(t, originalCreated, duplicate.CreatedAt)
	require.Equal(t, "queued", duplicate.Status)
	require.Equal(t, "pending", duplicate.BillingStatus)
	require.Equal(t, 8, duplicate.Pending.VideoDurationSeconds, "重复保存不能修改首次受理快照")
	for _, mutate := range []func(*service.LegacyVideoTask){
		func(t *service.LegacyVideoTask) { t.UserID++ },
		func(t *service.LegacyVideoTask) { t.BillingUserID++ },
		func(t *service.LegacyVideoTask) { t.AccountID++ },
		func(t *service.LegacyVideoTask) { t.GroupID++ },
		func(t *service.LegacyVideoTask) { t.RequestID += "other" },
	} {
		bad := *task
		mutate(&bad)
		require.ErrorIs(t, f.repo.Save(ctx, &bad), service.ErrLegacyVideoTaskConflict)
	}
	for _, ids := range [][2]int64{{0, f.key}, {f.user + 1000, f.key}, {f.user, f.key + 1000}} {
		_, err := f.repo.Get(ctx, task.Source, task.TaskID, ids[0], ids[1])
		require.ErrorIs(t, err, service.ErrLegacyVideoTaskNotFound)
	}
	// 历史未知付款人只能从零补齐一次；原 Key 范围内允许行为用户和付款用户读取。
	legacy := f.task()
	legacy.Pending, legacy.BillingUserID = nil, 0
	require.NoError(t, f.repo.Save(ctx, legacy))
	legacy.Pending = task.Pending
	legacy.BillingUserID = f.user + 2000
	require.NoError(t, f.repo.Save(ctx, legacy))
	for _, actor := range []int64{f.user, legacy.BillingUserID} {
		read, err := f.repo.Get(ctx, legacy.Source, legacy.TaskID, actor, f.key)
		require.NoError(t, err)
		require.NotNil(t, read.Pending)
	}
	legacy.BillingUserID++
	require.ErrorIs(t, f.repo.Save(ctx, legacy), service.ErrLegacyVideoTaskConflict)
}

func TestLegacyVideoRepositoryLeaseCASAndCheckpoint(t *testing.T) {
	f := newLegacyVideoFixture(t)
	ctx := context.Background()
	task := f.task()
	require.NoError(t, f.repo.Save(ctx, task))
	claimed, err := f.repo.ClaimDue(ctx, time.Now(), 4, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	first := claimed[0]
	again, err := f.repo.ClaimDue(ctx, time.Now(), 4, time.Minute)
	require.NoError(t, err)
	require.Empty(t, again)
	// 强制旧租约到期后由第二实例领取，原执行者即使带真实结果也不能覆盖新租约。
	_, err = integrationDB.ExecContext(ctx, `UPDATE legacy_video_tasks SET lease_until=NOW()-INTERVAL '1 second' WHERE id=$1`, first.ID)
	require.NoError(t, err)
	claimed, err = f.repo.ClaimDue(ctx, time.Now(), 4, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	current := claimed[0]
	require.NotEqual(t, first.LeaseToken, current.LeaseToken)
	require.ErrorIs(t, f.repo.Finish(ctx, first), service.ErrLegacyVideoTaskLeaseLost)
	current.Status = "completed"
	current.SettlementResult = json.RawMessage(`{"usage":{"output_tokens":5}}`)
	require.NoError(t, f.repo.Checkpoint(ctx, current))
	read, err := f.repo.Get(ctx, task.Source, task.TaskID, f.user, f.key)
	require.NoError(t, err)
	require.Equal(t, current.LeaseToken, read.LeaseToken)
	require.Equal(t, "completed", read.Status)
	// 终态证据先落库，随后同租约补充报价；不得被 COALESCE 固定成没有报价的旧证据。
	current.SettlementResult = json.RawMessage(`{"usage":{"output_tokens":5},"quote":{"amount":1.5}}`)
	require.NoError(t, f.repo.Checkpoint(ctx, current))
	read, err = f.repo.Get(ctx, task.Source, task.TaskID, f.user, f.key)
	require.NoError(t, err)
	require.JSONEq(t, string(current.SettlementResult), string(read.SettlementResult))
	bad := *current
	bad.AccountID++
	require.ErrorIs(t, f.repo.Checkpoint(ctx, &bad), service.ErrLegacyVideoTaskLeaseLost)
	bad = *current
	bad.Status = "processing"
	require.ErrorIs(t, f.repo.Finish(ctx, &bad), service.ErrLegacyVideoTaskLeaseLost, "已确认完成不能回退处理中")
	current.BillingStatus = "reconciliation"
	require.NoError(t, f.repo.Finish(ctx, current))
	read, err = f.repo.Get(ctx, task.Source, task.TaskID, f.user, f.key)
	require.NoError(t, err)
	require.Empty(t, read.LeaseToken)
	require.Nil(t, read.LeaseUntil)
	claimed, err = f.repo.ClaimDue(ctx, time.Now(), 4, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1, "待核对不是不可恢复终态")
	claimed[0].BillingStatus = "settled"
	require.NoError(t, f.repo.Finish(ctx, claimed[0]))
	claimed, err = f.repo.ClaimDue(ctx, time.Now(), 4, time.Minute)
	require.NoError(t, err)
	require.Empty(t, claimed, "结算成功不能再次进入后台队列")
}

func TestLegacyVideoRepositoryConcurrentClaims(t *testing.T) {
	f := newLegacyVideoFixture(t)
	ctx := context.Background()
	for range 40 {
		task := f.task()
		require.NoError(t, f.repo.Save(ctx, task))
	}
	future := f.task()
	future.NextPollAt = time.Now().Add(time.Hour)
	require.NoError(t, f.repo.Save(ctx, future))
	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := map[int64]bool{}
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			repo := NewLegacyVideoTaskRepository(integrationDB)
			tasks, err := repo.ClaimDue(ctx, time.Now(), 10, time.Minute)
			if err != nil {
				errs <- err
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, task := range tasks {
				if seen[task.ID] {
					errs <- fmt.Errorf("重复领取任务 %d", task.ID)
					return
				}
				seen[task.ID] = true
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Len(t, seen, 40)
	require.False(t, seen[future.ID])
}

func TestLegacyVideoRepositoryBackfillAndBillingEvidence(t *testing.T) {
	f := newLegacyVideoFixture(t)
	ctx := context.Background()
	media := NewMediaTaskRepository(integrationDB)
	for _, tc := range []struct {
		source, status, evidence string
		old                      bool
		emptyRequest             bool
	}{
		{"grok_video", "queued", "", false, false},
		{"seedance_video", "processing", "", false, true},
		{"grok_video", "completed", "", false, true},
		{"grok_video", "completed", "usage", false, false},
		{"grok_video", "completed", "dedup", false, false},
		{"seedance_video", "completed", "archive", false, true},
		{"grok_video", "queued", "", true, false},
		{"grok_video", "failed", "", false, false},
	} {
		id := uuid.NewString()
		key := id
		if tc.source == "seedance_video" {
			key = service.SeedanceTaskKey(id)
		}
		requestID := service.StableGrokVideoBillingRequestID(key)
		created := time.Now().Add(-time.Hour)
		if tc.old {
			created = time.Now().Add(-8 * 24 * time.Hour)
		}
		o := service.MediaTaskObservation{Source: tc.source, TaskID: id, MediaType: "video", Platform: "grok", Model: "grok-imagine-video",
			Status: tc.status, UserID: f.user, APIKeyID: f.key, GroupID: &f.group, AccountID: &f.account, RequestID: requestID, CreatedAt: created}
		if tc.emptyRequest {
			o.RequestID = ""
		}
		require.NoError(t, media.Observe(ctx, o))
		switch tc.evidence {
		case "usage":
			_, err := integrationDB.ExecContext(ctx, `INSERT INTO usage_logs(user_id,api_key_id,account_id,model,request_id,actual_cost) VALUES($1,$2,$3,'test',$4,1)`, f.user, f.key, f.account, requestID)
			require.NoError(t, err)
			present, err := f.repo.HasUsage(ctx, requestID, f.key)
			require.NoError(t, err)
			require.True(t, present)
		case "dedup", "archive":
			table := "usage_billing_dedup"
			if tc.evidence == "archive" {
				table += "_archive"
			}
			_, err := integrationDB.ExecContext(ctx, `INSERT INTO `+table+`(request_id,api_key_id,request_fingerprint,created_at) VALUES($1,$2,'test',NOW())`, requestID, f.key)
			require.NoError(t, err)
			present, err := f.repo.HasBilling(ctx, requestID, f.key)
			require.NoError(t, err)
			require.True(t, present)
		}
	}
	n, err := f.repo.Backfill(ctx, 100)
	require.NoError(t, err)
	require.Equal(t, 3, n)
	n, err = f.repo.Backfill(ctx, 100)
	require.NoError(t, err)
	require.Zero(t, n)
	tasks, err := f.repo.ClaimDue(ctx, time.Now(), 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, tasks, 3)
	for _, task := range tasks {
		require.Nil(t, task.Pending)
		require.Zero(t, task.BillingUserID, "历史归属不得猜测付款人")
		require.NotEmpty(t, task.RequestID)
	}
}

func TestLegacyVideoRepositoryBillingReceiptAtomicAndIdempotent(t *testing.T) {
	f := newLegacyVideoFixture(t)
	ctx := context.Background()
	task := f.task()
	require.NoError(t, f.repo.Save(ctx, task))
	// 让订阅覆盖一部分，余额使用独立倍率，重复恢复必须拿回原资金分配。
	plan := mustCreatePlan(t, integrationEntClient, &service.SubscriptionPlan{Name: "legacy-receipt-" + uuid.NewString(), Price: 10,
		ValidityDays: 30, ValidityUnit: "day", GroupIDs: []int64{f.group}, GroupRateMultipliers: map[int64]float64{f.group: 1},
		DailyLimitUSD: float64Ptr(1), WeeklyLimitUSD: float64Ptr(1), MonthlyLimitUSD: float64Ptr(1)})
	sub := mustCreateSubscription(t, integrationEntClient, &service.UserSubscription{UserID: f.user, PlanID: plan.ID,
		DailyLimitUSD: float64Ptr(1), WeeklyLimitUSD: float64Ptr(1), MonthlyLimitUSD: float64Ptr(1)})
	billing := NewUsageBillingRepository(integrationEntClient, integrationDB)
	cmd := service.UsageBillingCommand{LegacyVideoTaskID: task.ID, RequestID: task.RequestID, APIKeyID: f.key, UserID: f.user,
		ActorUserID: f.user, AccountID: f.account, GroupID: &f.group, APIKeyBillingMode: service.APIKeyBillingModeAuto,
		BillableAmountUSD: 3, BaseAmountUSD: 3, SubscriptionRateMultiplier: 1, SubscriptionRateMultiplierScale: 1, BalanceRateMultiplier: 0.5}
	first, err := billing.Apply(ctx, &cmd)
	require.NoError(t, err)
	require.True(t, first.Applied)
	require.InDelta(t, 1, first.SubscriptionAmountUSD, 1e-8)
	require.InDelta(t, 1, first.BalanceAmountUSD, 1e-8)
	var raw []byte
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT billing_result FROM legacy_video_tasks WHERE id=$1`, task.ID).Scan(&raw))
	require.NotEmpty(t, raw)
	second, err := billing.Apply(ctx, &cmd)
	require.NoError(t, err)
	require.False(t, second.Applied)
	require.Equal(t, first.SubscriptionAmountUSD, second.SubscriptionAmountUSD)
	require.Equal(t, first.BalanceAmountUSD, second.BalanceAmountUSD)
	var balance, subUsage float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, f.user).Scan(&balance))
	require.InDelta(t, 99, balance, 1e-8)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT daily_usage_usd FROM user_subscriptions WHERE id=$1`, sub.ID).Scan(&subUsage))
	require.InDelta(t, 1, subUsage, 1e-8)
	// 伪造同归属的另一个任务 ID 也必须失败，不能给无关任务保存当前请求的付款回执。
	other := f.task()
	require.NoError(t, f.repo.Save(ctx, other))
	for _, taskID := range []int64{other.ID, other.ID + 1000000} {
		bad := cmd
		bad.LegacyVideoTaskID = taskID
		bad.RequestID, bad.RequestFingerprint = "grok-video:"+uuid.NewString(), ""
		_, err := billing.Apply(ctx, &bad)
		require.Error(t, err)
		present, err := f.repo.HasBilling(ctx, bad.RequestID, f.key)
		require.NoError(t, err)
		require.False(t, present, "错误归属必须连同幂等键一起回滚")
	}
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, f.user).Scan(&balance))
	require.InDelta(t, 99, balance, 1e-8, "错误归属不得先扣款后报错")
}

// 账号软删除只停止旧异步任务的账号统计，用户扣款及去重回执仍须一次性完成。
func TestLegacyVideoRepositoryDeletedAccountStillSettlesAcceptedTask(t *testing.T) {
	f := newLegacyVideoFixture(t)
	ctx := context.Background()
	task := f.task()
	require.NoError(t, f.repo.Save(ctx, task))
	_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET deleted_at=NOW() WHERE id=$1`, f.account)
	require.NoError(t, err)
	billing := NewUsageBillingRepository(integrationEntClient, integrationDB)
	cmd := service.UsageBillingCommand{LegacyVideoTaskID: task.ID, RequestID: task.RequestID, APIKeyID: f.key, UserID: f.user,
		ActorUserID: f.user, AccountID: f.account, GroupID: &f.group, APIKeyBillingMode: service.APIKeyBillingModeBalance,
		BillableAmountUSD: 1, BaseAmountUSD: 1, BalanceRateMultiplier: 1,
		AccountType: service.AccountTypeAPIKey, AccountQuotaCost: 1}
	first, err := billing.Apply(ctx, &cmd)
	require.NoError(t, err)
	require.True(t, first.Applied)
	require.InDelta(t, 1, first.BalanceAmountUSD, 1e-8)
	require.Nil(t, first.QuotaState)
	second, err := billing.Apply(ctx, &cmd)
	require.NoError(t, err)
	require.False(t, second.Applied)
	require.Equal(t, first.BalanceAmountUSD, second.BalanceAmountUSD)
	var balance, accountUsage float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, f.user).Scan(&balance))
	require.InDelta(t, 99, balance, 1e-8)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COALESCE((extra->>'quota_used')::numeric,0) FROM accounts WHERE id=$1`, f.account).Scan(&accountUsage))
	require.Zero(t, accountUsage)
	// 通用请求没有旧视频持久归属时仍保持原来的严格账号校验，并整体回滚。
	ordinary := cmd
	ordinary.LegacyVideoTaskID = 0
	ordinary.RequestID, ordinary.RequestFingerprint = "ordinary-deleted-account-"+uuid.NewString(), ""
	_, err = billing.Apply(ctx, &ordinary)
	require.ErrorIs(t, err, service.ErrAccountNotFound)
	present, err := f.repo.HasBilling(ctx, ordinary.RequestID, f.key)
	require.NoError(t, err)
	require.False(t, present)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, f.user).Scan(&balance))
	require.InDelta(t, 99, balance, 1e-8)
}

// 回填抢先保存空快照时，正式创建补齐快照应唤醒恢复，但普通重复保存不能绕过退避。
func TestLegacyVideoRepositorySnapshotBackfillWakesDueWithoutChangingLease(t *testing.T) {
	f := newLegacyVideoFixture(t)
	ctx := context.Background()
	for _, status := range []string{"pending", "reconciliation", "settled", "not_billable"} {
		t.Run(status, func(t *testing.T) {
			task := f.task()
			pending := task.Pending
			task.Pending = nil
			task.BillingStatus = status
			task.NextPollAt = time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
			require.NoError(t, f.repo.Save(ctx, task))
			future := task.NextPollAt
			leaseToken := uuid.NewString()
			leaseUntil := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
			_, err := integrationDB.ExecContext(ctx, `UPDATE legacy_video_tasks SET lease_token=$2,lease_until=$3 WHERE id=$1`, task.ID, leaseToken, leaseUntil)
			require.NoError(t, err)
			task.Pending = pending
			require.NoError(t, f.repo.Save(ctx, task))
			require.Equal(t, leaseToken, task.LeaseToken)
			require.NotNil(t, task.LeaseUntil)
			require.True(t, leaseUntil.Equal(*task.LeaseUntil), "补齐快照不能废弃当前执行者的租约")
			require.Equal(t, status, task.BillingStatus)
			if status == "pending" || status == "reconciliation" {
				require.WithinDuration(t, time.Now(), task.NextPollAt, 5*time.Second)
			} else {
				require.True(t, future.Equal(task.NextPollAt), "计费终态不得被重新唤醒")
			}
			_, err = integrationDB.ExecContext(ctx, `UPDATE legacy_video_tasks SET next_poll_at=$2 WHERE id=$1`, task.ID, future)
			require.NoError(t, err)
			require.NoError(t, f.repo.Save(ctx, task))
			require.True(t, future.Equal(task.NextPollAt), "已有快照的重复 Save 必须保留退避")
		})
	}
}
