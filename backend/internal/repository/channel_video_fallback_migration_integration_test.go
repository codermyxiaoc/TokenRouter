//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"

	dbmigrations "github.com/TokenFlux/TokenRouter/migrations"
	"github.com/stretchr/testify/require"
)

// 迁移只增加可空列，保留旧矩阵和分组 JSON；重复执行不能覆盖明确配置的零价。
func TestMigration293VideoFallbackPrepayPreservesLegacy(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	_, err := tx.ExecContext(ctx, `ALTER TABLE channel_model_pricing DROP COLUMN video_fallback_price, DROP COLUMN video_token_prepay`)
	require.NoError(t, err)
	var channelID, pricingID, groupID int64
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO channels(name) VALUES('video-fallback-migration') RETURNING id`).Scan(&channelID))
	matrix := `[{"resolution":"540p","has_reference_video":false,"price":0.123456789012}]`
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO channel_model_pricing(channel_id,models,billing_mode,video_prices) VALUES($1,'["video-model"]','video_token',$2) RETURNING id`, channelID, matrix).Scan(&pricingID))
	groupJSON := `[{"models":["video-model"],"billing_mode":"video","per_request_price":0}]`
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO groups(name,platform,model_pricing) VALUES('video-fallback-migration','video',$1) RETURNING id`, groupJSON).Scan(&groupID))
	migration, err := dbmigrations.FS.ReadFile("293_video_fallback_and_token_prepay.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	var fallback sql.NullFloat64
	var prepay sql.NullString
	var after string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT video_fallback_price, video_token_prepay::text, video_prices::text FROM channel_model_pricing WHERE id=$1`, pricingID).Scan(&fallback, &prepay, &after))
	require.False(t, fallback.Valid)
	require.False(t, prepay.Valid)
	require.JSONEq(t, matrix, after)
	_, err = tx.ExecContext(ctx, `UPDATE channel_model_pricing SET video_fallback_price=0,video_token_prepay='{"price_per_second":0}' WHERE id=$1`, pricingID)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT video_fallback_price, video_token_prepay::text FROM channel_model_pricing WHERE id=$1`, pricingID).Scan(&fallback, &prepay))
	require.True(t, fallback.Valid)
	require.Zero(t, fallback.Float64)
	require.JSONEq(t, `{"price_per_second":0}`, prepay.String)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT model_pricing::text FROM groups WHERE id=$1`, groupID).Scan(&after))
	require.JSONEq(t, groupJSON, after)
}
