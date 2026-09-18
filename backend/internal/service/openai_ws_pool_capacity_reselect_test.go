package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

// 显式指纹 seed 使测试覆盖 session/full 的硬兼容键，而非关闭模式的空键。
func activeCodexFingerprintPoolAccountForTest(id int64) *Account {
	return &Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{
		codexFingerprintModeExtraKey: "session",
		codexFingerprintSeedExtraKey: "11111111-1111-4111-8111-111111111111",
	}}
}

func stableOpenAIWSIdentityHeadersForTest() http.Header {
	headers := make(http.Header)
	headers.Set("X-Codex-Beta-Features", "remote_compaction_v2,responses_websockets_v2")
	headers.Set("X-Codex-Installation-ID", "install-a")
	headers.Set("session-id", "session-hyphen-a")
	headers.Set("session_id", "session-underscore-a")
	headers.Set("thread-id", "thread-a")
	headers.Set("x-client-request-id", "client-request-a")
	headers.Set("x-codex-window-id", "window-a")
	return headers
}

// 广播不能让等待者越过 TLS 或稳定身份硬边界，哪怕不兼容连接先变为空闲。
func TestOpenAIWSConnPool_ReselectionPreservesHardCompatibility(t *testing.T) {
	for _, boundary := range []string{"tls", "stable_identity"} {
		t.Run(boundary, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 3
			cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 3
			cfg.Gateway.OpenAIWS.QueueLimitPerConn = 4
			pool := newOpenAIWSConnPool(cfg)
			t.Cleanup(pool.Close)
			account := &Account{ID: 1997, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
			req := openAIWSAcquireRequest{Account: account, WSURL: "wss://example.com/v1/responses", TLSProfileKey: "profile-a"}
			target := newOpenAIWSConn("target", account.ID, &openAIWSFakeConn{}, nil, nil, req.TLSProfileKey)
			other := newOpenAIWSConn("other", account.ID, &openAIWSFakeConn{}, nil, nil, req.TLSProfileKey)
			incompatible := newOpenAIWSConn("incompatible", account.ID, &openAIWSFakeConn{}, nil, nil, req.TLSProfileKey)
			if boundary == "tls" {
				incompatible.tlsProfileKey = "profile-b"
			} else {
				// 稳定身份的归一化另有矩阵覆盖，此处验证广播挑选不能忽略已有硬键。
				incompatible.handshakeCompatibility.sessionIDHyphen = "another-session"
			}
			for _, conn := range []*openAIWSConn{target, other, incompatible} {
				require.True(t, conn.tryAcquire())
				t.Cleanup(conn.close)
			}
			other.waiters.Add(1)
			defer other.waiters.Add(-1)
			ap := pool.getOrCreateAccountPool(account.ID)
			ap.mu.Lock()
			ap.conns[target.id], ap.conns[other.id], ap.conns[incompatible.id] = target, other, incompatible
			// 固定不兼容连接，使它不会被合法淘汰新建；这里只检测是否错误复用。
			ap.pinnedConns[incompatible.id] = 1
			ap.mu.Unlock()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			type result struct {
				lease *openAIWSConnLease
				err   error
			}
			results := make(chan result, 1)
			go func() { lease, err := pool.Acquire(ctx, req); results <- result{lease, err} }()
			require.Eventually(t, func() bool { return target.waiters.Load() == 1 }, time.Second, time.Millisecond)
			(&openAIWSConnLease{pool: pool, accountID: account.ID, conn: incompatible}).Release()
			select {
			case got := <-results:
				if got.lease != nil {
					got.lease.Release()
				}
				t.Fatal("不兼容连接释放后仍应等待匹配连接")
			case <-time.After(40 * time.Millisecond):
			}
			(&openAIWSConnLease{pool: pool, accountID: account.ID, conn: other}).Release()
			select {
			case got := <-results:
				require.NoError(t, got.err)
				require.NotNil(t, got.lease)
				require.Equal(t, other.id, got.lease.ConnID())
				got.lease.Release()
			case <-ctx.Done():
				t.Fatal("匹配连接释放后没有完成重新选择")
			}
			require.Zero(t, target.waiters.Load())
		})
	}
}

