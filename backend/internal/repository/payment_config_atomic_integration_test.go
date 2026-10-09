//go:build integration

package repository

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 屏障只协调测试开始；实际读写和跨实例互斥全部经过 PostgreSQL 仓储事务。
type paymentConfigBarrierRepository struct {
	service.SettingRepository
	updater service.SettingAtomicUpdater
	started *sync.WaitGroup
	release <-chan struct{}
}

func (r *paymentConfigBarrierRepository) UpdateMultiple(ctx context.Context, keys []string, update func(map[string]string) (map[string]string, error)) error {
	r.started.Done()
	<-r.release
	return r.updater.UpdateMultiple(ctx, keys, update)
}

func TestPaymentConfigConcurrentInstancesPreserveRechargeBonusInvariant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	keys := []string{service.SettingRechargeBonusMode, service.SettingRechargeBonusTiers, service.SettingRechargeBonusNotice}
	repo := NewSettingRepository(integrationEntClient)
	original, err := repo.GetMultiple(ctx, keys)
	require.NoError(t, err)
	t.Cleanup(func() {
		for _, key := range keys {
			if value, ok := original[key]; ok {
				require.NoError(t, repo.Set(context.Background(), key, value))
			} else {
				require.NoError(t, repo.Delete(context.Background(), key))
			}
		}
	})
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "initially_missing", true: "existing_rows"}[existing], func(t *testing.T) {
			for _, key := range keys {
				require.NoError(t, repo.Delete(ctx, key))
			}
			if existing {
				require.NoError(t, repo.SetMultiple(ctx, map[string]string{
					service.SettingRechargeBonusMode:  service.RechargeBonusModeBonus,
					service.SettingRechargeBonusTiers: `[{"min_amount":100,"bonus_percent":20}]`,
				}))
			}
			var started sync.WaitGroup
			started.Add(2)
			release := make(chan struct{})
			errorsByInstance := make(chan error, 2)
			discount, noticeDiscount, noticeBonus := service.RechargeBonusModeDiscount, "discount", "bonus"
			tiers := []service.RechargeBonusTier{{MinAmount: 100, BonusPercent: 200}}
			requests := []service.UpdatePaymentConfigRequest{
				{RechargeBonusMode: &discount, RechargeBonusNotice: &noticeDiscount},
				{RechargeBonusTiers: &tiers, RechargeBonusNotice: &noticeBonus},
			}
			for _, request := range requests {
				// 每个服务拥有独立仓储实例，不能依靠服务级或仓储级进程锁维持约束。
				instanceRepo := NewSettingRepository(integrationEntClient)
				barrier := &paymentConfigBarrierRepository{SettingRepository: instanceRepo,
					updater: instanceRepo.(service.SettingAtomicUpdater), started: &started, release: release}
				svc := service.NewPaymentConfigService(integrationEntClient, barrier, nil)
				go func() { errorsByInstance <- svc.UpdatePaymentConfig(ctx, request) }()
			}
			started.Wait()
			close(release)
			successes := 0
			for range 2 {
				if err := <-errorsByInstance; err == nil {
					successes++
				} else {
					require.Equal(t, "INVALID_RECHARGE_BONUS_TIERS", infraerrors.Reason(err))
				}
			}
			require.Equal(t, 1, successes)
			cfg, err := service.NewPaymentConfigService(integrationEntClient, repo, nil).GetPaymentConfig(ctx)
			require.NoError(t, err)
			require.NoError(t, service.ValidateRechargeBonusTiersForMode(cfg.RechargeBonusMode, cfg.RechargeBonusTiers))
			require.Equal(t, cfg.RechargeBonusMode, cfg.RechargeBonusNotice, "失败请求的其他配置也必须回滚")
		})
	}
}

// 初次配置校验失败后，不应留下占位键或任何部分写入。
func TestSettingAtomicUpdateRollsBackMissingKeys(t *testing.T) {
	ctx := context.Background()
	repo := &settingRepository{client: integrationEntClient}
	key := "payment_atomic_rollback_test"
	require.NoError(t, repo.Delete(ctx, key))
	want := errors.New("synthetic validation failure")
	err := repo.UpdateMultiple(ctx, []string{key}, func(stored map[string]string) (map[string]string, error) {
		require.Equal(t, "", stored[key])
		return map[string]string{key: "invalid"}, want
	})
	require.ErrorIs(t, err, want)
	_, err = repo.Get(ctx, key)
	require.ErrorIs(t, err, service.ErrSettingNotFound)
}
