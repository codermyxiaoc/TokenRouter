//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

// 跨组失败集合必须在真实基础/高级选号和粘性路径生效，同时不污染其它请求。
func TestSchedulersExcludeSharedRequestFailures(t *testing.T) {
	for _, platform := range []string{PlatformOpenAI, PlatformGemini, PlatformAnthropic} {
		for _, mode := range []GroupSchedulerType{GroupSchedulerTypeBasic, GroupSchedulerTypeAdvanced} {
			t.Run(platform+"/"+string(mode), func(t *testing.T) {
				group := &Group{ID: 1981, Platform: platform, Status: StatusActive, Hydrated: true, SchedulerType: mode}
				accounts := []Account{
					{ID: 19811, Platform: platform, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 2, Priority: 1, GroupIDs: []int64{group.ID}},
					{ID: 19812, Platform: platform, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 2, Priority: 2, GroupIDs: []int64{group.ID}},
				}
				for i := range accounts {
					accounts[i].Credentials = map[string]any{"model_whitelist": []any{"shared-model"}}
				}
				repo := advancedSchedulerRegressionAccountRepo(accounts)
				cache := &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{"sticky": accounts[0].ID}}
				cfg := testConfig()
				ctx := context.WithValue(context.Background(), ctxkey.Group, group)
				requestCtx := WithSmartRoutingAccountFailures(WithSmartRoutingScope(ctx))
				MarkSmartRoutingAccountFailed(requestCtx, accounts[0].ID)
				var selected *Account
				var err error
				if platform == PlatformOpenAI {
					svc := &OpenAIGatewayService{accountRepo: repo, cache: cache, cfg: cfg}
					selected, err = svc.SelectAccountForModelWithExclusions(requestCtx, &group.ID, "sticky", "shared-model", nil)
				} else {
					svc := &GatewayService{accountRepo: repo, cache: cache, cfg: cfg, groupRepo: &mockGroupRepoForGateway{groups: map[int64]*Group{group.ID: group}}}
					selected, err = svc.SelectAccountForModelWithExclusions(requestCtx, &group.ID, "sticky", "shared-model", nil)
				}
				require.NoError(t, err)
				require.NotNil(t, selected)
				require.Equal(t, accounts[1].ID, selected.ID)
				require.False(t, IsSmartRoutingAccountExcluded(WithSmartRoutingAccountFailures(ctx), accounts[0].ID), "失败事实不能泄漏到另一个客户端请求")
			})
		}
	}
}
