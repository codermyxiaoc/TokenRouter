package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 让旧请求取得摘要快照后立刻发生一次合成重发，验证消费必须依据当前缓存值。
type adversarialResetReplacementCache struct {
	service.EmailCache
	once    sync.Once
	replace func(context.Context) error
}

func (c *adversarialResetReplacementCache) GetPasswordResetToken(ctx context.Context, email string) (*service.PasswordResetTokenData, error) {
	data, err := c.EmailCache.GetPasswordResetToken(ctx, email)
	if err != nil {
		return data, err
	}
	var replacementErr error
	c.once.Do(func() { replacementErr = c.replace(ctx) })
	return data, replacementErr
}

// 旧令牌不能消费重发的新令牌；新令牌仍然只有一个并发消费者成功。
func TestAdversarialEmailResetResendBetweenReadAndConsume(t *testing.T) {
	cache, mr, _ := newMiniredisEmailCache(t)
	ctx := context.Background()
	const email = "reset-race@example.com"
	hash := func(token string) string {
		sum := sha256.Sum256([]byte(token))
		return hex.EncodeToString(sum[:])
	}
	require.NoError(t, cache.SetPasswordResetToken(ctx, email, &service.PasswordResetTokenData{Token: hash("synthetic-old")}, time.Minute))
	interleaved := &adversarialResetReplacementCache{EmailCache: cache,
		replace: func(ctx context.Context) error {
			return cache.SetPasswordResetToken(ctx, email, &service.PasswordResetTokenData{Token: hash("synthetic-new")}, 2*time.Minute)
		}}
	svc := service.NewEmailService(nil, interleaved)
	require.ErrorIs(t, svc.ConsumePasswordResetToken(ctx, email, "synthetic-old"), service.ErrInvalidResetToken)
	data, err := cache.GetPasswordResetToken(ctx, email)
	require.NoError(t, err)
	require.Equal(t, hash("synthetic-new"), data.Token)
	require.Equal(t, 2*time.Minute, mr.TTL(passwordResetKey(email)))

	const consumers = 16
	results := make(chan error, consumers)
	for range consumers {
		go func() { results <- svc.ConsumePasswordResetToken(ctx, email, "synthetic-new") }()
	}
	succeeded := 0
	for range consumers {
		if err := <-results; err == nil {
			succeeded++
		} else {
			require.ErrorIs(t, err, service.ErrInvalidResetToken)
		}
	}
	require.Equal(t, 1, succeeded)
	require.False(t, mr.Exists(passwordResetKey(email)))
}
