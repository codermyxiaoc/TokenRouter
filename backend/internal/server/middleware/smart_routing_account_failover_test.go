//go:build unit

package middleware

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 这里保留账号全集和真实请求失败集合，不能用只按组返回 true 的 stub 掩盖重复选号。
type smartRoutingAccountResolver struct {
	accounts map[int64][]int64
	cooling  map[string]map[int64]time.Duration
	binding  service.SmartRoutingContinuationBinding
}

func (r *smartRoutingAccountResolver) UsesSmartRoutingAccountCooldown() bool { return true }
func (r *smartRoutingAccountResolver) ResolveSmartRoutingContinuation(_ context.Context, _ *service.APIKey, id string) (service.SmartRoutingContinuationBinding, error) {
	if id == "" {
		return service.SmartRoutingContinuationBinding{}, nil
	}
	return r.binding, nil
}
func (r *smartRoutingAccountResolver) CooldownSmartRoutingAccounts(_ context.Context, key int64, ids []int64, model, endpoint string, ttl time.Duration) error {
	if r.cooling == nil {
		r.cooling = make(map[string]map[int64]time.Duration)
	}
	scope := fmt.Sprintf("%d/%s/%s", key, model, endpoint)
	if r.cooling[scope] == nil {
		r.cooling[scope] = make(map[int64]time.Duration)
	}
	for _, id := range ids {
		r.cooling[scope][id] = ttl
	}
	return nil
}
func (r *smartRoutingAccountResolver) EvaluateSmartRoutingGroup(ctx context.Context, group *service.Group, model, endpoint string) (service.SmartRoutingGroupAvailability, error) {
	result := service.SmartRoutingGroupAvailability{HasModel: len(r.accounts[group.ID]) > 0}
	matching, cooling := 0, 0
	for _, id := range r.accounts[group.ID] {
		if required := service.SmartRoutingRequiredAccountID(ctx); required > 0 && required != id {
			continue
		}
		matching++
		if ttl := r.cooling[fmt.Sprintf("1/%s/%s", model, endpoint)][id]; ttl > 0 {
			cooling++
			if result.RetryAfter == 0 || ttl < result.RetryAfter {
				result.RetryAfter = ttl
			}
			continue
		}
		if !service.IsSmartRoutingAccountExcluded(ctx, id) {
			result.Schedulable = true
		}
	}
	result.Cooling = matching > 0 && matching == cooling
	return result, nil
}

func newSmartRoutingAccountRouter(t *testing.T, key *service.APIKey, resolver *smartRoutingAccountResolver, handler gin.HandlerFunc) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	for i := range key.CompositeGroups {
		key.CompositeGroups[i].Group.Platform = service.PlatformOpenAI
	}
	repo := &stubApiKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) { return key, nil }}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	keyService := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
	router := gin.New()
	router.Use(WithSmartRoutingResolver(resolver, gin.HandlerFunc(NewAPIKeyAuthMiddleware(keyService, nil, cfg)), func(*gin.Context) bool { return true }))
	router.POST("/v1/responses", handler)
	return router
}

func TestSmartRoutingAccountFailoverNeverRepeatsSharedAccountAcrossGroups(t *testing.T) {
	key := smartRoutingTestKey()
	key.SmartRoutingCooldownSeconds = 60
	resolver := &smartRoutingAccountResolver{accounts: map[int64][]int64{1: {11}, 2: {11, 22}, 3: {33}}}
	var attempts []int64
	router := newSmartRoutingAccountRouter(t, key, resolver, func(c *gin.Context) {
		key, _ := GetAPIKeyFromContext(c)
		for _, id := range resolver.accounts[key.Group.ID] {
			if service.IsSmartRoutingAccountExcluded(c.Request.Context(), id) || resolver.cooling["1/target//v1/responses"][id] > 0 {
				continue
			}
			attempts = append(attempts, id)
			if id == 11 {
				c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{AccountID: 11, GroupID: key.Group.ID, Kind: "http_error", UpstreamStatusCode: 502}})
				upstreamFailure(c, 502, "bad account")
				return
			}
			c.JSON(200, gin.H{"ok": true})
			return
		}
		t.Fatal("选组不能给 handler 留下已失败账号")
	})
	response := callFailoverRouter(router, "/v1/responses", `{"model":"target"}`)
	require.Equal(t, 200, response.Code, response.Body.String())
	require.Equal(t, []int64{11, 22}, attempts)
	require.Len(t, resolver.cooling["1/target//v1/responses"], 1)
	attempts = nil
	response = callFailoverRouter(router, "/v1/responses", `{"model":"target"}`)
	require.Equal(t, 200, response.Code, response.Body.String())
	require.Equal(t, []int64{22}, attempts, "前一组冷却账号不能遮挡后一组的健康账号")
}

