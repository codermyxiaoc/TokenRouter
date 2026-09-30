package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// 白名单命中保留审计事实，移出白名单后也不参与历史自动封禁计数。
const ContentModerationModeCyberLogOnly = "cyber_log_only"
const ContentModerationModeRiskControlLogOnly = "risk_control_log_only"

// 沿用历史设置键，同时控制普通审核和上游 Cyber 风控。

// ParseCyberPolicyUserAllowlist 接受逗号或空白分隔的正用户 ID，任一非法项使整份配置无效。
func ParseCyberPolicyUserAllowlist(raw string) (map[int64]struct{}, error) {
	if len(raw) > 16384 {
		return nil, fmt.Errorf("cyber_policy_user_allowlist must not exceed 16384 bytes")
	}
	ids := make(map[int64]struct{})
	for _, field := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
		id, err := strconv.ParseInt(field, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("cyber_policy_user_allowlist must contain positive user IDs separated by commas or whitespace")
		}
		ids[id] = struct{}{}
	}
	return ids, nil
}

func (s *SettingService) IsCyberPolicyUserAllowlisted(ctx context.Context, userID int64) bool {
	if s == nil || s.settingRepo == nil || userID <= 0 {
		return false
	}
	s.GetCyberSessionBlockRuntime(ctx)
	if cached, ok := s.cyberSessionBlockCache.Load().(*cachedCyberSessionBlockRuntime); ok && cached != nil {
		_, allowed := cached.allowlistedUsers[userID]
		return allowed
	}
	return false
}

func (s *OpenAIGatewayService) CyberPolicyLogOnly(ctx context.Context, apiKey *APIKey) bool {
	if s == nil || apiKey == nil {
		return false
	}
	// 团队 Key 的 owner 仅承担计费，白名单必须匹配实际行为成员。
	userID := apiKey.UserID
	if apiKey.ActorUser != nil && apiKey.ActorUser.ID > 0 {
		userID = apiKey.ActorUser.ID
	}
	return s.settingService.IsCyberPolicyUserAllowlisted(ctx, userID)
}
