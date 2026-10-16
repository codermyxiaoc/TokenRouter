package service

import (
	"context"
	"strings"

	"github.com/tidwall/gjson"
)

// 历史官方帧数响应可能已被旧解析器标记完成，却仍保留请求 duration；捕获前必须重新核对原包。
// @project-doc docs/domains/video_tasks.md#video_task_billing
func (s *VideoTaskService) prepareSeedanceFrameSettlement(ctx context.Context, task *VideoTaskRecord) error {
	if task.Status != "completed" || task.BillingStatus == "settled" || task.BillingStatus == "released" ||
		task.Target.Version != 2 || task.Target.Endpoint != VideoEndpointSeedance || task.Target.ProtocolAdapter != VideoProtocolAdapterSeedance {
		return nil
	}
	root := gjson.ParseBytes(task.RawResponse)
	hasFrames, canonicalFrames := false, true
	root.ForEach(func(key, _ gjson.Result) bool {
		if strings.EqualFold(key.String(), "frames") {
			hasFrames = true
			canonicalFrames = canonicalFrames && key.String() == "frames"
		}
		return true
	})
	if !hasFrames {
		return nil
	}
	response, err := ExtractVideoProtocolResponse(task.Target, task.ResponseStatus, task.RawResponse, task.UpstreamTaskID)
	if err != nil || task.UpstreamTaskID == "" || task.ResponseStatus < 200 || task.ResponseStatus >= 300 || response == nil || response.Status != "completed" || task.Quote == nil {
		// 原包归属或完成证据有误时不得沿用旧时长，也不得把错误包解释为可退款的任务失败。
		task.PricingMismatch = true
		task.ResponseMetadataError = "Stored Seedance frame response requires confirmation"
	} else {
		if !canonicalFrames {
			response.MetadataError = "Invalid official Seedance frame field"
		}
		previousMismatch, previousError := task.PricingMismatch, task.ResponseMetadataError
		s.applyObservation(task, response)
		if previousMismatch && (response.MetadataError != "" || response.Metadata.DurationSeconds <= 0 || response.Metadata.Resolution == "" ||
			(task.Quote.Mode == BillingModeVideoToken && response.Metadata.Tokens == nil)) {
			// 帧数补齐不能抹掉既有档位或用量冲突；完整可靠的终态维度才可解除核对状态。
			task.PricingMismatch = true
			if previousError != "" {
				task.ResponseMetadataError = previousError
			} else if task.ResponseMetadataError == "" {
				task.ResponseMetadataError = "Stored Seedance output dimensions require confirmation"
			}
		}
	}
	if task.PricingMismatch {
		task.BillingStatus = "reconciliation"
	}
	// 先持久化更正后的秒数再捕获，进程中断后使用记录仍与唯一扣费回执使用相同的时长。
	return s.repo.Save(ctx, task, false)
}
