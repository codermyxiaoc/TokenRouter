//go:build unit

package handler

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 已成功的上游操作不能因本地捕获失败而退款；保留资源、费用审计与可恢复的资金合同。
func TestGeminiCachedContentsCaptureFailurePreservesRecoverableHold(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			env := newCachedBillingEnv(t, 500, 900)
			env.finance.captureError = errors.New("capture database unavailable")
			var status int
			if method == http.MethodPost {
				env.upstream.createBody = cachedBillingCreateBody(time.Now().Add(24 * time.Hour))
				status = env.create().Code
			} else {
				env.seed(time.Now().Add(time.Hour))
				status = env.patch().Code
				require.False(t, env.repo.pendingMutation(100), "已提交的资源有效期不再持有未完成修改租约")
			}
			require.Equal(t, http.StatusInternalServerError, status)
			require.Equal(t, 1, env.upstream.methodCalls(method))
			require.Zero(t, env.upstream.methodCalls(http.MethodDelete), "不能删除已登记、可用于核对的资源")
			require.Len(t, env.repo.records, 1)
			require.Len(t, env.finance.reservations, 1)
			require.Equal(t, 1, env.finance.captureCalls)
			require.Equal(t, 1, env.finance.reconciliationCalls)
			require.Zero(t, env.finance.releaseCalls, "未知结算结果不能走退出时退款")
			require.Len(t, env.usage.totals(), 1)
			require.Zero(t, env.usage.first().ActualCost, "使用记录不得把失败捕获记作已扣款")
			for _, hold := range env.finance.reservations {
				require.Equal(t, service.ImageBillingReconciliation, hold.State)
				require.InDelta(t, 500-hold.Hold.HoldAmount, env.finance.balance, 1e-9)
				// 管理员核对后使用同一合同捕获，重放不能再扣第二次。
				env.finance.captureError = nil
				result, err := env.finance.CaptureImageBilling(context.Background(), hold.ID, &service.UsageBillingCommand{}, hold.Hold.BaseAmountUSD)
				require.NoError(t, err)
				require.True(t, result.Applied)
				balanceAfterCapture := env.finance.balance
				result, err = env.finance.CaptureImageBilling(context.Background(), hold.ID, &service.UsageBillingCommand{}, hold.Hold.BaseAmountUSD)
				require.NoError(t, err)
				require.False(t, result.Applied)
				require.Equal(t, balanceAfterCapture, env.finance.balance)
			}
		})
	}
}

// 连待核对标记也暂时写不进去时，原始持久预留仍保留，不能释放成已退款。
func TestGeminiCachedContentsReconciliationFailureKeepsOriginalReservation(t *testing.T) {
	env := newCachedBillingEnv(t, 500, 900)
	env.finance.captureError = errors.New("capture database unavailable")
	env.finance.reconciliationError = errors.New("reconciliation database unavailable")
	env.upstream.createBody = cachedBillingCreateBody(time.Now().Add(24 * time.Hour))
	response := env.create()
	require.Equal(t, http.StatusInternalServerError, response.Code)
	require.Len(t, env.repo.records, 1)
	require.Len(t, env.finance.reservations, 1)
	require.Equal(t, 1, env.finance.captureCalls)
	require.Equal(t, 1, env.finance.reconciliationCalls)
	require.Zero(t, env.finance.releaseCalls)
	require.Zero(t, env.usage.first().ActualCost)
	for _, hold := range env.finance.reservations {
		require.Equal(t, service.ImageBillingReserved, hold.State)
		require.False(t, hold.ExpiresAt.IsZero(), "保留过期扫描恢复所需的持久时间")
		require.InDelta(t, 500-hold.Hold.HoldAmount, env.finance.balance, 1e-9)
		env.finance.reconciliationError = nil
		require.NoError(t, env.finance.MarkImageBillingReconciliation(context.Background(), hold.ID, hold.Hold.UserID, hold.Hold.APIKeyID))
		require.Equal(t, service.ImageBillingReconciliation, hold.State)
	}
}

// 资源落库失败且上游清理成功时，释放未捕获的费用，不给用户留下不存在的缓存或重复扣款。
func TestGeminiCachedContentsSaveFailureCleansUpAndReleasesUncapturedHold(t *testing.T) {
	env := newCachedBillingEnv(t, 500, 900)
	env.repo.failCreate = true
	env.upstream.createBody = cachedBillingCreateBody(time.Now().Add(24 * time.Hour))
	response := env.create()
	require.Equal(t, http.StatusInternalServerError, response.Code)
	require.Equal(t, 1, env.upstream.methodCalls(http.MethodPost))
	require.Equal(t, 1, env.upstream.methodCalls(http.MethodDelete))
	require.Empty(t, env.repo.records)
	require.Empty(t, env.usage.totals())
	require.Zero(t, env.finance.captureCalls)
	require.Equal(t, 1, env.finance.releaseCalls)
	require.Equal(t, 500.0, env.finance.balance)
	require.Len(t, env.finance.reservations, 1)
	for _, hold := range env.finance.reservations {
		require.Equal(t, service.ImageBillingReleased, hold.State)
		require.NoError(t, env.finance.ReleaseImageBilling(context.Background(), hold.ID, hold.Hold.UserID, hold.Hold.APIKeyID))
		require.Equal(t, 500.0, env.finance.balance, "重复退出清理不得多退余额")
	}
}

// 创建落库与上游删除同时失败时，仍然存在的资源成本不能通过默认退出释放资金。
func TestGeminiCachedContentsSaveAndCleanupFailureKeepsHoldForReconciliation(t *testing.T) {
	for _, mode := range []string{"http_503", "transport_timeout", "reconciliation_unavailable"} {
		t.Run(mode, func(t *testing.T) {
			env := newCachedBillingEnv(t, 500, 900)
			env.repo.failCreate = true
			env.upstream.createBody = cachedBillingCreateBody(time.Now().Add(24 * time.Hour))
			if mode == "http_503" {
				env.upstream.failures = map[string]cachedBillingUpstreamFailure{http.MethodDelete: {
					status: http.StatusServiceUnavailable, body: `{"error":{"code":503,"message":"unavailable","status":"UNAVAILABLE"}}`,
				}}
			} else {
				env.upstream.transportErrors = map[string]error{http.MethodDelete: context.DeadlineExceeded}
			}
			wantState := service.ImageBillingReconciliation
			if mode == "reconciliation_unavailable" {
				env.finance.reconciliationError = errors.New("reconciliation database unavailable")
				wantState = service.ImageBillingReserved
			}
			response := env.create()
			require.Equal(t, http.StatusInternalServerError, response.Code)
			require.Equal(t, 1, env.upstream.methodCalls(http.MethodPost))
			require.Equal(t, 1, env.upstream.methodCalls(http.MethodDelete), "清理失败不重建上游资源")
			require.Empty(t, env.repo.records)
			require.Empty(t, env.usage.totals(), "未捕获不能伪造成功扣费")
			require.Zero(t, env.finance.captureCalls)
			require.Zero(t, env.finance.releaseCalls, "不能将未确认删除当成创建未生效而退款")
			require.Equal(t, 1, env.finance.reconciliationCalls)
			require.Len(t, env.finance.reservations, 1)
			for _, hold := range env.finance.reservations {
				require.Equal(t, wantState, hold.State)
				require.InDelta(t, 500-hold.Hold.HoldAmount, env.finance.balance, 1e-9)
				require.False(t, hold.ExpiresAt.IsZero())
			}
		})
	}
}
