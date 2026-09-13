package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChannelPricingTierMultipliersMigration(t *testing.T) {
	content, err := FS.ReadFile("251_channel_pricing_tier_multipliers.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	for _, column := range []string{
		"fast_multiplier NUMERIC(20,8)",
		"flex_multiplier NUMERIC(20,8)",
		"input_multiplier NUMERIC(20,8)",
		"output_multiplier NUMERIC(20,8)",
		"cache_write_multiplier NUMERIC(20,8)",
		"cache_read_multiplier NUMERIC(20,8)",
	} {
		require.Contains(t, sql, column)
	}
	for _, name := range []string{
		"channel_model_pricing_fast_multiplier_positive",
		"channel_model_pricing_flex_multiplier_positive",
		"channel_pricing_intervals_input_multiplier_positive",
		"channel_pricing_intervals_output_multiplier_positive",
		"channel_pricing_intervals_cache_write_multiplier_positive",
		"channel_pricing_intervals_cache_read_multiplier_positive",
	} {
		require.Contains(t, sql, name)
	}
}
