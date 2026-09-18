//go:build unit

package middleware

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// recordConfirmedSmartRoutingError 模拟转发服务在实际访问上游后留下的失败快照。
func recordConfirmedSmartRoutingError(c *gin.Context, status int) {
	key, _ := GetAPIKeyFromContext(c)
	group := key.Group
	event := &service.OpsUpstreamErrorEvent{
		GroupID: group.ID, GroupName: group.Name, Platform: group.Platform,
		AccountID: group.ID + 100, Kind: "http_error", UpstreamStatusCode: status,
		Message: fmt.Sprintf("upstream-failed-%d", group.ID),
	}
	if status == 0 {
		event.Kind = "request_error"
	}
	c.Set(service.OpsUpstreamErrorsKey, append(smartRoutingUpstreamEvents(c), event))
	service.SetOpsUpstreamError(c, status, event.Message, "")
}

func TestSmartRoutingAnyConfirmedHTTPErrorRecovers(t *testing.T) {
	for _, protocol := range []string{"json", "sse", "gemini"} {
		for _, status := range []int{400, 401, 402, 403, 404, 405, 408, 409, 413, 415, 422, 429, 499, 500, 502, 524, 599} {
			t.Run(fmt.Sprintf("%s/%d", protocol, status), func(t *testing.T) {
				key := smartRoutingTestKey()
				key.SmartRoutingCooldownSeconds = 60
				for i := range key.CompositeGroups {
					key.CompositeGroups[i].Group.Name = fmt.Sprintf("test%d", i+1)
				}
				resolver := &failoverResolver{}
				var visits []int64
				var original *gin.Context
				router := newFailoverRouter(t, key, resolver, protocol == "gemini", func(c *gin.Context) {
					if original == nil {
						original = c
					}
					selected, _ := GetAPIKeyFromContext(c)
					id := selected.Group.ID
					visits = append(visits, id)
					if id < 3 {
						recordConfirmedSmartRoutingError(c, status)
						if protocol == "sse" {
							c.Header("Content-Type", "text/event-stream")
							service.MarkOpsStreamError(c, "upstream_error", "upstream-failed", status)
							_, _ = c.Writer.WriteString("event: error\ndata: {\"error\":{\"message\":\"upstream-failed\"}}\n\n")
						} else {
							c.JSON(status, gin.H{"error": gin.H{"message": "upstream-failed"}})
						}
						return
					}
					c.JSON(http.StatusOK, gin.H{"output": "recovered"})
				}, nil)
				path, body := "/v1/responses", `{"model":"target"}`
				if protocol == "gemini" {
					path, body = "/v1beta/models/target:generateContent", `{"contents":[]}`
				}
				response := callFailoverRouter(router, path, body)
				require.Equal(t, http.StatusOK, response.Code)
				require.Equal(t, []int64{1, 2, 3}, visits)
				require.NotContains(t, response.Body.String(), "upstream-failed")
				require.Len(t, resolver.cooling, 2)
				events := smartRoutingUpstreamEvents(original)
				require.Len(t, events, 2)
				for i, event := range events {
					require.Equal(t, int64(i+1), event.GroupID, "不能用恢复组覆盖失败归属")
					require.Equal(t, fmt.Sprintf("test%d", i+1), event.GroupName)
					require.Equal(t, int64(3), event.RecoveredGroupID)
					require.Equal(t, "test3", event.RecoveredGroupName)
					require.Equal(t, service.PlatformGemini, event.RecoveredPlatform)
				}
			})
		}
	}
}

func TestSmartRoutingAnyErrorLastFailureAndCooldown(t *testing.T) {
	key := smartRoutingTestKey()
	key.SmartRoutingCooldownSeconds = 60
	resolver := &failoverResolver{}
	var original *gin.Context
	visits := 0
	router := newFailoverRouter(t, key, resolver, false, func(c *gin.Context) {
		if original == nil {
			original = c
		}
		visits++
		selected, _ := GetAPIKeyFromContext(c)
		status := map[int64]int{1: 401, 2: 403, 3: 422}[selected.Group.ID]
		recordConfirmedSmartRoutingError(c, status)
		c.JSON(status, gin.H{"error": gin.H{"message": fmt.Sprintf("failed-%d", selected.Group.ID)}})
	}, nil)
	response := callFailoverRouter(router, "/v1/responses", `{"model":"target"}`)
	require.Equal(t, 422, response.Code)
	require.Contains(t, response.Body.String(), "failed-3")
	require.Equal(t, 3, visits)
	for _, event := range smartRoutingUpstreamEvents(original) {
		require.Zero(t, event.RecoveredGroupID)
	}
	response = callFailoverRouter(router, "/v1/responses", `{"model":"target"}`)
	require.Equal(t, 503, response.Code)
	require.Equal(t, 3, visits, "全部冷却时不得再次访问上游")
}

