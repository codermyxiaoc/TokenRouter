//go:build unit

package middleware

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 测试时钟验证跨请求冷却，不通过等待真实时间掩盖顺序问题。
type failoverResolver struct {
	now          time.Time
	cooling      map[[2]int64]time.Time
	availability func(int64) service.SmartRoutingGroupAvailability
}

func (r *failoverResolver) EvaluateSmartRoutingGroup(_ context.Context, group *service.Group, _, _ string) (service.SmartRoutingGroupAvailability, error) {
	if r.availability != nil {
		return r.availability(group.ID), nil
	}
	return service.SmartRoutingGroupAvailability{HasModel: true, Schedulable: true}, nil
}
func (r *failoverResolver) GetSmartRoutingCooldown(_ context.Context, key, group int64) (time.Duration, error) {
	return r.cooling[[2]int64{key, group}].Sub(r.now), nil
}
func (r *failoverResolver) CooldownSmartRoutingGroup(_ context.Context, key, group int64, duration time.Duration) error {
	r.cooling[[2]int64{key, group}] = r.now.Add(duration)
	return nil
}

func newFailoverRouter(t *testing.T, key *service.APIKey, resolver *failoverResolver, google bool, handler gin.HandlerFunc, guard SmartRoutingRetryGuard) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	if resolver.cooling == nil {
		resolver.cooling = make(map[[2]int64]time.Time)
	}
	if resolver.now.IsZero() {
		resolver.now = time.Now()
	}
	repo := &stubApiKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) { return key, nil }}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	keyService := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
	auth := gin.HandlerFunc(NewAPIKeyAuthMiddleware(keyService, nil, cfg))
	if google {
		auth = APIKeyAuthWithSubscriptionGoogle(keyService, nil, cfg)
	}
	if guard == nil {
		guard = func(*gin.Context) bool { return true }
	}
	router := gin.New()
	router.Use(WithSmartRoutingResolver(resolver, auth, guard))
	router.POST("/v1/responses", handler)
	router.POST("/v1/chat/completions", handler)
	router.POST("/v1beta/models/*modelAction", handler)
	return router
}

func callFailoverRouter(router *gin.Engine, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-smart")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func upstreamFailure(c *gin.Context, status int, message string) {
	service.SetOpsUpstreamError(c, status, message, "")
	c.Header("Retry-After", "999")
	c.JSON(status, gin.H{"error": gin.H{"message": message}})
}

func TestSmartRoutingFailoverOrderedSuccessAndOriginalBody(t *testing.T) {
	key := smartRoutingTestKey()
	key.SmartRoutingCooldownSeconds = 60
	key.ModelMapping = map[string]string{"alias": "target", "target": "wrong-second-hop"}
	resolver := &failoverResolver{}
	var visits []int64
	router := newFailoverRouter(t, key, resolver, false, func(c *gin.Context) {
		selected, _ := GetAPIKeyFromContext(c)
		id := *selected.GroupID
		visits = append(visits, id)
		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		require.Equal(t, "target", gjson.GetBytes(body, "model").String())
		require.Equal(t, "keep", gjson.GetBytes(body, "input").String())
		if id < 3 {
			upstreamFailure(c, map[int64]int{1: 502, 2: 429}[id], fmt.Sprint("failed-", id))
			return
		}
		c.JSON(200, gin.H{"model": "target", "output": "success"})
	}, nil)
	w := callFailoverRouter(router, "/v1/responses", `{"model":"alias","input":"keep"}`)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Equal(t, []int64{1, 2, 3}, visits)
	require.NotContains(t, w.Body.String(), "failed-")
	require.Equal(t, "alias", gjson.GetBytes(w.Body.Bytes(), "model").String())
	require.Empty(t, w.Header().Get("Retry-After"))
	require.Len(t, resolver.cooling, 2)
	visits = nil
	w = callFailoverRouter(router, "/v1/responses", `{"model":"alias","input":"keep"}`)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Equal(t, []int64{3}, visits)
	require.Nil(t, key.GroupID)
}

