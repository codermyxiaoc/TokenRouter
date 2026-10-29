//go:build integration

package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// creativeBillingFixture 建立一笔创作台任务的预占，用于验证捕获与释放互斥。
type creativeBillingFixture struct {
	t                      *testing.T
	repo                   *usageBillingRepository
	userID, keyID, groupID int64
	runID                  string
	hold                   service.BatchImageBalanceHoldCommand
}

func newCreativeBillingFixture(t *testing.T) *creativeBillingFixture {
	t.Helper()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{Email: "creative-billing-" + uuid.NewString() + "@example.com", Balance: 1000})
	group := mustCreateGroup(t, client, &service.Group{Name: "creative-" + uuid.NewString(), Platform: service.PlatformOpenAI, RateMultiplier: 1})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-creative-" + uuid.NewString(), Name: "creative-billing", GroupID: &group.ID, Quota: 1000, RateLimit5h: 1000, RateLimit1d: 1000, RateLimit7d: 1000})
	runID := "crun_" + uuid.NewString()
	_, err := integrationDB.ExecContext(context.Background(), `INSERT INTO creative_runs
		(run_id,user_id,group_id,api_key_id,model,operation,prompt_hash,request_fingerprint,status,
		 estimated_cost,hold_amount,balance_hold_amount,base_unit_price,subscription_rate_multiplier,balance_rate_multiplier)
		VALUES($1,$2,$3,$4,'gemini-3.1-flash-image-preview','generate','hash',$1,'provider_succeeded',0.0603,0.0603,0.0603,0.0603,1,1)`,
		runID, user.ID, group.ID, key.ID)
	require.NoError(t, err)
	hold := service.BatchImageBalanceHoldCommand{
		RequestID: service.CreativeHoldRequestID(runID), BatchID: runID, UserID: user.ID, ActorUserID: user.ID,
		APIKeyID: key.ID, GroupID: &group.ID, APIKeyBillingMode: service.APIKeyBillingModeAuto,
		PricingSnapshotVersion: 2 /* 与 buildCreativeHoldCommand 一致 */, BaseAmountUSD: 0.0603,
		SubscriptionRateMultiplier: 1, SubscriptionRateMultiplierScale: 1, BalanceRateMultiplier: 1, SettlementRateScale: 1,
		BalanceHoldAmount: 0.0603, CreativeEntity: true, ReservedAt: time.Now().UTC(),
	}
	return &creativeBillingFixture{t: t, repo: NewUsageBillingRepository(client, integrationDB).(*usageBillingRepository),
		userID: user.ID, keyID: key.ID, groupID: group.ID, runID: runID, hold: hold}
}

func (f *creativeBillingFixture) command(requestID string, actualBase float64) *service.BatchImageBalanceHoldCommand {
	cmd := f.hold
	cmd.RequestID = requestID
	cmd.ActualBaseAmountUSD = actualBase
	return &cmd
}

func (f *creativeBillingFixture) money(balance, frozen float64) {
	f.t.Helper()
	var b, h float64
	require.NoError(f.t, integrationDB.QueryRowContext(context.Background(), `SELECT balance,frozen_balance FROM users WHERE id=$1`, f.userID).Scan(&b, &h))
	require.InDelta(f.t, balance, b, 1e-8)
	require.InDelta(f.t, frozen, h, 1e-8)
}

