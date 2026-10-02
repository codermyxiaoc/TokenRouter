package service

import (
	"context"
	"errors"
	"time"
)

// 单独区分预览保留期结束，避免把有历史产物的任务误提示为从未保存链接。
var errMediaTaskVideoPreviewExpired = errors.New("video preview retention expired")

// 持久视频结果是可选能力，旧媒体仓储及测试替身不需要实现新的执行接口。
type mediaTaskVideoReader interface {
	GetVideoTask(context.Context, *MediaTask) (*VideoTaskRecord, error)
}

func (s *MediaTaskService) enrichVideoBilling(ctx context.Context, task *MediaTask) {
	if task == nil || task.Source != "video" {
		return
	}
	reader, ok := s.repo.(mediaTaskVideoReader)
	if !ok {
		return
	}
	record, err := reader.GetVideoTask(ctx, task)
	if err != nil || record.Quote == nil {
		return
	}
	// 展示投影会保护旧终态，视频费用恢复后的临时错误以持久任务的最新结果为准。
	task.ErrorMessage = record.ErrorMessage
	d := &VideoBillingDetails{Status: record.BillingStatus, Mode: string(record.Quote.Mode), Resolution: record.Quote.Resolution,
		HasReferenceVideo: record.Quote.HasReferenceVideo, UnitPrice: record.Quote.UnitPrice, Unit: record.Quote.Unit,
		Tokens: record.Metadata.Tokens, DurationSeconds: record.Metadata.DurationSeconds, ReservedAmount: record.Hold.HoldAmount, PricingSource: record.Quote.Source,
		DeferredBilling: record.Hold.VideoDeferredBilling}
	if record.Quote.Mode == BillingModeVideoPerRequest {
		// 单次计费不依赖生成时长，自动时长仍未知时留给界面显示“未记录”。
		if !validUsageVideoAmount(d.DurationSeconds) {
			d.DurationSeconds = 0
		}
		if d.Resolution == "" {
			d.Resolution = record.Metadata.Resolution
		}
	}
	if record.Hold.VideoTokenPrepay && record.Quote.TokenPrepay != nil {
		d.TokenPrepay = true
		d.PrepayPricePerSecond = record.Quote.TokenPrepay.Clone().PricePerSecond
		d.PrepayDurationSeconds = record.Hold.VideoPrepayDurationSeconds
	}
	if p := record.Quote.ImageInputPricing; p != nil && record.Quote.ReferenceImageCount != nil {
		count, free := *record.Quote.ReferenceImageCount, p.FreeImages
		billable := max(0, count-free)
		d.ReferenceImageCount, d.ReferenceImageFreeCount, d.BillableReferenceImageCount = &count, &free, &billable
		d.ReferenceImageUnitPrice = p.Clone().Price
		// 任务完成和账务完成不是同一个状态，未扣费时不把预估图片费显示为实际费用。
		if record.BillingStatus == "settled" && record.BillingResult != nil {
			if cost, err := record.Quote.FixedImageInputCost(); err == nil {
				// 实际收费沿用账本八位金额精度，避免把已舍入为零的估算显示为已扣费。
				amount := QuantizeUsageBillingAmount(cost)
				d.ReferenceImageCost = &amount
			}
		}
	}
	if record.BillingStatus == "settled" && record.BillingResult != nil {
		amount := record.BillingResult.ActualAmountUSD
		d.ActualAmount = &amount
		task.ActualCost = &amount
	}
	task.VideoBilling = d
	task.BillingMode = &d.Mode
}

// videoPreviewRecord 对独立视频始终复核持久身份和账务，缓存不能绕过已结算及保留期约束。
func (s *MediaTaskService) videoPreviewRecord(ctx context.Context, identity MediaTaskPreviewIdentity) (*MediaTaskVideoPreviewRecord, error) {
	if identity.Source != "video" {
		return s.previewCache.GetVideo(ctx, identity)
	}
	task, err := s.readPreviewVideoTask(ctx, identity)
	if err != nil {
		return nil, err
	}
	useContent := task.VideoURL == "" && s.canPreviewVideoContent(task)
	if !useContent && !safeMediaPreviewURL(task.VideoURL) {
		return nil, nil
	}
	return &MediaTaskVideoPreviewRecord{Identity: identity, ExpiresAt: task.CompletedAt.Add(24 * time.Hour),
		Media:              MediaTaskVideoSnapshot{URL: task.VideoURL, MimeType: "video/mp4", DurationSeconds: task.Metadata.DurationSeconds},
		UseContentEndpoint: useContent}, nil
}

// 预览和播放都只读原任务，不调用状态查询、恢复或结算；损坏的 JSON 不能扩大访问范围。
func (s *MediaTaskService) readPreviewVideoTask(ctx context.Context, identity MediaTaskPreviewIdentity) (*VideoTaskRecord, error) {
	if identity.Source != "video" || identity.TaskID == "" || identity.UserID <= 0 || identity.APIKeyID <= 0 {
		return nil, ErrMediaTaskNotFound
	}
	reader, ok := s.repo.(mediaTaskVideoReader)
	if !ok {
		return nil, ErrMediaTaskNotFound
	}
	task, err := reader.GetVideoTask(ctx, &MediaTask{ID: identity.ID, Source: "video", TaskID: identity.TaskID, UserID: identity.UserID, APIKeyID: identity.APIKeyID})
	if err != nil {
		return nil, err
	}
	if task == nil || task.ID != identity.TaskID || task.UserID != identity.UserID || task.APIKeyID != identity.APIKeyID ||
		task.Status != "completed" || task.BillingStatus != "settled" || task.CompletedAt == nil {
		return nil, ErrMediaTaskNotFound
	}
	if !task.CompletedAt.Add(24 * time.Hour).After(time.Now()) {
		return nil, errMediaTaskVideoPreviewExpired
	}
	// 签发票据之前拒绝已知错误归属的旧原包，避免继续暴露其媒体地址。
	if !videoStoredResponseMatchesTask(task) {
		return nil, errVideoTaskResponseMismatch
	}
	return task, nil
}

// 仅对明确支持固定内容端点且没有直链的任务兜底，不猜测其他供应商路径或替换不安全地址。
func (s *MediaTaskService) canPreviewVideoContent(task *VideoTaskRecord) bool {
	if task == nil || task.VideoURL != "" || s.videoContentTransport == nil || s.videoAccounts == nil ||
		task.Target.Version != 1 || task.Target.Endpoint != VideoEndpointOpenAIVideos ||
		task.AccountID <= 0 || task.Target.AccountID != task.AccountID || !safeMediaPreviewURL(task.Target.BaseURL) {
		return false
	}
	_, err := videoTaskPath(task.Target, task.UpstreamTaskID)
	return err == nil
}
