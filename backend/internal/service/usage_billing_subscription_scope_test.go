package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 异步约束只能收窄 auto 的候选，不能改变普通请求或指定订阅的既有归一化规则。
func TestUsageBillingCommandSubscriptionScopeNormalization(t *testing.T) {
	for _, mode := range []string{"", APIKeyBillingModeAuto, APIKeyBillingModeBalance, APIKeyBillingModeSubscription} {
		t.Run(mode, func(t *testing.T) {
			scopeID, preferredID := int64(11), int64(22)
			base := UsageBillingCommand{RequestID: "scope-test", UserID: 1, APIKeyID: 2, APIKeyBillingMode: mode, PreferredSubscriptionID: &preferredID}
			scoped := base
			scoped.SubscriptionScopeID = &scopeID
			base.Normalize()
			scoped.Normalize()
			if mode == "" || mode == APIKeyBillingModeAuto {
				require.Equal(t, &scopeID, scoped.SubscriptionScopeID)
				require.Nil(t, scoped.PreferredSubscriptionID)
				require.NotEqual(t, base.RequestFingerprint, scoped.RequestFingerprint)
			} else {
				require.Nil(t, scoped.SubscriptionScopeID)
				require.Equal(t, base.RequestFingerprint, scoped.RequestFingerprint)
				require.Equal(t, base.PreferredSubscriptionID, scoped.PreferredSubscriptionID)
			}
			fingerprint := scoped.RequestFingerprint
			scoped.Normalize()
			require.Equal(t, fingerprint, scoped.RequestFingerprint)
		})
	}
}

// 结算范围必须进入幂等指纹，防止同一个任务被重新指向另一个套餐后误判为正常重试。
func TestUsageBillingCommandSubscriptionScopeChangesFingerprint(t *testing.T) {
	firstID, secondID := int64(11), int64(12)
	first := UsageBillingCommand{APIKeyBillingMode: APIKeyBillingModeAuto, SubscriptionScopeID: &firstID}
	second := first
	second.SubscriptionScopeID = &secondID
	first.Normalize()
	second.Normalize()
	require.NotEqual(t, first.RequestFingerprint, second.RequestFingerprint)
}

func TestBuildUsageBillingCommandCopiesSubscriptionScope(t *testing.T) {
	scopeID := int64(11)
	params := &usageBillingParams{
		Cost: &CostBreakdown{ActualCost: 1}, User: &User{ID: 1}, Account: &Account{ID: 3},
		APIKey: &APIKey{ID: 2, BillingMode: APIKeyBillingModeAuto}, SubscriptionScopeID: &scopeID,
	}
	cmd := buildUsageBillingCommand("scope-copy", nil, params)
	require.NotNil(t, cmd)
	require.Equal(t, int64(11), *cmd.SubscriptionScopeID)
	scopeID = 12
	require.Equal(t, int64(11), *cmd.SubscriptionScopeID, "命令必须独立持有任务快照值")
	params.SubscriptionScopeID = nil
	ordinary := buildUsageBillingCommand("ordinary", nil, params)
	require.Nil(t, ordinary.SubscriptionScopeID)
}
