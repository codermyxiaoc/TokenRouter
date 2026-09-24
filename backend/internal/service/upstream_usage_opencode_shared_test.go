package service

import (
	"context"
	"github.com/stretchr/testify/require"
	"sync"
	"testing"
	"time"
)

// 同一官方 GO 凭据只探测一次，各调用方仍拿到自己的账号标识和独立结果。
func TestOpenCodeSharedUsageConcurrentAndCache(t *testing.T) {
	a := newCNUsageMonitorAccount(1, PlatformOpenCodeGo, AccountModeGo)
	b := newCNUsageMonitorAccount(2, PlatformOpenCodeGo, AccountModeGo)
	b.Credentials["base_url"] = DefaultOpenCodeGoAnthropicBaseURL
	repo := &cnUsageMonitorRepo{accounts: map[int64]*Account{1: a, 2: b}}
	upstream := &blockingUpstreamUsageHTTP{started: make(chan struct{}), release: make(chan struct{}), body: openCodeGoUsageFixture}
	svc := NewUpstreamUsageService(repo, upstream, testUpstreamUsageConfig(), nil)
	var wg sync.WaitGroup
	results := make([]*UpstreamUsageQueryResult, 2)
	failures := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], failures[i] = svc.QueryAccount(context.Background(), int64(i+1))
		}(i)
	}
	<-upstream.started
	close(upstream.release)
	wg.Wait()
	require.NoError(t, failures[0])
	require.NoError(t, failures[1])
	require.EqualValues(t, 1, upstream.calls.Load())
	require.EqualValues(t, 1, results[0].AccountID)
	require.EqualValues(t, 2, results[1].AccountID)
	*results[0].Limits[0].Used = 99
	require.Equal(t, 25.0, *results[1].Limits[0].Used)
	again, err := svc.QueryAccount(context.Background(), 2)
	require.NoError(t, err)
	require.Equal(t, 25.0, *again.Limits[0].Used)
	require.EqualValues(t, 1, upstream.calls.Load())
	require.Equal(t, results[1].ObservedAt, again.ObservedAt)
}

// 身份指纹覆盖代理、认证、TLS 和查询地址；第三方中继不进入官方共享缓存。
func TestOpenCodeSharedUsageIdentityIsolation(t *testing.T) {
	a := newCNUsageMonitorAccount(1, PlatformOpenCodeGo, AccountModeGo)
	cfg, err := EffectiveUpstreamUsageConfig(a)
	require.NoError(t, err)
	key, ok := openCodeGoSharedUsageKey(a, cfg)
	require.True(t, ok)
	for name, change := range map[string]func(*Account){
		"key": func(a *Account) { a.Credentials["api_key"] = "another" },
		"proxy": func(a *Account) {
			id := int64(4)
			a.ProxyID = &id
			a.Proxy = &Proxy{ID: 4, Host: "proxy.example", Port: 8080}
		},
		"headers": func(a *Account) { a.Credentials["header_overrides"] = map[string]any{"X-Workspace": "two"} },
		"tls":     func(a *Account) { a.Extra["enable_tls_fingerprint"] = true },
	} {
		t.Run(name, func(t *testing.T) {
			b := newCNUsageMonitorAccount(2, PlatformOpenCodeGo, AccountModeGo)
			change(b)
			other, ok := openCodeGoSharedUsageKey(b, cfg)
			require.True(t, ok)
			require.NotEqual(t, key, other)
		})
	}
	for _, base := range []string{"https://relay.example/prefix/v1", "https://opencode.ai.evil/zen/go/v1", "https://opencode.ai/zen/v1", "http://opencode.ai/zen/go/v1", "https://opencode.ai/zen/go/v1?key=x"} {
		b := newCNUsageMonitorAccount(2, PlatformOpenCodeGo, AccountModeGo)
		b.Credentials["base_url"] = base
		_, ok := openCodeGoSharedUsageKey(b, cfg)
		require.False(t, ok, base)
	}
}

func TestOpenCodeSharedUsageCredentialChangeAndExpiry(t *testing.T) {
	a := newCNUsageMonitorAccount(1, PlatformOpenCodeGo, AccountModeGo)
	repo := &cnUsageMonitorRepo{accounts: map[int64]*Account{1: a}}
	upstream := &cnUsageMonitorHTTP{body: openCodeGoUsageFixture}
	svc := NewUpstreamUsageService(repo, upstream, testUpstreamUsageConfig(), nil)
	_, err := svc.QueryAccount(context.Background(), 1)
	require.NoError(t, err)
	// 更换整份凭据，模拟数据库更新而不是原地修改仍在使用的快照。
	changed := newCNUsageMonitorAccount(1, PlatformOpenCodeGo, AccountModeGo)
	changed.Credentials["api_key"] = "new-key"
	repo.accounts[1] = changed
	_, err = svc.QueryAccount(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 2, upstream.calls)
	cfg, _ := EffectiveUpstreamUsageConfig(changed)
	key, _ := openCodeGoSharedUsageKey(changed, cfg)
	svc.openCodeSharedMu.Lock()
	entry := svc.openCodeSharedResults[key]
	entry.expiresAt = time.Now().Add(-time.Second)
	svc.openCodeSharedResults[key] = entry
	svc.openCodeSharedMu.Unlock()
	_, err = svc.QueryAccount(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 3, upstream.calls)
}

func TestOpenCodeSharedUsageRelayNeverUsesOfficialEndpoint(t *testing.T) {
	a := newCNUsageMonitorAccount(1, PlatformOpenCodeGo, AccountModeGo)
	a.Credentials["base_url"] = "https://relay.example/prefix"
	repo := &cnUsageMonitorRepo{accounts: map[int64]*Account{1: a}}
	upstream := &cnUsageMonitorHTTP{body: openCodeGoUsageFixture}
	svc := NewUpstreamUsageService(repo, upstream, testUpstreamUsageConfig(), nil)
	for range 2 {
		_, err := svc.QueryAccount(context.Background(), 1)
		require.NoError(t, err)
	}
	require.Equal(t, 2, upstream.calls)
	for _, req := range upstream.requests {
		require.Equal(t, "https://relay.example/prefix/v1/usage", req.URL.String())
	}
	require.Empty(t, svc.openCodeSharedResults)
}
