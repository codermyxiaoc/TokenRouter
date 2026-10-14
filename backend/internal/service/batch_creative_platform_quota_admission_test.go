//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// 只替换预占调用，验证平台额度错误不会被业务服务包装成内部错误。
type quotaAdmissionBillingStub struct {
	UsageBillingRepository
	err error
}

func (s *quotaAdmissionBillingStub) ReserveBatchImageBalance(context.Context, *BatchImageBalanceHoldCommand) (*BatchImageBalanceHoldResult, error) {
	return nil, s.err
}

func TestBatchCreativeReservePreservesPlatformQuotaError(t *testing.T) {
	for _, quotaErr := range []*infraerrors.ApplicationError{ErrUserPlatformDailyQuotaExhausted, ErrUserPlatformWeeklyQuotaExhausted, ErrUserPlatformMonthlyQuotaExhausted} {
		t.Run(quotaErr.Reason, func(t *testing.T) {
			reset := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
			expected := quotaErr.WithMetadata(map[string]string{"window_resets_at": reset})
			repo := &quotaAdmissionBillingStub{err: expected}
			job := testSettlingBatchImageJob("imgbatch_platform_quota_error")
			err := reserveBatchImageBalanceHold(context.Background(), repo, job, nil, "quota-error")
			require.ErrorIs(t, err, quotaErr)
			require.Equal(t, 429, infraerrors.Code(err))
			require.Equal(t, reset, infraerrors.FromError(err).Metadata["window_resets_at"])
			run := &CreativeRun{RunID: "quota-error", APIKeyID: 1, UserID: 1, GroupID: 1, EstimatedCost: 1, BaseUnitPrice: 1,
				RequestedOutputCount: 1, SubscriptionRateMultiplier: 1, BalanceRateMultiplier: 1}
			err = reserveCreativeBalanceHold(context.Background(), repo, run)
			require.ErrorIs(t, err, quotaErr)
			require.Equal(t, 429, infraerrors.Code(err))
			require.Equal(t, reset, infraerrors.FromError(err).Metadata["window_resets_at"])
		})
	}
}
