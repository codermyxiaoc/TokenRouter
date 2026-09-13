//go:build unit

package middleware

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// newSmartRoutingDisconnectRouter 使用真实 Recovery 与认证顺序，验证异常退出时仍能回写客户端。
func newSmartRoutingDisconnectRouter(t *testing.T, key *service.APIKey, terminal gin.HandlerFunc) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	repo := &stubApiKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) { return key, nil }}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	keyService := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
	resolver := &failoverResolver{now: time.Now(), cooling: make(map[[2]int64]time.Time)}
	router := gin.New()
	router.Use(Recovery())
	router.Use(WithSmartRoutingResolver(resolver, gin.HandlerFunc(NewAPIKeyAuthMiddleware(keyService, nil, cfg)), func(*gin.Context) bool { return true }))
	router.POST("/v1/responses", terminal)
	return router
}

// 首轮认证会通过 Next 执行业务；无业务输出的 panic 必须恢复底层 writer，不能吞掉 Recovery 的 500。
func TestSmartRoutingDisconnectRecoveryBeforeCommittedResponse(t *testing.T) {
	for _, name := range []string{"first_group", "buffered_prelude", "second_group", "ordinary_key"} {
		t.Run(name, func(t *testing.T) {
			key := smartRoutingTestKey()
			if name == "ordinary_key" {
				key.SmartRouting = false
				key.Group = key.CompositeGroups[0].Group
				key.GroupID = &key.Group.ID
			}
			calls := 0
			router := newSmartRoutingDisconnectRouter(t, key, func(c *gin.Context) {
				calls++
				if name == "second_group" && calls == 1 {
					upstreamFailure(c, http.StatusBadGateway, "discarded first failure")
					return
				}
				if name == "buffered_prelude" {
					c.Header("Content-Type", "text/event-stream")
					_, _ = c.Writer.WriteString("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"discarded-id\"}}\n\n")
					c.Writer.Flush()
				}
				panic("controlled smart routing panic")
			})
			response := callFailoverRouter(router, "/v1/responses", `{"model":"target","stream":true}`)
			require.Equal(t, http.StatusInternalServerError, response.Code, response.Body.String())
			require.NotEmpty(t, response.Body.String())
			require.Contains(t, response.Header().Get("Content-Type"), "application/json")
			require.NotContains(t, response.Body.String(), "discarded-id")
			require.NotContains(t, response.Body.String(), "discarded first failure")
			if name == "second_group" {
				require.Equal(t, 2, calls)
			} else {
				require.Equal(t, 1, calls)
			}
		})
	}
}

// 提前注册异常清理后，智能与普通 Key 的正常成功响应仍须完整返回。
func TestSmartRoutingDisconnectSuccessResponseUnchanged(t *testing.T) {
	for _, smart := range []bool{false, true} {
		name := "ordinary_key"
		if smart {
			name = "smart_key"
		}
		t.Run(name, func(t *testing.T) {
			key := smartRoutingTestKey()
			key.SmartRouting = smart
			if !smart {
				key.Group = key.CompositeGroups[0].Group
				key.GroupID = &key.Group.ID
			}
			key.ModelMapping = map[string]string{"alias": "target"}
			calls := 0
			router := newSmartRoutingDisconnectRouter(t, key, func(c *gin.Context) {
				calls++
				c.Header("X-Result", "preserved")
				c.JSON(http.StatusOK, gin.H{"model": "target", "output": "success"})
			})
			response := callFailoverRouter(router, "/v1/responses", `{"model":"alias"}`)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			require.JSONEq(t, `{"model":"alias","output":"success"}`, response.Body.String())
			require.Equal(t, "preserved", response.Header().Get("X-Result"))
			require.Equal(t, 1, calls)
		})
	}
}
