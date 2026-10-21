//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	dbmigrations "github.com/TokenFlux/TokenRouter/migrations"
	"github.com/stretchr/testify/require"
)

// 用真实数据库验证写入、更新及鉴权专用投影，防止 ORM 字段存在但热路径漏选列。
func TestGroupMediaSubscriptionPriorityRepositoryRoundTrip(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	groups := newGroupRepositoryWithSQL(tx.Client(), tx)
	group := &service.Group{
		Name: "media-priority-persistence", Platform: service.PlatformGrok,
		RateMultiplier: 1, Status: service.StatusActive,
		ImageSubscriptionRatePriority: true, VideoSubscriptionRatePriority: true,
	}
	require.NoError(t, groups.Create(ctx, group))
	loaded, err := groups.GetByID(ctx, group.ID)
	require.NoError(t, err)
	require.True(t, loaded.ImageSubscriptionRatePriority)
	require.True(t, loaded.VideoSubscriptionRatePriority)

	user, err := tx.Client().User.Create().SetEmail("media-priority@test.invalid").SetPasswordHash("test-only").Save(ctx)
	require.NoError(t, err)
	keys := newAPIKeyRepositoryWithSQL(tx.Client(), tx)
	key := &service.APIKey{UserID: user.ID, GroupID: &group.ID, Key: "sk-media-priority-persistence", Name: "media", Status: service.StatusActive}
	require.NoError(t, keys.Create(ctx, key))
	auth, err := keys.GetByKeyForAuth(ctx, key.Key)
	require.NoError(t, err)
	require.True(t, auth.Group.ImageSubscriptionRatePriority)
	require.True(t, auth.Group.VideoSubscriptionRatePriority)

	loaded.ImageSubscriptionRatePriority, loaded.VideoSubscriptionRatePriority = false, false
	require.NoError(t, groups.Update(ctx, loaded))
	auth, err = keys.GetByKeyForAuth(ctx, key.Key)
	require.NoError(t, err)
	require.False(t, auth.Group.ImageSubscriptionRatePriority)
	require.False(t, auth.Group.VideoSubscriptionRatePriority)
}

// 迁移重放不能改写显式设置，开关变更必须同时失效普通及复合 Key。
func TestMigration314DefaultsAndInvalidatesMediaPriorityCaches(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)
	sql, err := dbmigrations.FS.ReadFile("314_group_media_subscription_rate_priority.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(sql))
	require.NoError(t, err)
	var groupID, userID int64
	var image, video bool
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO groups(name,platform) VALUES('migration314','grok') RETURNING id,image_subscription_rate_priority,video_subscription_rate_priority`).Scan(&groupID, &image, &video))
	require.False(t, image)
	require.False(t, video)
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO users(email,password_hash) VALUES('migration314@test.invalid','test-only') RETURNING id`).Scan(&userID))
	_, err = tx.ExecContext(ctx, `INSERT INTO api_keys(user_id,group_id,key,name) VALUES($1,$2,'sk-m314-ordinary','ordinary')`, userID, groupID)
	require.NoError(t, err)
	var compositeID int64
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO api_keys(user_id,key,name,is_composite) VALUES($1,'sk-m314-composite','composite',true) RETURNING id`, userID).Scan(&compositeID))
	_, err = tx.ExecContext(ctx, `INSERT INTO api_key_composite_groups(api_key_id,group_id,prefix,normalized_prefix) VALUES($1,$2,'grok','grok')`, compositeID, groupID)
	require.NoError(t, err)
	const filter = `cache_key IN (encode(sha256(convert_to('sk-m314-ordinary','UTF8')),'hex'),encode(sha256(convert_to('sk-m314-composite','UTF8')),'hex'))`
	for _, column := range []string{"image_subscription_rate_priority", "video_subscription_rate_priority"} {
		_, err = tx.ExecContext(ctx, "DELETE FROM auth_cache_invalidation_outbox WHERE "+filter)
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, "UPDATE groups SET "+column+"=true WHERE id=$1", groupID)
		require.NoError(t, err)
		var count int
		require.NoError(t, tx.QueryRowContext(ctx, "SELECT COUNT(DISTINCT cache_key) FROM auth_cache_invalidation_outbox WHERE "+filter).Scan(&count))
		require.Equal(t, 2, count)
	}
	_, err = tx.ExecContext(ctx, string(sql))
	require.NoError(t, err)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT image_subscription_rate_priority,video_subscription_rate_priority FROM groups WHERE id=$1`, groupID).Scan(&image, &video))
	require.True(t, image)
	require.True(t, video)
}
