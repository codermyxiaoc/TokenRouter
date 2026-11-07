//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

// revocationCacheStub 是同时实现 refresh 存储与 access token 吊销能力的内存替身。
type revocationCacheStub struct {
	RefreshTokenCache
	sessions map[string]bool
	before   map[int64]time.Time
}

func newRevocationCacheStub() *revocationCacheStub {
	return &revocationCacheStub{sessions: map[string]bool{}, before: map[int64]time.Time{}}
}

func (s *revocationCacheStub) DeleteTokenFamily(context.Context, string) error      { return nil }
func (s *revocationCacheStub) DeleteUserRefreshTokens(context.Context, int64) error { return nil }
func (s *revocationCacheStub) RevokeAccessSession(_ context.Context, sid string, _ time.Duration) error {
	s.sessions[sid] = true
	return nil
}
func (s *revocationCacheStub) IsAccessSessionRevoked(_ context.Context, sid string) (bool, error) {
	return s.sessions[sid], nil
}
func (s *revocationCacheStub) RevokeUserAccessTokensBefore(_ context.Context, userID int64, at time.Time, _ time.Duration) error {
	s.before[userID] = at
	return nil
}
func (s *revocationCacheStub) UserAccessTokensRevokedBefore(_ context.Context, userID int64) (time.Time, error) {
	return s.before[userID], nil
}

func newRevocationTestAuthService(cache RefreshTokenCache) *AuthService {
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "test-secret-test-secret-test-secret", ExpireHour: 1}}
	return NewAuthService(nil, nil, nil, cache, cfg, nil, nil, nil, nil, nil, nil, nil, nil)
}

// 登出吊销当前会话的 access token，其它会话不受影响。
func TestAccessTokenRevokedOnLogout(t *testing.T) {
	cache := newRevocationCacheStub()
	svc := newRevocationTestAuthService(cache)
	user := &User{ID: 7, Email: "a@example.com", Role: RoleUser}
	current, err := svc.generateAccessToken(user, "session-a", "")
	require.NoError(t, err)
	other, err := svc.generateAccessToken(user, "session-b", "")
	require.NoError(t, err)

	svc.RevokeAccessToken(context.Background(), current)

	currentClaims, err := svc.ValidateToken(current)
	require.NoError(t, err)
	otherClaims, err := svc.ValidateToken(other)
	require.NoError(t, err)
	require.True(t, svc.IsAccessTokenRevoked(context.Background(), currentClaims))
	require.False(t, svc.IsAccessTokenRevoked(context.Background(), otherClaims))
}

// 撤销全部会话后此前签发的 access token 失效，撤销之后重新登录签发的新 token 仍可用。
func TestAccessTokensRevokedForUserBeforeCutoff(t *testing.T) {
	cache := newRevocationCacheStub()
	svc := newRevocationTestAuthService(cache)
	user := &User{ID: 8, Email: "b@example.com", Role: RoleAdmin}
	old, err := svc.generateAccessToken(user, "old", "")
	require.NoError(t, err)
	oldClaims, err := svc.ValidateToken(old)
	require.NoError(t, err)

	// 模拟一秒后撤销，避免与签发落在同一秒。
	cache.before[user.ID] = time.Now().Add(time.Second)
	require.True(t, svc.IsAccessTokenRevoked(context.Background(), oldClaims))

	fresh := *oldClaims
	fresh.IssuedAt.Time = time.Now().Add(2 * time.Second)
	require.False(t, svc.IsAccessTokenRevoked(context.Background(), &fresh))
}

// 吊销会话家族（会话绑定不匹配等）同时吊销该会话的 access token。
func TestRevokeSessionFamilyRevokesAccessSession(t *testing.T) {
	cache := newRevocationCacheStub()
	svc := newRevocationTestAuthService(cache)
	require.NoError(t, svc.RevokeSessionFamily(context.Background(), "family-1"))
	require.True(t, cache.sessions["family-1"])
}

// 未实现吊销能力的缓存保持原行为：不报错、不吊销。
func TestAccessTokenRevocationOptional(t *testing.T) {
	svc := newRevocationTestAuthService(nil)
	require.False(t, svc.IsAccessTokenRevoked(context.Background(), &JWTClaims{UserID: 1, SessionID: "x"}))
	require.NoError(t, svc.revokeUserAccessTokens(context.Background(), 1))
}
