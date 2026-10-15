//go:build unit

package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 完整 handler 验证明确拒绝先换号，成功账号成为唯一任务归属且所有并发槽都释放。
func TestSeedanceHandlerRejectedCreationUsesHealthyAccount(t *testing.T) {
	h, slots, bindings, upstream := newGrokMediaSlotHandler(t, false, false, service.PlatformOpenAI)
	var attempts []int64
	upstream.call = func(req *http.Request, id int64) (*http.Response, error) {
		attempts = append(attempts, id)
		status, body := 429, `{"error":{"code":"QuotaExceeded"}}`
		if len(attempts) > 1 {
			status, body = 200, `{"id":"accepted-healthy","status":"queued"}`
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}
	c, w := grokMediaSlotContext(context.Background(), true)
	key, _ := middleware.GetAPIKeyFromContext(c)
	key.Group.Platform = service.PlatformOpenAI
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v3/contents/generations/tasks", strings.NewReader(`{"model":"doubao-seedance","content":[{"type":"text","text":"waves"}]}`))
	c.Request.Header.Set("Content-Type", "application/json")
	h.SeedanceTasks(c)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Len(t, attempts, 2)
	require.NotEqual(t, attempts[0], attempts[1])
	require.Equal(t, attempts[1], bindings.owner)
	require.Len(t, bindings.pending, 1)
	slots.assertReleased(t)
}

func TestSeedanceHandlerAcceptedErrorNeverSwitchesAccount(t *testing.T) {
	h, slots, bindings, upstream := newGrokMediaSlotHandler(t, false, false, service.PlatformOpenAI)
	var attempts []int64
	upstream.call = func(req *http.Request, id int64) (*http.Response, error) {
		attempts = append(attempts, id)
		return &http.Response{StatusCode: 503, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"accepted-with-error","error":{"code":"ServiceUnavailable"}}`))}, nil
	}
	c, w := grokMediaSlotContext(context.Background(), true)
	key, _ := middleware.GetAPIKeyFromContext(c)
	key.Group.Platform = service.PlatformOpenAI
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v3/contents/generations/tasks", strings.NewReader(`{"model":"doubao-seedance","content":[{"type":"text","text":"waves"}]}`))
	c.Request.Header.Set("Content-Type", "application/json")
	h.SeedanceTasks(c)
	require.Equal(t, 503, w.Code, w.Body.String())
	require.Len(t, attempts, 1)
	require.Equal(t, attempts[0], bindings.owner)
	require.Len(t, bindings.pending, 1)
	require.Contains(t, w.Body.String(), "accepted-with-error")
	slots.assertReleased(t)
}

// 普通内部错误无法证明未受理，不能因新增耗尽透传逻辑而重复创建。
func TestSeedanceHandlerAmbiguousCreationPreservesNativeResponseWithoutRetry(t *testing.T) {
	h, slots, bindings, upstream := newGrokMediaSlotHandler(t, false, false, service.PlatformOpenAI)
	body := `{"error":{"code":"InternalError","message":"internal server error"}}`
	upstream.call = func(*http.Request, int64) (*http.Response, error) {
		return &http.Response{StatusCode: 503, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}
	c, w := grokMediaSlotContext(context.Background(), true)
	key, _ := middleware.GetAPIKeyFromContext(c)
	key.Group.Platform = service.PlatformOpenAI
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v3/contents/generations/tasks", strings.NewReader(`{"model":"doubao-seedance","content":[{"type":"text","text":"waves"}]}`))
	c.Request.Header.Set("Content-Type", "application/json")
	h.SeedanceTasks(c)
	require.Equal(t, 503, w.Code, w.Body.String())
	require.JSONEq(t, body, w.Body.String())
	require.Equal(t, 1, upstream.calls)
	require.Empty(t, bindings.pending)
	slots.assertReleased(t)
}
