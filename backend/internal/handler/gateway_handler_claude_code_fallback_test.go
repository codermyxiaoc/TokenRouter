//go:build unit

package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/ctxkey"
	middleware "github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// groupScopedSchedulerCache 按桶的分组只返回该分组的成员账号，并记录被查询过的分组。
type groupScopedSchedulerCache struct {
	*fakeSchedulerCache
	mu       sync.Mutex
	groupIDs []int64
}

func (c *groupScopedSchedulerCache) GetSnapshot(_ context.Context, bucket service.SchedulerBucket) ([]*service.Account, bool, error) {
	c.mu.Lock()
	c.groupIDs = append(c.groupIDs, bucket.GroupID)
	c.mu.Unlock()
	var members []*service.Account
	for _, account := range c.accounts {
		for _, ag := range account.AccountGroups {
			if ag.GroupID == bucket.GroupID {
				members = append(members, account)
				break
			}
		}
	}
	return members, true, nil
}

func (c *groupScopedSchedulerCache) queriedGroupIDs() []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int64(nil), c.groupIDs...)
}

type groupMapRepo struct {
	*fakeGroupRepo
	groups map[int64]*service.Group
}

func (r *groupMapRepo) GetByID(_ context.Context, id int64) (*service.Group, error) {
	if group, ok := r.groups[id]; ok {
		return group, nil
	}
	return nil, service.ErrGroupNotFound
}

func (r *groupMapRepo) GetByIDLite(ctx context.Context, id int64) (*service.Group, error) {
	return r.GetByID(ctx, id)
}

