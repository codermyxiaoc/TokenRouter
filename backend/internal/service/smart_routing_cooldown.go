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

// SmartRoutingAccountCooldownCache 按账号、模型和入口隔离失败，旧的整组键不再参与生产准入。
type SmartRoutingAccountCooldownCache interface {
	GetSmartRoutingAccountCooldowns(context.Context, int64, []int64, string, string) (map[int64]time.Duration, error)
	CooldownSmartRoutingAccounts(context.Context, int64, []int64, string, string, time.Duration) error
}

// 精确缓存直接返回单调时钟截止时间，跨仓储、服务和候选扫描不重新起算剩余时长。
type SmartRoutingAccountCooldownDeadlineCache interface {
	GetSmartRoutingAccountCooldownDeadlines(context.Context, int64, []int64, string, string) (map[int64]time.Time, error)
}

// UsesSmartRoutingAccountCooldown 区分生产账号冷却与旧适配器的组级兼容能力。
func (s *SmartRoutingService) UsesSmartRoutingAccountCooldown() bool { return true }

// CooldownSmartRoutingAccounts 仅记录有真实失败证据的账号，不暂停分组内其它健康账号。
func (s *SmartRoutingService) CooldownSmartRoutingAccounts(ctx context.Context, keyID int64, accountIDs []int64, model, endpoint string, duration time.Duration) error {
	if s == nil || s.gateway == nil || duration <= 0 || keyID <= 0 || len(accountIDs) == 0 {
		return nil
	}
	cache, ok := s.gateway.cache.(SmartRoutingAccountCooldownCache)
	if !ok {
		return nil
	}
	cacheCtx, cancel := context.WithTimeout(ctx, smartRoutingCooldownCacheTimeout)
	defer cancel()
	return cache.CooldownSmartRoutingAccounts(cacheCtx, keyID, accountIDs, model, endpoint, min(duration, time.Hour))
}

func (s *SmartRoutingService) loadSmartRoutingAccountCooldowns(ctx context.Context, accountIDs []int64) map[int64]time.Time {
	config, _ := ctx.Value(smartRoutingAccountCooldownContextKey{}).(smartRoutingAccountCooldownContext)
	if config.duration <= 0 || config.keyID <= 0 || len(accountIDs) == 0 || s == nil || s.gateway == nil {
		return nil
	}
	cache, ok := s.gateway.cache.(SmartRoutingAccountCooldownCache)
	if !ok {
		return nil
	}
	cacheCtx, cancel := context.WithTimeout(ctx, smartRoutingCooldownCacheTimeout)
	defer cancel()
	var err error
	deadlines := make(map[int64]time.Time)
	if exact, ok := cache.(SmartRoutingAccountCooldownDeadlineCache); ok {
		deadlines, err = exact.GetSmartRoutingAccountCooldownDeadlines(cacheCtx, config.keyID, accountIDs, config.model, config.endpoint)
	} else {
		// 旧内部适配器仍可返回时长，以查询发起时刻为基准避免加入读取耗时。
		began := time.Now()
		var remaining map[int64]time.Duration
		remaining, err = cache.GetSmartRoutingAccountCooldowns(cacheCtx, config.keyID, accountIDs, config.model, config.endpoint)
		for id, ttl := range remaining {
			if ttl > 0 {
				deadlines[id] = began.Add(ttl)
			}
		}
	}
	if err != nil {
		slog.Warn("smart_routing_account_cooldown_read_failed", "api_key_id", config.keyID, "error", err)
	}
	setSmartRoutingAccountCooldowns(ctx, accountIDs, deadlines)
	return deadlines
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
