package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"time"

	"github.com/google/uuid"
)

// 图片继续使用原执行器和账务；云归档只在原任务结算完成后取回输出。
type creativeCanvasImageExecutor struct {
	creative *CreativePublicService
	repo     CreativeCanvasRepository
}

// creativeImageProviderReconciliationDue 将执行超时后的图片任务投影为核对状态，避免界面无限显示“生成中”。
func creativeImageProviderReconciliationDue(run *CreativeRun, now time.Time, timeout time.Duration) bool {
	if run == nil || run.Operation == CreativeOperationTTS || run.ProvisioningPhase != CreativeProvisioningPhaseProviderInflight {
		return false
	}
	if run.ErrorCode != nil && *run.ErrorCode == "PROVIDER_RESULT_UNCONFIRMED" {
		return true
	}
	if timeout <= 0 || run.StartedAt == nil {
		return false
	}
	return !now.Before(run.StartedAt.Add(timeout))
}

func creativeCanvasImageExecutionTimeout(public *CreativePublicService) time.Duration {
	if public != nil && public.Config != nil && public.Config.Creative.ExecuteTimeoutSeconds > 0 {
		return time.Duration(public.Config.Creative.ExecuteTimeoutSeconds) * time.Second
	}
	return defaultCreativeExecuteTimeout
}