// 线上事故回归：已捕获的任务在十天后素材过期，恢复路径再次按原 hold 释放，把共享冻结池多减 0.0603，
// 导致后续视频预留不足而永远无法结算。互斥守卫必须拒绝“捕获后再释放”，且不改变任何资金。
func TestCreativeBillingReleaseAfterCaptureIsRejected(t *testing.T) {
	ctx := context.Background()
	f := newCreativeBillingFixture(t)
	_, err := f.repo.ReserveBatchImageBalance(ctx, &f.hold)
	require.NoError(t, err)
	f.money(1000-0.0603, 0.0603)

	result, err := f.repo.CaptureBatchImageBalance(ctx, f.command(service.CreativeCaptureRequestID(f.runID), 0.0603))
	require.NoError(t, err)
	require.InDelta(t, 0.0603, result.ActualAmountUSD, 1e-10)
	f.money(1000-0.0603, 0)

	// 十天后的恢复释放必须失败，且资金保持已捕获状态。
	_, err = f.repo.ReleaseBatchImageBalance(ctx, f.command(service.CreativeReleaseRequestID(f.runID), 0))
	require.ErrorIs(t, err, service.ErrBatchImageHoldAlreadyCaptured)
	f.money(1000-0.0603, 0)

	// 重复捕获仍是幂等的，不会二次扣款。
	_, err = f.repo.CaptureBatchImageBalance(ctx, f.command(service.CreativeCaptureRequestID(f.runID), 0.0603))
	require.NoError(t, err)
	f.money(1000-0.0603, 0)

	recorded, err := f.repo.UsageBillingRequestRecorded(ctx, service.CreativeCaptureRequestID(f.runID), f.keyID)
	require.NoError(t, err)
	require.True(t, recorded, "恢复路径据此识别已扣款任务，不能再按未交付释放")
	recorded, err = f.repo.UsageBillingRequestRecorded(ctx, service.CreativeReleaseRequestID(f.runID), f.keyID)
	require.NoError(t, err)
	require.False(t, recorded)
}

// 反向顺序同样互斥：已释放的预占不能再被捕获。
func TestCreativeBillingCaptureAfterReleaseIsRejected(t *testing.T) {
	ctx := context.Background()
	f := newCreativeBillingFixture(t)
	_, err := f.repo.ReserveBatchImageBalance(ctx, &f.hold)
	require.NoError(t, err)
	_, err = f.repo.ReleaseBatchImageBalance(ctx, f.command(service.CreativeReleaseRequestID(f.runID), 0))
	require.NoError(t, err)
	f.money(1000, 0)
	_, err = f.repo.CaptureBatchImageBalance(ctx, f.command(service.CreativeCaptureRequestID(f.runID), 0.0603))
	require.ErrorIs(t, err, service.ErrBatchImageHoldAlreadyReleased)
	f.money(1000, 0)
}

// 并发对向动作只能有一方生效，冻结与余额始终对得上。
func TestCreativeBillingCaptureAndReleaseRaceSettlesOnce(t *testing.T) {
	ctx := context.Background()
	f := newCreativeBillingFixture(t)
	_, err := f.repo.ReserveBatchImageBalance(ctx, &f.hold)
	require.NoError(t, err)
	errs := make(chan error, 2)
	go func() {
		_, err := f.repo.CaptureBatchImageBalance(ctx, f.command(service.CreativeCaptureRequestID(f.runID), 0.0603))
		errs <- err
	}()
	go func() {
		_, err := f.repo.ReleaseBatchImageBalance(ctx, f.command(service.CreativeReleaseRequestID(f.runID), 0))
		errs <- err
	}()
	var succeeded, rejected int
	for range 2 {
		err := <-errs
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, service.ErrBatchImageHoldAlreadyCaptured), errors.Is(err, service.ErrBatchImageHoldAlreadyReleased):
			rejected++
		default:
			require.NoError(t, err)
		}
	}
	require.Equal(t, 1, succeeded, "捕获与释放只有一个可以提交")
	require.Equal(t, 1, rejected)
	// 无论哪一方胜出，冻结都必须清零；余额要么已扣款（捕获胜），要么全额退回（释放胜），不会多退或多扣。
	var balance, frozen float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance,frozen_balance FROM users WHERE id=$1`, f.userID).Scan(&balance, &frozen))
	require.InDelta(t, 0, frozen, 1e-8)
	captured, err := f.repo.UsageBillingRequestRecorded(ctx, service.CreativeCaptureRequestID(f.runID), f.keyID)
	require.NoError(t, err)
	if captured {
		require.InDelta(t, 1000-0.0603, balance, 1e-8)
	} else {
		require.InDelta(t, 1000, balance, 1e-8)
	}
}
