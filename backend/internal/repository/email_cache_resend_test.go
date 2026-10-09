package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 在旧请求完成次数校验后、服务层删除验证码前插入一次真实重发，固定复现交错窗口。
type resendAfterVerificationAttemptCache struct {
	service.EmailCache
	once         sync.Once
	afterAttempt func(context.Context) error
	resendErr    error
}

func (c *resendAfterVerificationAttemptCache) after(ctx context.Context, n int, err error) (int, error) {
	if err != nil {
		return n, err
	}
	c.once.Do(func() { c.resendErr = c.afterAttempt(ctx) })
	return n, c.resendErr
}

func (c *resendAfterVerificationAttemptCache) IncrVerificationCodeAttempts(ctx context.Context, email, expectedCode string) (int, error) {
	n, err := c.EmailCache.IncrVerificationCodeAttempts(ctx, email, expectedCode)
	return c.after(ctx, n, err)
}

func (c *resendAfterVerificationAttemptCache) IncrNotifyVerifyCodeAttempts(ctx context.Context, email, expectedCode string) (int, error) {
	n, err := c.EmailCache.IncrNotifyVerifyCodeAttempts(ctx, email, expectedCode)
	return c.after(ctx, n, err)
}

type resendNotifyUserRepo struct {
	service.UserRepository
	user *service.User
}

func (r *resendNotifyUserRepo) GetByID(context.Context, int64) (*service.User, error) {
	return r.user, nil
}

func (r *resendNotifyUserRepo) Update(_ context.Context, user *service.User, _ service.UserUpdateFields) error {
	r.user = user
	return nil
}

// 同时覆盖真实注册验证与通知邮箱绑定，防止服务层回退到无条件清理接口。
func TestEmailCache_SuccessfulVerificationDoesNotDeleteResentCode(t *testing.T) {
	for _, kind := range []string{"registration", "notification"} {
		t.Run(kind, func(t *testing.T) {
			cache, mr, _ := newMiniredisEmailCache(t)
			ctx := context.Background()
			const email = "Resend@Example.com"
			set, get, key := cache.SetVerificationCode, cache.GetVerificationCode, verifyCodeKey(email)
			if kind == "notification" {
				set, get, key = cache.SetNotifyVerifyCode, cache.GetNotifyVerifyCode, notifyVerifyKey(email)
			}
			require.NoError(t, set(ctx, email, &service.VerificationCodeData{Code: "111111"}, time.Minute))

			interleaved := &resendAfterVerificationAttemptCache{
				EmailCache: cache,
				afterAttempt: func(ctx context.Context) error {
					// 新码已经有自己的验证次数，旧请求不能删除它或清零次数。
					return set(ctx, email, &service.VerificationCodeData{Code: "222222", Attempts: 2}, 2*time.Minute)
				},
			}
			verify := service.NewEmailService(nil, interleaved).VerifyCode
			var users *resendNotifyUserRepo
			if kind == "notification" {
				users = &resendNotifyUserRepo{user: &service.User{ID: 1}}
				userService := service.NewUserService(users, nil, nil, nil)
				verify = func(ctx context.Context, email, code string) error {
					return userService.VerifyAndAddNotifyEmail(ctx, 1, email, code, interleaved)
				}
			}

			require.NoError(t, verify(ctx, email, "111111"))
			data, err := get(ctx, email)
			require.NoError(t, err, "旧验证请求不得删除重发的新码")
			require.Equal(t, "222222", data.Code)
			require.Equal(t, 2, data.Attempts)
			require.Equal(t, 2*time.Minute, mr.TTL(key))
			require.Equal(t, 2*time.Minute, mr.TTL(key+attemptsKeySuffix))

			// 匹配新码后正常消费，同时清理验证码与次数，业务操作仍成功。
			require.NoError(t, verify(ctx, email, "222222"))
			require.False(t, mr.Exists(key))
			require.False(t, mr.Exists(key+attemptsKeySuffix))
			if users != nil {
				require.Len(t, users.user.BalanceNotifyExtraEmails, 1)
				require.True(t, users.user.BalanceNotifyExtraEmails[0].Verified)
			}
		})
	}
}
