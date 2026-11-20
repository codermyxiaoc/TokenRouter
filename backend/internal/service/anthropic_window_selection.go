package service

import (
	"context"
	"slices"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/ctxkey"
)

// 目录读取时每个账号只解码一次快照，多个公开别名可以指向同一个上游型号。
type anthropicWindowAccount struct {
	account *Account
	models  map[string]UpstreamModelMetadata
}

func (s *GatewayService) anthropicWindowAccounts(accounts []Account, platform string, groupID *int64) []anthropicWindowAccount {
	if platform != PlatformAnthropic {
		return nil
	}
	ungroupedOnly := groupID == nil && (s.cfg == nil || s.cfg.RunMode != config.RunModeSimple)
	var scopes []anthropicWindowAccount
	for i := range accounts {
		a := &accounts[i]
		if !a.IsAnthropic() || (ungroupedOnly && (len(a.GroupIDs) > 0 || len(a.AccountGroups) > 0)) {
			continue
		}
		snapshot := a.GetUpstreamModelMetadataSnapshot()
		if snapshot == nil {
			continue
		}
		// 空快照和失配的快照仍表明该分组采用能力同步，不能退回无约束选号。
		scope := anthropicWindowAccount{account: a}
		if snapshot.Binding != "" && snapshot.Binding == anthropicCatalogBinding(a) {
			scope.models = snapshot.Models
		}
		scopes = append(scopes, scope)
	}
	return scopes
}

type anthropicWindowSelectionKey struct{}
type anthropicWindowGroupKey struct{}

// longContextSelection 按最终 U 型号查能力；没有长窗口证据时返回空集合，只有全组从未同步才保留历史兼容。
func longContextSelection(scopes []anthropicWindowAccount, group *Group, routingModel string) map[int64]string {
	if len(scopes) == 0 {
		return nil
	}
	selected := make(map[int64]string)
	var routes []int64
	if group != nil {
		routes = group.GetRoutingAccountIDs(routingModel)
	}
	for _, scope := range scopes {
		a := scope.account
		upstream := resolveAnthropicAccountUpstreamModel(a, resolveAccountMappedModelForForward(a, routingModel))
		metadata, ok := scope.models[upstream]
		if !ok || metadata.ContextWindow <= 0 {
			continue
		}
		if metadata.ContextWindow < anthropicLongContextThreshold || (len(routes) > 0 && !slices.Contains(routes, a.ID)) || (group != nil && group.RequirePrivacySet && !a.IsPrivacySet()) {
			continue
		}
		selected[a.ID] = upstream
	}
	return selected
}

// WithAnthropicWindowSelection 在 Messages / CountTokens 的后缀被规范化后约束粘性和普通选号。
// 普通请求不读取能力数据。只有全组从未同步能力时仍走原有 [1m] 兼容路径。
func (s *GatewayService) WithAnthropicWindowSelection(ctx context.Context, group *Group, parsed *ParsedRequest) (context.Context, error) {
	if parsed == nil || !parsed.ClaudeLongContext {
		return ctx, nil
	}
	return s.loadAnthropicWindowSelection(ctx, group, parsed.Model)
}

// 如果 Claude Code 限制或上游回退切换分组，需要改按本次真正调度的分组筛选账号。
func (s *GatewayService) refreshAnthropicWindowGroup(ctx context.Context, group *Group, model string) (context.Context, error) {
	prior, ok := ctx.Value(anthropicWindowGroupKey{}).(int64)
	if !ok || (group == nil && prior == 0) || (group != nil && group.ID == prior) {
		return ctx, nil
	}
	return s.loadAnthropicWindowSelection(ctx, group, model)
}

func (s *GatewayService) loadAnthropicWindowSelection(ctx context.Context, group *Group, model string) (context.Context, error) {
	if s == nil || s.accountRepo == nil {
		return ctx, nil
	}
	var groupID *int64
	if group != nil {
		groupID = &group.ID
	}
	ctx = context.WithValue(ctx, anthropicWindowGroupKey{}, derefGroupID(groupID))
	if forced, _ := ctx.Value(ctxkey.ForcePlatform).(string); (forced != "" && forced != PlatformAnthropic) || (group != nil && group.Platform != PlatformAnthropic) {
		return context.WithValue(ctx, anthropicWindowSelectionKey{}, map[int64]string(nil)), nil
	}
	// 冷却中的账号仍可为该分组提供已同步标记；真正选号时由调度器剔除。
	queryGroup := int64(AccountListGroupUngrouped)
	if group != nil {
		queryGroup = group.ID
	} else if s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
		queryGroup = 0
	}
	accounts, err := s.accountRepo.ListAllWithFilters(ctx, PlatformAnthropic, "", "", "", queryGroup, "")
	if err != nil {
		return ctx, err
	}
	routingModel := s.channelMappedModelForGroup(ctx, groupID, model)
	selected := longContextSelection(s.anthropicWindowAccounts(accounts, PlatformAnthropic, groupID), group, routingModel)
	return context.WithValue(ctx, anthropicWindowSelectionKey{}, selected), nil
}

// expandAnthropicWindows 复用基础模型的准入和定价，只对声明长窗口的账号重算可请求性。
func (s *GatewayService) expandAnthropicWindows(ctx context.Context, groupID *int64, channel *Channel, accounts []Account, scopes []anthropicWindowAccount, group *Group, models []RequestableModel, variants []string) []RequestableModel {
	if len(scopes) == 0 {
		return models
	}
	if group == nil && groupID != nil {
		if bound, _ := ctx.Value(ctxkey.Group).(*Group); bound != nil && bound.ID == *groupID {
			group = bound
		}
	}
	seen := make(map[string]bool, len(models))
	for _, model := range models {
		seen[strings.ToLower(model.ID)] = true
	}
	for _, variant := range variants {
		base := normalizeClaudeCodeLongContextModel(variant)
		if !strings.EqualFold(variant, base+claudeCodeLongContextModelSuffix) || strings.Contains(base, "*") {
			continue
		}
		if seen[strings.ToLower(variant)] || (group != nil && group.CustomModelsListEnabled() && !slices.Contains(group.ModelsListConfig.Models, variant)) {
			continue
		}
		routingModel := s.channelMappedModelForGroup(ctx, groupID, base)
		selected := longContextSelection(scopes, group, routingModel)
		if len(selected) == 0 {
			continue
		}
		eligibleCtx := context.WithValue(ctx, anthropicWindowSelectionKey{}, selected)
		if model, ok := s.resolveRequestableModel(eligibleCtx, groupID, channel, accounts, base, group); ok {
			model.ID = variant
			models = append(models, model)
			seen[strings.ToLower(variant)] = true
		}
	}
	return models
}
