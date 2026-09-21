package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type responseBindingContextKey struct{}

// responseBindingContextCache 模拟 Redis 拒绝已取消上下文的行为。
type responseBindingContextCache struct {
	stubGatewayCache
	t      *testing.T
	writes int
}

func (c *responseBindingContextCache) SetSessionAccountID(ctx context.Context, groupID int64, sessionHash string, accountID int64, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	require.Equal(c.t, "request-trace", ctx.Value(responseBindingContextKey{}))
	deadline, ok := ctx.Deadline()
	require.True(c.t, ok, "取消隔离后的写入仍必须有超时限制")
	require.Positive(c.t, time.Until(deadline))
	require.LessOrEqual(c.t, time.Until(deadline), openAIWSStateStoreRedisTimeout)
	c.writes++
	return c.stubGatewayCache.SetSessionAccountID(ctx, groupID, sessionHash, accountID, ttl)
}

func TestOpenAIGatewayService_BindHTTPResponseAccountAfterRequestCanceled(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	groupID := int64(4201)
	c.Set("api_key", &APIKey{ID: 501, GroupID: &groupID})
	SetOpenAIHTTPResponseOwner(c, 601, 501)

	cache := &responseBindingContextCache{t: t}
	svc := &OpenAIGatewayService{cache: cache}
	account := &Account{ID: 37001, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), responseBindingContextKey{}, "request-trace"))
	cancel()
	svc.bindHTTPResponseAccount(ctx, c, account, "resp_canceled")
	require.Equal(t, 3, cache.writes, "账号与用户、密钥所有权均须持久化")

	// 通过新实例读取，避免进程内缓存掩盖客户端断开导致的 Redis 写入丢失。
	reader := &OpenAIGatewayService{cache: cache}
	got, err := reader.getOpenAIWSStateStore().GetResponseAccount(context.Background(), groupID, "resp_canceled")
	require.NoError(t, err)
	require.Equal(t, account.ID, got)
	owned, err := reader.ValidateOpenAIHTTPResponseOwner(context.Background(), groupID, "resp_canceled", 601, 501)
	require.NoError(t, err)
	require.True(t, owned)
	owned, err = reader.ValidateOpenAIHTTPResponseOwner(context.Background(), groupID, "resp_canceled", 602, 501)
	require.NoError(t, err)
	require.False(t, owned, "取消隔离不能绕过用户归属校验")
}
