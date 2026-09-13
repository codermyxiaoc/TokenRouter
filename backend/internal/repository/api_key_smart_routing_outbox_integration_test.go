//go:build integration

package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSmartRoutingCooldownUpdateEnqueuesDurableAuthInvalidation(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	user := mustCreateUser(t, client, &service.User{Email: "cooldown-outbox@example.com"})
	repo := newAPIKeyRepositoryWithSQL(client, tx)
	key := &service.APIKey{
		UserID: user.ID, Key: "sk-cooldown-outbox", Name: "cooldown", Status: service.StatusActive,
		SmartRouting: true, SmartRoutingCooldownSeconds: 60,
	}
	require.NoError(t, repo.Create(ctx, key))
	sum := sha256.Sum256([]byte(key.Key))
	cacheKey := hex.EncodeToString(sum[:])
	countEvents := func() int {
		var count int
		require.NoError(t, scanSingleRow(ctx, tx, `SELECT COUNT(*) FROM auth_cache_invalidation_outbox WHERE cache_key = $1`, []any{cacheKey}, &count))
		return count
	}
	require.Zero(t, countEvents())
	// 直接调用仓库绕过即时 Redis 失效，数据库仍应与配置更新一同保存可重试事件。
	key.SmartRoutingCooldownSeconds = 0
	require.NoError(t, repo.Update(ctx, key, service.APIKeyUpdateFields{SmartRoutingCooldown: true}))
	require.Equal(t, 1, countEvents())
	// 保存同值及用量热更新不能制造额外的鉴权失效风暴。
	require.NoError(t, repo.Update(ctx, key, service.APIKeyUpdateFields{SmartRoutingCooldown: true}))
	require.NoError(t, repo.IncrementRateLimitUsage(ctx, key.ID, 7))
	require.Equal(t, 1, countEvents())
	_, err := tx.ExecContext(ctx, `SAVEPOINT cooldown_outbox_rollback`)
	require.NoError(t, err)
	key.SmartRoutingCooldownSeconds = 120
	require.NoError(t, repo.Update(ctx, key, service.APIKeyUpdateFields{SmartRoutingCooldown: true}))
	require.Equal(t, 2, countEvents())
	_, err = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT cooldown_outbox_rollback`)
	require.NoError(t, err)
	require.Equal(t, 1, countEvents(), "配置回滚时失效事件也必须回滚")
	stored, err := repo.GetByKeyForAuth(ctx, key.Key)
	require.NoError(t, err)
	require.Zero(t, stored.SmartRoutingCooldownSeconds)
}
