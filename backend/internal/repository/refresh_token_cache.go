package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/redis/go-redis/v9"
)

const (
	refreshTokenKeyPrefix   = "refresh_token:"
	userRefreshTokensPrefix = "user_refresh_tokens:"
	tokenFamilyPrefix       = "token_family:"
	// 已吊销的 access token 会话与用户级吊销时间点。
	revokedAccessSessionPrefix = "revoked_access_session:"
	userAccessRevokedPrefix    = "user_access_revoked_before:"
)

// refreshTokenKey generates the Redis key for a refresh token.
func refreshTokenKey(tokenHash string) string {
	return refreshTokenKeyPrefix + tokenHash
}

// userRefreshTokensKey generates the Redis key for user's token set.
func userRefreshTokensKey(userID int64) string {
	return fmt.Sprintf("%s%d", userRefreshTokensPrefix, userID)
}

// tokenFamilyKey generates the Redis key for token family set.
func tokenFamilyKey(familyID string) string {
	return tokenFamilyPrefix + familyID
}

type refreshTokenCache struct {
	rdb *redis.Client
}

// NewRefreshTokenCache creates a new RefreshTokenCache implementation.
func NewRefreshTokenCache(rdb *redis.Client) service.RefreshTokenCache {
	return &refreshTokenCache{rdb: rdb}
}

func (c *refreshTokenCache) StoreRefreshToken(ctx context.Context, tokenHash string, data *service.RefreshTokenData, ttl time.Duration) error {
	key := refreshTokenKey(tokenHash)
	val, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal refresh token data: %w", err)
	}
	return c.rdb.Set(ctx, key, val, ttl).Err()
}

func (c *refreshTokenCache) GetRefreshToken(ctx context.Context, tokenHash string) (*service.RefreshTokenData, error) {
	key := refreshTokenKey(tokenHash)
	val, err := c.rdb.Get(ctx, key).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, service.ErrRefreshTokenNotFound
		}
		return nil, err
	}
	var data service.RefreshTokenData
	if err := json.Unmarshal([]byte(val), &data); err != nil {
		return nil, fmt.Errorf("unmarshal refresh token data: %w", err)
	}
	return &data, nil
}

func (c *refreshTokenCache) DeleteRefreshToken(ctx context.Context, tokenHash string) error {
	key := refreshTokenKey(tokenHash)
	return c.rdb.Del(ctx, key).Err()
}

func (c *refreshTokenCache) DeleteUserRefreshTokens(ctx context.Context, userID int64) error {
	// Get all token hashes for this user
	tokenHashes, err := c.GetUserTokenHashes(ctx, userID)
	if err != nil && err != redis.Nil {
		return fmt.Errorf("get user token hashes: %w", err)
	}

	if len(tokenHashes) == 0 {
		return nil
	}

	// Build keys to delete
	keys := make([]string, 0, len(tokenHashes)+1)
	for _, hash := range tokenHashes {
		keys = append(keys, refreshTokenKey(hash))
	}
	keys = append(keys, userRefreshTokensKey(userID))

	// Delete all keys in a pipeline
	pipe := c.rdb.Pipeline()
	for _, key := range keys {
		pipe.Del(ctx, key)
	}
	_, err = pipe.Exec(ctx)
	return err
}

func (c *refreshTokenCache) DeleteTokenFamily(ctx context.Context, familyID string) error {
	// Get all token hashes in this family
	tokenHashes, err := c.GetFamilyTokenHashes(ctx, familyID)
	if err != nil && err != redis.Nil {
		return fmt.Errorf("get family token hashes: %w", err)
	}

	if len(tokenHashes) == 0 {
		return nil
	}

	// Build keys to delete
	keys := make([]string, 0, len(tokenHashes)+1)
	for _, hash := range tokenHashes {
		keys = append(keys, refreshTokenKey(hash))
	}
	keys = append(keys, tokenFamilyKey(familyID))

	// Delete all keys in a pipeline
	pipe := c.rdb.Pipeline()
	for _, key := range keys {
		pipe.Del(ctx, key)
	}
	_, err = pipe.Exec(ctx)
	return err
}

func (c *refreshTokenCache) AddToUserTokenSet(ctx context.Context, userID int64, tokenHash string, ttl time.Duration) error {
	key := userRefreshTokensKey(userID)
	pipe := c.rdb.Pipeline()
	pipe.SAdd(ctx, key, tokenHash)
	pipe.Expire(ctx, key, ttl)
	_, err := pipe.Exec(ctx)
	return err
}

func (c *refreshTokenCache) AddToFamilyTokenSet(ctx context.Context, familyID string, tokenHash string, ttl time.Duration) error {
	key := tokenFamilyKey(familyID)
	pipe := c.rdb.Pipeline()
	pipe.SAdd(ctx, key, tokenHash)
	pipe.Expire(ctx, key, ttl)
	_, err := pipe.Exec(ctx)
	return err
}

func (c *refreshTokenCache) GetUserTokenHashes(ctx context.Context, userID int64) ([]string, error) {
	key := userRefreshTokensKey(userID)
	return c.rdb.SMembers(ctx, key).Result()
}

func (c *refreshTokenCache) GetFamilyTokenHashes(ctx context.Context, familyID string) ([]string, error) {
	key := tokenFamilyKey(familyID)
	return c.rdb.SMembers(ctx, key).Result()
}

// RevokeAccessSession 记录被吊销的会话，TTL 覆盖 access token 剩余有效期。
func (c *refreshTokenCache) RevokeAccessSession(ctx context.Context, sessionID string, ttl time.Duration) error {
	return c.rdb.Set(ctx, revokedAccessSessionPrefix+sessionID, "1", ttl).Err()
}

// IsAccessSessionRevoked 判断会话是否已被吊销。
func (c *refreshTokenCache) IsAccessSessionRevoked(ctx context.Context, sessionID string) (bool, error) {
	n, err := c.rdb.Exists(ctx, revokedAccessSessionPrefix+sessionID).Result()
	return n > 0, err
}

// RevokeUserAccessTokensBefore 记录用户级吊销时间点（Unix 纳秒）。
func (c *refreshTokenCache) RevokeUserAccessTokensBefore(ctx context.Context, userID int64, at time.Time, ttl time.Duration) error {
	return c.rdb.Set(ctx, fmt.Sprintf("%s%d", userAccessRevokedPrefix, userID), at.UnixNano(), ttl).Err()
}

// UserAccessTokensRevokedBefore 读取用户级吊销时间点，未设置时返回零值。
func (c *refreshTokenCache) UserAccessTokensRevokedBefore(ctx context.Context, userID int64) (time.Time, error) {
	nanos, err := c.rdb.Get(ctx, fmt.Sprintf("%s%d", userAccessRevokedPrefix, userID)).Int64()
	if err == redis.Nil {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(0, nanos), nil
}

func (c *refreshTokenCache) IsTokenInFamily(ctx context.Context, familyID string, tokenHash string) (bool, error) {
	key := tokenFamilyKey(familyID)
	return c.rdb.SIsMember(ctx, key, tokenHash).Result()
}
