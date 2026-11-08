//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 测试替身仅返回合成用户，不访问生产数据库。
type regressionUserRepo struct {
	UserRepository
	user *User
}

func (r *regressionUserRepo) GetByID(context.Context, int64) (*User, error) { return r.user, nil }

// 验证撤销全部会话不能遗漏同一秒内先签发的令牌。
func TestRegressionRevokeAllSameSecond(t *testing.T) {
	cache := newRevocationCacheStub()
	svc := newRevocationTestAuthService(cache)
	user := &User{ID: 719, Email: "synthetic@example.invalid", Role: RoleAdmin}
	svc.userRepo = &regressionUserRepo{user: user}
	for attempt := 0; attempt < 3; attempt++ {
		token, err := svc.generateAccessToken(user, "old-session", "")
		require.NoError(t, err)
		claims, err := svc.ValidateToken(token)
		require.NoError(t, err)
		require.NoError(t, svc.RevokeAllUserTokens(context.Background(), user.ID))
		cutoff := cache.before[user.ID]
		if !claims.IssuedAt.Time.Equal(cutoff.Truncate(time.Second)) {
			continue
		}
		t.Logf("token_iat=%s revoke_cutoff=%s revoked=%v", claims.IssuedAt.Time.Format(time.RFC3339Nano), cutoff.Format(time.RFC3339Nano), svc.IsAccessTokenRevoked(context.Background(), claims))
		require.True(t, svc.IsAccessTokenRevoked(context.Background(), claims), "撤销全部会话成功返回后，同秒旧令牌仍被接受")
		return
	}
	t.Fatal("未命中同秒边界，请重跑")
}

// 模拟仅写归属失败，读恢复后无记录；禁止将新声音误认为历史公共声音。
type regressionWriteFailVoiceCache struct{ voiceOwnerCacheStub }

func (s *regressionWriteFailVoiceCache) SetGrokCustomVoiceOwner(context.Context, string, int64) error {
	return errors.New("synthetic Redis write failure")
}

func TestRegressionGrokOwnerWriteFailureMustNotAuthorizeOthers(t *testing.T) {
	cache := &regressionWriteFailVoiceCache{voiceOwnerCacheStub{owners: map[string]int64{}}}
	svc := &OpenAIGatewayService{cache: cache}
	ctx := context.Background()
	response := svc.applyGrokCustomVoiceOwnership(ctx, voiceTestContext(http.MethodPost, 1), []string{"custom-voices"}, []byte(`{"voice_id":"new-private-voice"}`))
	require.Contains(t, string(response), "new-private-voice")
	t.Logf("create_response=%s owner_count=%d", response, len(cache.owners))
	for _, endpoint := range [][]string{{"custom-voices", "new-private-voice"}, {"tts"}} {
		err := svc.authorizeGrokCustomVoice(ctx, voiceTestContext(http.MethodPost, 2), endpoint, []byte(`{"text":"synthetic","voice_id":"new-private-voice"}`))
		require.Error(t, err, "归属写失败后其他用户仍可调用新私有声音 endpoint=%v", endpoint)
	}
}
