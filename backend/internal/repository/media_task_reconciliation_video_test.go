//go:build unit

package repository

import (
	"math"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/domain"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

func TestVideoMediaTaskBillingIdentityKeepsTeamPayer(t *testing.T) {
	group, account := int64(3), int64(4)
	media := &service.MediaTask{Source: "video", TaskID: "vid-test", UserID: 1, APIKeyID: 2, GroupID: &group, AccountID: &account}
	task := &service.VideoTaskRecord{ID: media.TaskID, UserID: 1, APIKeyID: 2, GroupID: 3, AccountID: 4,
		Hold: service.BatchImageBalanceHoldCommand{VideoEntity: true, UserID: 99, ActorUserID: 1, BatchID: media.TaskID, APIKeyID: 2, GroupID: &group, VideoAccountID: 4}}
	require.True(t, videoMediaTaskBillingIdentityMatches(media, task))
	for _, mutate := range []func(*service.VideoTaskRecord){
		func(v *service.VideoTaskRecord) { v.Hold.ActorUserID = v.Hold.UserID },
		func(v *service.VideoTaskRecord) { v.Hold.UserID = 0 },
		func(v *service.VideoTaskRecord) { v.Hold.APIKeyID++ },
		func(v *service.VideoTaskRecord) { v.Hold.BatchID += "-other" },
		func(v *service.VideoTaskRecord) { v.Hold.VideoAccountID++ },
		func(v *service.VideoTaskRecord) { v.Hold.GroupID = nil },
		func(v *service.VideoTaskRecord) { v.AccountID++ },
		func(v *service.VideoTaskRecord) { v.UserID++ },
	} {
		changed := *task
		mutate(&changed)
		require.False(t, videoMediaTaskBillingIdentityMatches(media, &changed))
	}
}

func TestVideoMediaTaskReservedAmountsRejectsMissingOrInvalidFunds(t *testing.T) {
	subID := int64(5)
	base := service.BatchImageBalanceHoldCommand{HoldAmount: 2.7, BalanceHoldAmount: 1.7, VideoTokenPrepay: true,
		SubscriptionHoldAllocations: []domain.BillingAllocation{{Type: domain.BillingAllocationTypeSubscription, SubscriptionID: &subID, AmountUSD: 1}}}
	result, err := videoMediaTaskReservedAmounts(&base)
	require.NoError(t, err)
	require.Equal(t, 2.7, result.ActualAmountUSD)
	require.Equal(t, 1.0, result.SubscriptionAmountUSD)
	require.Equal(t, 1.7, result.BalanceAmountUSD)
	require.Len(t, result.BillingAllocations, 2)
	for _, mutate := range []func(*service.BatchImageBalanceHoldCommand){
		func(v *service.BatchImageBalanceHoldCommand) { v.BalanceHoldAmount = math.NaN() },
		func(v *service.BatchImageBalanceHoldCommand) { v.HoldAmount = math.Inf(1) },
		func(v *service.BatchImageBalanceHoldCommand) { v.HoldAmount = -1 },
		func(v *service.BatchImageBalanceHoldCommand) { v.HoldAmount = 3 },
		func(v *service.BatchImageBalanceHoldCommand) { v.VideoDeferredBilling = true },
		func(v *service.BatchImageBalanceHoldCommand) { v.SubscriptionHoldAllocations[0].SubscriptionID = nil },
		func(v *service.BatchImageBalanceHoldCommand) { v.SubscriptionHoldAllocations[0].AmountUSD = -1 },
		func(v *service.BatchImageBalanceHoldCommand) {
			v.SubscriptionHoldAllocations[0].Type = domain.BillingAllocationTypeBalance
		},
	} {
		changed := base
		changed.SubscriptionHoldAllocations = append([]domain.BillingAllocation(nil), base.SubscriptionHoldAllocations...)
		mutate(&changed)
		_, err := videoMediaTaskReservedAmounts(&changed)
		require.ErrorIs(t, err, service.ErrVideoTaskConflict)
	}
	// 无资金后扣任务可以关闭核对，但不能把未冻结的预估金额当成可退款本金。
	zero, err := videoMediaTaskReservedAmounts(&service.BatchImageBalanceHoldCommand{VideoDeferredBilling: true})
	require.NoError(t, err)
	require.Zero(t, zero.ActualAmountUSD)
}
