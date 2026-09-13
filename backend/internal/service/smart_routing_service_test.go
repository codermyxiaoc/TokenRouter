package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

// smartRoutingAccountRepo 只提供只读候选查询，调用任何调度写入都会使测试失败。
type smartRoutingAccountRepo struct {
	AccountRepository
	accounts map[int64][]Account
	err      error
	groups   []int64
}

func (r *smartRoutingAccountRepo) ListAllWithFilters(_ context.Context, _, _, status, search string, groupID int64, _ string) ([]Account, error) {
	if groupID <= 0 || status != "" || search != "" {
		panic("智能路由不得扩大分组范围")
	}
	r.groups = append(r.groups, groupID)
	return append([]Account(nil), r.accounts[groupID]...), r.err
}

func (r *smartRoutingAccountRepo) SetError(_ context.Context, id int64, message string) error {
	for groupID, accounts := range r.accounts {
		for i := range accounts {
			if accounts[i].ID == id {
				r.accounts[groupID][i].Status = StatusError
				r.accounts[groupID][i].Schedulable = false
				r.accounts[groupID][i].ErrorMessage = message
			}
		}
	}
	return nil
}

func (r *smartRoutingAccountRepo) ListSchedulableByGroupIDAndPlatform(_ context.Context, groupID int64, _ string) ([]Account, error) {
	return append([]Account(nil), r.accounts[groupID]...), r.err
}

func (r *smartRoutingAccountRepo) ListSchedulableByPlatform(context.Context, string) ([]Account, error) {
	accounts := make([]Account, 0)
	for _, groupAccounts := range r.accounts {
		accounts = append(accounts, groupAccounts...)
	}
	return accounts, r.err
}

func (r *smartRoutingAccountRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	for _, accounts := range r.accounts {
		for i := range accounts {
			if accounts[i].ID == id {
				return &accounts[i], r.err
			}
		}
	}
	return nil, r.err
}

// smartRoutingStickyCache 仅允许读取旧绑定，错误组不得刷新会话。
type smartRoutingStickyCache struct {
	GatewayCache
	accountID int64
}

func (c *smartRoutingStickyCache) GetSessionAccountID(context.Context, int64, string) (int64, error) {
	return c.accountID, nil
}

// smartRoutingSnapshotCache 返回 simple 模式的共享快照，校验读取层不会泄漏其他分组。
type smartRoutingSnapshotCache struct {
	SchedulerCache
	accounts []*Account
}

func (c *smartRoutingSnapshotCache) GetSnapshot(context.Context, SchedulerBucket) ([]*Account, bool, error) {
	return c.accounts, true, nil
}

// smartRoutingConcurrencyCache 没有槽获取实现，确保预检查只读取并发计数。
type smartRoutingConcurrencyCache struct {
	ConcurrencyCache
	counts map[int64]int
	err    error
}

// smartRoutingRuntimeCache 模拟已满的窗口费用和 RPM，不实现注册或递增接口。
type smartRoutingRuntimeCache struct {
	SessionLimitCache
	RPMCache
	cost float64
	rpm  int
}

func (c *smartRoutingRuntimeCache) GetWindowCost(context.Context, int64) (float64, bool, error) {
	return c.cost, true, nil
}

func (c *smartRoutingRuntimeCache) GetRPM(context.Context, int64) (int, error) {
	return c.rpm, nil
}

func (c *smartRoutingConcurrencyCache) GetAccountConcurrencyBatch(context.Context, []int64) (map[int64]int, error) {
	return c.counts, c.err
}

func smartRoutingTestAccount(platform string) Account {
	return Account{
		ID: 10, Platform: platform, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 2,
		Credentials: map[string]any{"model_mapping": map[string]any{"shared-model": "shared-model"}},
	}
}

