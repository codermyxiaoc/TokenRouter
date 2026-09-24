//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/service"
)

// 使用真实事务验证延期接口返回时窗口已持久化，无需再等一次模型请求激活。
func TestSubscriptionExtensionPersistsWindowRecovery(t *testing.T) {
	for _, tc := range []struct {
		name       string
		setDays    bool
		oldWindows bool
		expired    bool
	}{
		{name: "extend_missing_windows"},
		{name: "set_days_missing_windows", setDays: true},
		{name: "extend_expired_windows", oldWindows: true},
		{name: "set_days_expired_windows", setDays: true, oldWindows: true},
		{name: "extend_expired_subscription", expired: true, oldWindows: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			client := testEntClient(t)
			user := mustCreateUser(t, client, &service.User{Email: "extend-" + uuid.NewString() + "@example.com"})
			plan := mustCreatePlan(t, client, &service.SubscriptionPlan{Name: "延期持久化", Price: 10, ValidityDays: 30, ValidityUnit: "day"})
			now := time.Now().Truncate(time.Microsecond)
			expiry := now.Add(12 * time.Hour)
			if tc.expired {
				expiry = now.Add(-time.Hour)
			}
			sub := &service.UserSubscription{
				UserID: user.ID, PlanID: plan.ID, StartsAt: now.AddDate(0, 0, -45), ExpiresAt: expiry,
				DailyLimitUSD: float64Ptr(10), WeeklyLimitUSD: float64Ptr(20), MonthlyLimitUSD: float64Ptr(30),
				DailyUsageUSD: 3, WeeklyUsageUSD: 6, MonthlyUsageUSD: 9,
			}
			if tc.expired {
				sub.Status = service.SubscriptionStatusExpired
			}
			if tc.oldWindows {
				old := now.AddDate(0, 0, -35)
				sub.DailyWindowStart, sub.WeeklyWindowStart, sub.MonthlyWindowStart = &old, &old, &old
			}
			mustCreateSubscription(t, client, sub)
			// 管理员延期必须保留本期累计次数；水位设为测试开始时刻，避免补算历史混淆断言。
			_, err := integrationDB.ExecContext(ctx, `UPDATE user_subscriptions SET daily_reset_count=5, weekly_reset_count=3, monthly_reset_count=1, reset_counted_at=$1 WHERE id=$2`, now, sub.ID)
			require.NoError(t, err)
			repo := NewUserSubscriptionRepository(client)
			svc := service.NewSubscriptionService(nil, repo, nil, client, nil)
			mutationStarted := time.Now()
			if tc.setDays {
				_, err = svc.SetSubscriptionValidityDays(ctx, sub.ID, 35)
			} else {
				_, err = svc.ExtendSubscription(ctx, sub.ID, 35)
			}
			require.NoError(t, err)
			mutationFinished := time.Now()
			persisted, err := repo.GetByID(ctx, sub.ID)
			require.NoError(t, err)
			require.Equal(t, service.SubscriptionStatusActive, persisted.Status)
			require.Equal(t, plan.ID, persisted.PlanID)
			require.Equal(t, []int64{5, 3, 1}, resetCountValues(persisted))
			require.NotNil(t, persisted.DailyWindowStart)
			require.True(t, timezone.StartOfDay(mutationFinished).Equal(*persisted.DailyWindowStart), "日窗口仍按项目时区零点")
			for _, window := range []*time.Time{persisted.WeeklyWindowStart, persisted.MonthlyWindowStart} {
				require.NotNil(t, window)
				if !tc.oldWindows {
					require.False(t, window.Before(mutationStarted.Add(-time.Microsecond)))
					require.False(t, window.After(mutationFinished.Add(time.Microsecond)), "缺失的周/月窗口按实际激活时刻建立")
				}
			}
			if tc.oldWindows {
				// 35 天前的旧窗口应按 7/30 日整数周期推进，月窗口仍保留 5 天前的锚点。
				require.True(t, now.Equal(*persisted.WeeklyWindowStart))
				require.True(t, now.AddDate(0, 0, -5).Equal(*persisted.MonthlyWindowStart))
				require.Zero(t, persisted.DailyUsageUSD)
				require.Zero(t, persisted.WeeklyUsageUSD)
				require.Zero(t, persisted.MonthlyUsageUSD)
			} else {
				// 无窗口的迁移数据不能因为激活窗口而抹去已有真实扣费。
				require.Equal(t, 3.0, persisted.DailyUsageUSD)
				require.Equal(t, 6.0, persisted.WeeklyUsageUSD)
				require.Equal(t, 9.0, persisted.MonthlyUsageUSD)
			}
		})
	}
}

