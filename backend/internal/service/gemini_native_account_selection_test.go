//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

// 记录真实申请槽的账号，确保协议不匹配者在占槽和等待之前已被过滤。
type geminiNativeSelectionConcurrency struct {
	mockConcurrencyCache
	acquiredIDs []int64
}

func (m *geminiNativeSelectionConcurrency) AcquireAccountSlot(ctx context.Context, id int64, max int, requestID string) (bool, error) {
	m.acquiredIDs = append(m.acquiredIDs, id)
	return m.mockConcurrencyCache.AcquireAccountSlot(ctx, id, max, requestID)
}

// 原生资格共用于基础、负载与高级调度，且覆盖黏性、模型路由和满并发等待。
func TestGeminiNativeAccountSelectionFiltersClaudeBeforeSlots(t *testing.T) {
	for _, scheduler := range []string{"legacy", "load", "advanced"} {
		for _, full := range []bool{false, true} {
			for _, sticky := range []bool{false, true} {
				for _, routed := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/full=%t/sticky=%t/routed=%t", scheduler, full, sticky, routed), func(t *testing.T) {
						group := &Group{ID: 811, Platform: PlatformAntigravity, Status: StatusActive, Hydrated: true, ModelRoutingEnabled: routed}
						if routed {
							group.ModelRouting = map[string][]int64{"friendly": {8111, 8112}}
						}
						if scheduler == "advanced" {
							group.SchedulerType = GroupSchedulerTypeAdvanced
						}
						accounts := []Account{
							{ID: 8111, Platform: PlatformAntigravity, Type: AccountTypeOAuth, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 2, GroupIDs: []int64{group.ID}, AccountGroups: []AccountGroup{{AccountID: 8111, GroupID: group.ID}}, Credentials: map[string]any{"model_mapping": map[string]any{"friendly": "claude-opus-4-6"}}},
							{ID: 8112, Platform: PlatformAntigravity, Type: AccountTypeOAuth, Priority: 2, Status: StatusActive, Schedulable: true, Concurrency: 2, GroupIDs: []int64{group.ID}, AccountGroups: []AccountGroup{{AccountID: 8112, GroupID: group.ID}}, Credentials: map[string]any{"model_mapping": map[string]any{"friendly": "gemini-4-argon-high"}}},
						}
						repo := advancedSchedulerRegressionAccountRepo(accounts)
						cache := &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{}}
						session := ""
						if sticky {
							session = "native-sticky"
							cache.sessionBindings[session] = 8111
						}
						loadRate := 0
						if full {
							loadRate = 100
						}
						concurrency := &geminiNativeSelectionConcurrency{mockConcurrencyCache: mockConcurrencyCache{
							acquireResults: map[int64]bool{8111: true, 8112: !full},
							loadMap:        map[int64]*AccountLoadInfo{8111: {AccountID: 8111, LoadRate: 0}, 8112: {AccountID: 8112, LoadRate: loadRate}},
						}}
						cfg := testConfig()
						cfg.Gateway.Scheduling.LoadBatchEnabled = scheduler != "legacy"
						svc := &GatewayService{
							accountRepo: repo, groupRepo: &mockGroupRepoForGateway{groups: map[int64]*Group{group.ID: group}},
							cache: cache, cfg: cfg, concurrencyService: NewConcurrencyService(concurrency),
							channelService: newRequestableModelsChannelService(group.ID, PlatformAntigravity, Channel{ID: 81, Status: StatusActive, ModelMapping: map[string]map[string]string{PlatformAntigravity: {"channel-alias": "friendly"}}}),
						}
						ctx := WithGeminiNativeAccountSelection(context.WithValue(context.Background(), ctxkey.Group, group))
						selection, err := svc.SelectAccountWithLoadAwareness(ctx, &group.ID, session, "channel-alias", nil, "", 0)
						require.NoError(t, err)
						require.NotNil(t, selection)
						require.Equal(t, int64(8112), selection.Account.ID)
						require.NotContains(t, concurrency.acquiredIDs, int64(8111), "不能对协议不匹配账号先占槽再试错")
						if full {
							require.NotNil(t, selection.WaitPlan, "有效 Gemini 账号满载时只等待该账号")
						} else {
							require.True(t, selection.Acquired)
						}
						if selection.ReleaseFunc != nil {
							selection.ReleaseFunc()
						}
						// 缓存绑定只允许原账号时仍拒绝，不借协议过滤绕过显式账号绑定。
						bound := WithGeminiCachedContentBound(ctx)
						_, err = svc.SelectAccountWithLoadAwareness(bound, &group.ID, "", "channel-alias", map[int64]struct{}{8112: {}}, "", 0)
						require.Error(t, err)
						require.NotContains(t, concurrency.acquiredIDs, int64(8111))
					})
				}
			}
		}
	}
}

// 未标记的 Messages/Chat 调度仍可选择 Claude，同一资格函数只在原生入口收窄。
func TestGeminiNativeAccountSelectionDoesNotChangeOtherProtocols(t *testing.T) {
	svc := &GatewayService{}
	a := &Account{Platform: PlatformAntigravity, Type: AccountTypeOAuth, Credentials: map[string]any{"model_mapping": map[string]any{"friendly": "claude-opus-4-6"}}}
	require.True(t, svc.isRoutingModelSupportedByAccountWithContext(context.Background(), a, "friendly"))
	require.False(t, svc.isRoutingModelSupportedByAccountWithContext(WithGeminiNativeAccountSelection(context.Background()), a, "friendly"))
}