// 覆盖广播重选、容量缩放与稳定身份硬兼容；保留原池测试中的 TLS 回归。
func TestOpenAIWSConnPool_AcquireAtCapacityCanceledWaiterDoesNotTakeReleasedConn(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 2
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 4

	accountID := int64(995)
	account := &Account{ID: accountID, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	req := openAIWSAcquireRequest{Account: account, WSURL: "wss://example.com/v1/responses"}
	pool := newOpenAIWSConnPool(cfg)
	t.Cleanup(pool.Close)
	target := newOpenAIWSConn("target", accountID, &openAIWSFakeConn{}, nil, nil, "")
	other := newOpenAIWSConn("other", accountID, &openAIWSFakeConn{}, nil, nil, "")
	require.True(t, target.tryAcquire())
	require.True(t, other.tryAcquire())
	other.waiters.Add(1)

	ap := pool.ensureAccountPoolLocked(accountID)
	ap.mu.Lock()
	ap.conns[target.id] = target
	ap.conns[other.id] = other
	ap.lastAcquire = &req
	ap.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		lease *openAIWSConnLease
		err   error
	}
	resultCh := make(chan result, 1)
	go func() {
		lease, err := pool.Acquire(ctx, req)
		resultCh <- result{lease: lease, err: err}
	}()
	require.Eventually(t, func() bool { return target.waiters.Load() == 1 }, time.Second, 5*time.Millisecond)

	// 持锁广播：等待者被唤醒后卡在重新取锁上，此时取消请求并归还 other 的令牌，
	// 解锁后重选会看到一条空闲连接，但请求已经取消，不能带着租约返回。
	ap.mu.Lock()
	ap.signalChangedLocked()
	cancel()
	other.release()
	ap.mu.Unlock()

	select {
	case got := <-resultCh:
		require.ErrorIs(t, got.err, context.Canceled)
		require.Nil(t, got.lease, "a canceled waiter must not come back with a lease after a pool change wake-up")
	case <-time.After(500 * time.Millisecond):
		t.Fatal("canceled waiter must return promptly")
	}
	require.True(t, other.tryAcquire(), "the released token must stay available to other acquirers")
	other.release()
	require.Equal(t, int32(0), target.waiters.Load())
}