func TestSmartRoutingAnyErrorKeepsReplayBoundaries(t *testing.T) {
	for _, scenario := range []string{"local_validation", "stale_history", "same_status_local_validation", "policy", "cyber_policy", "request_scoped", "usage", "output", "cancelled", "ordinary"} {
		t.Run(scenario, func(t *testing.T) {
			key := smartRoutingTestKey()
			key.SmartRoutingCooldownSeconds = 60
			if scenario == "ordinary" {
				key.SmartRouting = false
				key.Group = key.CompositeGroups[0].Group
				key.GroupID = &key.Group.ID
				key.CompositeGroups = nil
			}
			resolver := &failoverResolver{}
			visits := 0
			router := newFailoverRouter(t, key, resolver, false, func(c *gin.Context) {
				visits++
				if scenario != "local_validation" {
					recordConfirmedSmartRoutingError(c, 403)
				}
				switch scenario {
				case "local_validation", "stale_history":
					service.SetOpsUpstreamError(c, 400, "invalid-local-input", "")
				case "same_status_local_validation":
					// 相同状态码无法证明新错误仍来自上游，本地校验必须明确标记来源。
					service.SetOpsUpstreamError(c, 403, "invalid-local-input", "")
					service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalPolicyDenied)
				case "policy":
					service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalPolicyDenied)
				case "cyber_policy":
					service.MarkOpsCyberPolicy(c, service.CyberPolicyMark{Message: "policy-result", UpstreamStatus: 403})
				case "request_scoped":
					service.MarkOpsStreamErrorValue(c, service.OpsStreamError{RequestScoped: true, IntendedStatus: 400, Message: "policy-result"})
				case "usage":
					service.MarkSmartRoutingAttemptNonReplayable(c.Request.Context(), "usage_recorded")
				case "cancelled":
					ctx, cancel := context.WithCancel(c.Request.Context())
					c.Request = c.Request.WithContext(ctx)
					cancel()
				case "output":
					c.Header("Content-Type", "text/event-stream")
					_, _ = c.Writer.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
					_, _ = c.Writer.WriteString("event: error\ndata: {\"error\":{\"message\":\"failed\"}}\n\n")
					return
				}
				c.JSON(400, gin.H{"error": gin.H{"message": "failed"}})
			}, nil)
			callFailoverRouter(router, "/v1/responses", `{"model":"target"}`)
			require.Equal(t, 1, visits)
			if scenario == "usage" || scenario == "output" || scenario == "cancelled" {
				require.Len(t, resolver.cooling, 1, "仅限制重放时仍记录实际上游失败冷却")
			} else {
				require.Empty(t, resolver.cooling)
			}
		})
	}
}

func TestSmartRoutingConfirmedTransportErrorAndHTTP200Envelope(t *testing.T) {
	for _, status := range []int{0, 403} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			visits := 0
			router := newFailoverRouter(t, smartRoutingTestKey(), &failoverResolver{}, false, func(c *gin.Context) {
				visits++
				if visits == 1 {
					recordConfirmedSmartRoutingError(c, status)
					// 原生协议可能用 200 包装错误，必须按实际上游错误事实决定重试。
					c.JSON(200, gin.H{"error": gin.H{"message": "failed"}})
					return
				}
				c.JSON(200, gin.H{"output": "success"})
			}, nil)
			response := callFailoverRouter(router, "/v1/responses", `{"model":"target"}`)
			require.Equal(t, 2, visits)
			require.Contains(t, response.Body.String(), "success")
		})
	}
}

func TestSmartRoutingCancelledTransportCannotReuseHistoricalHTTPStatus(t *testing.T) {
	key := smartRoutingTestKey()
	key.SmartRoutingCooldownSeconds = 60
	resolver := &failoverResolver{}
	visits := 0
	router := newFailoverRouter(t, key, resolver, false, func(c *gin.Context) {
		visits++
		recordConfirmedSmartRoutingError(c, 503)
		// 模拟组内下一次请求由客户端取消，SetOpsUpstreamError(0) 不会清空旧 503。
		recordConfirmedSmartRoutingError(c, 0)
		ctx, cancel := context.WithCancel(c.Request.Context())
		c.Request = c.Request.WithContext(ctx)
		cancel()
		c.JSON(502, gin.H{"error": gin.H{"message": "request cancelled"}})
	}, nil)
	callFailoverRouter(router, "/v1/responses", `{"model":"target"}`)
	require.Equal(t, 1, visits)
	require.Empty(t, resolver.cooling)
}

func TestSmartRoutingRecoveryDestinationRequiresSuccessfulDelivery(t *testing.T) {
	for _, outcome := range []string{"same_group_success", "stream_failure", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			var original *gin.Context
			key := smartRoutingTestKey()
			key.CompositeGroups = key.CompositeGroups[:1]
			router := newFailoverRouter(t, key, &failoverResolver{}, false, func(c *gin.Context) {
				original = c
				recordConfirmedSmartRoutingError(c, 403)
				if outcome == "cancelled" {
					ctx, cancel := context.WithCancel(c.Request.Context())
					c.Request = c.Request.WithContext(ctx)
					cancel()
				}
				if outcome == "stream_failure" {
					service.MarkOpsStreamError(c, "upstream_error", "failed", 403)
				}
				c.JSON(200, gin.H{"output": "body", "error": nil})
			}, nil)
			callFailoverRouter(router, "/v1/responses", `{"model":"target"}`)
			events := smartRoutingUpstreamEvents(original)
			require.Len(t, events, 1)
			if outcome == "same_group_success" {
				require.Equal(t, int64(1), events[0].RecoveredGroupID)
			} else {
				require.Zero(t, events[0].RecoveredGroupID)
			}
		})
	}
}
