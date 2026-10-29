package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// 创作台画布素材保存期：管理员按小时配置，单张素材在上传时冻结到期时间，到期后由清理循环删除文件。
// 修改只影响之后新写入的素材，历史素材保留其原到期时间。
// @project-doc docs/domains/cloud_canvas.md#asset_retention
const (
	// DefaultCreativeCanvasAssetRetentionHours 保持升级前的七天行为。
	DefaultCreativeCanvasAssetRetentionHours = 168
	// MaxCreativeCanvasAssetRetentionHours 限制最长保存期为一年。
	MaxCreativeCanvasAssetRetentionHours = 8760
)

// ValidateCreativeCanvasAssetRetentionHours 统一管理员接口与内部写入的边界。
func ValidateCreativeCanvasAssetRetentionHours(hours int) error {
	if hours < 1 || hours > MaxCreativeCanvasAssetRetentionHours {
		return fmt.Errorf("creative_canvas_asset_retention_hours must be an integer between 1 and %d", MaxCreativeCanvasAssetRetentionHours)
	}
	return nil
}

// parseCreativeCanvasAssetRetentionForDisplay 为管理页提供可修正的展示值；非法值回退默认。
func parseCreativeCanvasAssetRetentionForDisplay(value string) int {
	hours, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || ValidateCreativeCanvasAssetRetentionHours(hours) != nil {
		return DefaultCreativeCanvasAssetRetentionHours
	}
	return hours
}

// GetCreativeCanvasAssetRetention 每次归档读取最新配置；读取失败或值损坏时回退默认值，
// 不能因为设置暂时不可读就让素材永不过期。
func (s *SettingService) GetCreativeCanvasAssetRetention(ctx context.Context) time.Duration {
	if s == nil || s.settingRepo == nil {
		return time.Duration(DefaultCreativeCanvasAssetRetentionHours) * time.Hour
	}
	value, err := s.settingRepo.GetValue(ctx, SettingKeyCreativeCanvasAssetRetentionHours)
	if errors.Is(err, ErrSettingNotFound) {
		return time.Duration(DefaultCreativeCanvasAssetRetentionHours) * time.Hour
	}
	if err != nil {
		slog.Warn("failed to read creative canvas asset retention; using default", "error", err)
		return time.Duration(DefaultCreativeCanvasAssetRetentionHours) * time.Hour
	}
	return time.Duration(parseCreativeCanvasAssetRetentionForDisplay(value)) * time.Hour
}