func TestSmartRoutingFailoverLastErrorAndAllCooling(t *testing.T) {
	for _, google := range []bool{false, true} {
		t.Run(fmt.Sprint(google), func(t *testing.T) {
			key := smartRoutingTestKey()
			key.SmartRoutingCooldownSeconds = 60
			resolver := &failoverResolver{}
			var visits []int64
			router := newFailoverRouter(t, key, resolver, google, func(c *gin.Context) {
				selected, _ := GetAPIKeyFromContext(c)
				id := *selected.GroupID
				visits = append(visits, id)
				upstreamFailure(c, map[int64]int{1: 502, 2: 429, 3: 524}[id], fmt.Sprint("failed-", id))
			}, nil)
			path, body := "/v1/responses", `{"model":"target"}`
			if google {
				path, body = "/v1beta/models/target:generateContent", `{"contents":[]}`
			}
			w := callFailoverRouter(router, path, body)
			require.Equal(t, 524, w.Code, w.Body.String())
			require.JSONEq(t, `{"error":{"message":"failed-3"}}`, w.Body.String())
			require.Equal(t, []int64{1, 2, 3}, visits)
			visits = nil
			w = callFailoverRouter(router, path, body)
			require.Equal(t, 503, w.Code, w.Body.String())
			require.Empty(t, visits)
			require.Equal(t, "60", w.Header().Get("Retry-After"))
			resolver.now = resolver.now.Add(61 * time.Second)
			w = callFailoverRouter(router, path, body)
			require.Equal(t, 524, w.Code, w.Body.String())
			require.Equal(t, []int64{1, 2, 3}, visits)
		})
	}
}

func TestSmartRoutingFailoverZeroCooldownStillRetries(t *testing.T) {
	key := smartRoutingTestKey()
	resolver := &failoverResolver{}
	var visits []int64
	router := newFailoverRouter(t, key, resolver, false, func(c *gin.Context) {
		selected, _ := GetAPIKeyFromContext(c)
		id := *selected.GroupID
		visits = append(visits, id)
		if id == 1 {
			upstreamFailure(c, 502, "failed")
			return
		}
		c.JSON(200, gin.H{"ok": true})
	}, nil)
	for i := 0; i < 2; i++ {
		require.Equal(t, 200, callFailoverRouter(router, "/v1/responses", `{"model":"target"}`).Code)
	}
	require.Equal(t, []int64{1, 2, 1, 2}, visits)
	require.Empty(t, resolver.cooling)
}

func TestSmartRoutingFailoverPreservesLocalErrorsAndUsage(t *testing.T) {
	for _, test := range []string{"local429", "local500", "policy", "usage", "cancelled", "ordinary", "continuation", "compaction", "image", "server-tool", "background"} {
		t.Run(test, func(t *testing.T) {
			key := smartRoutingTestKey()
			key.SmartRoutingCooldownSeconds = 60
			if test == "ordinary" {
				key.SmartRouting = false
				key.Group = key.CompositeGroups[0].Group
				key.GroupID = &key.Group.ID
			}
			resolver := &failoverResolver{}
			calls := 0
			router := newFailoverRouter(t, key, resolver, false, func(c *gin.Context) {
				calls++
				switch test {
				case "local429":
					c.JSON(429, gin.H{"error": "local"})
					return
				case "local500":
					c.JSON(500, gin.H{"error": "local"})
					return
				case "policy":
					service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalPolicyDenied)
				case "usage":
					service.MarkSmartRoutingAttemptNonReplayable(c.Request.Context(), "usage")
				case "cancelled":
					ctx, cancel := context.WithCancel(c.Request.Context())
					cancel()
					c.Request = c.Request.WithContext(ctx)
				}
				upstreamFailure(c, 502, "last-error")
			}, nil)
			body := `{"model":"target"}`
			switch test {
			case "continuation":
				body = `{"model":"target","previous_response_id":"resp_123"}`
			case "compaction":
				body = `{"model":"target","input":[{"type":"compaction","encrypted_content":"opaque"}]}`
			case "image":
				body = `{"model":"target","tools":[{"type":"image_generation"}]}`
			case "server-tool":
				body = `{"model":"target","tools":[{"type":"mcp"}]}`
			case "background":
				body = `{"model":"target","background":true}`
			}
			w := callFailoverRouter(router, "/v1/responses", body)
			require.GreaterOrEqual(t, w.Code, 400, w.Body.String())
			require.Equal(t, 1, calls)
			if test == "local429" || test == "local500" || test == "policy" || test == "ordinary" {
				require.Empty(t, resolver.cooling)
			}
		})
	}
}

