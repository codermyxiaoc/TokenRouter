//go:build unit

package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPlatformQuotaInitialEmptyConfigDoesNotTouchDatabase(t *testing.T) {
	repo := &userPlatformQuotaRepository{}
	require.NoError(t, repo.BulkInsertInitial(context.Background(), []UserPlatformQuotaRecord{{UserID: 1, Platform: "openai"}}))
	zero := 0.0
	recs := configuredQuotaRecords([]UserPlatformQuotaRecord{{Platform: "openai"}, {Platform: "minimax", DailyLimitUSD: &zero}, {Platform: "opencode_go", MonthlyLimitUSD: &zero}})
	require.Len(t, recs, 2)
	require.Equal(t, "minimax", recs[0].Platform)
	require.Equal(t, "opencode_go", recs[1].Platform)
	for _, rec := range recs {
		require.True(t, rec.HasAnyLimit())
	}
}
