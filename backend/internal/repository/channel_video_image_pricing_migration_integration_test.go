//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"

	dbmigrations "github.com/TokenFlux/TokenRouter/migrations"
	"github.com/stretchr/testify/require"
)

// 新列对旧价卡默认禁用，重复执行迁移不得覆盖已设附加价或改写分组 JSON。
func TestMigration292VideoImageInputPricingPreservesLegacy(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	_, err := tx.ExecContext(ctx, `ALTER TABLE channel_model_pricing DROP COLUMN video_image_input_pricing`)
	require.NoError(t, err)
	var channelID, pricingID, groupID int64
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO channels(name) VALUES('video-image-migration') RETURNING id`).Scan(&channelID))
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO channel_model_pricing(channel_id,models,billing_mode,per_request_price,video_prices) VALUES($1,'["video-model"]','video',0,'[{"resolution":"768p","has_reference_video":false,"price":0.425}]') RETURNING id`, channelID).Scan(&pricingID))
	groupJSON := `[{"models":["video-model"],"billing_mode":"video","per_request_price":0}]`
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO groups(name,platform,model_pricing) VALUES('video-image-migration','video',$1) RETURNING id`, groupJSON).Scan(&groupID))
	migration, err := dbmigrations.FS.ReadFile("292_video_image_input_pricing.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	var config sql.NullString
	var oldPrice float64
	var matrix, after string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT video_image_input_pricing::text,per_request_price,video_prices::text FROM channel_model_pricing WHERE id=$1`, pricingID).Scan(&config, &oldPrice, &matrix))
	require.False(t, config.Valid)
	require.Zero(t, oldPrice)
	require.JSONEq(t, `[{"resolution":"768p","has_reference_video":false,"price":0.425}]`, matrix)
	_, err = tx.ExecContext(ctx, `UPDATE channel_model_pricing SET video_image_input_pricing='{"free_images":0,"price":0}' WHERE id=$1`, pricingID)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT video_image_input_pricing::text FROM channel_model_pricing WHERE id=$1`, pricingID).Scan(&config))
	require.JSONEq(t, `{"free_images":0,"price":0}`, config.String)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT model_pricing::text FROM groups WHERE id=$1`, groupID).Scan(&after))
	require.JSONEq(t, groupJSON, after)
}
