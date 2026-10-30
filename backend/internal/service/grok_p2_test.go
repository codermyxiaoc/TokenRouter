//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGrokModelQuotaBlock_FiltersOnlyNamedModel(t *testing.T) {
	id := time.Now().UnixNano()%1_000_000 + 5000
	markGrokModelQuotaBlock(id, "grok-4.5", time.Now().Add(time.Hour))
	now := time.Now()
	require.True(t, isGrokModelQuotaBlocked(id, "grok-4.5", now))
	require.False(t, isGrokModelQuotaBlocked(id, "grok-4.3", now))

	accounts := []Account{
		{ID: id, Platform: PlatformGrok, Type: AccountTypeOAuth},
		{ID: id + 1, Platform: PlatformGrok, Type: AccountTypeOAuth},
	}
	filtered := filterGrokModelQuotaBlockedAccounts(accounts, "grok-4.5", now)
	require.Len(t, filtered, 1)
	require.Equal(t, id+1, filtered[0].ID)
}

func TestGrokModelQuotaBlockFiltersMappedUpstreamModel(t *testing.T) {
	id := time.Now().UnixNano()%1_000_000 + 7000
	markGrokModelQuotaBlock(id, "grok-4.5", time.Now().Add(time.Hour))
	account := Account{
		ID:       id,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"gpt-*": "grok-4.5"},
		},
	}

	require.Empty(t, filterGrokModelQuotaBlockedAccounts([]Account{account}, "gpt-5", time.Now()))
}

func TestIsGrokModelSpecificFreeUsage(t *testing.T) {
	require.True(t, isGrokModelSpecificFreeUsage(
		"you've used all the included free usage for model grok-4.5", "grok-4.5"))
	require.True(t, isGrokModelSpecificFreeUsage("模型额度用完 grok-4.3", "grok-4.3"))
	require.False(t, isGrokModelSpecificFreeUsage("free usage exhausted", "grok-4.5"))
}

func TestGrokStickyAffinitySeed_ScopesByModel(t *testing.T) {
	a := grokStickyAffinitySeed("session-1", []byte(`{"model":"grok-4.5"}`))
	b := grokStickyAffinitySeed("session-1", []byte(`{"model":"grok-4.3"}`))
	c := grokStickyAffinitySeed("session-1", []byte(`{"model":"grok-4.5"}`))
	require.NotEqual(t, a, b)
	require.Equal(t, a, c)
	require.Contains(t, a, "grok-affinity:v1:")
}

func TestExtractGrokModelIDsFromModelsBody(t *testing.T) {
	body := []byte(`{"object":"list","data":[{"id":"grok-4.5"},{"id":"grok-4.3"},{"id":"grok-4.5"}]}`)
	ids, err := extractGrokModelIDsFromModelsBody(body)
	require.NoError(t, err)
	// 严格解析与手动同步一致：去重后按 ID 排序。
	require.Equal(t, []string{"grok-4.3", "grok-4.5"}, ids)
}

func TestAccountGrokNeedsReauth(t *testing.T) {
	require.False(t, accountGrokNeedsReauth(nil))
	require.True(t, accountGrokNeedsReauth(&Account{
		Extra: map[string]any{"grok_needs_reauth": true},
	}))
	require.True(t, accountGrokNeedsReauth(&Account{
		Status:       StatusError,
		ErrorMessage: "Grok spending limit reached; reauthorize or wait for billing reset",
	}))
}

func TestApplyGrokUpstreamFailure_ModelSpecificFreeUsage(t *testing.T) {
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{ID: 9109, Platform: PlatformGrok, Type: AccountTypeOAuth}
	body := []byte(`{"error":{"code":"subscription:free-usage-exhausted","message":"You've used all the included free usage for model grok-4.5. Usage resets over a rolling 24-hour window."}}`)

	svc.handleGrokAccountUpstreamError(context.Background(), account, 400, nil, body)

	require.Zero(t, repo.tempUnschedCalls, "model-scoped free usage must not cool sibling models")
	require.True(t, isGrokModelQuotaBlocked(account.ID, "grok-4.5", time.Now()))
	require.False(t, isGrokModelQuotaBlocked(account.ID, "grok-4.3", time.Now()))
}