// 注入维护失败，确认延期及排队套餐的顺延不会发生部分提交。
type failingWindowActivationRepository struct {
	service.UserSubscriptionRepository
}

func (r failingWindowActivationRepository) ActivateWindows(context.Context, int64, time.Time, service.SubscriptionWindowActivation) error {
	return errors.New("测试窗口维护故障")
}

func TestSubscriptionExtensionRollbackPreservesPendingChain(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{Email: "rollback-" + uuid.NewString() + "@example.com"})
	plan := mustCreatePlan(t, client, &service.SubscriptionPlan{Name: "事务回滚", Price: 10, ValidityDays: 30, ValidityUnit: "day"})
	now := time.Now().Truncate(time.Second)
	expiry := now.Add(12 * time.Hour)
	active := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID: user.ID, PlanID: plan.ID, StartsAt: now.Add(-24 * time.Hour), ExpiresAt: expiry,
		DailyLimitUSD: float64Ptr(10), DailyUsageUSD: 3,
	})
	pending := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID: user.ID, PlanID: plan.ID, StartsAt: expiry, ExpiresAt: expiry.AddDate(0, 0, 30),
		Status: service.SubscriptionStatusPending, DailyLimitUSD: float64Ptr(10),
	})
	repo := NewUserSubscriptionRepository(client)
	svc := service.NewSubscriptionService(nil, failingWindowActivationRepository{repo}, nil, client, nil)
	_, err := svc.ExtendSubscription(ctx, active.ID, 2)
	require.ErrorContains(t, err, "测试窗口维护故障")
	current, err := repo.GetByID(ctx, active.ID)
	require.NoError(t, err)
	require.True(t, expiry.Equal(current.ExpiresAt), "窗口故障时有效期应回滚")
	queued, err := repo.GetByID(ctx, pending.ID)
	require.NoError(t, err)
	require.True(t, pending.StartsAt.Equal(queued.StartsAt), "同一事务内顺延应一起回滚")
	require.True(t, pending.ExpiresAt.Equal(queued.ExpiresAt))

	svc = service.NewSubscriptionService(nil, repo, nil, client, nil)
	_, err = svc.ExtendSubscription(ctx, active.ID, 2)
	require.NoError(t, err)
	queued, err = repo.GetByID(ctx, pending.ID)
	require.NoError(t, err)
	require.True(t, pending.StartsAt.AddDate(0, 0, 2).Equal(queued.StartsAt))
	require.True(t, pending.ExpiresAt.AddDate(0, 0, 2).Equal(queued.ExpiresAt))
	require.Equal(t, service.SubscriptionStatusPending, queued.Status)
	require.Nil(t, queued.DailyWindowStart, "待生效套餐不能提前激活额度窗口")
}

