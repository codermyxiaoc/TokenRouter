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

type creativeTextBillingFixture struct {
	identity *legacyVideoFixture
	command  service.UsageBillingCommand
	repo     service.UsageBillingRepository
}

// 复用隔离资金夹具；文本执行不创建任何上游请求，证据只包含安全账务对象。
func newCreativeTextBillingFixture(t *testing.T, configure func(*legacyVideoFixture, *service.UsageBillingCommand)) *creativeTextBillingFixture {
	t.Helper()
	f := newLegacyVideoFixture(t)
	ctx := context.Background()
	insertSettlementPlatformQuota(t, f.user, service.PlatformGrok, float64Ptr(100))
	executionID, canvasID := uuid.NewString(), uuid.NewString()
	cmd := service.UsageBillingCommand{CreativeTextExecutionID: executionID, RequestID: "creative_text:" + executionID,
		UserID: f.user, ActorUserID: f.user, APIKeyID: f.key, GroupID: &f.group, AccountID: f.account,
		QuotaPlatform: service.PlatformGrok, AccountType: service.AccountTypeAPIKey, Model: "grok-4.5",
		APIKeyBillingMode: service.APIKeyBillingModeBalance, BaseAmountUSD: 0.5, BillableAmountUSD: 0.25,
		BalanceRateMultiplier: 0.5, SubscriptionRateMultiplier: 1, SubscriptionRateMultiplierScale: 1,
		APIKeyQuotaCost: 0.25, APIKeyRateLimitCost: 0.25, AccountQuotaCost: 0.75, InputTokens: 100, OutputTokens: 30}
	if configure != nil {
		configure(f, &cmd)
	}
	cmd.Normalize()
	usage := service.UsageLog{UserID: f.user, BillingUserID: f.user, APIKeyID: f.key, GroupID: &f.group,
		AccountID: f.account, Model: cmd.Model, RequestID: cmd.RequestID, InputTokens: cmd.InputTokens, OutputTokens: cmd.OutputTokens,
		TotalCost: cmd.BaseAmountUSD, ActualCost: cmd.BillableAmountUSD, CreatedAt: time.Now()}
	evidence, err := json.Marshal(creativeTextBillingEvidenceRecord{Version: 1, Command: cmd, Usage: usage})
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO creative_canvases(id,user_id,title,document) VALUES($1,$2,'文本资金隔离测试','{"nodes":[{"id":"text","type":"text"}],"connections":[],"viewport":{"x":0,"y":0,"k":1}}')`, canvasID, f.user)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO creative_canvas_executions(id,user_id,canvas_id,node_id,kind,params,idempotency_key,fingerprint,text_phase,text_billing_evidence) VALUES($1,$2,$3,'text','text','{}',$4,$5,'settlement_pending',$6)`, executionID, f.user, canvasID, executionID, cmd.RequestFingerprint, string(evidence))
	require.NoError(t, err)
	return &creativeTextBillingFixture{identity: f, command: cmd, repo: NewUsageBillingRepository(integrationEntClient, integrationDB)}
}

func (f *creativeTextBillingFixture) assertMoney(t *testing.T, spent, keyCost, accountCost float64) {
	t.Helper()
	ctx := context.Background()
	var balance, quota, rate, account float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, f.identity.user).Scan(&balance))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT quota_used,usage_5h FROM api_keys WHERE id=$1`, f.identity.key).Scan(&quota, &rate))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COALESCE((extra->>'quota_used')::numeric,0) FROM accounts WHERE id=$1`, f.identity.account).Scan(&account))
	require.InDelta(t, 100-spent, balance, 1e-8)
	require.InDelta(t, keyCost, quota, 1e-8)
	require.InDelta(t, keyCost, rate, 1e-8)
	require.InDelta(t, accountCost, account, 1e-8)
	assertSettlementPlatformQuota(t, f.identity.user, service.PlatformGrok, spent)
}

