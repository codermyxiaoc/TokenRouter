package service

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// 测试缓存按生产维度保存数据，避免整组 stub 掩盖模型和入口隔离。
type smartRoutingAccountHealthCache struct {
	GatewayCache
	values map[string]map[int64]time.Duration
}

// 续接归属测试必须保留分组维度，并把缓存未命中与基础设施错误区分开。
type smartRoutingContinuationCache struct {
	GatewayCache
	values map[string]int64
}

func (c *smartRoutingContinuationCache) GetSessionAccountID(_ context.Context, groupID int64, key string) (int64, error) {
	if value, ok := c.values[fmt.Sprintf("%d/%s", groupID, key)]; ok {
		return value, nil
	}
	return 0, redis.Nil
}

func (c *smartRoutingContinuationCache) SetSessionAccountID(_ context.Context, groupID int64, key string, value int64, _ time.Duration) error {
	if c.values == nil {
		c.values = make(map[string]int64)
	}
	c.values[fmt.Sprintf("%d/%s", groupID, key)] = value
	return nil
}

func (c *smartRoutingAccountHealthCache) GetSmartRoutingAccountCooldowns(_ context.Context, key int64, ids []int64, model, endpoint string) (map[int64]time.Duration, error) {
	result := make(map[int64]time.Duration)
	for _, id := range ids {
		result[id] = c.values[fmt.Sprintf("%d/%s/%s", key, model, endpoint)][id]
	}
	return result, nil
}
func (c *smartRoutingAccountHealthCache) CooldownSmartRoutingAccounts(_ context.Context, key int64, ids []int64, model, endpoint string, ttl time.Duration) error {
	if c.values == nil {
		c.values = make(map[string]map[int64]time.Duration)
	}
	scope := fmt.Sprintf("%d/%s/%s", key, model, endpoint)
	if c.values[scope] == nil {
		c.values[scope] = make(map[int64]time.Duration)
	}
	for _, id := range ids {
		c.values[scope][id] = ttl
	}
	return nil
}

func TestSmartRoutingAccountFailureStateRequestIsolation(t *testing.T) {
	first := WithSmartRoutingAccountFailures(context.Background())
	second := WithSmartRoutingAccountFailures(context.Background())
	var work sync.WaitGroup
	for i := int64(1); i <= 40; i++ {
		work.Add(1)
		go func(id int64) {
			defer work.Done()
			MarkSmartRoutingAccountFailed(first, id)
			_ = IsSmartRoutingAccountExcluded(first, id)
		}(i)
	}
	work.Wait()
	for i := int64(1); i <= 40; i++ {
		require.True(t, IsSmartRoutingAccountExcluded(WithSmartRoutingAccountFailures(first), i))
		require.False(t, IsSmartRoutingAccountExcluded(second, i))
	}
	require.False(t, IsSmartRoutingAccountExcluded(context.Background(), 1))
	SetSmartRoutingContinuationAccount(second, 100)
	require.Equal(t, int64(100), SmartRoutingRequiredAccountID(second))
	require.True(t, IsSmartRoutingAccountExcluded(second, 101))
	require.False(t, IsSmartRoutingAccountExcluded(second, 100))
}

func TestSmartRoutingCooldownAdmissionKeepsEarliestRedisObservationAndScope(t *testing.T) {
	base := WithSmartRoutingAccountFailures(context.Background())
	first := WithSmartRoutingAccountCooldown(base, 1, 10*time.Second, "model", "/v1/responses")
	RememberSmartRoutingCooldownAdmission(first, 5, 200)
	RememberSmartRoutingCooldownAdmission(first, 5, 100)
	RememberSmartRoutingCooldownAdmission(first, 5, 300)
	started, admitted, tracked := SmartRoutingCooldownRequestInfo(first, 5)
	require.True(t, tracked)
	require.False(t, started.IsZero())
	require.EqualValues(t, 100, admitted)
	for _, other := range []context.Context{
		WithSmartRoutingAccountCooldown(base, 2, 10*time.Second, "model", "/v1/responses"),
		WithSmartRoutingAccountCooldown(base, 1, 10*time.Second, "other", "/v1/responses"),
		WithSmartRoutingAccountCooldown(base, 1, 10*time.Second, "model", "/v1/messages"),
	} {
		_, value, ok := SmartRoutingCooldownRequestInfo(other, 5)
		require.True(t, ok)
		require.Zero(t, value)
	}
	_, value, _ := SmartRoutingCooldownRequestInfo(first, 6)
	require.Zero(t, value)
}

