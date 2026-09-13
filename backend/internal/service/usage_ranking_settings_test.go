package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type usageRankingSettingsRepoStub struct {
	SettingRepository
	values map[string]string
}

func (s *usageRankingSettingsRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := s.values[key]; ok {
			result[key] = value
		}
	}
	return result, nil
}

func TestGetUsageRankingSettingsUsesCompatibleDefaults(t *testing.T) {
	svc := NewSettingService(&usageRankingSettingsRepoStub{values: map[string]string{}}, nil)

	settings, err := svc.GetUsageRankingSettings(context.Background())

	require.NoError(t, err)
	require.True(t, settings.Enabled)
	require.Equal(t, UsageRankingSortByTotalTokens, settings.SortBy)
	require.True(t, settings.ShowTotalTokens)
	require.True(t, settings.ShowRequests)
	require.True(t, settings.ShowActualCost)
	require.Equal(t, DefaultUsageRankingLimit, settings.Limit)
}

func TestGetUsageRankingSettingsForcesSortMetricVisible(t *testing.T) {
	svc := NewSettingService(&usageRankingSettingsRepoStub{values: map[string]string{
		SettingKeyUsageRankingSortBy:          string(UsageRankingSortByActualCost),
		SettingKeyUsageRankingShowTotalTokens: "false",
		SettingKeyUsageRankingShowRequests:    "false",
		SettingKeyUsageRankingShowActualCost:  "false",
		SettingKeyUsageRankingLimit:           "999",
	}}, nil)

	settings, err := svc.GetUsageRankingSettings(context.Background())

	require.NoError(t, err)
	require.Equal(t, UsageRankingSortByActualCost, settings.SortBy)
	require.False(t, settings.ShowTotalTokens)
	require.False(t, settings.ShowRequests)
	require.True(t, settings.ShowActualCost)
	require.Equal(t, MaxUsageRankingLimit, settings.Limit)
}