func TestCreativeTextBillingConcurrentReceiptAndArchivedReplayIntegration(t *testing.T) {
	f := newCreativeTextBillingFixture(t, nil)
	ctx := context.Background()
	type outcome struct {
		result *service.UsageBillingApplyResult
		err    error
	}
	outcomes := make(chan outcome, 24)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			cmd := f.command
			result, err := f.repo.Apply(ctx, &cmd)
			outcomes <- outcome{result, err}
		}()
	}
	close(start)
	wg.Wait()
	close(outcomes)
	applied := 0
	for result := range outcomes {
		require.NoError(t, result.err)
		require.NotNil(t, result.result)
		if result.result.Applied {
			applied++
		}
		require.InDelta(t, 0.25, result.result.BalanceAmountUSD, 1e-8)
		require.Zero(t, result.result.SubscriptionAmountUSD)
		require.NotEmpty(t, result.result.BillingAllocations)
	}
	require.Equal(t, 1, applied)
	f.assertMoney(t, 0.25, 0.25, 0.75)
	var receipt []byte
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT text_billing_result FROM creative_canvas_executions WHERE id=$1`, f.command.CreativeTextExecutionID).Scan(&receipt))
	var stored service.UsageBillingApplyResult
	require.NoError(t, json.Unmarshal(receipt, &stored))
	require.True(t, stored.Applied)
	_, err := integrationDB.ExecContext(ctx, `WITH moved AS (DELETE FROM usage_billing_dedup WHERE request_id=$1 AND api_key_id=$2 RETURNING request_id,api_key_id,request_fingerprint,created_at) INSERT INTO usage_billing_dedup_archive(request_id,api_key_id,request_fingerprint,created_at) SELECT request_id,api_key_id,request_fingerprint,created_at FROM moved`, f.command.RequestID, f.command.APIKeyID)
	require.NoError(t, err)
	replay, err := NewUsageBillingRepository(integrationEntClient, integrationDB).Apply(ctx, &f.command)
	require.NoError(t, err)
	stored.Applied = false
	require.Equal(t, stored, *replay)
	f.assertMoney(t, 0.25, 0.25, 0.75)
}

func TestCreativeTextBillingReceiptFailureRollsBackIntegration(t *testing.T) {
	f := newCreativeTextBillingFixture(t, nil)
	ctx := context.Background()
	// 限制只针对当前 UUID 的回执写入，模拟资金事务最后一步失败。
	_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE creative_canvas_executions ADD CONSTRAINT test_text_receipt_fault CHECK (id <> '%s'::uuid OR text_billing_result IS NULL)`, f.command.CreativeTextExecutionID))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `ALTER TABLE creative_canvas_executions DROP CONSTRAINT IF EXISTS test_text_receipt_fault`)
	})
	_, err = f.repo.Apply(ctx, &f.command)
	require.Error(t, err)
	f.assertMoney(t, 0, 0, 0)
	var dedup int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM usage_billing_dedup WHERE request_id=$1 AND api_key_id=$2`, f.command.RequestID, f.command.APIKeyID).Scan(&dedup))
	require.Zero(t, dedup)
	_, err = integrationDB.ExecContext(ctx, `ALTER TABLE creative_canvas_executions DROP CONSTRAINT test_text_receipt_fault`)
	require.NoError(t, err)
	result, err := f.repo.Apply(ctx, &f.command)
	require.NoError(t, err)
	require.True(t, result.Applied)
	f.assertMoney(t, 0.25, 0.25, 0.75)
}

func TestCreativeTextBillingEvidenceMismatchRejectedIntegration(t *testing.T) {
	for _, field := range []string{"user", "group", "key", "account", "amount_same_fingerprint", "missing_evidence", "wrong_kind"} {
		t.Run(field, func(t *testing.T) {
			f := newCreativeTextBillingFixture(t, nil)
			ctx := context.Background()
			changed := f.command
			switch field {
			case "user":
				other := mustCreateUser(t, integrationEntClient, &service.User{Balance: 100})
				changed.UserID, changed.ActorUserID = other.ID, other.ID
			case "group":
				id := *changed.GroupID + 1000
				changed.GroupID = &id
			case "key":
				changed.APIKeyID += 1000
			case "account":
				changed.AccountID += 1000
			case "amount_same_fingerprint":
				changed.BaseAmountUSD = 8
			case "missing_evidence":
				_, err := integrationDB.ExecContext(ctx, `UPDATE creative_canvas_executions SET text_billing_evidence=NULL WHERE id=$1`, changed.CreativeTextExecutionID)
				require.NoError(t, err)
			case "wrong_kind":
				_, err := integrationDB.ExecContext(ctx, `UPDATE creative_canvas_executions SET kind='image' WHERE id=$1`, changed.CreativeTextExecutionID)
				require.NoError(t, err)
			}
			_, err := f.repo.Apply(ctx, &changed)
			require.ErrorIs(t, err, service.ErrUsageBillingRequestConflict)
			f.assertMoney(t, 0, 0, 0)
			var count int
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM usage_billing_dedup WHERE request_id=$1`, f.command.RequestID).Scan(&count))
			require.Zero(t, count)
		})
	}
}

