package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/redis/go-redis/v9"
)

const (
	verifyCodeKeyPrefix          = "verify_code:"
	notifyVerifyKeyPrefix        = "notify_verify:"
	passwordResetKeyPrefix       = "password_reset:v2:"
	passwordResetSentAtKeyPrefix = "password_reset_sent:"
	notifyCodeUserRateKeyPrefix  = "notify_code_user_rate:"

	// 验证次数使用独立键，以原子 INCR 记录并发尝试。
	attemptsKeySuffix = ":attempts"
)

// 当前验证码必须与读取快照相同；原子累计次数并继承验证码有效期。
// KEYS[1] 为验证码，KEYS[2] 为计数；验证码已过期或重发时返回 -1。
var incrAttemptsScript = redis.NewScript(`
local raw = redis.call('GET', KEYS[1])
if not raw then return -1 end
local ok, data = pcall(cjson.decode, raw)
if not ok or type(data) ~= 'table' or data['Code'] ~= ARGV[1] then return -1 end
local n = redis.call('INCR', KEYS[2])
local ttl = redis.call('PTTL', KEYS[1])
if ttl > 0 then
  redis.call('PEXPIRE', KEYS[2], ttl)
end
return n
`)

// 成功验证的在途请求只能删除仍匹配的验证码，并原子清理相应次数。
// 如果其间发生重发，保留新验证码、有效期和新一轮计数。
var deleteMatchingCodeScript = redis.NewScript(`
local raw = redis.call('GET', KEYS[1])
if not raw then return 0 end
local ok, data = pcall(cjson.decode, raw)
if not ok or type(data) ~= 'table' or data['Code'] ~= ARGV[1] then return 0 end
redis.call('DEL', KEYS[1], KEYS[2])
return 1
`)

// 重置令牌摘要比较与删除在 Redis 中原子执行，最多一个调用者成功。
var consumeResetTokenScript = redis.NewScript(`
local v = redis.call('GET', KEYS[1])
if not v then
  return 0
end
local ok, d = pcall(cjson.decode, v)
if not ok or type(d) ~= 'table' or d['Token'] ~= ARGV[1] then
  return 0
end
redis.call('DEL', KEYS[1])
return 1
`)

// verifyCodeKey generates the Redis key for email verification code.
// Email is lowercased for case-insensitive consistency.
func verifyCodeKey(email string) string {
	return verifyCodeKeyPrefix + strings.ToLower(email)
}

// notifyVerifyKey generates the Redis key for notify email verification code.
// Email is lowercased to prevent case-sensitive key mismatch (the business layer
// uses strings.EqualFold for comparison).
func notifyVerifyKey(email string) string {
	return notifyVerifyKeyPrefix + strings.ToLower(email)
}

// passwordResetKey generates the Redis key for password reset token.
func passwordResetKey(email string) string {
	return passwordResetKeyPrefix + strings.ToLower(email)
}

// passwordResetSentAtKey generates the Redis key for password reset email sent timestamp.
func passwordResetSentAtKey(email string) string {
	return passwordResetSentAtKeyPrefix + strings.ToLower(email)
}

type emailCache struct {
	rdb *redis.Client
}

func NewEmailCache(rdb *redis.Client) service.EmailCache {
	return &emailCache{rdb: rdb}
}

