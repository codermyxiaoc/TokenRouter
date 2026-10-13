package service

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDailyActivityConfigValidation(t *testing.T) {
	require.NoError(t, ValidateDailyActivityConfig(DefaultDailyActivityConfig()))
	for _, value := range []float64{0, -1, .001, math.NaN(), math.Inf(1), 1000001} {
		cfg := DefaultDailyActivityConfig()
		cfg.Recharge.Threshold = value
		require.Error(t, ValidateDailyActivityConfig(cfg))
	}
	for _, value := range []float64{.09, .101, math.NaN(), math.Inf(-1), 1000001} {
		cfg := DefaultDailyActivityConfig()
		cfg.Consumption.RewardMax = value
		require.Error(t, ValidateDailyActivityConfig(cfg))
	}
	cfg := DefaultDailyActivityConfig()
	cfg.DailyGift.RewardMax = .1
	require.NoError(t, ValidateDailyActivityConfig(cfg))
}

func TestDailyActivityRandomCents(t *testing.T) {
	for i := 0; i < 1000; i++ {
		cents, err := DailyActivityRewardCents(.11)
		require.NoError(t, err)
		require.GreaterOrEqual(t, cents, int64(10))
		require.LessOrEqual(t, cents, int64(11))
	}
	cents, err := DailyActivityRewardCents(.1)
	require.NoError(t, err)
	require.Equal(t, int64(10), cents)
	_, err = DailyActivityRewardCents(0)
	require.Error(t, err)
}

func TestDailyActivityShanghaiMidnight(t *testing.T) {
	before := time.Date(2026, 10, 13, 15, 59, 59, 0, time.UTC)
	day, start, end := DailyActivityDay(before)
	require.Equal(t, "2026-10-13", day)
	require.Equal(t, time.Date(2026, 10, 12, 16, 0, 0, 0, time.UTC), start.UTC())
	next, nextStart, _ := DailyActivityDay(before.Add(time.Second))
	require.Equal(t, "2026-10-14", next)
	require.True(t, end.Equal(nextStart))
}

func TestDailyActivityClaimInput(t *testing.T) {
	input := DailyActivityClaimInput{ActivityType: "recharge", IdempotencyKey: "operation-1234567890"}
	require.NoError(t, ValidateDailyActivityClaim(input))
	input.ActivityType = "invite"
	require.Error(t, ValidateDailyActivityClaim(input))
	input.ActivityType = "consumption"
	input.IdempotencyKey = "x"
	require.Error(t, ValidateDailyActivityClaim(input))
}
