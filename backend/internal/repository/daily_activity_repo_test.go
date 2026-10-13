package repository

import (
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestDailyActivityEntitlementsExactDecimal(t *testing.T) {
	for _, tc := range []struct {
		amount        string
		claimed, want int64
	}{
		{"0.3", 0, 3}, {"0.2999999999999999999999", 0, 2}, {"0.30", 2, 1}, {"0.1", 3, 0},
	} {
		state, err := dailyActivityState("consumption", service.DailyActivityRule{Threshold: .1, RewardMax: .1}, decimal.RequireFromString(tc.amount), tc.claimed)
		require.NoError(t, err)
		require.Equal(t, tc.want, state.AvailableCount)
	}
	state, err := dailyActivityState("daily_gift", service.DailyActivityRule{Threshold: .1, RewardMax: .1}, decimal.NewFromInt(1000), 0)
	require.NoError(t, err)
	require.Equal(t, int64(1), state.AvailableCount)
}
