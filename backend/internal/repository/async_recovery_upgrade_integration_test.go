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

// 升级测试使用独立容器，验证恢复表的新增不会替旧任务猜测费用、释放冻结或重置资金快照。
func TestAsyncRecoveryUpgradePreservesPendingAndSettledFinancialState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	container, err := tcpostgres.Run(ctx, selectDockerImage(ctx, postgresImageTag),
		tcpostgres.WithDatabase("async_recovery_upgrade"), tcpostgres.WithUsername("postgres"),
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
		if name >= "306_" {
			continue
		}
		body, readErr := dbmigrations.FS.ReadFile(name)
		require.NoError(t, readErr)
		history[name] = &fstest.MapFile{Data: body}
	}
	require.NoError(t, applyMigrationsFS(ctx, db, history))
	var userID, keyID, groupID int64
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance,frozen_balance)
		VALUES('async-upgrade@example.com','test',123.12345678,4.125) RETURNING id`).Scan(&userID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO groups(name,platform,rate_multiplier)
		VALUES('async-upgrade','grok',0.3) RETURNING id`).Scan(&groupID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO api_keys(user_id,key,name,group_id,quota_used)
		VALUES($1,'sk-async-upgrade-fixture','upgrade',$2,6.125) RETURNING id`, userID, groupID).Scan(&keyID))
	for _, state := range []string{"reserved", "captured", "released", "reconciliation"} {
		_, err = db.ExecContext(ctx, `INSERT INTO image_billing_reservations
			(request_id,api_key_id,user_id,fingerprint,state,snapshot,quote,result,expires_at)
			VALUES($1,$2,$3,'original-fingerprint',$4,'{"balance_hold_amount":0.125,"subscription_allocations":[]}',
			'{"model":"image","price":0.125}','{"balance_amount_usd":0.125}',NOW()+interval '1 hour')`,
			"upgrade-image-"+state, keyID, userID, state)
		require.NoError(t, err)
	}
	for _, status := range []string{"queued", "running", "succeeded", "result_lost", "cancelled"} {
		_, err = db.ExecContext(ctx, `INSERT INTO creative_runs
			(run_id,user_id,group_id,api_key_id,model,operation,prompt_hash,request_fingerprint,status,
			estimated_cost,hold_amount,actual_cost,balance_hold_amount,base_unit_price,subscription_rate_multiplier,balance_rate_multiplier)
			VALUES($1,$2,$3,$4,'image','generate','hash',$1,$5,0.72,0.72,0.12,0.12,0.12,6,1)`,
			"upgrade-creative-"+status, userID, groupID, keyID, status)
		require.NoError(t, err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO media_tasks(source,task_id,media_type,platform,model,status,user_id,api_key_id)
		VALUES('grok_video','upgrade-video','video','grok','grok-imagine-video','queued',$1,$2)`, userID, keyID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO usage_billing_dedup(request_id,api_key_id,request_fingerprint)
		VALUES('upgrade-captured',$1,'original-capture')`, keyID)
	require.NoError(t, err)

	// 比较完整旧行，包含 NULL、状态、金额精度、资金来源和时间，避免只检查总余额遗漏账本变化。
	excluded := map[string]string{
		"users": "{}", "api_keys": "{}", "media_tasks": "{billing_review_retry_at}",
		"groups": "{image_subscription_rate_priority,video_subscription_rate_priority}", "usage_billing_dedup": "{}",
		"image_billing_reservations": "{settlement_evidence,settlement_retry_at,settlement_attempts,settlement_completed_at}",
		// 后续迁移新增的可空列不属于旧记录内容，单独断言保持 NULL，而不是让整行比较误报。
		"creative_runs": "{usage_recorded_at,quota_platform,image_attempt_account_ids,image_execution_deadline_at}",
	}
	queries, before := map[string]string{}, map[string]string{}
	for table, columns := range excluded {
		expression := fmt.Sprintf("to_jsonb(t) - '%s'::text[]", columns)
		queries[table] = fmt.Sprintf("SELECT COALESCE(jsonb_agg(%s ORDER BY (%s)::text),'[]'::jsonb)::text FROM %s t", expression, expression, table)
		var snapshot string
		require.NoError(t, db.QueryRowContext(ctx, queries[table]).Scan(&snapshot))
		before[table] = snapshot
	}
	for round := 0; round < 2; round++ {
		require.NoError(t, ApplyMigrations(ctx, db))
		for table, query := range queries {
			var after string
			require.NoError(t, db.QueryRowContext(ctx, query).Scan(&after))
			require.JSONEq(t, before[table], after, "升级及重复启动必须保留 %s 原记录", table)
		}
	}
	var count int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM legacy_video_tasks`).Scan(&count))
	require.Zero(t, count, "迁移本身不得产生旧视频扣款任务")
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM image_billing_reservations
		WHERE settlement_evidence IS NOT NULL OR settlement_retry_at IS NOT NULL OR settlement_completed_at IS NOT NULL OR settlement_attempts<>0`).Scan(&count))
	require.Zero(t, count, "未知旧图片结果不得伪造结算依据")
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM creative_runs WHERE usage_recorded_at IS NOT NULL`).Scan(&count))
	require.Zero(t, count, "尚未验证日志投递的历史任务不能标记为已投递")
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM creative_runs WHERE quota_platform<>''`).Scan(&count))
	require.Zero(t, count, "历史任务平台未冻结时应保留兼容回退，不能由迁移猜测归属")
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM creative_runs
		WHERE image_execution_deadline_at IS NOT NULL OR jsonb_array_length(image_attempt_account_ids)<>0`).Scan(&count))
	require.Zero(t, count, "迁移不能为历史任务伪造图片发送记录或执行截止时间")
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE filename>='306_' AND filename<'310_'`).Scan(&count))
	require.Equal(t, 4, count)
}