func TestSmartRoutingAccountCooldownKeepsHealthyCandidatesAndCatalog(t *testing.T) {
	first := smartRoutingTestAccount(PlatformOpenAI)
	second := first
	second.ID = 11
	cache := &smartRoutingAccountHealthCache{}
	require.NoError(t, cache.CooldownSmartRoutingAccounts(context.Background(), 7, []int64{10}, "shared-model", "/v1/responses", time.Minute))
	repo := &smartRoutingAccountRepo{accounts: map[int64][]Account{1: {first, second}, 2: {first}}}
	svc := NewSmartRoutingService(&GatewayService{accountRepo: repo, cache: cache}, nil)
	ctx := WithSmartRoutingAccountCooldown(WithSmartRoutingAccountFailures(context.Background()), 7, time.Minute, "shared-model", "/v1/responses")
	group := &Group{ID: 1, Platform: PlatformOpenAI, Status: StatusActive}
	result, err := svc.EvaluateSmartRoutingGroup(ctx, group, "shared-model", "/v1/responses")
	require.NoError(t, err)
	require.True(t, result.HasModel)
	require.True(t, result.Schedulable, "同组另一个健康账号仍能提供服务")
	require.False(t, result.Cooling)
	require.True(t, IsSmartRoutingAccountExcluded(ctx, 10))
	require.False(t, IsSmartRoutingAccountExcluded(ctx, 11))
	group.ID = 2
	result, err = svc.EvaluateSmartRoutingGroup(ctx, group, "shared-model", "/v1/responses")
	require.NoError(t, err)
	require.True(t, result.HasModel)
	require.False(t, result.Schedulable)
	require.True(t, result.Cooling)
	require.LessOrEqual(t, result.RetryAfter, time.Minute)
	require.Greater(t, result.RetryAfter, 59*time.Second)
	require.False(t, result.RetryAt.IsZero())
	result, err = svc.EvaluateSmartRoutingGroup(ctx, group, "unknown", "/v1/responses")
	require.NoError(t, err)
	require.False(t, result.HasModel, "冷却不能捏造目录")
	for _, changed := range []struct {
		key      int64
		endpoint string
		duration time.Duration
	}{{8, "/v1/responses", time.Minute}, {7, "/v1/chat/completions", time.Minute}, {7, "/v1/responses", 0}} {
		fresh := WithSmartRoutingAccountCooldown(WithSmartRoutingAccountFailures(context.Background()), changed.key, changed.duration, "shared-model", changed.endpoint)
		result, err = svc.EvaluateSmartRoutingGroup(fresh, group, "shared-model", changed.endpoint)
		require.NoError(t, err)
		require.True(t, result.Schedulable)
	}
}

func TestSmartRoutingAccountFailureExcludesSameAccountAcrossGroups(t *testing.T) {
	first := smartRoutingTestAccount(PlatformOpenAI)
	second := first
	second.ID = 11
	repo := &smartRoutingAccountRepo{accounts: map[int64][]Account{1: {first}, 2: {first, second}}}
	svc := NewSmartRoutingService(&GatewayService{accountRepo: repo}, nil)
	ctx := WithSmartRoutingAccountFailures(context.Background())
	MarkSmartRoutingAccountFailed(ctx, 10)
	for _, test := range []struct {
		group       int64
		schedulable bool
	}{{1, false}, {2, true}} {
		result, err := svc.EvaluateSmartRoutingGroup(ctx, &Group{ID: test.group, Platform: PlatformOpenAI, Status: StatusActive}, "shared-model", "/v1/responses")
		require.NoError(t, err)
		require.True(t, result.HasModel)
		require.Equal(t, test.schedulable, result.Schedulable)
	}
}

