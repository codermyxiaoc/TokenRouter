//go:build unit

package service

import (
	"context"
	"net"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

// 构造启用会话屏蔽的隔离服务；缓存全部使用内存替身。
func regressionCyberService() (*OpenAIGatewayService, *comboCacheAndStore) {
	combo := &comboCacheAndStore{}
	settings := &SettingService{settingRepo: &fakeSettingRepo{vals: map[string]string{
		SettingKeyCyberSessionBlockEnabled: "true", SettingKeyCyberSessionBlockTTLSeconds: "3600",
	}}}
	return &OpenAIGatewayService{cache: combo, settingService: settings}, combo
}

// 旧 Redis 保存精确键和 scope、没有新增长度索引；升级不能静默放行未到期被屏蔽会话。
func TestRegressionCyberUpgradePreservesExistingTranscriptBlock(t *testing.T) {
	svc, combo := regressionCyberService()
	body := []byte(`{"model":"gpt-6.1","input":[{"role":"user","content":"synthetic-history"}]}`)
	ctx, _ := newCyberBlockTestCtx(nil, string(body))
	key := CyberSessionTranscriptBlocks(71, body)[0].Key
	scope := CyberSessionScopeKey(71, "203.0.113.7", "client/1.0")
	combo.store.blocked = map[string]bool{key: true}
	combo.store.scopes = map[string]bool{scope: true}
	// 与上一版持久化结构一致：长度索引不存在。
	actual := svc.FindCyberSessionBlockedForRequest(context.Background(), 71, ctx, body, "203.0.113.7", "client/1.0")
	t.Logf("old_exact_key_exists=%v scope_exists=%v recorded_lengths=%v result=%q", combo.store.blocked[key], combo.store.scopes[scope], combo.store.lengths, actual)
	require.Equal(t, key, actual, "升级后同一未过期屏蔽会话被放行")
}

// 会话阻断按 Key 隔离；模型变更不能绕过同一历史的屏蔽，其他 Key 不受影响。
func TestRegressionCyberKeyIsolationAndModelChange(t *testing.T) {
	svc, _ := regressionCyberService()
	first := []byte(`{"model":"gpt-a","input":[{"role":"user","content":"synthetic-one"}]}`)
	changed := []byte(`{"model":"gpt-b","input":[{"role":"user","content":"synthetic-one"}]}`)
	block := CyberSessionTranscriptBlocks(71, first)[0]
	svc.MarkCyberSessionBlocked(context.Background(), CyberSessionBlock{APIKeyID: 71, ScopeKey: CyberSessionScopeKey(71, "203.0.113.7", "client/1.0"), Keys: []string{block.Key}, TranscriptLengths: []int{block.Length}})
	c, _ := newCyberBlockTestCtx(nil, string(changed))
	require.Equal(t, block.Key, svc.FindCyberSessionBlockedForRequest(context.Background(), 71, c, changed, "203.0.113.7", "client/1.0"))
	require.Empty(t, svc.FindCyberSessionBlockedForRequest(context.Background(), 72, c, changed, "203.0.113.7", "client/1.0"))
}

// OpenAI 命名的冷却开关不能关闭共用传输处理器的 Grok 账号保护。
func TestRegressionTransportCooldownIsOpenAIOnly(t *testing.T) {
	for _, platform := range []string{PlatformOpenAI, PlatformGrok} {
		t.Run(platform, func(t *testing.T) {
			repo := &openAITransportAccountRepoStub{}
			svc := &OpenAIGatewayService{accountRepo: repo, cfg: &config.Config{Gateway: config.GatewayConfig{DisableOpenAITransportErrorCooldown: true}}}
			account := &Account{ID: 718, Platform: platform}
			c, _ := newOpenAITransportErrTestContext()
			err := svc.handleOpenAIUpstreamTransportError(context.Background(), c, account, &net.DNSError{Err: "no such host", Name: "synthetic.invalid", IsNotFound: true}, false)
			var failover *UpstreamFailoverError
			require.ErrorAs(t, err, &failover)
			t.Logf("platform=%s persisted_cooldowns=%d runtime_blocked=%v", platform, len(repo.tempUnschedCalls), svc.isOpenAIAccountRuntimeBlocked(account))
			if platform == PlatformOpenAI {
				require.Empty(t, repo.tempUnschedCalls)
			} else {
				require.Len(t, repo.tempUnschedCalls, 1, "仅关闭 OpenAI 冷却时 Grok 的持久网络故障保护也失效")
			}
		})
	}
}
