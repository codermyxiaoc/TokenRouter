//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	dbmigrations "github.com/TokenFlux/TokenRouter/migrations"
	"github.com/stretchr/testify/require"
)

// 升级只新增说明列，旧零价及分组JSON保持原值；重复执行不得覆盖新增说明。
func TestMigration299ModelDetailsPreservesLegacy(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	_, err := tx.ExecContext(ctx, `ALTER TABLE channel_model_pricing DROP COLUMN model_details`)
	require.NoError(t, err)
	var channelID, pricingID, groupID int64
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO channels(name) VALUES('details-migration') RETURNING id`).Scan(&channelID))
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO channel_model_pricing(channel_id,models,billing_mode,per_request_price) VALUES($1,'["video-model"]','video',0) RETURNING id`, channelID).Scan(&pricingID))
	groupJSON := `[{"models":["video-model"],"billing_mode":"video","per_request_price":0}]`
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO groups(name,platform,model_pricing) VALUES('details-migration','video',$1) RETURNING id`, groupJSON).Scan(&groupID))
	migration, err := dbmigrations.FS.ReadFile("299_channel_model_details.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	var details, after string
	var price float64
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT model_details::text,per_request_price FROM channel_model_pricing WHERE id=$1`, pricingID).Scan(&details, &price))
	require.JSONEq(t, `{}`, details)
	require.Zero(t, price)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT model_pricing::text FROM groups WHERE id=$1`, groupID).Scan(&after))
	require.JSONEq(t, groupJSON, after)
	expected := `{"video-model":{"enabled":true,"description":"480p / 5秒"}}`
	_, err = tx.ExecContext(ctx, `UPDATE channel_model_pricing SET model_details=$1 WHERE id=$2`, expected, pricingID)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT model_details::text FROM channel_model_pricing WHERE id=$1`, pricingID).Scan(&details))
	require.JSONEq(t, expected, details)
}

// 完整渠道创建、更新、批量缓存读取、清空以及分组JSON持久化均保留独立说明。
func TestChannelModelDetailsRepositoryRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := &channelRepository{db: integrationDB}
	price := 0.3
	card := service.ChannelModelPricing{Platform: service.PlatformVideo, Models: []string{"a", "b"}, BillingMode: service.BillingModeVideo,
		VideoFallbackPrice: &price, ModelDetails: map[string]service.ModelDetails{"a": {Enabled: true, Description: "720p"}, "b": {Description: "未公开草稿"}}}
	channel := &service.Channel{Name: fmt.Sprintf("model-details-%d", time.Now().UnixNano()), Status: service.StatusActive,
		BillingModelSource: service.BillingModelSourceRequested, Features: "[]", ModelPricing: []service.ChannelModelPricing{card}}
	require.NoError(t, repo.Create(ctx, channel))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM channels WHERE id=$1`, channel.ID)
	})
	got, err := repo.GetByID(ctx, channel.ID)
	require.NoError(t, err)
	require.Len(t, got.ModelPricing, 1)
	require.Equal(t, card.ModelDetails, got.ModelPricing[0].ModelDetails)
	got.ModelPricing[0].ModelDetails["a"] = service.ModelDetails{Enabled: true, Description: "1080p"}
	require.NoError(t, repo.UpdateModelPricing(ctx, &got.ModelPricing[0]))
	batch, err := repo.batchLoadModelPricing(ctx, []int64{channel.ID})
	require.NoError(t, err)
	require.Equal(t, "1080p", batch[channel.ID][0].ModelDetails["a"].Description)
	require.Equal(t, price, *batch[channel.ID][0].VideoFallbackPrice)
	got.ModelPricing[0].ModelDetails = nil
	require.NoError(t, repo.ReplaceModelPricing(ctx, channel.ID, got.ModelPricing))
	after, err := repo.ListModelPricing(ctx, channel.ID)
	require.NoError(t, err)
	require.Empty(t, after[0].ModelDetails)
	require.Equal(t, price, *after[0].VideoFallbackPrice)

	groupRepo := NewGroupRepository(integrationEntClient, integrationDB)
	group := &service.Group{Name: fmt.Sprintf("model-details-%d", time.Now().UnixNano()), Platform: service.PlatformVideo, Status: service.StatusActive,
		RateMultiplier: 1, ModelPricing: []service.ChannelModelPricing{card}}
	require.NoError(t, groupRepo.Create(ctx, group))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM groups WHERE id=$1`, group.ID)
	})
	groupAfter, err := groupRepo.GetByID(ctx, group.ID)
	require.NoError(t, err)
	require.Equal(t, card.ModelDetails, groupAfter.ModelPricing[0].ModelDetails)
	encoded, err := json.Marshal(groupAfter.ModelPricing[0].ModelDetails)
	require.NoError(t, err)
	require.Contains(t, string(encoded), "未公开草稿")
}
