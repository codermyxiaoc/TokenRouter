package service

import (
	"context"
	"strings"
	"time"
)

const DefaultImageModelCooldown = 10 * time.Second

// ImageModelCooldownCache 按实际账号、上游模型和图片操作共享故障窗口，不封禁整个分组。
// 这里的模型必须已完成映射，读写时不能再次套用账号映射。
type ImageModelCooldownCache interface {
	GetImageModelCooldown(context.Context, int64, string, string) (time.Duration, error)
	CooldownImageModel(context.Context, int64, string, string, time.Duration) error
}

// NormalizeImageModelOperation 只接收图片操作，音频和普通文本不会借用生图健康状态。
func NormalizeImageModelOperation(operation string) string {
	switch strings.ToLower(strings.TrimSpace(operation)) {
	case CreativeOperationGenerate:
		return CreativeOperationGenerate
	case CreativeOperationEdit:
		return CreativeOperationEdit
	case CreativeOperationInpaint:
		return CreativeOperationInpaint
	default:
		return ""
	}
}

// ImageModelCooldownRemaining 使用缓存的有界本地降级结果；未知状态不能伪装成账号故障。
func ImageModelCooldownRemaining(ctx context.Context, cache any, accountID int64, finalModel, operation string) time.Duration {
	store, ok := cache.(ImageModelCooldownCache)
	operation = NormalizeImageModelOperation(operation)
	if !ok || store == nil || accountID <= 0 || strings.TrimSpace(finalModel) == "" || operation == "" {
		return 0
	}
	if ctx == nil {
		ctx = context.Background()
	}
	budget, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	remaining, _ := store.GetImageModelCooldown(budget, accountID, strings.TrimSpace(finalModel), operation)
	if remaining > 0 {
		return remaining
	}
	return 0
}

// RecordImageModelCooldown 仅由已分类的上游故障调用；客户端取消和本地存储失败不应进入这里。
// 实际故障记录不依赖客户端仍保持连接，写入拥有独立且有限的预算。
func RecordImageModelCooldown(ctx context.Context, cache any, accountID int64, finalModel, operation string, duration time.Duration) error {
	store, ok := cache.(ImageModelCooldownCache)
	operation = NormalizeImageModelOperation(operation)
	if !ok || store == nil || accountID <= 0 || strings.TrimSpace(finalModel) == "" || operation == "" {
		return nil
	}
	if duration <= 0 {
		duration = DefaultImageModelCooldown
	}
	if ctx == nil {
		ctx = context.Background()
	}
	budget, cancel := context.WithTimeout(context.WithoutCancel(ctx), 250*time.Millisecond)
	defer cancel()
	return store.CooldownImageModel(budget, accountID, strings.TrimSpace(finalModel), operation, min(duration, time.Hour))
}

func (s *OpenAIGatewayService) ImageModelCooldownRemaining(ctx context.Context, accountID int64, finalModel, operation string) time.Duration {
	if s == nil {
		return 0
	}
	return ImageModelCooldownRemaining(ctx, s.cache, accountID, finalModel, operation)
}

func (s *OpenAIGatewayService) RecordImageModelCooldown(ctx context.Context, accountID int64, finalModel, operation string, duration time.Duration) error {
	if s == nil {
		return nil
	}
	return RecordImageModelCooldown(ctx, s.cache, accountID, finalModel, operation, duration)
}

func (s *GatewayService) ImageModelCooldownRemaining(ctx context.Context, accountID int64, finalModel, operation string) time.Duration {
	if s == nil {
		return 0
	}
	return ImageModelCooldownRemaining(ctx, s.cache, accountID, finalModel, operation)
}

func (s *GatewayService) RecordImageModelCooldown(ctx context.Context, accountID int64, finalModel, operation string, duration time.Duration) error {
	if s == nil {
		return nil
	}
	return RecordImageModelCooldown(ctx, s.cache, accountID, finalModel, operation, duration)
}
