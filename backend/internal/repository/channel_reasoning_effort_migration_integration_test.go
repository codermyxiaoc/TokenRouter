//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/service"
	dbmigrations "github.com/TokenFlux/TokenRouter/migrations"
	"github.com/stretchr/testify/require"
)

// 升级只增加可选字段，旧 Max、免费价和分组 JSON 必须原样保留，重放不能覆盖新配置。
func TestMigration287ReasoningEffortPreservesLegacy(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	_, err := tx.ExecContext(ctx, `ALTER TABLE channel_model_pricing DROP COLUMN reasoning_effort_multipliers`)
	require.NoError(t, err)
	var channelID, pricingID, groupID int64
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO channels (name) VALUES ('reasoning-compat-test') RETURNING id`).Scan(&channelID))
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO channel_model_pricing (channel_id, models, input_price, max_reasoning_effort_multiplier) VALUES ($1, '["claude-fable-5-1"]', 0, 2.5) RETURNING id`, channelID).Scan(&pricingID))
	groupJSON := `[{"models":["claude-fable-5-1"],"max_reasoning_effort_multiplier":2.5,"input_price":0}]`
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO groups (name, platform, model_pricing) VALUES ('reasoning-group-test', 'anthropic', $1) RETURNING id`, groupJSON).Scan(&groupID))
	migration, err := dbmigrations.FS.ReadFile("287_channel_reasoning_effort_multipliers.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	var legacy, price float64
	var multipliers, groupAfter string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT max_reasoning_effort_multiplier, input_price, reasoning_effort_multipliers::text FROM channel_model_pricing WHERE id=$1`, pricingID).Scan(&legacy, &price, &multipliers))
	require.Equal(t, 2.5, legacy)
	require.Zero(t, price)
	require.JSONEq(t, `{}`, multipliers)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT model_pricing::text FROM groups WHERE id=$1`, groupID).Scan(&groupAfter))
	require.JSONEq(t, groupJSON, groupAfter)
	_, err = tx.ExecContext(ctx, `UPDATE channel_model_pricing SET reasoning_effort_multipliers='{"high":1.5}' WHERE id=$1`, pricingID)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT reasoning_effort_multipliers::text FROM channel_model_pricing WHERE id=$1`, pricingID).Scan(&multipliers))
	require.JSONEq(t, `{"high":1.5}`, multipliers)
}

// 对迁移前后真实 SQL 价卡分别结算，防止列兼容但实际有效价变化的升级回归。
func TestMigration287EffectiveLegacyPrices(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	_, err := tx.ExecContext(ctx, `ALTER TABLE channel_model_pricing DROP COLUMN reasoning_effort_multipliers`)
	require.NoError(t, err)
	var channelID int64
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO channels (name) VALUES ('reasoning-effective-compat') RETURNING id`).Scan(&channelID))
	legacy := 2.5
	cases := []struct {
		name            string
		legacy          *float64
		price, expected float64
		id              int64
		before          *service.CostBreakdown
	}{
		{name: "fable-default", price: .01, expected: 3},
		{name: "legacy-max", legacy: &legacy, price: .01, expected: 2.5},
		{name: "explicit-free", legacy: &legacy, price: 0, expected: 0},
	}
	bs := service.NewBillingService(&config.Config{}, nil)
	resolver := service.NewModelPricingResolver(nil, bs)
	calculate := func(price float64, old *float64, levels map[string]float64) *service.CostBreakdown {
		cost, costErr := bs.CalculateCostUnified(service.CostInput{Ctx: ctx, Model: "claude-fable-5-1", ReasoningEffort: "max",
			Tokens: service.UsageTokens{InputTokens: 100}, RateMultiplier: .4, Resolver: resolver,
			Resolved: &service.ResolvedPricing{Mode: service.BillingModeToken, BasePricing: &service.ModelPricing{
				InputPricePerToken: price, MaxReasoningEffortMultiplier: old, ReasoningEffortMultipliers: levels,
			}},
		})
		require.NoError(t, costErr)
		return cost
	}
	for i := range cases {
		tc := &cases[i]
		require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO channel_model_pricing (channel_id, models, input_price, max_reasoning_effort_multiplier) VALUES ($1,'["claude-fable-5-1"]',$2,$3) RETURNING id`, channelID, tc.price, tc.legacy).Scan(&tc.id))
		var price float64
		var old *float64
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT input_price, max_reasoning_effort_multiplier FROM channel_model_pricing WHERE id=$1`, tc.id).Scan(&price, &old))
		tc.before = calculate(price, old, nil)
		require.InDelta(t, tc.expected, tc.before.TotalCost, 1e-12)
	}
	migration, err := dbmigrations.FS.ReadFile("287_channel_reasoning_effort_multipliers.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var price float64
			var old *float64
			var raw []byte
			require.NoError(t, tx.QueryRowContext(ctx, `SELECT input_price, max_reasoning_effort_multiplier, reasoning_effort_multipliers FROM channel_model_pricing WHERE id=$1`, tc.id).Scan(&price, &old, &raw))
			var levels map[string]float64
			require.NoError(t, json.Unmarshal(raw, &levels))
			require.Empty(t, levels)
			after := calculate(price, old, levels)
			require.Equal(t, tc.before.TotalCost, after.TotalCost)
			require.Equal(t, tc.before.ActualCost, after.ActualCost)
			require.InDelta(t, tc.expected*.4, after.ActualCost, 1e-12)
		})
	}
}