func TestOpenAIWSConnPool_AcquireAtCapacityWakesWhenAnotherConnReleases(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 2
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 4

	pool := newOpenAIWSConnPool(cfg)
	t.Cleanup(pool.Close)
	accountID := int64(993)
	account := &Account{ID: accountID, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	req := openAIWSAcquireRequest{Account: account, WSURL: "wss://example.com/v1/responses"}
	target := newOpenAIWSConn("target", accountID, &openAIWSFakeConn{}, nil, nil, "")
	other := newOpenAIWSConn("other", accountID, &openAIWSFakeConn{}, nil, nil, "")
	require.True(t, target.tryAcquire())
	require.True(t, other.tryAcquire())
	// other 上已有一个等待者，新来的等待者会挂到 target 上。
	other.waiters.Add(1)

	ap := pool.ensureAccountPoolLocked(accountID)
	ap.mu.Lock()
	ap.conns[target.id] = target
	ap.conns[other.id] = other
	ap.lastAcquire = &req
	ap.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	type result struct {
		lease *openAIWSConnLease
		err   error
	}
	resultCh := make(chan result, 1)
	go func() {
		lease, err := pool.Acquire(ctx, req)
		resultCh <- result{lease: lease, err: err}
	}()
	require.Eventually(t, func() bool { return target.waiters.Load() == 1 }, time.Second, 5*time.Millisecond)

	time.Sleep(40 * time.Millisecond)
	otherLease := &openAIWSConnLease{pool: pool, accountID: accountID, conn: other}
	otherLease.Release()

	select {
	case got := <-resultCh:
		require.NoError(t, got.err)
		require.NotNil(t, got.lease)
		require.Equal(t, other.id, got.lease.ConnID())
		require.True(t, got.lease.Reused())
		require.GreaterOrEqual(t, got.lease.QueueWaitDuration(), 30*time.Millisecond, "queue wait accumulated before the wake-up must be carried into the lease")
		got.lease.Release()
	case <-time.After(500 * time.Millisecond):
		t.Fatal("waiter queued on a busy connection must be woken when another connection is released")
	}
	require.Equal(t, int32(0), target.waiters.Load())
	metrics := pool.SnapshotMetrics()
	require.Equal(t, int64(1), metrics.AcquireQueueWaitTotal)
	require.GreaterOrEqual(t, metrics.AcquireQueueWaitMsTotal, int64(30))
}

func TestOpenAIWSConnPool_AcquireAtCapacityWakesWhenCapacityFreedByEviction(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 2
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 4

	pool := newOpenAIWSConnPool(cfg)
	t.Cleanup(pool.Close)
	dialer := &openAIWSCountingDialer{}
	pool.setClientDialerForTest(dialer)
	accountID := int64(994)
	account := &Account{ID: accountID, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	req := openAIWSAcquireRequest{Account: account, WSURL: "wss://example.com/v1/responses"}
	target := newOpenAIWSConn("target", accountID, &openAIWSFakeConn{}, nil, nil, "")
	other := newOpenAIWSConn("other", accountID, &openAIWSFakeConn{}, nil, nil, "")
	require.True(t, target.tryAcquire())
	require.True(t, other.tryAcquire())
	other.waiters.Add(1)

	ap := pool.ensureAccountPoolLocked(accountID)
	ap.mu.Lock()
	ap.conns[target.id] = target
	ap.conns[other.id] = other
	ap.lastAcquire = &req
	ap.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	type result struct {
		lease *openAIWSConnLease
		err   error
	}
	resultCh := make(chan result, 1)
	go func() {
		lease, err := pool.Acquire(ctx, req)
		resultCh <- result{lease: lease, err: err}
	}()
	require.Eventually(t, func() bool { return target.waiters.Load() == 1 }, time.Second, 5*time.Millisecond)

	// 剔除另一条连接腾出名额，等待者应重新选择并新拨号，而不是继续等 target。
	time.Sleep(40 * time.Millisecond)
	pool.evictConn(accountID, other.id)

	select {
	case got := <-resultCh:
		require.NoError(t, got.err)
		require.NotNil(t, got.lease)
		require.False(t, got.lease.Reused())
		require.NotEqual(t, target.id, got.lease.ConnID())
		require.GreaterOrEqual(t, got.lease.QueueWaitDuration(), 30*time.Millisecond, "queue wait must be carried into a lease obtained by dialing after the wake-up")
		got.lease.Release()
	case <-time.After(500 * time.Millisecond):
		t.Fatal("waiter queued on a busy connection must be woken when pool capacity is freed")
	}
	require.Equal(t, 1, dialer.DialCount())
	require.Equal(t, int32(0), target.waiters.Load())
	metrics := pool.SnapshotMetrics()
	require.Equal(t, int64(1), metrics.AcquireQueueWaitTotal)
	require.GreaterOrEqual(t, metrics.AcquireQueueWaitMsTotal, int64(30))
}

func TestOpenAIWSConnPool_AcquireDoesNotReuseDifferentStableIdentity(t *testing.T) {
	for _, tt := range []struct {
		name   string
		header string
		value  string
	}{
		{name: "installation", header: "x-codex-installation-id", value: "install-b"},
		{name: "session hyphen", header: "session-id", value: "session-hyphen-b"},
		{name: "session underscore", header: "session_id", value: "session-underscore-b"},
		{name: "thread", header: "thread-id", value: "thread-b"},
		{name: "client request", header: "x-client-request-id", value: "client-request-b"},
		{name: "window", header: "x-codex-window-id", value: "window-b"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 2
			cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
			cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 2

			pool := newOpenAIWSConnPool(cfg)
			t.Cleanup(pool.Close)
			dialer := &openAIWSCountingDialer{}
			pool.setClientDialerForTest(dialer)
			account := activeCodexFingerprintPoolAccountForTest(133)

			first, err := pool.Acquire(context.Background(), openAIWSAcquireRequest{
				Account: account,
				WSURL:   "wss://example.com/v1/responses",
				Headers: stableOpenAIWSIdentityHeadersForTest(),
			})
			require.NoError(t, err)
			firstConnID := first.ConnID()
			first.Release()

			nextHeaders := stableOpenAIWSIdentityHeadersForTest()
			nextHeaders.Set(tt.header, tt.value)
			second, err := pool.Acquire(context.Background(), openAIWSAcquireRequest{
				Account: account,
				WSURL:   "wss://example.com/v1/responses",
				Headers: nextHeaders,
			})
			require.NoError(t, err)
			require.False(t, second.Reused())
			require.NotEqual(t, firstConnID, second.ConnID())
			second.Release()
			require.Equal(t, 2, dialer.DialCount())
		})
	}
}

func TestOpenAIWSConnPool_AcquireRetainedSessionsUsesScaledCapacity(t *testing.T) {
	for _, mode := range []struct {
		name string
		v2   bool
	}{
		{name: "legacy"},
		{name: "mode router v2", v2: true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = mode.v2
			cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 3
			cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 3
			cfg.Gateway.OpenAIWS.PoolTargetUtilization = 1
			cfg.Gateway.OpenAIWS.DynamicMaxConnsByAccountConcurrencyEnabled = true
			cfg.Gateway.OpenAIWS.OAuthMaxConnsFactor = 2
			pool := newOpenAIWSConnPool(cfg)
			t.Cleanup(pool.Close)
			pool.setClientDialerForTest(&openAIWSFakeDialer{})
			t.Cleanup(pool.Close)
			req := openAIWSAcquireRequest{
				Account: &Account{ID: 902, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 2},
				WSURL:   "wss://example.com/v1/responses",
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			leases := make([]*openAIWSConnLease, 0, 3)
			for range 3 {
				lease, err := pool.Acquire(ctx, req)
				require.NoError(t, err, "轮次之间仍持有连接的会话应可使用系数扩出的容量")
				t.Cleanup(lease.Release)
				leases = append(leases, lease)
			}

			fullCtx, fullCancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer fullCancel()
			_, err := pool.Acquire(fullCtx, req)
			require.ErrorIs(t, err, context.DeadlineExceeded, "扩容后仍必须受全局硬上限约束")

			leases[0].Release()
			reused, err := pool.Acquire(ctx, req)
			require.NoError(t, err)
			defer reused.Release()
			require.Equal(t, leases[0].ConnID(), reused.ConnID())
			require.True(t, reused.Reused())
		})
	}
}

func TestOpenAIWSConnPool_AcquireReusesSameStableIdentityWithDifferentTurnMetadata(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1

	pool := newOpenAIWSConnPool(cfg)
	t.Cleanup(pool.Close)
	dialer := &openAIWSCountingDialer{}
	pool.setClientDialerForTest(dialer)
	account := activeCodexFingerprintPoolAccountForTest(132)
	headers := stableOpenAIWSIdentityHeadersForTest()
	headers.Set("Authorization", "Bearer token-a")
	headers.Set("x-codex-turn-metadata", `{"turn_id":"turn-a"}`)

	first, err := pool.Acquire(context.Background(), openAIWSAcquireRequest{
		Account: account,
		WSURL:   "wss://example.com/v1/responses",
		Headers: headers,
	})
	require.NoError(t, err)
	firstConnID := first.ConnID()
	first.Release()

	nextHeaders := stableOpenAIWSIdentityHeadersForTest()
	nextHeaders.Set("Authorization", "Bearer token-b")
	nextHeaders.Set("x-codex-turn-metadata", `{"turn_id":"turn-b"}`)
	nextHeaders.Set(openAICodexRoutingHintHeader, "model=gpt-5.6-codex;tier=priority")
	second, err := pool.Acquire(context.Background(), openAIWSAcquireRequest{
		Account: account,
		WSURL:   "wss://example.com/v1/responses",
		Headers: nextHeaders,
	})
	require.NoError(t, err)
	require.True(t, second.Reused())
	require.Equal(t, firstConnID, second.ConnID())
	second.Release()
	require.Equal(t, 1, dialer.DialCount(), "stable identity match should ignore auth, turn metadata, and soft routing hints")
}

func TestOpenAIWSConnPool_AcquireRoutingHintRemainsSoftAffinity(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1

	pool := newOpenAIWSConnPool(cfg)
	t.Cleanup(pool.Close)
	dialer := &openAIWSCountingDialer{}
	pool.setClientDialerForTest(dialer)
	account := activeCodexFingerprintPoolAccountForTest(134)

	firstHeaders := stableOpenAIWSIdentityHeadersForTest()
	firstHeaders.Set(openAICodexRoutingHintHeader, "model=gpt-5.6-codex")
	first, err := pool.Acquire(context.Background(), openAIWSAcquireRequest{
		Account: account,
		WSURL:   "wss://example.com/v1/responses",
		Headers: firstHeaders,
	})
	require.NoError(t, err)
	firstConnID := first.ConnID()
	first.Release()

	secondHeaders := stableOpenAIWSIdentityHeadersForTest()
	secondHeaders.Set(openAICodexRoutingHintHeader, "model=gpt-5.6-codex;tier=priority")
	second, err := pool.Acquire(context.Background(), openAIWSAcquireRequest{
		Account: account,
		WSURL:   "wss://example.com/v1/responses",
		Headers: secondHeaders,
	})
	require.NoError(t, err)
	require.True(t, second.Reused())
	require.Equal(t, firstConnID, second.ConnID())
	second.Release()
	require.Equal(t, 1, dialer.DialCount())
}

func TestOpenAIWSConnPool_DeviceModeKeysOnlyInstallationIdentity(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 2
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 2

	pool := newOpenAIWSConnPool(cfg)
	t.Cleanup(pool.Close)
	dialer := &openAIWSCountingDialer{}
	pool.setClientDialerForTest(dialer)
	account := activeCodexFingerprintPoolAccountForTest(135)
	account.Extra[codexFingerprintModeExtraKey] = "device"

	firstHeaders := stableOpenAIWSIdentityHeadersForTest()
	first, err := pool.Acquire(context.Background(), openAIWSAcquireRequest{
		Account: account,
		WSURL:   "wss://example.com/v1/responses",
		Headers: firstHeaders,
	})
	require.NoError(t, err)
	firstConnID := first.ConnID()
	first.Release()

	sessionChanged := stableOpenAIWSIdentityHeadersForTest()
	sessionChanged.Set("session-id", "session-hyphen-b")
	sessionChanged.Set("session_id", "session-underscore-b")
	sessionChanged.Set("thread-id", "thread-b")
	sessionChanged.Set("x-client-request-id", "client-request-b")
	sessionChanged.Set("x-codex-window-id", "window-b")
	second, err := pool.Acquire(context.Background(), openAIWSAcquireRequest{
		Account: account,
		WSURL:   "wss://example.com/v1/responses",
		Headers: sessionChanged,
	})
	require.NoError(t, err)
	require.True(t, second.Reused())
	require.Equal(t, firstConnID, second.ConnID())
	second.Release()

	installationChanged := sessionChanged.Clone()
	installationChanged.Set("x-codex-installation-id", "install-b")
	third, err := pool.Acquire(context.Background(), openAIWSAcquireRequest{
		Account: account,
		WSURL:   "wss://example.com/v1/responses",
		Headers: installationChanged,
	})
	require.NoError(t, err)
	require.False(t, third.Reused())
	require.NotEqual(t, firstConnID, third.ConnID())
	third.Release()
	require.Equal(t, 2, dialer.DialCount())
}

func TestOpenAIWSConnPool_EffectiveMaxConnsByAccount_ModeRouterV2(t *testing.T) {
	tests := []struct {
		name        string
		accountType string
		concurrency int
		factor      float64
		dynamic     bool
		want        int
	}{
		{name: "oauth expansion", accountType: AccountTypeOAuth, concurrency: 1, factor: 5, dynamic: true, want: 5},
		{name: "apikey expansion", accountType: AccountTypeAPIKey, concurrency: 1, factor: 5, dynamic: true, want: 5},
		{name: "oauth fraction", accountType: AccountTypeOAuth, concurrency: 20, factor: 0.3, dynamic: true, want: 6},
		{name: "apikey rounding", accountType: AccountTypeAPIKey, concurrency: 3, factor: 0.6, dynamic: true, want: 2},
		{name: "minimum one", accountType: AccountTypeOAuth, concurrency: 1, factor: 0.3, dynamic: true, want: 1},
		{name: "hard cap", accountType: AccountTypeOAuth, concurrency: 3, factor: 5, dynamic: true, want: 8},
		{name: "dynamic disabled", accountType: AccountTypeAPIKey, concurrency: 2, factor: 0.6, want: 8},
		{name: "zero concurrency", accountType: AccountTypeOAuth, factor: 5, dynamic: true, want: 0},
		{name: "negative concurrency", accountType: AccountTypeAPIKey, concurrency: -1, factor: 5, dynamic: true, want: 0},
		{name: "zero concurrency dynamic disabled", accountType: AccountTypeOAuth, factor: 5, want: 0},
		{name: "negative concurrency dynamic disabled", accountType: AccountTypeAPIKey, concurrency: -1, factor: 5, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
			cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 8
			cfg.Gateway.OpenAIWS.DynamicMaxConnsByAccountConcurrencyEnabled = tt.dynamic
			cfg.Gateway.OpenAIWS.OAuthMaxConnsFactor = 1
			cfg.Gateway.OpenAIWS.APIKeyMaxConnsFactor = 1
			if tt.accountType == AccountTypeOAuth {
				cfg.Gateway.OpenAIWS.OAuthMaxConnsFactor = tt.factor
			} else {
				cfg.Gateway.OpenAIWS.APIKeyMaxConnsFactor = tt.factor
			}
			pool := newOpenAIWSConnPool(cfg)
			t.Cleanup(pool.Close)
			account := &Account{Platform: PlatformOpenAI, Type: tt.accountType, Concurrency: tt.concurrency}
			require.Equal(t, tt.want, pool.effectiveMaxConnsByAccount(account))
			require.Equal(t, 8, pool.effectiveMaxConnsByAccount(nil))
		})
	}
}
