//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

// hydrate 失败时，已经占用的并发槽位必须在返回前释放，否则调用方拿不到
// ReleaseFunc，槽位会一直泄漏到 TTL 到期。
func TestGatewayServiceNewSelectionResultReleasesSlotOnHydrationError(t *testing.T) {
	// 缓存未命中且关闭数据库回源时，hydrate 返回 ErrSchedulerCacheNotReady。
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.DbFallbackEnabled = false
	svc := &GatewayService{
		schedulerSnapshot: NewSchedulerSnapshotService(&snapshotHydrationCache{accounts: map[int64]*Account{}}, nil, nil, nil, cfg),
	}

	released := 0
	selection, err := svc.newSelectionResult(context.Background(), &Account{ID: 42}, true, func() { released++ }, nil)

	require.Error(t, err)
	require.Nil(t, selection)
	require.Equal(t, 1, released)
}

// hydrate 成功时不能提前释放槽位，ReleaseFunc 应原样交给调用方。
func TestGatewayServiceNewSelectionResultKeepsReleaseOnSuccess(t *testing.T) {
	account := &Account{ID: 42, Platform: PlatformAnthropic}
	cache := &snapshotHydrationCache{
		accounts: map[int64]*Account{account.ID: account},
	}
	svc := &GatewayService{
		schedulerSnapshot: NewSchedulerSnapshotService(cache, nil, nil, nil, nil),
	}

	released := 0
	selection, err := svc.newSelectionResult(context.Background(), account, true, func() { released++ }, nil)

	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Zero(t, released)
	require.NotNil(t, selection.ReleaseFunc)

	selection.ReleaseFunc()
	require.Equal(t, 1, released)
}