func (c *emailCache) getCode(ctx context.Context, key string) (*service.VerificationCodeData, error) {
	val, err := c.rdb.Get(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	var data service.VerificationCodeData
	if err := json.Unmarshal([]byte(val), &data); err != nil {
		return nil, err
	}
	if n, err := c.rdb.Get(ctx, key+attemptsKeySuffix).Int(); err == nil && n > data.Attempts {
		data.Attempts = n
	}
	return &data, nil
}

func (c *emailCache) setCode(ctx context.Context, key string, data *service.VerificationCodeData, ttl time.Duration) error {
	val, err := json.Marshal(data)
	if err != nil {
		return err
	}
	pipe := c.rdb.TxPipeline()
	pipe.Set(ctx, key, val, ttl)
	pipe.Del(ctx, key+attemptsKeySuffix)
	if data.Attempts > 0 {
		pipe.Set(ctx, key+attemptsKeySuffix, data.Attempts, ttl)
	}
	_, err = pipe.Exec(ctx)
	return err
}

func (c *emailCache) incrCodeAttempts(ctx context.Context, key, expectedCode string) (int, error) {
	n, err := incrAttemptsScript.Run(ctx, c.rdb, []string{key, key + attemptsKeySuffix}, expectedCode).Int()
	if err != nil {
		return 0, err
	}
	if n < 0 {
		return 0, redis.Nil
	}
	return n, nil
}

func (c *emailCache) deleteCode(ctx context.Context, key string) error {
	return c.rdb.Del(ctx, key, key+attemptsKeySuffix).Err()
}

func (c *emailCache) deleteCodeIfMatches(ctx context.Context, key, expectedCode string) error {
	return deleteMatchingCodeScript.Run(ctx, c.rdb, []string{key, key + attemptsKeySuffix}, expectedCode).Err()
}

func (c *emailCache) GetVerificationCode(ctx context.Context, email string) (*service.VerificationCodeData, error) {
	return c.getCode(ctx, verifyCodeKey(email))
}

func (c *emailCache) SetVerificationCode(ctx context.Context, email string, data *service.VerificationCodeData, ttl time.Duration) error {
	return c.setCode(ctx, verifyCodeKey(email), data, ttl)
}

func (c *emailCache) IncrVerificationCodeAttempts(ctx context.Context, email, expectedCode string) (int, error) {
	return c.incrCodeAttempts(ctx, verifyCodeKey(email), expectedCode)
}

func (c *emailCache) DeleteVerificationCode(ctx context.Context, email string) error {
	return c.deleteCode(ctx, verifyCodeKey(email))
}

func (c *emailCache) DeleteVerificationCodeIfMatches(ctx context.Context, email, expectedCode string) error {
	return c.deleteCodeIfMatches(ctx, verifyCodeKey(email), expectedCode)
}

// Password reset token methods

func (c *emailCache) GetPasswordResetToken(ctx context.Context, email string) (*service.PasswordResetTokenData, error) {
	key := passwordResetKey(email)
	val, err := c.rdb.Get(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	var data service.PasswordResetTokenData
	if err := json.Unmarshal([]byte(val), &data); err != nil {
		return nil, err
	}
	return &data, nil
}

func (c *emailCache) SetPasswordResetToken(ctx context.Context, email string, data *service.PasswordResetTokenData, ttl time.Duration) error {
	key := passwordResetKey(email)
	val, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, key, val, ttl).Err()
}

// ConsumePasswordResetToken 原子比较并消费摘要，令牌仅能成功使用一次。
func (c *emailCache) ConsumePasswordResetToken(ctx context.Context, email, tokenHash string) (bool, error) {
	n, err := consumeResetTokenScript.Run(ctx, c.rdb, []string{passwordResetKey(email)}, tokenHash).Int()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

func (c *emailCache) DeletePasswordResetToken(ctx context.Context, email string) error {
	key := passwordResetKey(email)
	return c.rdb.Del(ctx, key).Err()
}

// Password reset email cooldown methods

func (c *emailCache) IsPasswordResetEmailInCooldown(ctx context.Context, email string) bool {
	key := passwordResetSentAtKey(email)
	exists, err := c.rdb.Exists(ctx, key).Result()
	return err == nil && exists > 0
}

func (c *emailCache) SetPasswordResetEmailCooldown(ctx context.Context, email string, ttl time.Duration) error {
	key := passwordResetSentAtKey(email)
	return c.rdb.Set(ctx, key, "1", ttl).Err()
}

// Notify email verification code methods

func (c *emailCache) GetNotifyVerifyCode(ctx context.Context, email string) (*service.VerificationCodeData, error) {
	return c.getCode(ctx, notifyVerifyKey(email))
}

func (c *emailCache) SetNotifyVerifyCode(ctx context.Context, email string, data *service.VerificationCodeData, ttl time.Duration) error {
	return c.setCode(ctx, notifyVerifyKey(email), data, ttl)
}

func (c *emailCache) IncrNotifyVerifyCodeAttempts(ctx context.Context, email, expectedCode string) (int, error) {
	return c.incrCodeAttempts(ctx, notifyVerifyKey(email), expectedCode)
}

func (c *emailCache) DeleteNotifyVerifyCode(ctx context.Context, email string) error {
	return c.deleteCode(ctx, notifyVerifyKey(email))
}

func (c *emailCache) DeleteNotifyVerifyCodeIfMatches(ctx context.Context, email, expectedCode string) error {
	return c.deleteCodeIfMatches(ctx, notifyVerifyKey(email), expectedCode)
}

// User-level rate limiting for notify email verification codes

func notifyCodeUserRateKey(userID int64) string {
	return notifyCodeUserRateKeyPrefix + fmt.Sprintf("%d", userID)
}

func (c *emailCache) IncrNotifyCodeUserRate(ctx context.Context, userID int64, window time.Duration) (int64, error) {
	key := notifyCodeUserRateKey(userID)
	count, err := c.rdb.Incr(ctx, key).Result()
	if err != nil {
		return 0, err
	}
	// Always set TTL (idempotent) to avoid orphan keys if process crashes between INCR and EXPIRE.
	if err := c.rdb.Expire(ctx, key, window).Err(); err != nil {
		return count, fmt.Errorf("expire notify code rate key: %w", err)
	}
	return count, nil
}

func (c *emailCache) GetNotifyCodeUserRate(ctx context.Context, userID int64) (int64, error) {
	key := notifyCodeUserRateKey(userID)
	count, err := c.rdb.Get(ctx, key).Int64()
	if err != nil {
		return 0, err
	}
	return count, nil
}
