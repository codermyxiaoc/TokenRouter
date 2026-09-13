//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// 全局限额为一时，一条请求应能换组，下一条新请求仍须被限流。
func TestSmartRoutingRPMCountsOneRequestAcrossGroups(t *testing.T) {
	cache := &userRPMCacheStub{userCounts: []int{1, 2}}
	svc := newBillingServiceForRPM(t, cache, nil)
	user := &User{ID: 1, RPMLimit: 1}
	ctx := WithSmartRoutingRPMAdmission(context.Background())
	for _, id := range []int64{1, 2, 3} {
		require.NoError(t, svc.checkRPM(ctx, user, &Group{ID: id, RPMLimit: 10}))
	}
	require.EqualValues(t, 1, cache.userCalls)
	require.EqualValues(t, 3, cache.userGroupCalls)
	next := WithSmartRoutingRPMAdmission(context.Background())
	require.ErrorIs(t, svc.checkRPM(next, user, &Group{ID: 1, RPMLimit: 10}), ErrUserRPMExceeded)
}

func TestSmartRoutingRPMKeepsGroupLimitsAndUpdatedUserLimit(t *testing.T) {
	cache := &userRPMCacheStub{userCounts: []int{5}, userGroupCounts: []int{1, 11}}
	svc := newBillingServiceForRPM(t, cache, nil)
	user := &User{ID: 1, RPMLimit: 10}
	ctx := WithSmartRoutingRPMAdmission(context.Background())
	require.NoError(t, svc.checkRPM(ctx, user, &Group{ID: 1, RPMLimit: 10}))
	require.ErrorIs(t, svc.checkRPM(ctx, user, &Group{ID: 2, RPMLimit: 10}), ErrGroupRPMExceeded)
	user.RPMLimit = 4
	require.ErrorIs(t, svc.checkRPM(ctx, user, &Group{ID: 1, RPMLimit: 10}), ErrUserRPMExceeded)
	require.EqualValues(t, 1, cache.userCalls)
	require.EqualValues(t, 2, cache.userGroupCalls)
}