func TestSmartRoutingAccountContinuationUsesVerifiedOwnerAndNeverDrifts(t *testing.T) {
	for _, test := range []struct {
		name, body string
		binding    service.SmartRoutingContinuationBinding
		cooling    []int64
		wantStatus int
		wantGroup  int64
	}{
		{"后继真实归属", `{"model":"target","previous_response_id":"resp_bound"}`, service.SmartRoutingContinuationBinding{GroupID: 2, AccountID: 22}, []int64{11}, 200, 2},
		{"绑定账号冷却不能换同组账号", `{"model":"target","previous_response_id":"resp_bound"}`, service.SmartRoutingContinuationBinding{GroupID: 2, AccountID: 22}, []int64{22}, 503, 0},
		{"未知句柄不能借冷却跨组", `{"model":"target","previous_response_id":"resp_unknown"}`, service.SmartRoutingContinuationBinding{}, []int64{11}, 503, 0},
		{"压缩历史不能借冷却跨组", `{"model":"target","input":[{"type":"compaction","encrypted_content":"opaque"}]}`, service.SmartRoutingContinuationBinding{}, []int64{11}, 503, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			key := smartRoutingTestKey()
			key.SmartRoutingCooldownSeconds = 60
			resolver := &smartRoutingAccountResolver{accounts: map[int64][]int64{1: {11}, 2: {22, 23}, 3: {33}}, binding: test.binding}
			require.NoError(t, resolver.CooldownSmartRoutingAccounts(context.Background(), 1, test.cooling, "target", "/v1/responses", time.Minute))
			var visited int64
			router := newSmartRoutingAccountRouter(t, key, resolver, func(c *gin.Context) {
				selected, _ := GetAPIKeyFromContext(c)
				visited = selected.Group.ID
				if test.binding.AccountID > 0 {
					require.True(t, service.IsSmartRoutingAccountExcluded(c.Request.Context(), 23))
					require.False(t, service.IsSmartRoutingAccountExcluded(c.Request.Context(), 22))
				}
				c.JSON(200, gin.H{"ok": true})
			})
			response := callFailoverRouter(router, "/v1/responses", test.body)
			require.Equal(t, test.wantStatus, response.Code, response.Body.String())
			require.Equal(t, test.wantGroup, visited)
		})
	}
}

func TestSmartRoutingAccountCooldownDoesNotRecordUserParameterErrors(t *testing.T) {
	key := smartRoutingTestKey()
	key.SmartRoutingCooldownSeconds = 60
	resolver := &smartRoutingAccountResolver{accounts: map[int64][]int64{1: {11}, 2: {22}, 3: {33}}}
	router := newSmartRoutingAccountRouter(t, key, resolver, func(c *gin.Context) {
		selected, _ := GetAPIKeyFromContext(c)
		c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{AccountID: selected.Group.ID + 10, Kind: "http_error", UpstreamStatusCode: 400}})
		service.MarkOpsRequestScopedError(c, "invalid_request_error")
		upstreamFailure(c, http.StatusBadRequest, "invalid user parameter")
	})
	response := callFailoverRouter(router, "/v1/responses", `{"model":"target"}`)
	require.Equal(t, 400, response.Code)
	require.Empty(t, resolver.cooling)
}

func TestSmartRoutingRecoveredAccountFailuresDoNotCoolSuccessfulAccount(t *testing.T) {
	for _, successID := range []int64{11, 22} {
		t.Run(fmt.Sprint(successID), func(t *testing.T) {
			key := smartRoutingTestKey()
			key.SmartRoutingCooldownSeconds = 60
			resolver := &smartRoutingAccountResolver{accounts: map[int64][]int64{1: {11, 22}}}
			router := newSmartRoutingAccountRouter(t, key, resolver, func(c *gin.Context) {
				c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{AccountID: 11, GroupID: 1, Kind: "http_error", UpstreamStatusCode: 502}})
				c.Set("ops_account_id", successID)
				c.JSON(200, gin.H{"ok": true})
			})
			response := callFailoverRouter(router, "/v1/responses", `{"model":"target"}`)
			require.Equal(t, 200, response.Code)
			if successID == 11 {
				require.Empty(t, resolver.cooling)
			} else {
				require.Positive(t, resolver.cooling["1/target//v1/responses"][11])
				require.Zero(t, resolver.cooling["1/target//v1/responses"][22])
			}
		})
	}
}

func TestSmartRoutingFailedAccountEvidenceClassification(t *testing.T) {
	for _, test := range []struct {
		name  string
		event service.OpsUpstreamErrorEvent
		want  bool
	}{
		{"模型缺失稳定原因", service.OpsUpstreamErrorEvent{UpstreamStatusCode: 404, Reason: "model_not_found"}, true},
		{"未留存正文的显示文案不是结构化证据", service.OpsUpstreamErrorEvent{UpstreamStatusCode: 404, Message: "The requested model or endpoint is unavailable"}, false},
		{"结构化模型错误", service.OpsUpstreamErrorEvent{UpstreamStatusCode: 404, UpstreamResponseBody: `{"error":{"code":"model_not_found"}}`}, true},
		{"其它404不冷却", service.OpsUpstreamErrorEvent{UpstreamStatusCode: 404, Message: "Not Found"}, false},
		{"请求回显不判为模型故障", service.OpsUpstreamErrorEvent{UpstreamStatusCode: 404, UpstreamResponseBody: `{"echo":{"message":"model not found"},"error":{"message":"route missing"}}`}, false},
		{"参数错误不冷却", service.OpsUpstreamErrorEvent{UpstreamStatusCode: 400, Message: "invalid parameter"}, false},
		{"请求范围不冷却", service.OpsUpstreamErrorEvent{UpstreamStatusCode: 503, Scope: "request"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(nil)
			test.event.AccountID = 10
			c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{&test.event})
			if test.want {
				require.Equal(t, []int64{10}, smartRoutingFailedAccountIDs(c))
			} else {
				require.Empty(t, smartRoutingFailedAccountIDs(c))
			}
		})
	}
}