// 经过服务层脱敏后写入 PostgreSQL，验证大于旧 64 KiB 上限的失败请求仍可完整回读。
func TestOpsFailedPayloadFullBodyPersistence(t *testing.T) {
	ctx := context.Background()
	repo := NewOpsRepository(integrationDB)
	svc := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	id := "payload-" + uuid.NewString()
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(ctx, `DELETE FROM ops_request_details WHERE request_id = $1`, id)
		require.NoError(t, err)
	})
	input := strings.Repeat("完整正文", 128*1024)
	body, err := json.Marshal(map[string]any{"input": input, "nested": map[string]any{"api_key": "private-value"}})
	require.NoError(t, err)
	detail := &service.OpsRequestPayloadDetail{
		RequestID: id, ClientRequestID: "client-" + id, Method: "POST", Path: "/v1/responses",
		StatusCode: 502, RequestBody: string(body), ResponseBody: `{"error":{"message":"unavailable"}}`,
		RequestHeaders:  `{"Authorization":["Bearer private-value"],"Content-Type":["application/json"]}`,
		ResponseHeaders: `{"Set-Cookie":["private-value"],"Content-Type":["application/json"]}`,
		CreatedAt:       time.Now(), CompletedAt: time.Now(),
	}
	require.NoError(t, svc.RecordRequestPayloadDetail(ctx, detail))
	for _, lookup := range []string{id, "client:" + detail.ClientRequestID} {
		got, err := svc.GetRequestPayloadDetail(ctx, lookup)
		require.NoError(t, err)
		require.NotNil(t, got)
		require.Greater(t, len(got.RequestBody), service.OpsRequestPayloadMaxBytes)
		var decoded map[string]any
		require.NoError(t, json.Unmarshal([]byte(got.RequestBody), &decoded))
		require.Equal(t, input, decoded["input"])
		require.NotContains(t, got.RequestBody+got.RequestHeaders+got.ResponseHeaders, "private-value")
		require.False(t, got.RequestTruncated)
	}
	// 同一请求重复落库只更新原有快照，不制造无限重复数据。
	detail.ResponseBody = `{"error":{"message":"final error"}}`
	require.NoError(t, svc.RecordRequestPayloadDetail(ctx, detail))
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM ops_request_details WHERE request_id = $1`, id).Scan(&count))
	require.Equal(t, 1, count)
	got, err := svc.GetRequestPayloadDetail(ctx, id)
	require.NoError(t, err)
	require.JSONEq(t, detail.ResponseBody, got.ResponseBody)
}

// 启动迁移重复执行不能改写记录、时间或已安装的扩展表结构。
func TestV16FollowupMigrationsAreRestartSafe(t *testing.T) {
	ctx := context.Background()
	readMigrations := func() map[string]string {
		rows, err := integrationDB.QueryContext(ctx, `SELECT filename, checksum, applied_at FROM schema_migrations ORDER BY filename`)
		require.NoError(t, err)
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var filename, checksum string
			var appliedAt time.Time
			require.NoError(t, rows.Scan(&filename, &checksum, &appliedAt))
			out[filename] = fmt.Sprintf("%s|%s", checksum, appliedAt.Format(time.RFC3339Nano))
		}
		require.NoError(t, rows.Err())
		return out
	}
	before := readMigrations()
	for _, name := range []string{"278_add_ops_request_details.sql", "279_ops_request_details_client_request_id_index.sql", "280_plugins.sql", "281_plugin_artifacts.sql"} {
		require.Contains(t, before, name)
	}
	require.NoError(t, ApplyMigrations(ctx, integrationDB))
	require.NoError(t, ApplyMigrations(ctx, integrationDB))
	require.Equal(t, before, readMigrations())
}

// 在真实 Redis 上验证命名空间隔离、TTL 和删除作用域，补足内存模拟器之外的持久接口验证。
func TestPluginKVRealRedisNamespaceAndTTL(t *testing.T) {
	ctx := context.Background()
	store := NewPluginKVStore(integrationRedis)
	pluginA, pluginB := "test-a-"+uuid.NewString(), "test-b-"+uuid.NewString()
	t.Cleanup(func() {
		for _, plugin := range []string{pluginA, pluginB} {
			require.NoError(t, store.Delete(ctx, plugin, "state", "shared"))
		}
	})
	require.NoError(t, store.Set(ctx, pluginA, "state", "shared", []byte("first"), time.Minute))
	require.NoError(t, store.Set(ctx, pluginB, "state", "shared", []byte("second"), 0))
	firstTTL, err := integrationRedis.TTL(ctx, store.(*pluginKVStore).fullKey(pluginA, "state", "shared")).Result()
	require.NoError(t, err)
	require.Greater(t, firstTTL, 50*time.Second)
	secondTTL, err := integrationRedis.TTL(ctx, store.(*pluginKVStore).fullKey(pluginB, "state", "shared")).Result()
	require.NoError(t, err)
	require.Equal(t, -time.Nanosecond, secondTTL)
	keys, err := store.List(ctx, pluginA, "state", "", 100)
	require.NoError(t, err)
	require.Equal(t, []string{"shared"}, keys)
	value, found, err := store.Get(ctx, pluginA, "state", "shared")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "first", string(value))
	require.NoError(t, store.Delete(ctx, pluginA, "state", "shared"))
	value, found, err = store.Get(ctx, pluginB, "state", "shared")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "second", string(value))
}
