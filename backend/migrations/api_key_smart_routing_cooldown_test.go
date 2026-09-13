package migrations

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSmartRoutingCooldownMigration(t *testing.T) {
	// 已有 Key 与新建 Key 使用相同默认值，DDL 可重复执行且禁止越界配置。
	content, err := FS.ReadFile("271_api_key_smart_routing_cooldown.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS smart_routing_cooldown_seconds INTEGER NOT NULL DEFAULT 60")
	require.Contains(t, sql, "IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'api_keys_smart_routing_cooldown_check')")
	require.Contains(t, sql, "CHECK (smart_routing_cooldown_seconds BETWEEN 0 AND 3600)")
	require.Contains(t, sql, "COMMENT ON COLUMN api_keys.smart_routing_cooldown_seconds")
	require.NotContains(t, sql, "CREATE OR REPLACE FUNCTION enqueue_api_key_auth_cache_invalidation()")
}

func TestSmartRoutingCooldownMigrationPublishedChecksumIsImmutable(t *testing.T) {
	// 该版本已被真实部署应用，任何后续改动（包括内部换行）都必须新建迁移。
	content, err := FS.ReadFile("271_api_key_smart_routing_cooldown.sql")
	require.NoError(t, err)
	sum := sha256.Sum256([]byte(strings.TrimSpace(string(content))))
	require.Equal(t, "c600119f5da453b541b264d51203e425ebe23ac89abe87b6e33d8f44d8d7a7f0", hex.EncodeToString(sum[:]))
}

func TestSmartRoutingCooldownOutboxMigration(t *testing.T) {
	// 272 独立补充缓存失效和表范围约束，不改写已有冷却配置。
	content, err := FS.ReadFile("272_api_key_smart_routing_cooldown_outbox.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "conrelid = 'api_keys'::regclass")
	require.Contains(t, sql, "CHECK (smart_routing_cooldown_seconds BETWEEN 0 AND 3600)")
	require.Contains(t, sql, "CREATE OR REPLACE FUNCTION enqueue_api_key_auth_cache_invalidation()")
	require.Contains(t, sql, "OLD.smart_routing_cooldown_seconds IS DISTINCT FROM NEW.smart_routing_cooldown_seconds")
	require.Contains(t, sql, "OLD.billing_mode IS DISTINCT FROM NEW.billing_mode")
	require.Contains(t, sql, "OLD.preferred_subscription_id IS DISTINCT FROM NEW.preferred_subscription_id")
	require.NotContains(t, strings.ToUpper(sql), "UPDATE API_KEYS")
}
