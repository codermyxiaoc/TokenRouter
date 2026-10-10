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

// 旧行与 JSON null 关闭新合同，显式零价不能被误当作未设置。
func TestChannelVideoFallbackPrepayReadAndBatch(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fallback any
		prepay   any
		batch    bool
		bad      bool
	}{
		{name: "legacy-null"},
		{name: "json-null", prepay: `null`, batch: true},
		{name: "explicit-zero", fallback: 0.0, prepay: `{"price_per_second":0}`},
		{name: "batch-priced", fallback: 2.0, prepay: `{"price_per_second":0.3}`, batch: true},
		{name: "malformed", prepay: `{"price_per_second":"bad"}`, bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, mock := newChannelPricingTimeRepo(t)
			query := mock.ExpectQuery(`SELECT .*video_fallback_price, video_token_prepay`)
			if tc.batch {
				query.WithArgs(sqlmock.AnyArg())
			} else {
				query.WithArgs(int64(7))
			}
			query.WillReturnRows(sqlmock.NewRows(channelPricingTimeColumns).AddRow(
				int64(11), int64(7), "video", `["model"]`, service.BillingModeVideoToken,
				nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, time.Time{}, time.Time{}, `{}`, `[]`, nil, tc.fallback, tc.prepay, `{}`))
			if !tc.bad {
				mock.ExpectQuery(`SELECT id, pricing_id, min_tokens`).WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"id"}))
			}
			var entries []service.ChannelModelPricing
			var err error
			if tc.batch {
				var result map[int64][]service.ChannelModelPricing
				result, err = repo.batchLoadModelPricing(context.Background(), []int64{7})
				entries = result[7]
			} else {
				entries, err = repo.ListModelPricing(context.Background(), 7)
			}
			if tc.bad {
				require.ErrorContains(t, err, "unmarshal video token prepay")
			} else {
				require.NoError(t, err)
				require.Len(t, entries, 1)
				if tc.fallback == nil {
					require.Nil(t, entries[0].VideoFallbackPrice)
				} else {
					require.Equal(t, tc.fallback, *entries[0].VideoFallbackPrice)
				}
				if tc.prepay == nil || tc.prepay == `null` {
					require.Nil(t, entries[0].VideoTokenPrepay)
				} else {
					encoded, err := marshalVideoTokenPrepay(entries[0].VideoTokenPrepay)
					require.NoError(t, err)
					require.JSONEq(t, tc.prepay.(string), encoded.(string))
				}
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// 新建、更新及整组替换都保存这两个字段，明确清空会写入 SQL NULL。
func TestChannelVideoFallbackPrepayWriteAndClear(t *testing.T) {
	for _, action := range []string{"create", "update", "replace"} {
		for _, enabled := range []bool{false, true} {
			t.Run(action+map[bool]string{false: "-disabled", true: "-zero"}[enabled], func(t *testing.T) {
				repo, mock := newChannelPricingTimeRepo(t)
				pricing := &service.ChannelModelPricing{ID: 11, ChannelID: 7, Platform: "video", Models: []string{"model"}, BillingMode: service.BillingModeVideoToken}
				var fallback, prepay driver.Value
				if enabled {
					zero := 0.0
					pricing.VideoFallbackPrice = &zero
					pricing.VideoTokenPrepay = &service.VideoTokenPrepayConfig{PricePerSecond: &zero}
					fallback, prepay = 0.0, `{"price_per_second":0}`
				}
				if action == "update" {
					mock.ExpectExec(`UPDATE channel_model_pricing .*video_fallback_price = \$21, video_token_prepay = \$22.*WHERE id = \$24`).
						WithArgs([]byte(`["model"]`), service.BillingModeVideoToken, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "video", `{}`, `[]`, nil, fallback, prepay, `{}`, int64(11)).
						WillReturnResult(sqlmock.NewResult(0, 1))
					require.NoError(t, repo.UpdateModelPricing(context.Background(), pricing))
				} else {
					if action == "replace" {
						mock.ExpectBegin()
						mock.ExpectExec(`DELETE FROM channel_model_pricing`).WithArgs(int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
					}
					mock.ExpectQuery(`INSERT INTO channel_model_pricing .*video_fallback_price, video_token_prepay, model_details\)`).
						WithArgs(int64(7), "video", []byte(`["model"]`), service.BillingModeVideoToken, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, `{}`, `[]`, nil, fallback, prepay, `{}`).
						WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(int64(11), time.Time{}, time.Time{}))
					if action == "replace" {
						mock.ExpectCommit()
						require.NoError(t, repo.ReplaceModelPricing(context.Background(), 7, []service.ChannelModelPricing{*pricing}))
					} else {
						require.NoError(t, repo.CreateModelPricing(context.Background(), pricing))
					}
				}
				require.NoError(t, mock.ExpectationsWereMet())
			})
		}
	}
}
