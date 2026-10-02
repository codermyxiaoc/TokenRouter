//go:build unit

package middleware

import (
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 视频任务可在提交前越过不可用分组，进入付费创建后即使收到可恢复错误也不能跨组重复生成。
func TestVideoSmartRoutingReadinessSelectsBeforeSubmitButNeverReplays(t *testing.T) {
	paths := []string{
		"/v1/video/generations", "/v1/videos", "/api/v3/contents/generations/tasks",
		"/omni-video/video-model", "/api/v1/services/aigc/video-generation/video-synthesis", "/v2/video_generation",
	}
	for _, path := range paths {
		for _, status := range []int{http.StatusBadRequest, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
			t.Run(fmt.Sprintf("%s/%d", path, status), func(t *testing.T) {
				key := smartRoutingTestKey()
				for i := range key.CompositeGroups {
					key.CompositeGroups[i].Group.Platform = service.PlatformVideo
				}
				var checked, dispatched []int64
				resolver := &failoverResolver{availability: func(id int64) service.SmartRoutingGroupAvailability {
					checked = append(checked, id)
					return service.SmartRoutingGroupAvailability{HasModel: true, Schedulable: id > 1}
				}}
				router := newFailoverRouter(t, key, resolver, false, func(*gin.Context) {}, nil)
				router.POST(path, func(c *gin.Context) {
					selected, ok := GetAPIKeyFromContext(c)
					require.True(t, ok)
					dispatched = append(dispatched, selected.Group.ID)
					body, err := io.ReadAll(c.Request.Body)
					require.NoError(t, err)
					require.Equal(t, "video-model", gjson.GetBytes(body, "model").String())
					require.Equal(t, "keep", gjson.GetBytes(body, "future.value").String())
					require.False(t, smartRoutingReplayEndpoint(c))
					recordConfirmedSmartRoutingError(c, status)
					c.JSON(status, gin.H{"error": "fixture upstream create failure"})
				})
				response := callFailoverRouter(router, path, `{"model":"video-model","duration":8,"future":{"value":"keep"}}`)
				require.Equal(t, status, response.Code, response.Body.String())
				require.Equal(t, []int64{1, 2}, checked, "发送前允许从满载分组重新选择")
				require.Equal(t, []int64{2}, dispatched, "付费创建不能因为 4xx/5xx 再发送到第三组")
				require.Nil(t, key.GroupID, "共享密钥配置不能被本次路由改写")
			})
		}
	}
}
