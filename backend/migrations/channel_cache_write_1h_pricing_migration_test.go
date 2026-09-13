package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChannelCacheWrite1hPricingMigration(t *testing.T) {
	content, err := FS.ReadFile("261_channel_cache_write_1h_pricing.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	for _, table := range []string{
		"channel_model_pricing",
		"channel_pricing_intervals",
		"channel_account_stats_model_pricing",
		"channel_account_stats_pricing_intervals",
	} {
		require.Contains(t, sql, "ALTER TABLE "+table)
	}
	require.Contains(t, sql, "cache_write_1h_price NUMERIC(20,12)")
	require.Contains(t, sql, "NULL 时沿用旧 cache_write_price 语义")
}
