package service

import (
	"context"
	"strings"
	"time"
)

// AccessTokenRevocationStore 是 RefreshTokenCache 的可选扩展：记录已吊销的会话和用户级吊销时间点，
// 使无状态 access token 在登出、撤销全部会话或解绑登录方式后立即失效，而不必等到自然过期。
// 未实现该接口的缓存（例如测试替身）保持原有行为。
type AccessTokenRevocationStore interface {
	// RevokeAccessSession 吊销单个会话（sid）签发的 access token，ttl 覆盖其剩余有效期。
	RevokeAccessSession(ctx context.Context, sessionID string, ttl time.Duration) error
	// IsAccessSessionRevoked 判断会话是否已吊销。
	IsAccessSessionRevoked(ctx context.Context, sessionID string) (bool, error)
	// RevokeUserAccessTokensBefore 吊销用户在 at 之前签发的全部 access token。
	RevokeUserAccessTokensBefore(ctx context.Context, userID int64, at time.Time, ttl time.Duration) error
	// UserAccessTokensRevokedBefore 返回用户级吊销时间点；未吊销时返回零值。
	UserAccessTokensRevokedBefore(ctx context.Context, userID int64) (time.Time, error)
}

func (s *AuthService) accessTokenRevocationStore() AccessTokenRevocationStore {
	if s == nil || s.refreshTokenCache == nil {
		return nil
	}
	store, _ := s.refreshTokenCache.(AccessTokenRevocationStore)
	return store
}

// accessTokenRevocationTTL 覆盖 access token 的最长有效期，过期后吊销记录自然清理。
func (s *AuthService) accessTokenRevocationTTL() time.Duration {
	ttl := time.Duration(s.GetAccessTokenExpiresIn()) * time.Second
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return ttl + time.Minute
}

// revokeAccessSession 吊销单个会话的 access token；失败只记录不阻断原有登出/撤销流程。
func (s *AuthService) revokeAccessSession(ctx context.Context, sessionID string) {
	store := s.accessTokenRevocationStore()
	sessionID = strings.TrimSpace(sessionID)
	if store == nil || sessionID == "" {
		return
	}
	_ = store.RevokeAccessSession(ctx, sessionID, s.accessTokenRevocationTTL())
}

// revokeUserAccessTokens 吊销用户此刻之前签发的全部 access token。
func (s *AuthService) revokeUserAccessTokens(ctx context.Context, userID int64) error {
	store := s.accessTokenRevocationStore()
	if store == nil || userID <= 0 {
		return nil
	}
	return store.RevokeUserAccessTokensBefore(ctx, userID, time.Now(), s.accessTokenRevocationTTL())
}

// RevokeAccessToken 吊销当前 access token 所属会话（登出时调用）；token 无效时忽略。
func (s *AuthService) RevokeAccessToken(ctx context.Context, tokenString string) {
	claims, err := s.ValidateToken(strings.TrimSpace(tokenString))
	if err != nil || claims == nil {
		return
	}
	s.revokeAccessSession(ctx, claims.SessionID)
}

// DiscardTokenPair 作废一对已签发但不会交付给客户端的凭证（例如登录被两步验证拦截）。
func (s *AuthService) DiscardTokenPair(ctx context.Context, pair *TokenPair) {
	if s == nil || pair == nil {
		return
	}
	claims, err := s.ValidateToken(pair.AccessToken)
	if err != nil || claims == nil || strings.TrimSpace(claims.SessionID) == "" {
		return
	}
	_ = s.RevokeSessionFamily(ctx, claims.SessionID)
}

// IsAccessTokenRevoked 判断 access token 是否已被会话级或用户级吊销。
// 吊销存储读取失败时放行（fail-open），与 refresh 存储故障时不影响已登录请求的既有行为一致。
func (s *AuthService) IsAccessTokenRevoked(ctx context.Context, claims *JWTClaims) bool {
	store := s.accessTokenRevocationStore()
	if store == nil || claims == nil {
		return false
	}
	if sid := strings.TrimSpace(claims.SessionID); sid != "" {
		if revoked, err := store.IsAccessSessionRevoked(ctx, sid); err == nil && revoked {
			return true
		}
	}
	before, err := store.UserAccessTokensRevokedBefore(ctx, claims.UserID)
	if err != nil || before.IsZero() || claims.IssuedAt == nil {
		return false
	}
	// 签发时间精度为秒：同一秒内撤销后立即重新登录的新 token 不能被误判吊销。
	return claims.IssuedAt.Time.Before(before.Truncate(time.Second))
}
