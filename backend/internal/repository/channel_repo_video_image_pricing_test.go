//go:build unit

package repository

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 旧行与显式 JSON null 都代表禁用，零价和零免费张数不能被读写过程省略。
func TestChannelVideoImageInputPricingRead(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  any
		want *service.VideoImageInputPricing
	}{
		{name: "legacy-null"},
		{name: "json-null", raw: `null`},
		{name: "charge-first-image", raw: `{"free_images":0,"price":0.25}`, want: videoImageInputPricing(0, .25)},
		{name: "explicit-free", raw: `{"free_images":2,"price":0}`, want: videoImageInputPricing(2, 0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, mock := newChannelPricingTimeRepo(t)
			mock.ExpectQuery(`SELECT .*video_image_input_pricing`).WithArgs(int64(7)).
				WillReturnRows(videoImageInputPricingRows(tc.raw))
			mock.ExpectQuery(`SELECT id, pricing_id, min_tokens`).WithArgs(sqlmock.AnyArg()).
				WillReturnRows(sqlmock.NewRows([]string{"id"}))
			entries, err := repo.ListModelPricing(context.Background(), 7)
			require.NoError(t, err)
			require.Len(t, entries, 1)
			require.Equal(t, tc.want, entries[0].VideoImageInputPricing)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// 批量渠道缓存加载走同一配置列，损坏 JSON 必须显式失败而非静默免费。
func TestChannelVideoImageInputPricingBatchAndMalformed(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		bad  bool
	}{
		{name: "batch", raw: `{"free_images":1,"price":0.3}`},
		{name: "malformed", raw: `{"free_images":"one","price":0.3}`, bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, mock := newChannelPricingTimeRepo(t)
			mock.ExpectQuery(`SELECT .*video_image_input_pricing`).WithArgs(sqlmock.AnyArg()).
				WillReturnRows(videoImageInputPricingRows(tc.raw))
			if !tc.bad {
				mock.ExpectQuery(`SELECT id, pricing_id, min_tokens`).WithArgs(sqlmock.AnyArg()).
					WillReturnRows(sqlmock.NewRows([]string{"id"}))
			}
			entries, err := repo.batchLoadModelPricing(context.Background(), []int64{7})
			if tc.bad {
				require.ErrorContains(t, err, "unmarshal video image input pricing")
			} else {
				require.NoError(t, err)
				require.Equal(t, videoImageInputPricing(1, .3), entries[7][0].VideoImageInputPricing)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// 新建和整组替换必须写入附加价；nil 则仍使用 SQL NULL。
func TestChannelVideoImageInputPricingCreateAndReplace(t *testing.T) {
	for _, tc := range []struct {
		name    string
		config  *service.VideoImageInputPricing
		json    driver.Value
		replace bool
	}{
		{name: "legacy-disabled"},
		{name: "explicit-zero", config: videoImageInputPricing(0, 0), json: `{"free_images":0,"price":0}`},
		{name: "replace", config: videoImageInputPricing(2, .25), json: `{"free_images":2,"price":0.25}`, replace: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, mock := newChannelPricingTimeRepo(t)
			pricing := &service.ChannelModelPricing{ChannelID: 7, Platform: "video", Models: []string{"model"}, BillingMode: service.BillingModeVideo, VideoImageInputPricing: tc.config}
			if tc.replace {
				mock.ExpectBegin()
				mock.ExpectExec(`DELETE FROM channel_model_pricing`).WithArgs(int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			mock.ExpectQuery(`INSERT INTO channel_model_pricing .*video_token_prepay\)`).
				WithArgs(int64(7), "video", []byte(`["model"]`), service.BillingModeVideo,
					nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, `{}`, `[]`, tc.json, nil, nil).
				WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(int64(11), time.Time{}, time.Time{}))
			if tc.replace {
				mock.ExpectCommit()
				require.NoError(t, repo.ReplaceModelPricing(context.Background(), 7, []service.ChannelModelPricing{*pricing}))
			} else {
				require.NoError(t, repo.CreateModelPricing(context.Background(), pricing))
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// 更新允许明确清空配置，并维持其他价卡列的位置与数值。
func TestChannelVideoImageInputPricingUpdateAndClear(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config *service.VideoImageInputPricing
		json   driver.Value
	}{
		{name: "update", config: videoImageInputPricing(3, .2), json: `{"free_images":3,"price":0.2}`},
		{name: "clear"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, mock := newChannelPricingTimeRepo(t)
			pricing := &service.ChannelModelPricing{ID: 11, Platform: "video", Models: []string{"model"}, BillingMode: service.BillingModeVideoToken, VideoImageInputPricing: tc.config}
			mock.ExpectExec(`UPDATE channel_model_pricing .*video_image_input_pricing = \$20.*WHERE id = \$23`).
				WithArgs([]byte(`["model"]`), service.BillingModeVideoToken,
					nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "video", `{}`, `[]`, tc.json, nil, nil, int64(11)).
				WillReturnResult(sqlmock.NewResult(0, 1))
			require.NoError(t, repo.UpdateModelPricing(context.Background(), pricing))
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func videoImageInputPricing(free int, price float64) *service.VideoImageInputPricing {
	return &service.VideoImageInputPricing{FreeImages: free, Price: &price}
}

func videoImageInputPricingRows(raw any) *sqlmock.Rows {
	return sqlmock.NewRows(channelPricingTimeColumns).AddRow(
		int64(11), int64(7), "video", `["model"]`, service.BillingModeVideo,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, time.Time{}, time.Time{}, `{}`, `[]`, raw, nil, nil)
}