func (x *creativeCanvasImageExecutor) Recover(ctx context.Context, e *CreativeCanvasExecution) (string, error) {
	run, err := x.creative.Repo.GetCreativeRunByIdempotencyKey(ctx, CreativeRunScope{UserID: e.UserID, WorkspaceID: e.CanvasID}, "canvas:"+e.ID)
	if errors.Is(err, ErrCreativeRunNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return run.RunID, nil
}

func (x *creativeCanvasImageExecutor) Submit(ctx context.Context, e *CreativeCanvasExecution, assets []CreativeCanvasInputAsset) (string, error) {
	params, err := creativeCanvasImageParams(e, assets)
	if err != nil {
		return "", err
	}
	run, err := x.creative.CreateRun(ctx, CreativeRunScope{UserID: e.UserID, WorkspaceID: e.CanvasID}, params, "canvas:"+e.ID)
	if err != nil {
		return "", err
	}
	return run.ID, nil
}

// creativeCanvasImageParams 让批次准入与真实提交使用同一参考素材转换，避免校验与执行漂移。
func creativeCanvasImageParams(e *CreativeCanvasExecution, assets []CreativeCanvasInputAsset) (CreateCreativeRunParamsPublic, error) {
	p := e.Params
	if e.Kind == "audio" {
		// 音频只朗读文字，不把引用图片/视频偷偷转换成未支持的语音输入。
		if len(assets) != 0 || len(p.References) != 0 || p.MaskAssetID != "" || p.BatchID != "" || p.BatchIndex != 0 || p.BatchSize != 0 || p.BatchOutputCount != 0 || p.Operation != "" && p.Operation != CreativeOperationGenerate || p.ImageSize != "" || p.AspectRatio != "" || p.Quality != "" || p.Background != "" || p.ThinkingLevel != "" || p.Endpoint != "" || p.Duration != 0 || p.Resolution != "" || p.GenerateAudio != nil || p.Watermark != nil {
			return CreateCreativeRunParamsPublic{}, ErrCreativeInvalidParams
		}
		return CreateCreativeRunParamsPublic{GroupID: p.GroupID, Model: p.Model, Operation: CreativeOperationTTS, Prompt: p.Prompt, Voice: p.Voice, AudioFormat: p.AudioFormat, Speed: p.Speed, OutputCount: 1, canvasAudio: true}, nil
	}
	params := CreateCreativeRunParamsPublic{GroupID: p.GroupID, Model: p.Model, Operation: p.Operation, Prompt: p.Prompt, ImageSize: p.ImageSize, AspectRatio: p.AspectRatio, Quality: p.Quality, Background: p.Background, ThinkingLevel: p.ThinkingLevel}
	if p.BatchID != "" {
		if p.BatchSize < 1 || p.BatchOutputCount < 1 || p.BatchOutputCount%p.BatchSize != 0 {
			return params, ErrCreativeInvalidParams
		}
		params.OutputCount = p.BatchOutputCount / p.BatchSize
		params.ExpectedOutputCount = params.OutputCount
	}
	if params.Operation == "" {
		params.Operation = CreativeOperationGenerate
	}
	for _, asset := range assets {
		if asset.Asset.MediaType != "image" {
			return params, ErrCreativeInvalidParams
		}
		image := CreativeInputImage{Mime: asset.Asset.MIMEType, Bytes: asset.Content}
		if asset.Role == "mask" {
			params.Mask = &image
		} else {
			params.SourceImages = append(params.SourceImages, image)
		}
	}
	return params, nil
}
func (x *creativeCanvasImageExecutor) Poll(ctx context.Context, e *CreativeCanvasExecution) (*CreativeCanvasExecutionResult, error) {
	scope := CreativeRunScope{UserID: e.UserID, WorkspaceID: e.CanvasID}
	run, err := x.creative.Repo.GetCreativeRunByRunIDForOwner(ctx, scope, e.TaskID)
	if err != nil {
		return nil, err
	}
	out := &CreativeCanvasExecutionResult{Status: "running"}
	if run.Operation == CreativeOperationTTS {
		switch run.Status {
		case CreativeRunStatusProviderSucceeded, CreativeRunStatusSettlementPending:
			out.ErrorCode, out.ErrorMessage = "CREATIVE_AUDIO_SETTLEMENT_PENDING", "语音已生成，正在确认结算并保存成品，请勿重复生成"
		case CreativeRunStatusReleasePending:
			out.ErrorCode, out.ErrorMessage = "CREATIVE_AUDIO_RELEASE_PENDING", "语音任务正在释放预扣，请等待处理完成"
		}
	}
	if run.Operation == CreativeOperationTTS && run.ProvisioningPhase == CreativeProvisioningPhaseProviderInflight && run.ErrorCode != nil && *run.ErrorCode == "PROVIDER_RESULT_UNCONFIRMED" {
		out.ErrorCode, out.ErrorMessage = "CREATIVE_AUDIO_SUBMISSION_UNCONFIRMED", "语音请求结果正在核对，请勿重复生成；费用以最终结算为准"
	}
	if run.Operation != CreativeOperationTTS && creativeImageProviderReconciliationDue(run, time.Now(), creativeCanvasImageExecutionTimeout(x.creative)) {
		out.ErrorCode, out.ErrorMessage = "CREATIVE_IMAGE_SUBMISSION_UNCONFIRMED", "图片请求结果正在核对，请勿重复生成；费用以最终结算为准"
	}
	switch run.Status {
	case CreativeRunStatusFailed, CreativeRunStatusCancelled:
		out.Status = "failed"
		out.ErrorCode = creativeDerefString(run.ErrorCode)
		out.ErrorMessage = creativeCanvasExecutionFailureMessage(e.Kind, out.ErrorCode)
		if out.ErrorMessage == "" {
			out.ErrorMessage = "生成未完成，费用按原任务规则处理"
		}
		return out, nil
	case CreativeRunStatusResultLost:
		out.Status = "result_lost"
		out.ErrorMessage = "成品临时缓存已丢失"
		return out, nil
	case CreativeRunStatusSucceeded:
	default:
		return out, nil
	}
	outputs, err := x.creative.Repo.ListCreativeRunOutputs(ctx, e.TaskID)
	if err != nil {
		return nil, err
	}
	out.Status = "succeeded"
	for _, o := range outputs {
		if o.Status != CreativeRunOutputStatusSucceeded && o.Status != CreativeRunOutputStatusAcked {
			continue
		}
		item := CreativeCanvasOutput{Index: o.OutputIndex}
		if o.MimeType != nil {
			item.MIMEType = *o.MimeType
		}
		if o.ByteSize != nil {
			item.Size = *o.ByteSize
		}
		if o.Status == CreativeRunOutputStatusSucceeded {
			data, err := x.creative.TransientStore.LoadOutput(ctx, e.TaskID, o.OutputIndex)
			if err != nil {
				// 云归档可能已在 Redis 读取前完成；此时无需再次读临时媒体。
				assetID := creativeCanvasAssetID(e.ID, o.OutputIndex)
				if x.repo == nil {
					return nil, err
				}
				asset, assetErr := x.repo.GetAsset(ctx, e.UserID, assetID)
				if assetErr != nil || asset.Status != "ready" {
					for _, old := range out.Outputs {
						if old.Reader != nil {
							old.Reader.Close()
						}
					}
					return nil, err
				}
				item.MIMEType, item.Size = asset.MIMEType, asset.ByteSize
				out.Outputs = append(out.Outputs, item)
				continue
			}
			item.Reader = io.NopCloser(bytes.NewReader(data))
		}
		out.Outputs = append(out.Outputs, item)
	}
	return out, nil
}

func creativeCanvasAssetID(executionID string, index int) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(executionID+":"+string(rune(index+65)))).String()
}
func (x *creativeCanvasImageExecutor) Ack(ctx context.Context, e *CreativeCanvasExecution, index int) error {
	scope := CreativeRunScope{UserID: e.UserID, WorkspaceID: e.CanvasID}
	run, err := x.creative.Repo.GetCreativeRunByRunIDForOwner(ctx, scope, e.TaskID)
	if err != nil {
		return err
	}
	if run.Status != CreativeRunStatusSucceeded {
		return ErrCreativeOutputNotReady
	}
	if err = x.creative.Repo.MarkCreativeRunOutputAcked(ctx, e.TaskID, index, time.Now()); err != nil {
		return err
	}
	return x.creative.TransientStore.DeleteOutput(ctx, e.TaskID, index)
}
