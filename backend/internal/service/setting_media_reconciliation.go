package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultMediaTaskReconciliationTimeoutMinutes 保持升级后失败任务默认等待一小时。
	DefaultMediaTaskReconciliationTimeoutMinutes = 60
	// MaxMediaTaskReconciliationTimeoutMinutes 限制最长人工核对期为七天。
	MaxMediaTaskReconciliationTimeoutMinutes = 7 * 24 * 60
)

// ValidateMediaTaskReconciliationTimeoutMinutes 统一管理员接口与内部写入的边界。
func ValidateMediaTaskReconciliationTimeoutMinutes(minutes int) error {
	if minutes < 1 || minutes > MaxMediaTaskReconciliationTimeoutMinutes {
		return fmt.Errorf("media_task_reconciliation_timeout_minutes must be an integer between 1 and %d", MaxMediaTaskReconciliationTimeoutMinutes)
	}
	return nil
}

// parseMediaTaskReconciliationTimeoutForDisplay 仅为管理页提供可修正的展示值，不用于余额释放判断。
func parseMediaTaskReconciliationTimeoutForDisplay(value string) int {
	minutes, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || ValidateMediaTaskReconciliationTimeoutMinutes(minutes) != nil {
		return DefaultMediaTaskReconciliationTimeoutMinutes
	}
	return minutes
}

// GetMediaTaskReconciliationTimeout 每轮清理读取最新配置；读取失败或值损坏时必须跳过自动释放。
func (s *SettingService) GetMediaTaskReconciliationTimeout(ctx context.Context) (time.Duration, error) {
	if s == nil || s.settingRepo == nil {
		return 0, fmt.Errorf("media task reconciliation setting repository unavailable")
	}
	value, err := s.settingRepo.GetValue(ctx, SettingKeyMediaTaskReconciliationTimeoutMinutes)
	if errors.Is(err, ErrSettingNotFound) {
		return time.Duration(DefaultMediaTaskReconciliationTimeoutMinutes) * time.Minute, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read media task reconciliation timeout: %w", err)
	}
	minutes, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("parse media task reconciliation timeout: %w", err)
	}
	if err := ValidateMediaTaskReconciliationTimeoutMinutes(minutes); err != nil {
		return 0, err
	}
	return time.Duration(minutes) * time.Minute, nil
}