func TestCreativeTextBillingMixedSubscriptionReceiptReplayIntegration(t *testing.T) {
	var subID int64
	f := newCreativeTextBillingFixture(t, func(identity *legacyVideoFixture, cmd *service.UsageBillingCommand) {
		client := integrationEntClient
		plan := mustCreatePlan(t, client, &service.SubscriptionPlan{Name: "text-mixed-" + uuid.NewString(), GroupIDs: []int64{identity.group}, Price: 1, ValidityDays: 30, ValidityUnit: "day", DailyLimitUSD: float64Ptr(0.1)})
		window := time.Now().Add(-time.Hour)
		sub := mustCreateSubscription(t, client, &service.UserSubscription{UserID: identity.user, PlanID: plan.ID, DailyLimitUSD: float64Ptr(0.1), DailyWindowStart: &window})
		subID = sub.ID
		cmd.APIKeyBillingMode = service.APIKeyBillingModeAuto
		cmd.BaseAmountUSD, cmd.BillableAmountUSD, cmd.BalanceRateMultiplier = 0.5, 0.5, 1
		cmd.APIKeyQuotaCost, cmd.APIKeyRateLimitCost = 0.5, 0.5
	})
	ctx := context.Background()
	first, err := f.repo.Apply(ctx, &f.command)
	require.NoError(t, err)
	require.InDelta(t, 0.1, first.SubscriptionAmountUSD, 1e-8)
	require.InDelta(t, 0.4, first.BalanceAmountUSD, 1e-8)
	require.Len(t, first.BillingAllocations, 2)
	f.assertMoney(t, 0.4, 0.5, 0.75)
	// 已扣款后套餐被停用，重放仍返回第一次真实资金分配而非按当前套餐重新计算。
	_, err = integrationDB.ExecContext(ctx, `UPDATE user_subscriptions SET expires_at=NOW()-INTERVAL '1 day' WHERE id=$1`, subID)
	require.NoError(t, err)
	replay, err := f.repo.Apply(ctx, &f.command)
	require.NoError(t, err)
	first.Applied = false
	require.Equal(t, *first, *replay)
	f.assertMoney(t, 0.4, 0.5, 0.75)
	var usage float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT daily_usage_usd FROM user_subscriptions WHERE id=$1`, subID).Scan(&usage))
	require.InDelta(t, 0.1, usage, 1e-8)
}

func TestCreativeTextBillingMissingReceiptNeverChargesAgainIntegration(t *testing.T) {
	f := newCreativeTextBillingFixture(t, nil)
	ctx := context.Background()
	_, err := f.repo.Apply(ctx, &f.command)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE creative_canvas_executions SET text_billing_result=NULL WHERE id=$1`, f.command.CreativeTextExecutionID)
	require.NoError(t, err)
	_, err = f.repo.Apply(ctx, &f.command)
	require.Error(t, err)
	f.assertMoney(t, 0.25, 0.25, 0.75)
}

func TestCreativeTextBillingReceiptSurvivesMissingDedupIntegration(t *testing.T) {
	f := newCreativeTextBillingFixture(t, nil)
	ctx := context.Background()
	_, err := f.repo.Apply(ctx, &f.command)
	require.NoError(t, err)
	// 即使异常清理丢了热去重键，已持久化回执仍必须阻止再次收费。
	_, err = integrationDB.ExecContext(ctx, `DELETE FROM usage_billing_dedup WHERE request_id=$1 AND api_key_id=$2`, f.command.RequestID, f.command.APIKeyID)
	require.NoError(t, err)
	_, err = f.repo.Apply(ctx, &f.command)
	require.ErrorIs(t, err, service.ErrUsageBillingRequestConflict)
	f.assertMoney(t, 0.25, 0.25, 0.75)
	var claimed int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM usage_billing_dedup WHERE request_id=$1 AND api_key_id=$2`, f.command.RequestID, f.command.APIKeyID).Scan(&claimed))
	require.Zero(t, claimed, "异常重放不得遗留第二次认领")
}
