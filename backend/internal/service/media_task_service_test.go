package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// 列表和详情都从冻结任务读取费用；测试仓储不查询上游，也没有扣费接口。
type videoImageFeeMediaRepo struct {
	MediaTaskRepository
	record *VideoTaskRecord
	source string
	reads  int
}

func (r *videoImageFeeMediaRepo) List(context.Context, MediaTaskActor, MediaTaskFilter) (*MediaTaskList, error) {
	return &MediaTaskList{Items: []MediaTask{{ID: 1, Source: r.source, TaskID: "task", UserID: 1, APIKeyID: 2}}}, nil
}

func (r *videoImageFeeMediaRepo) Get(context.Context, MediaTaskActor, int64) (*MediaTask, error) {
	return &MediaTask{ID: 1, Source: r.source, TaskID: "task", UserID: 1, APIKeyID: 2}, nil
}

func (r *videoImageFeeMediaRepo) GetVideoTask(context.Context, *MediaTask) (*VideoTaskRecord, error) {
	r.reads++
	return cloneVideoLifecycleTask(r.record), nil
}

func TestMediaTaskVideoImageFeeListAndDetailUseFrozenInputCount(t *testing.T) {
	count, outputCount, price := 4, 999, .25
	record := &VideoTaskRecord{BillingStatus: "settled", Metadata: VideoRequestMetadata{ReferenceImageCount: &outputCount},
		Quote: &VideoPriceQuote{Version: 1, Mode: BillingModeVideoToken, UnitPrice: 1, ReferenceImageCount: &count, ImageInputPricing: &VideoImageInputPricing{FreeImages: 2, Price: &price}},
		Hold:  BatchImageBalanceHoldCommand{HoldAmount: 2.5, VideoFixedAmountUSD: .5}, BillingResult: &BatchImageBalanceHoldResult{ActualAmountUSD: .9}}
	repo := &videoImageFeeMediaRepo{record: record, source: "video"}
	s := NewMediaTaskService(repo)
	for _, admin := range []bool{false, true} {
		actor := MediaTaskActor{UserID: 1, IsAdmin: admin}
		list, err := s.List(context.Background(), actor, MediaTaskFilter{})
		require.NoError(t, err)
		detail, err := s.Get(context.Background(), actor, 1)
		require.NoError(t, err)
		for _, task := range []*MediaTask{&list.Items[0], detail} {
			bill := task.VideoBilling
			require.NotNil(t, bill)
			require.Equal(t, 4, *bill.ReferenceImageCount)
			require.Equal(t, 2, *bill.ReferenceImageFreeCount)
			require.Equal(t, 2, *bill.BillableReferenceImageCount)
			require.Equal(t, .25, *bill.ReferenceImageUnitPrice)
			require.Equal(t, .5, *bill.ReferenceImageCost)
			require.Equal(t, .9, *bill.ActualAmount)
			require.Equal(t, .9, *task.ActualCost)
			require.Equal(t, 2.5, bill.ReservedAmount)
			*bill.ReferenceImageUnitPrice = 99
			require.Equal(t, .25, *record.Quote.ImageInputPricing.Price, "响应不能污染持久价卡")
		}
	}
	require.Equal(t, 4, repo.reads)
}