func TestSmartRoutingCatalogRejectsUnlistedProbes(t *testing.T) {
	tests := []struct {
		name        string
		credentials map[string]any
		model       string
		want        bool
	}{
		{"空模型", nil, "", false},
		{"未配置账号拒绝未知模型", nil, "unknown-probe-model", false},
		{"默认目录拒绝其他平台探针", nil, "claude-haiku-4-5-20251001", false},
		{"保留有限默认模型", nil, "gpt-5.6-sol", true},
		{"通配映射不生成未知型号", map[string]any{"model_mapping": map[string]any{"*": "gpt-5.6-sol"}}, "client-probe-unknown", false},
		{"精确账号别名", map[string]any{"model_mapping": map[string]any{"custom-model": "upstream-model"}}, "custom-model", true},
		{"独立白名单", map[string]any{"model_whitelist": []any{"custom-model"}}, "custom-model", true},
		{"白名单不继承平台默认", map[string]any{"model_whitelist": []any{"custom-model"}}, "gpt-5.6-sol", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			account := smartRoutingTestAccount(PlatformOpenAI)
			account.Credentials = test.credentials
			require.Equal(t, test.want, smartRoutingAccountCatalogContains(&account, test.model))
		})
	}
}

func TestSmartRoutingDistinguishesModelAbsenceAndTemporaryUnavailability(t *testing.T) {
	group := &Group{ID: 1, Platform: PlatformOpenAI, Status: StatusActive}
	future := time.Now().Add(time.Hour)
	tests := []struct {
		name     string
		change   func(*Account)
		model    string
		endpoint string
		want     SmartRoutingGroupAvailability
	}{
		{"有目录且可调度", nil, "shared-model", "/v1/responses", SmartRoutingGroupAvailability{true, true}},
		{"未知模型", nil, "unknown-probe", "/v1/responses", SmartRoutingGroupAvailability{}},
		{"账号暂时停调", func(a *Account) { a.TempUnschedulableUntil = &future }, "shared-model", "/v1/responses", SmartRoutingGroupAvailability{true, false}},
		{"账号限流", func(a *Account) { a.RateLimitResetAt = &future }, "shared-model", "/v1/responses", SmartRoutingGroupAvailability{true, false}},
		{"账号持久暂停仍有目录", func(a *Account) { a.Schedulable = false }, "shared-model", "/v1/responses", SmartRoutingGroupAvailability{true, false}},
		{"账号错误仍有目录", func(a *Account) { a.Status = StatusError; a.Schedulable = false }, "shared-model", "/v1/responses", SmartRoutingGroupAvailability{true, false}},
		{"模型限流", func(a *Account) {
			a.Extra = map[string]any{modelRateLimitsKey: map[string]any{"shared-model": map[string]any{"rate_limit_reset_at": future.Format(time.RFC3339)}}}
		}, "shared-model", "/v1/responses", SmartRoutingGroupAvailability{true, false}},
		{"配额耗尽", func(a *Account) { a.Extra = map[string]any{"quota_limit": float64(1), "quota_used": float64(1)} }, "shared-model", "/v1/responses", SmartRoutingGroupAvailability{true, false}},
		{"compact能力禁用", func(a *Account) { a.Extra = map[string]any{"openai_compact_supported": false} }, "shared-model", "/v1/responses/compact", SmartRoutingGroupAvailability{true, false}},
		{"停用账号无目录", func(a *Account) { a.Status = StatusDisabled }, "shared-model", "/v1/responses", SmartRoutingGroupAvailability{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			account := smartRoutingTestAccount(PlatformOpenAI)
			if test.change != nil {
				test.change(&account)
			}
			repo := &smartRoutingAccountRepo{accounts: map[int64][]Account{1: {account}}}
			svc := NewSmartRoutingService(&GatewayService{accountRepo: repo}, nil)
			result, err := svc.EvaluateSmartRoutingGroup(context.Background(), group, test.model, test.endpoint)
			require.NoError(t, err)
			require.Equal(t, test.want, result)
		})
	}
}