func TestSmartRoutingFailoverReauthAndGuard(t *testing.T) {
	for _, test := range []string{"guard", "quota", "revoked", "mode_changed"} {
		t.Run(test, func(t *testing.T) {
			key := smartRoutingTestKey()
			resolver := &failoverResolver{}
			calls := 0
			guardCalls := 0
			router := newFailoverRouter(t, key, resolver, false, func(c *gin.Context) {
				calls++
				if test == "quota" {
					key.Quota = 1
					key.QuotaUsed = 1
				}
				if test == "revoked" {
					key.Status = service.StatusDisabled
				}
				if test == "mode_changed" {
					key.SmartRouting = false
					key.Group = key.CompositeGroups[2].Group
					key.GroupID = &key.Group.ID
				}
				upstreamFailure(c, 502, "failed-first")
			}, func(c *gin.Context) bool {
				guardCalls++
				c.AbortWithStatusJSON(403, gin.H{"error": "guard-rejected"})
				return false
			})
			w := callFailoverRouter(router, "/v1/responses", `{"model":"target"}`)
			require.Equal(t, 1, calls)
			if test == "mode_changed" {
				require.Equal(t, 502, w.Code)
				require.Contains(t, w.Body.String(), "failed-first")
				require.Zero(t, guardCalls)
				return
			}
			require.NotContains(t, w.Body.String(), "failed-first")
			if test == "guard" {
				require.Equal(t, 403, w.Code)
				require.Equal(t, 1, guardCalls)
			} else {
				require.GreaterOrEqual(t, w.Code, 400)
				require.Zero(t, guardCalls)
			}
		})
	}
}

func TestSmartRoutingFailoverSSEOutputBoundary(t *testing.T) {
	for _, business := range []bool{false, true} {
		t.Run(fmt.Sprint(business), func(t *testing.T) {
			key := smartRoutingTestKey()
			resolver := &failoverResolver{}
			calls := 0
			router := newFailoverRouter(t, key, resolver, false, func(c *gin.Context) {
				calls++
				c.Header("Content-Type", "text/event-stream")
				if calls > 1 {
					_, _ = c.Writer.WriteString("event: response.output_text.delta\ndata: {\"delta\":\"success\"}\n\n")
					c.Writer.Flush()
					return
				}
				_, _ = c.Writer.WriteString(": heartbeat\n\nevent: response.created\ndata: {\"response\":{\"id\":\"failed-id\"}}\n\n")
				c.Writer.Flush()
				if business {
					_, _ = c.Writer.WriteString("event: response.output_text.delta\ndata: {\"delta\":\"partial-output\"}\n\n")
					c.Writer.Flush()
				}
				service.SetOpsUpstreamError(c, 502, "failed", "")
				_, _ = c.Writer.WriteString("data: {\"type\": \"error\", \"message\":\"first-failure\"}\n\n")
				c.Writer.Flush()
			}, nil)
			w := callFailoverRouter(router, "/v1/responses", `{"model":"target","stream":true}`)
			if business {
				require.Equal(t, 1, calls)
				require.Contains(t, w.Body.String(), "partial-output")
			} else {
				require.Equal(t, 2, calls, w.Body.String())
				require.Contains(t, w.Body.String(), "success")
				require.NotContains(t, w.Body.String(), "first-failure")
				require.NotContains(t, w.Body.String(), "failed-id")
			}
		})
	}
}

func TestSmartRoutingFailoverDoesNotReplaceLastErrorWithSelectionError(t *testing.T) {
	key := smartRoutingTestKey()
	resolver := &failoverResolver{availability: func(id int64) service.SmartRoutingGroupAvailability {
		return service.SmartRoutingGroupAvailability{HasModel: id < 3, Schedulable: id == 1}
	}}
	calls := 0
	router := newFailoverRouter(t, key, resolver, false, func(c *gin.Context) { calls++; upstreamFailure(c, 429, "actual-last") }, nil)
	w := callFailoverRouter(router, "/v1/responses", `{"model":"target"}`)
	require.Equal(t, 429, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "actual-last")
	require.Equal(t, 1, calls)
}

func TestSmartRoutingFailoverPendingLimit(t *testing.T) {
	key := smartRoutingTestKey()
	resolver := &failoverResolver{}
	calls := 0
	router := newFailoverRouter(t, key, resolver, false, func(c *gin.Context) {
		calls++
		upstreamFailure(c, 502, strings.Repeat("x", smartRoutingPendingLimit+1))
	}, nil)
	w := callFailoverRouter(router, "/v1/responses", `{"model":"target"}`)
	require.Equal(t, 502, w.Code)
	require.Equal(t, 1, calls)
	require.Greater(t, w.Body.Len(), smartRoutingPendingLimit)
}