func TestMediaTaskVideoImageFeeActualAmountOnlyAppearsAfterSettlement(t *testing.T) {
	count, price := 4, .25
	for _, status := range []string{"pending", "reserved", "reconciliation", "released", "settled_without_result", "settled"} {
		t.Run(status, func(t *testing.T) {
			record := &VideoTaskRecord{BillingStatus: status, Quote: &VideoPriceQuote{Version: 1, Mode: BillingModeVideoToken, UnitPrice: 1,
				ReferenceImageCount: &count, ImageInputPricing: &VideoImageInputPricing{FreeImages: 2, Price: &price}}, Hold: BatchImageBalanceHoldCommand{VideoDeferredBilling: true, VideoFixedAmountUSD: .5}}
			if status == "settled_without_result" {
				record.BillingStatus = "settled"
			}
			if status == "settled" || status == "released" {
				record.BillingResult = &BatchImageBalanceHoldResult{ActualAmountUSD: .9}
			}
			s := NewMediaTaskService(&videoImageFeeMediaRepo{record: record, source: "video"})
			task, err := s.Get(context.Background(), MediaTaskActor{UserID: 1}, 1)
			require.NoError(t, err)
			require.True(t, task.VideoBilling.DeferredBilling)
			require.Equal(t, 4, *task.VideoBilling.ReferenceImageCount)
			require.Zero(t, task.VideoBilling.ReservedAmount)
			raw, err := json.Marshal(task.VideoBilling)
			require.NoError(t, err)
			if status == "settled" {
				require.Equal(t, .5, *task.VideoBilling.ReferenceImageCost)
				require.Equal(t, .9, *task.ActualCost)
				require.Contains(t, string(raw), `"reference_image_cost":0.5`)
			} else {
				require.Nil(t, task.VideoBilling.ReferenceImageCost)
				require.Nil(t, task.VideoBilling.ActualAmount)
				require.Nil(t, task.ActualCost)
				require.NotContains(t, string(raw), `"reference_image_cost"`)
			}
		})
	}
}

func TestMediaTaskVideoImageFeePreservesZeroAndOmitsLegacyDetails(t *testing.T) {
	zeroCount, zeroPrice := 0, 0.0
	record := &VideoTaskRecord{BillingStatus: "settled", Quote: &VideoPriceQuote{Version: 1, Mode: BillingModeVideoToken, ReferenceImageCount: &zeroCount,
		ImageInputPricing: &VideoImageInputPricing{FreeImages: 0, Price: &zeroPrice}}, BillingResult: &BatchImageBalanceHoldResult{}}
	repo := &videoImageFeeMediaRepo{record: record, source: "video"}
	s := NewMediaTaskService(repo)
	task, err := s.Get(context.Background(), MediaTaskActor{UserID: 1}, 1)
	require.NoError(t, err)
	raw, err := json.Marshal(task.VideoBilling)
	require.NoError(t, err)
	for _, field := range []string{"reference_image_count", "billable_reference_image_count", "reference_image_free_count", "reference_image_unit_price", "reference_image_cost"} {
		require.Contains(t, string(raw), `"`+field+`":0`)
	}
	record.Quote.ImageInputPricing = nil
	task, err = s.Get(context.Background(), MediaTaskActor{UserID: 1}, 1)
	require.NoError(t, err)
	raw, err = json.Marshal(task.VideoBilling)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "reference_image")
	for _, source := range []string{"async_image", "seedance_video", "grok_video"} {
		repo.source = source
		previousReads := repo.reads
		task, err = s.Get(context.Background(), MediaTaskActor{UserID: 1}, 1)
		require.NoError(t, err)
		require.Nil(t, task.VideoBilling)
		require.Equal(t, previousReads, repo.reads)
	}
}

// 真实费用使用账本精度，极小非零单价不能显示为已经扣走尚未入账的金额。
func TestMediaTaskVideoImageFeeUsesLedgerPrecision(t *testing.T) {
	count, price := 1, .000000004
	record := &VideoTaskRecord{BillingStatus: "settled", Quote: &VideoPriceQuote{Version: 1, Mode: BillingModeVideo,
		ReferenceImageCount: &count, ImageInputPricing: &VideoImageInputPricing{Price: &price}}, BillingResult: &BatchImageBalanceHoldResult{}}
	s := NewMediaTaskService(&videoImageFeeMediaRepo{record: record, source: "video"})
	task, err := s.Get(context.Background(), MediaTaskActor{UserID: 1}, 1)
	require.NoError(t, err)
	require.Equal(t, price, *task.VideoBilling.ReferenceImageUnitPrice)
	require.Zero(t, *task.VideoBilling.ReferenceImageCost)
}
