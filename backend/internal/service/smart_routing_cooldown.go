package service

import (
	"context"
	"log/slog"
	"time"
)

// SmartRoutingCooldownCache 是网关缓存的可选能力，按 Key 与整个分组共享冷却。
// @project-doc docs/domains/smart_routing_api_keys.md#request_selection
type SmartRoutingCooldownCache interface {
	GetSmartRoutingCooldown(context.Context, int64, int64) (time.Duration, error)
	CooldownSmartRoutingGroup(context.Context, int64, int64, time.Duration) error
}

const smartRoutingCooldownCacheTimeout = 250 * time.Millisecond

func (s *SmartRoutingService) cooldownCache() SmartRoutingCooldownCache {
	if s == nil || s.gateway == nil {
		return nil
	}
	cache, _ := s.gateway.cache.(SmartRoutingCooldownCache)
	return cache
}

// GetSmartRoutingCooldown 读取当前 Key 的分组冷却；缓存故障只能降级为已知本地结果，不能伪造全冷却。
// 冷却配置为零时由候选协调器直接跳过此查询，避免读取此前配置遗留的 TTL。
func (s *SmartRoutingService) GetSmartRoutingCooldown(ctx context.Context, keyID, groupID int64) (time.Duration, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	cache := s.cooldownCache()
	if cache == nil || keyID <= 0 || groupID <= 0 {
		return 0, nil
	}
	cacheCtx, cancel := context.WithTimeout(ctx, smartRoutingCooldownCacheTimeout)
	defer cancel()
	remaining, err := cache.GetSmartRoutingCooldown(cacheCtx, keyID, groupID)
	if err != nil {
		slog.Warn("smart_routing_cooldown_read_failed", "api_key_id", keyID, "group_id", groupID, "error", err)
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
	}
	if remaining < 0 {
		remaining = 0
	}
	return remaining, nil
}

// CooldownSmartRoutingGroup 不修改分组或账号状态；零时长完全跳过读写。
func (s *SmartRoutingService) CooldownSmartRoutingGroup(ctx context.Context, keyID, groupID int64, duration time.Duration) error {
	if duration <= 0 || keyID <= 0 || groupID <= 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	cache := s.cooldownCache()
	if cache == nil {
		return nil
	}
	cacheCtx, cancel := context.WithTimeout(ctx, smartRoutingCooldownCacheTimeout)
	defer cancel()
	if err := cache.CooldownSmartRoutingGroup(cacheCtx, keyID, groupID, min(duration, time.Hour)); err != nil {
		slog.Warn("smart_routing_cooldown_write_failed", "api_key_id", keyID, "group_id", groupID, "error", err)
		return ctx.Err()
	}
	return nil
}
