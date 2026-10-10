//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

func TestChannelVideoPricingReadPreservesZeroAndReferenceCondition(t *testing.T) {
	repo, mock := newChannelPricingTimeRepo(t)
	videoJSON := `[{"resolution":"1080p","has_reference_video":true,"price":46},{"resolution":"1080p","has_reference_video":false,"price":0}]`
	mock.ExpectQuery(`SELECT .*reasoning_effort_multipliers, video_prices`).WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows(channelPricingTimeColumns).AddRow(
			int64(11), int64(7), "video", `["video-model"]`, service.BillingModeVideoToken,
			nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, time.Time{}, time.Time{}, `{}`, videoJSON, nil, nil, nil, `{}`))
	mock.ExpectQuery(`SELECT id, pricing_id, min_tokens`).WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	entries, err := repo.ListModelPricing(context.Background(), 7)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Len(t, entries[0].VideoPrices, 2)
	require.True(t, entries[0].VideoPrices[0].HasReferenceVideo)
	require.Equal(t, 46.0, *entries[0].VideoPrices[0].Price)
	require.False(t, entries[0].VideoPrices[1].HasReferenceVideo)
	require.NotNil(t, entries[0].VideoPrices[1].Price)
	require.Zero(t, *entries[0].VideoPrices[1].Price)
	encoded, err := marshalVideoPrices(entries[0].VideoPrices)
	require.NoError(t, err)
	// 新写入省略无条件行的历史 false 标记，显式零价和旧 true 行均保留。
	require.JSONEq(t, `[{"resolution":"1080p","has_reference_video":true,"price":46},{"resolution":"1080p","price":0}]`, encoded)
	require.NoError(t, mock.ExpectationsWereMet())
}
