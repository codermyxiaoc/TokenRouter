package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

func TestHydrateUsageVideoBillingBatchesAndSkipsLegacyRows(t *testing.T) {
	db, mock := newSQLMock(t)
	logs := []*service.UsageLog{
		{UserID: 7, BillingUserID: 99, APIKeyID: 10, VideoCount: 1, RequestID: "video_capture:vid_first", ImageInputCost: .3},
		{UserID: 7, APIKeyID: 10, VideoCount: 1, RequestID: "video_capture:vid_first", ImageInputCost: .3},
		{UserID: 8, APIKeyID: 11, VideoCount: 1, RequestID: "video_capture:vid_missing"},
		{UserID: 8, APIKeyID: 11, VideoCount: 1, RequestID: "video_capture:vid_invalid"},
		{UserID: 7, APIKeyID: 10, VideoCount: 1, RequestID: "grok_video:old"}, nil,
	}
	// 同一页按任务、行为用户、原 Key 去重；团队 owner 不会替代行为用户。
	mock.ExpectQuery(`SELECT source.task_id, source.user_id, source.api_key_id,`).
		WithArgs("vid_first", int64(7), int64(10), "vid_missing", int64(8), int64(11), "vid_invalid", int64(8), int64(11)).
		WillReturnRows(sqlmock.NewRows([]string{"task_id", "user_id", "api_key_id", "quote", "metadata"}).
			AddRow("vid_first", 7, 10, `{"mode":"video","unit":"second","unit_price":0.425,"resolution":"768p","reference_image_count":7,"image_input_pricing":{"free_images":5,"price":0.15}}`, `{"duration_seconds":2.5}`).
			AddRow("vid_missing", 8, 11, nil, nil).
			AddRow("vid_invalid", 8, 11, `"bad quote"`, `{}`))
	require.NoError(t, hydrateUsageVideoBilling(context.Background(), db, logs))
	for _, log := range logs[:2] {
		require.NotNil(t, log.VideoBilling)
		require.Equal(t, 2.5, log.VideoBilling.DurationSeconds)
		require.Equal(t, 7, *log.VideoBilling.ReferenceImageCount)
		require.Equal(t, .3, *log.VideoBilling.ReferenceImageCost)
	}
	for _, log := range logs[2:5] {
		require.Nil(t, log.VideoBilling)
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestHydrateUsageVideoBillingOrdinaryRowsNeverQuery(t *testing.T) {
	db, mock := newSQLMock(t)
	logs := []*service.UsageLog{
		{UserID: 7, APIKeyID: 10, RequestID: "normal"},
		{UserID: 7, APIKeyID: 10, VideoCount: 1, RequestID: "old-grok"},
		{UserID: 7, APIKeyID: 10, VideoCount: 1, RequestID: "video_capture:"},
		{APIKeyID: 10, VideoCount: 1, RequestID: "video_capture:vid_no_user"},
		{UserID: 7, VideoCount: 1, RequestID: "video_capture:vid_no_key"}, nil,
	}
	require.NoError(t, hydrateUsageVideoBilling(context.Background(), db, logs))
	require.NoError(t, mock.ExpectationsWereMet())
}