func TestSmartRoutingCatalogSurvivesRealUpstreamErrorPolicy(t *testing.T) {
	for _, status := range []int{429, 502, 524} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			account := smartRoutingTestAccount(PlatformOpenAI)
			account.Credentials["custom_error_codes_enabled"] = true
			account.Credentials["custom_error_codes"] = []any{float64(status)}
			repo := &smartRoutingAccountRepo{accounts: map[int64][]Account{1: {account}}}
			group := &Group{ID: 1, Platform: PlatformOpenAI, Status: StatusActive}
			svc := NewSmartRoutingService(&GatewayService{accountRepo: repo}, nil)
			policy := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			decision := policy.ApplyUpstreamError(context.Background(), &account, status, http.Header{}, []byte(`{"error":{"message":"configured upstream failure"}}`), "shared-model")
			require.True(t, decision.StopScheduling)
			require.Equal(t, StatusError, repo.accounts[1][0].Status)
			result, err := svc.EvaluateSmartRoutingGroup(context.Background(), group, "shared-model", "/v1/responses")
			require.NoError(t, err)
			require.Equal(t, SmartRoutingGroupAvailability{HasModel: true}, result)
			models, err := svc.ResolveSmartRoutingModels(context.Background(), group)
			require.NoError(t, err)
			require.Contains(t, models, "shared-model")
			result, err = svc.EvaluateSmartRoutingGroup(context.Background(), group, "unknown-probe", "/v1/responses")
			require.NoError(t, err)
			require.Equal(t, SmartRoutingGroupAvailability{}, result)
		})
	}
}

func TestSmartRoutingEndpointPlatformBoundaries(t *testing.T) {
	tests := []struct {
		platform string
		endpoint string
		want     bool
	}{
		{PlatformAnthropic, "/v1beta/models/shared-model:generateContent", false},
		{PlatformGemini, "/v1beta/models/shared-model:generateContent", true},
		{PlatformAntigravity, "/v1beta/models/shared-model:generateContent", false},
		{PlatformAntigravity, "/antigravity/v1beta/models/shared-model:generateContent", true},
		{PlatformGemini, "/v1/images/generations", false},
		{PlatformAnthropic, "/v1/images/edits", false},
		{PlatformOpenAI, "/v1/images/generations", true},
		{PlatformOpenAI, "/v1/videos/generations", false},
		{PlatformGrok, "/v1/videos/generations", true},
		{PlatformGemini, "/v1/images/batches", true},
		{PlatformOpenAI, "/v1/images/batches", false},
		{PlatformGemini, "/v1/responses/input_tokens", false},
		{PlatformQoder, "/v1/responses/input_tokens", false},
		{PlatformKimi, "/v1/responses/input_tokens", true},
		{PlatformOpenAI, "/backend-api/codex/responses/input_tokens", true},
		{PlatformAntigravity, "/v1/messages/count_tokens", false},
		{PlatformQoder, "/messages/count_tokens", false},
		{PlatformGrok, "/messages/count_tokens", true},
		{PlatformOpenAI, "/v1/embeddings", true},
		{PlatformGrok, "/v1/embeddings", false},
	}
	for _, test := range tests {
		t.Run(test.platform+test.endpoint, func(t *testing.T) {
			require.Equal(t, test.want, smartRoutingGroupEndpointEligible(test.platform, test.endpoint))
		})
	}
	// 端点不兼容时应在读账号前退出，不能因模型同名而把客户端探针打到该组。
	svc := NewSmartRoutingService(&GatewayService{accountRepo: &smartRoutingAccountRepo{}}, nil)
	result, err := svc.EvaluateSmartRoutingGroup(context.Background(), &Group{ID: 1, Status: StatusActive, Platform: PlatformGemini}, "shared-model", "/v1/responses/input_tokens")
	require.NoError(t, err)
	require.Equal(t, SmartRoutingGroupAvailability{}, result)
}