func TestGatewayOpenAICompatibleHandlersClaudeCodeOnlyFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const (
		primaryGroupID    = int64(9300)
		fallbackGroupID   = int64(9301)
		primaryAccountID  = int64(9310)
		fallbackAccountID = int64(9311)
	)

	// 账号不带 api_key：转发在取令牌时失败，请求不会触达上游，断言只看选号结果。
	newAccount := func(id, groupID int64) *service.Account {
		return &service.Account{
			ID: id, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey,
			Status: service.StatusActive, Schedulable: true, Concurrency: 1,
			AccountGroups: []service.AccountGroup{{AccountID: id, GroupID: groupID}},
		}
	}

	endpoints := []struct {
		name string
		path string
		body string
		call func(*GatewayHandler, *gin.Context)
	}{
		{
			name: "responses", path: "/v1/responses",
			body: `{"model":"claude-sonnet-4-5","input":"hello","stream":false}`,
			call: (*GatewayHandler).Responses,
		},
		{
			name: "chat completions", path: "/v1/chat/completions",
			body: `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}],"stream":false}`,
			call: (*GatewayHandler).ChatCompletions,
		},
	}

	for _, ep := range endpoints {
		for _, tc := range []struct {
			name                string
			hasFallback         bool
			smartRouting        bool
			denyProtocol        bool
			denyGroup           bool
			disabled            bool
			foreignPlatform     bool
			subscriptionOutside bool
			subscriptionCovered bool
			composite           bool
			cycle               bool
		}{
			{name: "with fallback group", hasFallback: true},
			{name: "without fallback group", hasFallback: false},
			{name: "smart routing rejects traditional fallback", hasFallback: true, smartRouting: true},
			{name: "fallback protocol denied", hasFallback: true, denyProtocol: true},
			{name: "fallback group denied", hasFallback: true, denyGroup: true},
			{name: "fallback group disabled", hasFallback: true, disabled: true},
			{name: "fallback platform unsupported", hasFallback: true, foreignPlatform: true},
			{name: "preferred subscription excludes fallback", hasFallback: true, subscriptionOutside: true},
			{name: "preferred subscription covers fallback", hasFallback: true, subscriptionCovered: true},
			{name: "prefix composite key keeps selected billing group", hasFallback: true, composite: true},
			{name: "fallback cycle denied", hasFallback: true, cycle: true},
		} {
			t.Run(ep.name+"/"+tc.name, func(t *testing.T) {
				primary := &service.Group{
					ID: primaryGroupID, Hydrated: true, Platform: service.PlatformAnthropic,
					Status: service.StatusActive, ClaudeCodeOnly: true,
				}
				if tc.hasFallback {
					fallbackID := fallbackGroupID
					primary.FallbackGroupID = &fallbackID
				}
				fallback := &service.Group{
					ID: fallbackGroupID, Hydrated: true, Platform: service.PlatformAnthropic,
					Status: service.StatusActive,
				}

				fallback.AllowedClientProtocols = []service.GroupClientProtocol{service.GroupClientProtocolOpenAIResponses, service.GroupClientProtocolOpenAIChatCompletions}
				if tc.denyProtocol {
					fallback.AllowedClientProtocols = nil
				}
				if tc.denyGroup {
					fallback.IsExclusive = true
				}
				if tc.disabled {
					fallback.Status = service.StatusDisabled
				}
				if tc.foreignPlatform {
					fallback.Platform = service.PlatformOpenAI
				}
				if tc.cycle {
					fallback.ClaudeCodeOnly = true
					primaryID := primaryGroupID
					fallback.FallbackGroupID = &primaryID
				}

				schedulerCache := &groupScopedSchedulerCache{fakeSchedulerCache: &fakeSchedulerCache{accounts: []*service.Account{
					newAccount(primaryAccountID, primaryGroupID),
					newAccount(fallbackAccountID, fallbackGroupID),
				}}}
				gatewayService := service.NewGatewayService(
					nil, &groupMapRepo{fakeGroupRepo: &fakeGroupRepo{}, groups: map[int64]*service.Group{
						primaryGroupID:  primary,
						fallbackGroupID: fallback,
					}}, nil, nil, nil, nil, nil, nil, nil,
					service.NewSchedulerSnapshotService(schedulerCache, nil, nil, nil, nil),
					nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
				)
				cfg := &config.Config{RunMode: config.RunModeSimple}
				billingCacheService := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
				t.Cleanup(billingCacheService.Stop)
				h := &GatewayHandler{
					gatewayService:      gatewayService,
					billingCacheService: billingCacheService,
					concurrencyHelper:   NewConcurrencyHelper(service.NewConcurrencyService(&fakeConcurrencyCache{}), SSEPingFormatClaude, 0),
					maxAccountSwitches:  1,
					cfg:                 cfg,
				}

				primaryGroupIDRef := primaryGroupID
				apiKey := &service.APIKey{
					ID: 9320, UserID: 9330, GroupID: &primaryGroupIDRef, Group: primary, Status: service.StatusActive,
					User: &service.User{ID: 9330, Concurrency: 10, Balance: 100},
				}
				apiKey.SmartRouting = tc.smartRouting
				apiKey.IsComposite = tc.composite
				if tc.subscriptionOutside || tc.subscriptionCovered {
					apiKey.BillingMode = service.APIKeyBillingModeSubscription
					preferredID := int64(9400)
					apiKey.PreferredSubscriptionID = &preferredID
				}
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				ctx := context.WithValue(context.Background(), ctxkey.Group, primary)
				req := httptest.NewRequest(http.MethodPost, ep.path, bytes.NewBufferString(ep.body)).WithContext(ctx)
				req.Header.Set("Content-Type", "application/json")
				c.Request = req
				c.Set(string(middleware.ContextKeyAPIKey), apiKey)
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.UserID, Concurrency: 10})
				if tc.subscriptionOutside || tc.subscriptionCovered {
					// 使用真实套餐范围验证，不以缺失订阅替代套餐外目标。
					groupIDs := []int64{primaryGroupID}
					if tc.subscriptionCovered {
						groupIDs = append(groupIDs, fallbackGroupID)
					}
					c.Set(string(middleware.ContextKeySubscription), &service.UserSubscription{
						ID: 9400, UserID: apiKey.UserID,
						Plan: &service.SubscriptionPlan{ID: 1, GroupIDs: groupIDs},
					})
				}

				ep.call(h, c)

				selected, reachedSelection := c.Get(opsAccountIDKey)
				if !tc.hasFallback || tc.smartRouting || tc.denyProtocol || tc.denyGroup || tc.disabled || tc.foreignPlatform || tc.subscriptionOutside || tc.cycle {
					require.Equal(t, http.StatusForbidden, recorder.Code)
					require.Contains(t, recorder.Body.String(), "This group is restricted to Claude Code clients")
					require.False(t, reachedSelection, "受限请求必须在账号选择前拒绝")
					require.Empty(t, schedulerCache.queriedGroupIDs())
					return
				}
				require.NotContains(t, recorder.Body.String(), "restricted to Claude Code clients")
				require.True(t, reachedSelection, "有权限的降级请求必须进入目标组账号选择")
				require.Equal(t, fallbackAccountID, selected)
				require.Equal(t, primaryGroupID, *apiKey.GroupID, "选号降级不能改写原 Key 的计费分组")
				queried := schedulerCache.queriedGroupIDs()
				require.Contains(t, queried, fallbackGroupID)
				require.NotContains(t, queried, primaryGroupID)
			})
		}
	}
}
