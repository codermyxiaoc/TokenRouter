//go:build integration

package repository

import (
	"context"
	"fmt"
	"io/fs"
	"testing"
	"testing/fstest"
	"time"

	dbmigrations "github.com/TokenFlux/TokenRouter/migrations"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// 从 v2.7 的 288 迁移基线建立旧账本，再运行完整迁移；专用容器不连接共享或用户数据库。
func TestVideoV27UpgradePreservesFinancialState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	container, err := tcpostgres.Run(ctx, selectDockerImage(ctx, postgresImageTag),
		tcpostgres.WithDatabase("video_v27_upgrade_test"), tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	require.NoError(t, err)
	db, err := openSQLWithRetry(ctx, dsn, 30*time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	names, err := fs.Glob(dbmigrations.FS, "*.sql")
	require.NoError(t, err)
	history := fstest.MapFS{}
	for _, name := range names {
		if name >= "289_" {
			continue
		}
		body, err := dbmigrations.FS.ReadFile(name)
		require.NoError(t, err)
		history[name] = &fstest.MapFile{Data: body}
	}
	require.NoError(t, applyMigrationsFS(ctx, db, history))
	var baseline string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT max(filename) FROM schema_migrations`).Scan(&baseline))
	require.Contains(t, baseline, "288_")

	var userID, groupID, keyID, planID, channelID int64
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance,frozen_balance)
		VALUES('video-v27-upgrade@example.com','test-hash',123.12345678,4.125) RETURNING id`).Scan(&userID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO groups(name,platform,rate_multiplier,video_price_720p,model_pricing)
		VALUES('video-v27-upgrade','grok',0.38,0.12345678,'[{"models":["legacy-video"],"billing_mode":"video","per_request_price":0.123456789012}]') RETURNING id`).Scan(&groupID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO api_keys(user_id,key,name,group_id,quota,quota_used,usage_5h,usage_1d,usage_7d)
		VALUES($1,'sk-v27-upgrade-fixture','upgrade',$2,50,6.125,1.25,2.5,6.125) RETURNING id`, userID, groupID).Scan(&keyID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO subscription_plans(name,price,validity_days,daily_limit_usd,weekly_limit_usd,monthly_limit_usd)
		VALUES('upgrade-plan',19.99,90,100,500,1000) RETURNING id`).Scan(&planID))
	_, err = db.ExecContext(ctx, `INSERT INTO subscription_plan_groups(plan_id,group_id,rate_multiplier) VALUES($1,$2,0.25)`, planID, groupID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO user_subscriptions(user_id,plan_id,starts_at,expires_at,status,
		daily_limit_usd,weekly_limit_usd,monthly_limit_usd,daily_usage_usd,weekly_usage_usd,monthly_usage_usd,
		daily_window_start,weekly_window_start,monthly_window_start,daily_reset_count,weekly_reset_count,monthly_reset_count,reset_counted_at)
		VALUES($1,$2,'2026-09-01T09:32:10Z','2026-12-01T09:32:10Z','active',100,500,1000,1.123456789,12.25,89.5,
		'2026-10-02T00:00:00Z','2026-09-29T09:32:10Z','2026-10-01T09:32:10Z',31,4,1,'2026-10-02T09:32:10Z')`, userID, planID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO user_subscriptions(user_id,plan_id,starts_at,expires_at,status,daily_limit_usd,daily_usage_usd)
		VALUES($1,$2,'2026-12-01T09:32:10Z','2027-03-01T09:32:10Z','pending',100,0)`, userID, planID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO user_platform_quotas(user_id,platform,daily_limit_usd,weekly_limit_usd,monthly_limit_usd,
		daily_usage_usd,weekly_usage_usd,monthly_usage_usd,daily_window_start,weekly_window_start,monthly_window_start)
		VALUES($1,'openai',0,NULL,1000,1.123456789,12.25,89.5,'2026-10-02T00:00:00Z','2026-09-28T00:00:00Z','2026-09-17T11:22:33Z')`, userID)
	require.NoError(t, err)
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO channels(name) VALUES('video-v27-upgrade') RETURNING id`).Scan(&channelID))
	_, err = db.ExecContext(ctx, `INSERT INTO channel_model_pricing(channel_id,models,billing_mode,input_price,output_price)
		VALUES($1,'["legacy-video"]','video',0.00000012,0)`, channelID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO media_tasks(source,task_id,media_type,platform,model,status,user_id,api_key_id)
		VALUES('grok_video','legacy-grok','video','grok','legacy-video','completed',$1,$2),
		('seedance_video','legacy-seedance','video','openai','legacy-video','completed',$1,$2)`, userID, keyID)
	require.NoError(t, err)

	// 对比完整旧行而非少数字段，连有效期、NULL 窗口、重置水位和更新时间一起保护。
	queries := map[string]string{}
	for _, table := range []string{"users", "api_keys", "groups", "subscription_plans", "subscription_plan_groups", "user_subscriptions", "user_platform_quotas", "channel_model_pricing", "media_tasks"} {
		excluded := "{}"
		if table == "user_subscriptions" || table == "user_platform_quotas" {
			excluded = "{daily_reset_generation,weekly_reset_generation,monthly_reset_generation}"
		} else if table == "channel_model_pricing" {
			excluded = "{video_prices,video_image_input_pricing,video_fallback_price,video_token_prepay}"
		}
		expression := fmt.Sprintf("to_jsonb(t) - '%s'::text[]", excluded)
		queries[table] = fmt.Sprintf("SELECT COALESCE(jsonb_agg(%s ORDER BY (%s)::text),'[]'::jsonb)::text FROM %s t", expression, expression, table)
	}
	before := map[string]string{}
	for table, query := range queries {
		var snapshot string
		require.NoError(t, db.QueryRowContext(ctx, query).Scan(&snapshot))
		before[table] = snapshot
	}
	// 再次启动使用完整迁移及历史校验和；重放仍不得重置旧资金和定价。
	for round := 1; round <= 2; round++ {
		require.NoError(t, ApplyMigrations(ctx, db))
		for table, query := range queries {
			var after string
			require.NoError(t, db.QueryRowContext(ctx, query).Scan(&after))
			require.JSONEq(t, before[table], after, "第 %d 次完整迁移改变了 %s 旧行", round, table)
		}
	}
	var migrated, tasks, videoQuotas, invalidDefaults int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE filename >= '289_' AND filename < '294_'`).Scan(&migrated))
	require.Equal(t, 5, migrated)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM video_tasks`).Scan(&tasks))
	require.Zero(t, tasks, "旧媒体任务不能迁移成新的待扣费任务")
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM user_platform_quotas WHERE platform='video'`).Scan(&videoQuotas))
	require.Zero(t, videoQuotas, "升级不能替旧用户创建 Video 额度")
	for _, table := range []string{"user_subscriptions", "user_platform_quotas"} {
		require.NoError(t, db.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*) FROM %s WHERE daily_reset_generation<>0 OR weekly_reset_generation<>0 OR monthly_reset_generation<>0`, table)).Scan(&invalidDefaults))
		require.Zero(t, invalidDefaults)
	}
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM channel_model_pricing WHERE video_prices<>'[]'::jsonb OR video_image_input_pricing IS NOT NULL OR video_fallback_price IS NOT NULL OR video_token_prepay IS NOT NULL`).Scan(&invalidDefaults))
	require.Zero(t, invalidDefaults)
}
