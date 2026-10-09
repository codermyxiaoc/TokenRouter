package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newMiniredisEmailCache(t *testing.T) (service.EmailCache, *miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewEmailCache(rdb), mr, rdb
}

func TestEmailCache_ConcurrentWrongCodesCannotExceedAttemptCap(t *testing.T) {
	cache, _, _ := newMiniredisEmailCache(t)
	ctx := context.Background()
	email := "user@example.com"

	svc := service.NewEmailService(nil, cache)
	require.NoError(t, cache.SetVerificationCode(ctx, email, &service.VerificationCodeData{
		Code:      "123456",
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(15 * time.Minute),
	}, 15*time.Minute))

	const workers = 50
	var invalid, maxed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := svc.VerifyCode(ctx, email, "000000")
			switch {
			case errors.Is(err, service.ErrInvalidVerifyCode):
				invalid.Add(1)
			case errors.Is(err, service.ErrVerifyCodeMaxAttempts):
				maxed.Add(1)
			default:
				t.Errorf("unexpected result: %v", err)
			}
		}()
	}
	wg.Wait()

	// 最多四次错误比较，之后均由次数上限拒绝。
	require.LessOrEqual(t, int(invalid.Load()), 4)
	require.Equal(t, workers, int(invalid.Load()+maxed.Load()))

	// 达到上限后正确验证码也应拒绝。
	require.ErrorIs(t, svc.VerifyCode(ctx, email, "123456"), service.ErrVerifyCodeMaxAttempts)

	data, err := cache.GetVerificationCode(ctx, email)
	require.NoError(t, err)
	require.GreaterOrEqual(t, data.Attempts, 5)
}

func TestEmailCache_AttemptsResetOnNewCodeAndTTLFollowsCode(t *testing.T) {
	cache, mr, _ := newMiniredisEmailCache(t)
	ctx := context.Background()
	email := "User@Example.com"

	require.NoError(t, cache.SetVerificationCode(ctx, email, &service.VerificationCodeData{Code: "1"}, time.Minute))
	n, err := cache.IncrVerificationCodeAttempts(ctx, email, "1")
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Greater(t, mr.TTL(verifyCodeKey(email)+attemptsKeySuffix), time.Duration(0))

	require.NoError(t, cache.SetVerificationCode(ctx, email, &service.VerificationCodeData{Code: "2"}, time.Minute))
	data, err := cache.GetVerificationCode(ctx, email)
	require.NoError(t, err)
	require.Equal(t, 0, data.Attempts)

	require.NoError(t, cache.DeleteVerificationCode(ctx, email))
	_, err = cache.IncrVerificationCodeAttempts(ctx, email, "1")
	require.Error(t, err)
	require.False(t, mr.Exists(verifyCodeKey(email)+attemptsKeySuffix))
}

func TestEmailCache_PasswordResetTokenHashedAndSingleUse(t *testing.T) {
	cache, mr, _ := newMiniredisEmailCache(t)
	ctx := context.Background()
	email := "reset@example.com"

	svc := service.NewEmailService(nil, cache)

	// 与发送邮件流程一致，仅写入令牌摘要。
	token, err := svc.GeneratePasswordResetToken()
	require.NoError(t, err)
	sum := sha256.Sum256([]byte(token))
	require.NoError(t, cache.SetPasswordResetToken(ctx, email, &service.PasswordResetTokenData{
		Token: hex.EncodeToString(sum[:]), CreatedAt: time.Now(),
	}, 30*time.Minute))

	raw, err := mr.Get(passwordResetKey(email))
	require.NoError(t, err)
	require.False(t, strings.Contains(raw, token), "plaintext token must not be stored")

	require.NoError(t, svc.VerifyPasswordResetToken(ctx, email, token))
	require.ErrorIs(t, svc.ConsumePasswordResetToken(ctx, email, "wrong"), service.ErrInvalidResetToken)

	const workers = 30
	var ok atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if svc.ConsumePasswordResetToken(ctx, email, token) == nil {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1), ok.Load())
	require.False(t, mr.Exists(passwordResetKey(email)))
}

func TestEmailCache_ConsumePasswordResetTokenMismatchKeepsToken(t *testing.T) {
	cache, mr, _ := newMiniredisEmailCache(t)
	ctx := context.Background()
	email := "keep@example.com"
	require.NoError(t, cache.SetPasswordResetToken(ctx, email, &service.PasswordResetTokenData{Token: "abc"}, time.Minute))

	ok, err := cache.ConsumePasswordResetToken(ctx, email, "xyz")
	require.NoError(t, err)
	require.False(t, ok)
	require.True(t, mr.Exists(passwordResetKey(email)))

	ok, err = cache.ConsumePasswordResetToken(ctx, email, "abc")
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = cache.ConsumePasswordResetToken(ctx, email, "abc")
	require.NoError(t, err)
	require.False(t, ok)
}

// 重新发送验证码后，在途旧请求不能占用新验证码的次数。
func TestEmailCache_StaleAttemptDoesNotConsumeResentCode(t *testing.T) {
	cache, mr, _ := newMiniredisEmailCache(t)
	ctx := context.Background()
	for _, notify := range []bool{false, true} {
		set, incr, key := cache.SetVerificationCode, cache.IncrVerificationCodeAttempts, verifyCodeKey("resend@example.com")
		if notify {
			set, incr, key = cache.SetNotifyVerifyCode, cache.IncrNotifyVerifyCodeAttempts, notifyVerifyKey("resend@example.com")
		}
		require.NoError(t, set(ctx, "resend@example.com", &service.VerificationCodeData{Code: "old"}, time.Minute))
		require.NoError(t, set(ctx, "resend@example.com", &service.VerificationCodeData{Code: "new"}, time.Minute))
		_, err := incr(ctx, "resend@example.com", "old")
		require.Error(t, err)
		require.False(t, mr.Exists(key+attemptsKeySuffix))
		count, err := incr(ctx, "resend@example.com", "new")
		require.NoError(t, err)
		require.Equal(t, 1, count)
	}
}

// 缓存发生故障时不能继续比较验证码而绕过并发次数上限。
func TestEmailCache_VerificationFailsClosedOnRedisFailure(t *testing.T) {
	cache, mr, _ := newMiniredisEmailCache(t)
	ctx := context.Background()
	require.NoError(t, cache.SetVerificationCode(ctx, "failure@example.com", &service.VerificationCodeData{Code: "123456", ExpiresAt: time.Now().Add(time.Minute)}, time.Minute))
	mr.SetError("ERR injected cache failure")
	require.Error(t, service.NewEmailService(nil, cache).VerifyCode(ctx, "failure@example.com", "123456"))
}