func TestSmartRoutingContinuationResolvesOnlyAuthorizedOwner(t *testing.T) {
	cache := &smartRoutingContinuationCache{}
	upstream := &OpenAIGatewayService{cache: cache}
	ctx := context.Background()
	require.NoError(t, upstream.BindOpenAIHTTPResponseOwner(ctx, 2, "resp_second", 7, 8))
	require.NoError(t, upstream.getOpenAIWSStateStore().BindResponseAccount(ctx, 2, "resp_second", 20, time.Hour))
	key := &APIKey{ID: 9, User: &User{ID: 7}, CompositeGroups: []APIKeyCompositeGroup{
		{GroupID: 1, Group: &Group{ID: 1, Platform: PlatformOpenAI, Status: StatusActive}},
		{GroupID: 2, Group: &Group{ID: 2, Platform: PlatformOpenAI, Status: StatusActive}},
	}}
	svc := NewSmartRoutingService(nil, upstream)
	binding, err := svc.ResolveSmartRoutingContinuation(ctx, key, "resp_second")
	require.NoError(t, err)
	require.Equal(t, SmartRoutingContinuationBinding{GroupID: 2, AccountID: 20}, binding)
	key.User.ID = 99
	binding, err = svc.ResolveSmartRoutingContinuation(ctx, key, "resp_second")
	require.NoError(t, err)
	require.Zero(t, binding.GroupID)
	key.User.ID = 7
	key.CompositeGroups = key.CompositeGroups[:1]
	binding, err = svc.ResolveSmartRoutingContinuation(ctx, key, "resp_second")
	require.NoError(t, err)
	require.Zero(t, binding.GroupID, "不扩大到当前密钥未授权的分组")
}

// 归属记录不完整或相互冲突时不能猜测账号，取消请求也不能返回旧的本地归属。
func TestSmartRoutingContinuationRejectsIncompleteOrAmbiguousBindings(t *testing.T) {
	for _, scenario := range []string{"missing-account", "ambiguous-owner", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			upstream := &OpenAIGatewayService{cache: &smartRoutingContinuationCache{}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			require.NoError(t, upstream.BindOpenAIHTTPResponseOwner(ctx, 1, "resp_bound", 7, 8))
			key := &APIKey{ID: 8, User: &User{ID: 7}, CompositeGroups: []APIKeyCompositeGroup{
				{GroupID: 1, Group: &Group{ID: 1, Platform: PlatformOpenAI, Status: StatusActive}},
				{GroupID: 2, Group: &Group{ID: 2, Platform: PlatformOpenAI, Status: StatusActive}},
			}}
			if scenario != "missing-account" {
				require.NoError(t, upstream.getOpenAIWSStateStore().BindResponseAccount(ctx, 1, "resp_bound", 10, time.Hour))
			}
			if scenario == "ambiguous-owner" {
				require.NoError(t, upstream.BindOpenAIHTTPResponseOwner(ctx, 2, "resp_bound", 7, 8))
				require.NoError(t, upstream.getOpenAIWSStateStore().BindResponseAccount(ctx, 2, "resp_bound", 20, time.Hour))
			}
			if scenario == "cancelled" {
				cancel()
			}
			binding, err := NewSmartRoutingService(nil, upstream).ResolveSmartRoutingContinuation(ctx, key, "resp_bound")
			require.Error(t, err)
			require.Zero(t, binding.GroupID)
		})
	}
}
