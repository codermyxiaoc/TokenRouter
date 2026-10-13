//go:build unit

package handler

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/ctxkey"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// cachedCancellationSlots 记录真实槽占用，取消客户端不能让仍运行的上游绕过并发限制。
type cachedCancellationSlots struct {
	fakeConcurrencyCache
	users    atomic.Int32
	accounts atomic.Int32
}

func (s *cachedCancellationSlots) AcquireUserSlot(context.Context, int64, int, string) (bool, error) {
	s.users.Add(1)
	return true, nil
}
func (s *cachedCancellationSlots) ReleaseUserSlot(context.Context, int64, string) error {
	s.users.Add(-1)
	return nil
}
func (s *cachedCancellationSlots) AcquireAccountSlot(context.Context, int64, int, string) (bool, error) {
	s.accounts.Add(1)
	return true, nil
}
func (s *cachedCancellationSlots) ReleaseAccountSlot(context.Context, int64, string) error {
	s.accounts.Add(-1)
	return nil
}

func TestGeminiCachedContentsCreate_ClientCancellationKeepsSlotAndSettles(t *testing.T) {
	env := newCachedBillingEnv(t, 500, 900)
	slots := &cachedCancellationSlots{}
	env.h.concurrencyHelper = NewConcurrencyHelper(service.NewConcurrencyService(slots), SSEPingFormatNone, 0)
	started, finish := make(chan struct{}), make(chan struct{})
	env.upstream.createBody = cachedBillingCreateBody(time.Now().Add(24 * time.Hour))
	env.upstream.beforeCreate = func() { close(started); <-finish }
	c, response := newGeminiCachedContentTestContext(http.MethodPost, "/v1beta/cachedContents", env.apiKey)
	ctx, cancel := context.WithCancel(context.WithValue(c.Request.Context(), ctxkey.Group, env.group))
	defer cancel()
	c.Request = c.Request.WithContext(ctx)
	c.Request.Body = io.NopCloser(strings.NewReader(`{"model":"models/gemini-2.5-pro","ttl":"86400s","contents":[{"role":"user","parts":[{"text":"doc"}]}]}`))
	done := make(chan struct{})
	go func() { defer close(done); env.h.GeminiCachedContentsCreate(c) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		close(finish)
		t.Fatal("上游未开始")
	}
	cancel()
	// 使用持续断言捕获旧代码的异步取消释放，而不只是读取竞态瞬间。
	require.Eventually(t, func() bool { return ctx.Err() != nil }, time.Second, time.Millisecond)
	require.Never(t, func() bool { return slots.users.Load() != 1 }, 40*time.Millisecond, time.Millisecond)
	close(finish)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("断连后的结算未完成")
	}
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Len(t, env.repo.records, 1)
	require.Len(t, env.usage.totals(), 1, "上游已创建的缓存必须在客户端离线后完成持久计费")
	require.Zero(t, slots.users.Load())
}

// 已取得和排队取得的账号槽都必须由实际操作结束释放。
func TestGeminiCachedContentAccountSlotCancellationDoesNotRelease(t *testing.T) {
	for _, acquired := range []bool{true, false} {
		t.Run(map[bool]string{true: "selected", false: "waiting"}[acquired], func(t *testing.T) {
			key := geminiCachedContentTestAPIKey(service.PlatformGemini)
			c, _ := newGeminiCachedContentTestContext(http.MethodPost, "/v1beta/cachedContents", key)
			ctx, cancel := context.WithCancel(c.Request.Context())
			defer cancel()
			c.Request = c.Request.WithContext(ctx)
			slots := &cachedCancellationSlots{}
			selection := &service.AccountSelectionResult{Account: &service.Account{ID: 900}, Acquired: acquired}
			if acquired {
				slots.accounts.Add(1)
				selection.ReleaseFunc = func() { slots.accounts.Add(-1) }
			} else {
				selection.WaitPlan = &service.AccountWaitPlan{MaxConcurrency: 1, MaxWaiting: 2, Timeout: time.Second}
			}
			h := &GatewayHandler{}
			release, ok := h.acquireGeminiCachedContentAccountSlot(c, zap.NewNop(), NewConcurrencyHelper(service.NewConcurrencyService(slots), SSEPingFormatNone, 0), selection)
			require.True(t, ok)
			cancel()
			require.Never(t, func() bool { return slots.accounts.Load() != 1 }, 40*time.Millisecond, time.Millisecond)
			release()
			require.Zero(t, slots.accounts.Load())
		})
	}
}

// 缺少输入价的公开别名即使能映射到有存储价的模型，也必须在上游调用前拒绝。
func TestGeminiCachedContentsCreate_MissingInputPriceNeverCallsUpstream(t *testing.T) {
	env := newCachedBillingEnv(t, 500, 900)
	const alias = "unpriced-public-alias-v216"
	env.accounts[900].Credentials["model_mapping"] = map[string]any{alias: "gemini-2.5-pro"}
	response := env.serve(http.MethodPost, "/v1beta/cachedContents",
		`{"model":"models/`+alias+`","ttl":"3600s","contents":[{"role":"user","parts":[{"text":"doc"}]}]}`,
		nil, env.h.GeminiCachedContentsCreate)
	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), "input pricing")
	require.Zero(t, env.upstream.methodCalls(http.MethodPost))
	require.Empty(t, env.repo.records)
	require.Empty(t, env.usage.totals())
}
