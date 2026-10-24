package service

import infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"

const (
	// DefaultSmartRoutingCooldownSeconds 用于新建 Key 的缺省冷却；已有显式或历史存量值不覆盖。
	DefaultSmartRoutingCooldownSeconds = 10
	// MaxSmartRoutingCooldownSeconds 限制错误分组最长回避一小时。
	MaxSmartRoutingCooldownSeconds = 3600
)

// ErrSmartRoutingCooldownInvalid 用于拒绝超出配置范围的冷却秒数。
var ErrSmartRoutingCooldownInvalid = infraerrors.BadRequest("SMART_ROUTING_COOLDOWN_INVALID", "smart_routing_cooldown_seconds must be an integer between 0 and 3600")

// ValidateSmartRoutingCooldownSeconds 校验 Key 配置；0 关闭冷却但不改变跨组重试资格。
// @project-doc docs/domains/smart_routing_api_keys.md#configuration
func ValidateSmartRoutingCooldownSeconds(seconds int) error {
	if seconds < 0 || seconds > MaxSmartRoutingCooldownSeconds {
		return ErrSmartRoutingCooldownInvalid
	}
	return nil
}