func TestSmartRoutingConcurrencyAndGroupIsolation(t *testing.T) {
	account := smartRoutingTestAccount(PlatformOpenAI)
	repo := &smartRoutingAccountRepo{accounts: map[int64][]Account{1: {account}, 2: {account}}}
	cache := &smartRoutingConcurrencyCache{counts: map[int64]int{10: 2}}
	svc := NewSmartRoutingService(&GatewayService{accountRepo: repo, concurrencyService: &ConcurrencyService{cache: cache}}, nil)
	group := &Group{ID: 1, Platform: PlatformOpenAI, Status: StatusActive}
	result, err := svc.EvaluateSmartRoutingGroup(context.Background(), group, "shared-model", "/v1/responses")
	require.NoError(t, err)
	require.Equal(t, SmartRoutingGroupAvailability{HasModel: true}, result)
	cache.counts[10] = 1
	result, err = svc.EvaluateSmartRoutingGroup(context.Background(), group, "shared-model", "/v1/responses")
	require.NoError(t, err)
	require.True(t, result.Schedulable)
	require.Equal(t, []int64{1, 1}, repo.groups)

	cache.err = errors.New("redis unavailable")
	_, err = svc.EvaluateSmartRoutingGroup(context.Background(), group, "shared-model", "/v1/responses")
	require.ErrorContains(t, err, "redis unavailable")
	repo.err = errors.New("database unavailable")
	_, err = svc.EvaluateSmartRoutingGroup(context.Background(), group, "shared-model", "/v1/responses")
	require.ErrorContains(t, err, "database unavailable")

	ctx := context.WithValue(context.Background(), ctxkey.ForcePlatform, PlatformAntigravity)
	result, err = svc.EvaluateSmartRoutingGroup(ctx, group, "shared-model", "/v1/responses")
	require.NoError(t, err)
	require.Equal(t, SmartRoutingGroupAvailability{}, result)
}

func TestSmartRoutingChannelMappingAndCatalogAgree(t *testing.T) {
	group := &Group{ID: 1, Platform: PlatformOpenAI, Status: StatusActive}
	account := smartRoutingTestAccount(PlatformOpenAI)
	account.Credentials = map[string]any{
		"model_mapping": map[string]any{"channel-model": "upstream-model"}, "model_whitelist": []any{"upstream-model"},
	}
	channel := Channel{ID: 7, Status: StatusActive, ModelMapping: map[string]map[string]string{
		PlatformOpenAI: {"client-alias": "channel-model", "channel-model": "wrong-second-hop"},
	}}
	repo := &smartRoutingAccountRepo{accounts: map[int64][]Account{1: {account}}}
	svc := NewSmartRoutingService(&GatewayService{accountRepo: repo, channelService: newRequestableModelsChannelService(1, PlatformOpenAI, channel)}, nil)
	result, err := svc.EvaluateSmartRoutingGroup(context.Background(), group, "client-alias", "/v1/responses")
	require.NoError(t, err)
	require.Equal(t, SmartRoutingGroupAvailability{true, true}, result)
	models, err := svc.ResolveSmartRoutingModels(context.Background(), group)
	require.NoError(t, err)
	require.Contains(t, models, "client-alias")
	require.NotContains(t, models, "channel-model")
	require.NotContains(t, models, "wrong-second-hop")

	// 暂时限流不应伪装成不存在的模型，也不应让账号模型目录退回平台默认列表。
	future := time.Now().Add(time.Hour)
	repo.accounts[1][0].RateLimitResetAt = &future
	models, err = svc.ResolveSmartRoutingModels(context.Background(), group)
	require.NoError(t, err)
	require.Contains(t, models, "client-alias")
	require.NotContains(t, models, "gpt-5.6-sol")
}

func TestSmartRoutingRespectsWindowRPMAndGroupRequirements(t *testing.T) {
	account := smartRoutingTestAccount(PlatformAnthropic)
	account.Type = AccountTypeOAuth
	account.Extra = map[string]any{"window_cost_limit": float64(10), "base_rpm": 5}
	group := &Group{ID: 1, Status: StatusActive, Platform: PlatformAnthropic}
	repo := &smartRoutingAccountRepo{accounts: map[int64][]Account{1: {account}}}
	cache := &smartRoutingRuntimeCache{cost: 10}
	svc := NewSmartRoutingService(&GatewayService{accountRepo: repo, sessionLimitCache: cache, rpmCache: cache}, nil)
	result, err := svc.EvaluateSmartRoutingGroup(context.Background(), group, "shared-model", "/v1/messages")
	require.NoError(t, err)
	require.Equal(t, SmartRoutingGroupAvailability{HasModel: true}, result)
	cache.cost = 0
	cache.rpm = 5
	result, err = svc.EvaluateSmartRoutingGroup(context.Background(), group, "shared-model", "/v1/messages")
	require.NoError(t, err)
	require.False(t, result.Schedulable)
	cache.rpm = 0
	result, err = svc.EvaluateSmartRoutingGroup(context.Background(), group, "shared-model", "/v1/messages")
	require.NoError(t, err)
	require.True(t, result.Schedulable)

	account = smartRoutingTestAccount(PlatformOpenAI)
	repo.accounts[1] = []Account{account}
	group.Platform = PlatformOpenAI
	group.RequireOAuthOnly = true
	result, err = svc.EvaluateSmartRoutingGroup(context.Background(), group, "shared-model", "/v1/responses")
	require.NoError(t, err)
	require.Equal(t, SmartRoutingGroupAvailability{HasModel: true}, result)
	group.RequireOAuthOnly = false
	group.RequirePrivacySet = true
	result, err = svc.EvaluateSmartRoutingGroup(context.Background(), group, "shared-model", "/v1/responses")
	require.NoError(t, err)
	require.False(t, result.Schedulable)
}

