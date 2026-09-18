package service

import (
	"context"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 更换管理会话会清除持久化快照，也必须失效进程内缓存。
func TestOllamaProbeSessionChangeInvalidatesCachedExhaustion(t *testing.T) {
	now := time.Now().UTC()
	account := ollamaUsageAccount(801)
	account.Extra[OllamaCloudUsageSessionExtraKey] = "cipher:wos-session=first"
	repo := &ollamaUsageTestRepo{accountServiceTestRepo: &accountServiceTestRepo{
		accounts: map[int64]*Account{account.ID: account},
	}}
	upstream := &ollamaUsageHTTPStub{body: ollamaCloudProbeUsageBody(100, now.Add(time.Hour).Format(time.RFC3339))}
	svc := NewOllamaCloudUsageService(repo, upstream, nil, ollamaUsageTestEncryptor{}, true)
	svc.now = func() time.Time { return now }
	defer svc.Stop()
	callbacks := 0
	callback := func(int64, time.Time) { callbacks++ }
	svc.runOllamaCloudUsageProbe(context.Background(), account.ID, callback)
	require.Equal(t, 1, callbacks)
	require.Equal(t, int64(1), upstream.calls.Load())

	current, err := repo.GetByID(context.Background(), account.ID)
	require.NoError(t, err)
	require.NoError(t, repo.SaveOllamaCloudUsageSession(context.Background(), current, "cipher:wos-session=second", false))
	upstream.body = ollamaCloudProbeUsageBody(10, now.Add(time.Hour).Format(time.RFC3339))
	svc.runOllamaCloudUsageProbe(context.Background(), account.ID, callback)
	require.Equal(t, int64(2), upstream.calls.Load())
	require.Equal(t, 1, callbacks, "新会话未耗尽，不能回放旧会话的耗尽结果")
}

// 即使快照允许保存，抓取期间修改的账号配置也使旧探测回调失效。
func TestOllamaProbeAccountEditDuringFetchDropsCallback(t *testing.T) {
	now := time.Now().UTC()
	account := ollamaUsageAccount(802)
	account.Extra[OllamaCloudUsageSessionExtraKey] = "cipher:wos-session=first"
	repo := &ollamaUsageTestRepo{accountServiceTestRepo: &accountServiceTestRepo{
		accounts: map[int64]*Account{account.ID: account},
	}}
	upstream := &ollamaUsageHTTPStub{
		body: ollamaCloudProbeUsageBody(100, now.Add(time.Hour).Format(time.RFC3339)),
		beforeResponse: func(*http.Request) {
			repo.mu.Lock()
			repo.accounts[account.ID].Name = "edited during fetch"
			repo.mu.Unlock()
		},
	}
	svc := NewOllamaCloudUsageService(repo, upstream, nil, ollamaUsageTestEncryptor{}, true)
	svc.now = func() time.Time { return now }
	defer svc.Stop()
	called := false
	svc.runOllamaCloudUsageProbe(context.Background(), account.ID, func(int64, time.Time) { called = true })
	require.False(t, called)
	require.Equal(t, int64(1), upstream.calls.Load())
}

// 探测等待同 Key 的人工查询时可独立取消，不取消或重复执行人工请求。
func TestOllamaProbeCancelableWaitPreservesManualRefresh(t *testing.T) {
	now := time.Now().UTC()
	account := ollamaUsageAccount(803)
	account.Extra[OllamaCloudUsageSessionExtraKey] = "cipher:wos-session=first"
	repo := &ollamaUsageTestRepo{accountServiceTestRepo: &accountServiceTestRepo{
		accounts: map[int64]*Account{account.ID: account},
	}}
	entered, release := make(chan struct{}), make(chan struct{})
	upstream := &ollamaUsageHTTPStub{
		body:           ollamaCloudProbeUsageBody(100, now.Add(time.Hour).Format(time.RFC3339)),
		beforeResponse: func(*http.Request) { close(entered); <-release },
	}
	svc := NewOllamaCloudUsageService(repo, upstream, nil, ollamaUsageTestEncryptor{}, true)
	svc.now = func() time.Time { return now }
	defer svc.Stop()
	manualDone := make(chan error, 1)
	go func() {
		_, err := svc.refreshAccount(context.Background(), account.ID, nil, false)
		manualDone <- err
	}()
	defer func() { close(release); require.NoError(t, <-manualDone) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("人工查询未开始")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	probeDone := make(chan struct{})
	go func() {
		svc.runOllamaCloudUsageProbe(ctx, account.ID, func(int64, time.Time) { t.Error("取消后不得回调") })
		close(probeDone)
	}()
	select {
	case <-probeDone:
	case <-time.After(time.Second):
		t.Fatal("探测不应被人工查询的 singleflight 等待阻塞")
	}
	require.Equal(t, int64(1), upstream.calls.Load())
}

// 异常百分比和未来观测时间均不足以确认可恢复时间。
func TestOllamaExhaustionRejectsInvalidObservation(t *testing.T) {
	now, reset := time.Now().UTC(), time.Now().Add(time.Hour).UTC()
	for _, percent := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		snapshot := &OllamaCloudUsageSnapshot{
			Status: OllamaCloudUsageStatusOK, FetchedAt: &now,
			Data: &OllamaCloudUsageData{FiveHour: &OllamaCloudUsageWindow{UsedPercent: percent, ResetAt: &reset}},
		}
		_, ok := ollamaCloudUsageExhaustionResetAt(snapshot, now, now.Add(-time.Minute))
		require.False(t, ok)
	}
	future := now.Add(time.Minute)
	_, ok := ollamaCloudUsageExhaustionResetAt(&OllamaCloudUsageSnapshot{
		Status: OllamaCloudUsageStatusOK, FetchedAt: &future,
		Data: &OllamaCloudUsageData{FiveHour: &OllamaCloudUsageWindow{UsedPercent: 100, ResetAt: &reset}},
	}, now, now.Add(-time.Minute))
	require.False(t, ok)
}
