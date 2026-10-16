package service

import (
	"context"
	"strings"
)

type geminiNativeAccountSelectionKey struct{}

// WithGeminiNativeAccountSelection 在共享选号前声明原生协议，避免先选到 Claude 再拒绝。
// 该标记只约束 Antigravity 候选，不改变其它平台和 Messages/Chat 的既有资格。
func WithGeminiNativeAccountSelection(ctx context.Context) context.Context {
	return context.WithValue(ctx, geminiNativeAccountSelectionKey{}, true)
}

func requiresGeminiNativeAccountSelection(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	required, _ := ctx.Value(geminiNativeAccountSelectionKey{}).(bool)
	return required
}

// IsAntigravityGeminiModel 检查最终上游型号，不能按客户端别名决定协议。
func IsAntigravityGeminiModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "gemini-")
}

// FilterAntigravityGeminiModels 在原可请求目录上按 R→C→U 再过滤，保留映射到 Gemini 的别名。
// 查询失败采用空结果，防止目录回退把 Claude 暴露给 Gemini 原生入口。
func (s *GatewayService) FilterAntigravityGeminiModels(ctx context.Context, groupID *int64, models []string) []string {
	if s == nil || groupID == nil {
		return nil
	}
	accounts, err := s.listRequestableModelAccounts(ctx, groupID)
	if err != nil {
		return nil
	}
	accounts = filterRequestableModelAccounts(accounts, PlatformAntigravity)
	var channel *Channel
	if s.channelService != nil {
		channel, err = s.channelService.GetChannelForGroup(ctx, *groupID)
		if err != nil {
			return nil
		}
	}
	result := make([]string, 0, len(models))
	for _, model := range models {
		target := model
		if channel != nil {
			mapping := s.channelService.ResolveChannelMapping(ctx, *groupID, model)
			if strings.TrimSpace(mapping.MappedModel) != "" {
				target = mapping.MappedModel
			}
		}
		for i := range accounts {
			supported := false
			for _, upstream := range s.resolveAccountUpstreamModelsForListing(ctx, &accounts[i], target) {
				if IsAntigravityGeminiModel(upstream) {
					supported = true
					break
				}
			}
			if supported {
				result = append(result, model)
				break
			}
		}
	}
	return result
}

// IsAntigravityGeminiAccountModel 在实际派发前按相同档位和账号映射校验最终协议。
func IsAntigravityGeminiAccountModel(ctx context.Context, account *Account, model string) bool {
	// 候选资格查询只解析，不登记响应模型别名，避免被淘汰的账号留下转发副作用。
	return account != nil && account.Platform == PlatformAntigravity && IsAntigravityGeminiModel(resolveFinalAntigravityModelKey(ctx, account, model))
}