func TestApplyGrokUpstreamFailure_SpendingLimitRemainsRecoverable(t *testing.T) {
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{ID: 9110, Platform: PlatformGrok, Type: AccountTypeOAuth}
	body := []byte(`{"code":"personal-team-blocked:spending-limit","error":"spending limit reached"}`)

	svc.handleGrokAccountUpstreamError(context.Background(), account, 403, nil, body)

	require.Equal(t, 1, repo.rateLimitedCalls)
	require.Zero(t, repo.tempUnschedCalls)
	// 缺少账期快照时使用可恢复的短期探测冷却。
	require.WithinDuration(t, time.Now().Add(grokSpendingLimitProbeCooldown), repo.lastRateLimitResetAt, 2*time.Second)
}

// 后台目录严格解析：HTTP 200 的错误包、非数组目录、空目录都不能被当作模型 ID 写入快照。
func TestGrokCatalogStrictParsing(t *testing.T) {
	for _, body := range []string{`{"error":{"message":"denied"}}`, `{"data":{"id":"x"}}`, `{"object":"list"}`, `[1,2]`, `null`, `not json`} {
		_, err := extractGrokModelIDsFromModelsBody([]byte(body))
		require.ErrorIs(t, err, errGrokCatalogInvalidResponse, body)
	}
	_, err := extractGrokModelIDsFromModelsBody([]byte(`{"data":[]}`))
	require.ErrorIs(t, err, errGrokCatalogEmpty)
	// 与手动同步一致：优先 model 字段，字符串数组与 models 字段也接受。
	ids, err := extractGrokModelIDsFromModelsBody([]byte(`{"data":[{"id":"display","model":"grok-4.6"}],"models":["grok-imagine"]}`))
	require.NoError(t, err)
	require.Equal(t, []string{"grok-4.6", "grok-imagine"}, ids)
}

// 快照只在来源相同、时间合法且不在未来时视为新鲜；切换端点或旧格式快照会重新同步。
func TestGrokObservedSnapshotFreshness(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	scope, err := NewGrokCatalogScope(7, AccountTypeOAuth, "https://cli-chat-proxy.grok.com/v1/models")
	require.NoError(t, err)
	other, err := NewGrokCatalogScope(7, AccountTypeOAuth, "https://api.x.ai/v1/models")
	require.NoError(t, err)
	snap := &grokObservedModelsSnapshot{Models: []string{"grok-4.6"}, FetchedAt: now.Add(-time.Hour).Format(time.RFC3339), Scope: scope}
	require.True(t, grokObservedSnapshotFresh(snap, scope, now))
	require.False(t, grokObservedSnapshotFresh(snap, other, now), "来源变化")
	future := *snap
	future.FetchedAt = now.Add(time.Hour).Format(time.RFC3339)
	require.False(t, grokObservedSnapshotFresh(&future, scope, now), "未来时间不能永远新鲜")
	legacy := *snap
	legacy.Scope = nil
	require.False(t, grokObservedSnapshotFresh(&legacy, scope, now), "旧格式快照没有来源")
	stale := *snap
	stale.FetchedAt = now.Add(-7 * time.Hour).Format(time.RFC3339Nano)
	require.False(t, grokObservedSnapshotFresh(&stale, scope, now))
	// 日志字段只取分类与状态码，不带出底层错误。
	kind, status := grokObservedModelsSyncLogFields(&grokCatalogSyncError{Kind: "http_error", StatusCode: 403})
	require.Equal(t, "http_error", kind)
	require.Equal(t, 403, status)
}
