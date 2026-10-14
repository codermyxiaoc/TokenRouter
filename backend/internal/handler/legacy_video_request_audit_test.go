//go:build unit

package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	middleware "github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 查询与删除都必须使用原任务、原 Key 和原分组；拒绝后不能访问上游或改写删除证据。
func TestSeedanceDurableLookupRejectsForeignIdentity(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		for _, mismatch := range []string{"user", "key", "group", "task"} {
			t.Run(method+"/"+mismatch, func(t *testing.T) {
				h, slots, bindings, upstream := newGrokMediaSlotHandler(t, false, false, service.PlatformOpenAI)
				store := &legacyVideoHandlerStore{task: &service.LegacyVideoTask{Source: "seedance_video", TaskID: "task", UserID: 10, BillingUserID: 10, APIKeyID: 20, GroupID: 24, AccountID: 1}}
				service.NewLegacyVideoRecovery(store, h.gatewayService, nil, nil, nil)
				c, w := legacyVideoHandlerContext(context.Background(), true, false)
				c.Request.Method = method
				key, _ := middleware.GetAPIKeyFromContext(c)
				switch mismatch {
				case "user":
					key.UserID, key.User.ID = 99, 99
					c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 99, Concurrency: 5})
				case "key":
					key.ID = 99
				case "group":
					other := int64(99)
					key.GroupID, key.Group.ID = &other, other
				case "task":
					c.Params = gin.Params{{Key: "task_id", Value: "another-task"}}
				}
				h.SeedanceTasks(c)
				require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
				require.Zero(t, upstream.calls)
				require.Zero(t, store.deletions)
				require.Nil(t, store.task.UpstreamDeletedAt)
				require.Empty(t, bindings.claimContexts)
				slots.assertReleased(t)
			})
		}
	}
}

// 即使持久任务中存在异常 ID，也不能使路径、查询参数或片段逃逸到其它上游资源。
func TestSeedanceDurableDeleteRejectsUnsafePath(t *testing.T) {
	for _, id := range []string{"..", "../other", "task/other", "task?admin=1", "task#fragment", "task%2fother"} {
		t.Run(id, func(t *testing.T) {
			h, slots, _, upstream := newGrokMediaSlotHandler(t, false, false, service.PlatformOpenAI)
			store := &legacyVideoHandlerStore{task: &service.LegacyVideoTask{Source: "seedance_video", TaskID: id, UserID: 10, BillingUserID: 10, APIKeyID: 20, GroupID: 24, AccountID: 1}}
			service.NewLegacyVideoRecovery(store, h.gatewayService, nil, nil, nil)
			c, w := legacyVideoHandlerContext(context.Background(), true, false)
			c.Request.Method = http.MethodDelete
			c.Params = gin.Params{{Key: "task_id", Value: id}}
			h.SeedanceTasks(c)
			require.GreaterOrEqual(t, w.Code, http.StatusBadRequest, w.Body.String())
			require.Zero(t, upstream.calls)
			require.Zero(t, store.deletions)
			require.Nil(t, store.task.UpstreamDeletedAt)
			slots.assertReleased(t)
		})
	}
}

// 团队任务的执行成员与付款人不同，删除事实必须保留两种身份，不能改归到付款人名下。
func TestSeedanceDurableDeletePreservesTeamActorAndPayer(t *testing.T) {
	h, slots, bindings, upstream := newGrokMediaSlotHandler(t, false, false, service.PlatformOpenAI)
	store := &legacyVideoHandlerStore{task: &service.LegacyVideoTask{Source: "seedance_video", TaskID: "task", UserID: 37, BillingUserID: 10, APIKeyID: 20, GroupID: 24, AccountID: 1}}
	service.NewLegacyVideoRecovery(store, h.gatewayService, nil, nil, nil)
	c, w := legacyVideoHandlerContext(context.Background(), true, false)
	c.Request.Method = http.MethodDelete
	key, _ := middleware.GetAPIKeyFromContext(c)
	key.UserID, key.ActorUser = 37, &service.User{ID: 37}
	store.onDelete = func(_ context.Context, task *service.LegacyVideoTask) {
		require.Equal(t, int64(37), task.UserID)
		require.Equal(t, int64(10), task.BillingUserID)
	}
	upstream.call = func(req *http.Request, accountID int64) (*http.Response, error) {
		require.Equal(t, http.MethodDelete, req.Method)
		require.Equal(t, int64(1), accountID)
		return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	h.SeedanceTasks(c)
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	require.Equal(t, 1, upstream.calls)
	require.Equal(t, 1, store.deletions)
	require.Empty(t, bindings.claimContexts)
	slots.assertReleased(t)
}

// 本地请求格式错误应在任何付费创建之前拒绝，不以空模型或缺失内容尝试上游。
func TestSeedanceMalformedCreateDoesNotReachUpstream(t *testing.T) {
	for _, body := range []string{"{", "[]", `{"model":123,"content":[{}]}`, `{"model":"","content":[{}]}`, `{"model":"seedance","content":[]}`, `{"model":"seedance","content":{}}`} {
		t.Run(body, func(t *testing.T) {
			h, slots, _, upstream := newGrokMediaSlotHandler(t, false, false, service.PlatformOpenAI)
			store := &legacyVideoHandlerStore{}
			service.NewLegacyVideoRecovery(store, h.gatewayService, nil, nil, nil)
			c, w := legacyVideoHandlerContext(context.Background(), true, true)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v3/contents/generations/tasks", strings.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			h.SeedanceTasks(c)
			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			require.Zero(t, upstream.calls)
			require.Zero(t, store.saves)
			slots.assertReleased(t)
		})
	}
}