func TestSmartRoutingSimpleModeCannotEscapeSelectedGroup(t *testing.T) {
	accountA := smartRoutingTestAccount(PlatformOpenAI)
	accountA.GroupIDs = []int64{1}
	accountB := smartRoutingTestAccount(PlatformOpenAI)
	accountB.ID = 11
	accountB.GroupIDs = []int64{2}
	repo := &smartRoutingAccountRepo{accounts: map[int64][]Account{1: {accountA}, 2: {accountB}}}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	groupID := int64(1)
	ctx := WithSmartRoutingScope(context.Background())
	snapshot := &SchedulerSnapshotService{cfg: cfg, cache: &smartRoutingSnapshotCache{accounts: []*Account{&accountA, &accountB}}}
	for _, useSnapshot := range []bool{false, true} {
		t.Run(map[bool]string{false: "数据库查询", true: "共享快照"}[useSnapshot], func(t *testing.T) {
			gateway := &GatewayService{accountRepo: repo, cfg: cfg}
			openAI := &OpenAIGatewayService{accountRepo: repo, cfg: cfg}
			if useSnapshot {
				gateway.schedulerSnapshot = snapshot
				openAI.schedulerSnapshot = snapshot
			}
			accounts, _, err := gateway.listSchedulableAccounts(ctx, &groupID, PlatformOpenAI, false)
			require.NoError(t, err)
			require.Len(t, accounts, 1)
			require.Equal(t, accountA.ID, accounts[0].ID)
			accounts, err = openAI.listSchedulableAccounts(ctx, &groupID, PlatformOpenAI)
			require.NoError(t, err)
			require.Len(t, accounts, 1)
			require.Equal(t, accountA.ID, accounts[0].ID)

			// 未启用智能路由的 simple 请求仍沿用全平台账号池。
			accounts, err = openAI.listSchedulableAccounts(context.Background(), &groupID, PlatformOpenAI)
			require.NoError(t, err)
			require.Len(t, accounts, 2)
			require.False(t, openAI.openAIAccountMatchesSchedulingGroup(ctx, &accountB, &groupID))
			require.True(t, openAI.openAIAccountMatchesSchedulingGroup(context.Background(), &accountB, &groupID))
		})
	}
	// 同一共享快照依旧完整；另一个智能分组也能按自身成员取得候选。
	groupID = 2
	accounts, _, err := snapshot.ListSchedulableAccounts(ctx, &groupID, PlatformOpenAI, false)
	require.NoError(t, err)
	require.Len(t, accounts, 1)
	require.Equal(t, accountB.ID, accounts[0].ID)
}

func TestSmartRoutingGeminiRejectsHistoricalForeignStickyAccount(t *testing.T) {
	account := smartRoutingTestAccount(PlatformGemini)
	account.GroupIDs = []int64{2}
	repo := &smartRoutingAccountRepo{accounts: map[int64][]Account{2: {account}}}
	svc := &GeminiMessagesCompatService{
		accountRepo: repo, cfg: &config.Config{RunMode: config.RunModeSimple},
		cache: &smartRoutingStickyCache{accountID: account.ID},
	}
	groupID := int64(1)
	selected := svc.tryStickySessionHit(WithSmartRoutingScope(context.Background()), &groupID, "session", "cached-session", "shared-model", nil, PlatformGemini, true)
	require.Nil(t, selected)
}
