package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/ctxkey"
)

// SmartRoutingGroupAvailability 将有限模型目录命中与瞬时可调度性分开，供鉴权层按顺序选组。
type SmartRoutingGroupAvailability struct {
	HasModel    bool
	Schedulable bool
}

// SmartRoutingGroupResolver 只判断一个分组；密钥权限、候选顺序和选定后的计费由鉴权层负责。
type SmartRoutingGroupResolver interface {
	EvaluateSmartRoutingGroup(context.Context, *Group, string, string) (SmartRoutingGroupAvailability, error)
}

// SmartRoutingService 在真实账号调度之前进行只读选组，不获取并发槽、不绑定会话或执行分组回退。
type SmartRoutingService struct {
	gateway *GatewayService
	openAI  *OpenAIGatewayService
}

// NewSmartRoutingService 复用现有网关依赖，避免给密钥服务引入循环依赖。
func NewSmartRoutingService(gateway *GatewayService, openAI *OpenAIGatewayService) *SmartRoutingService {
	return &SmartRoutingService{gateway: gateway, openAI: openAI}
}

// EvaluateSmartRoutingGroup 接收已经执行一次 Key 重定向的模型，只在本次分组内解析渠道和账号模型。
// 数据源失败必须返回错误，不能将查询故障解释成模型不存在后静默跨组选取。
// @project-doc docs/domains/smart_routing_api_keys.md#request_selection
func (s *SmartRoutingService) EvaluateSmartRoutingGroup(ctx context.Context, group *Group, requestedModel, endpoint string) (SmartRoutingGroupAvailability, error) {
	result := SmartRoutingGroupAvailability{}
	requestedModel = strings.TrimSpace(requestedModel)
	if group == nil || group.ID <= 0 || !group.IsActive() || requestedModel == "" || strings.Contains(requestedModel, "*") {
		return result, nil
	}
	if s == nil || s.gateway == nil || s.gateway.accountRepo == nil {
		return result, errors.New("smart routing account resolver unavailable")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	platform := group.Platform
	if !smartRoutingGroupEndpointEligible(platform, endpoint) {
		return result, nil
	}
	forcePlatform, _ := ctx.Value(ctxkey.ForcePlatform).(string)
	if forcePlatform != "" && forcePlatform != platform {
		return result, nil
	}
	useMixed := forcePlatform == "" && (platform == PlatformAnthropic || platform == PlatformGemini)
	// 固定本次绑定分组，simple 模式也不能扩大到其他分组的账号。
	accounts, err := s.listSmartRoutingCatalogAccounts(ctx, group, useMixed)
	if err != nil {
		return result, fmt.Errorf("load smart routing accounts: %w", err)
	}
	ctx = context.WithValue(ctx, ctxkey.Group, group)
	if strings.Contains(endpoint, "/images/") && !strings.Contains(endpoint, "/images/batches") {
		ctx = WithOpenAIImageGenerationIntent(ctx)
	}
	routingModel := requestedModel
	billingSource := BillingModelSourceRequested
	var channel *Channel
	if s.gateway.channelService != nil {
		channel, err = s.gateway.channelService.GetChannelForGroup(ctx, group.ID)
		if err != nil {
			return result, fmt.Errorf("load smart routing channel: %w", err)
		}
		mapping := s.gateway.channelService.ResolveChannelMapping(ctx, group.ID, requestedModel)
		if mapped := strings.TrimSpace(mapping.MappedModel); mapped != "" {
			routingModel = mapped
		}
		billingSource = mapping.BillingModelSource
	}
	channelModel := routingModel
	if strings.Contains(endpoint, "/messages") && (platform == PlatformOpenAI || platform == PlatformGrok) {
		if mapped := strings.TrimSpace(group.ResolveMessagesDispatchModel(routingModel)); mapped != "" {
			routingModel = mapped
		}
	}
	if IsExplicitImageGenerationIntent(endpoint, routingModel, nil) {
		ctx = WithOpenAIImageGenerationIntent(ctx)
	}
	if channel != nil && channel.RestrictModels && billingSource != BillingModelSourceUpstream {
		pricingModel := billingModelForRestriction(billingSource, requestedModel, channelModel)
		if s.gateway.requestableModelRestricted(ctx, &group.ID, pricingModel) {
			return result, nil
		}
	}
	if s.openAI != nil {
		ctx = s.openAI.withOpenAIQuotaAutoPauseContext(ctx)
	}
	thresholds := defaultAccountSchedulingThresholds()
	if s.gateway.settingService != nil {
		thresholds = s.gateway.settingService.GetAccountSchedulingThresholds(ctx)
	}
	now := time.Now()
	eligible := make([]*Account, 0, len(accounts))
	for i := range accounts {
		account := &accounts[i]
		if !smartRoutingAccountCatalogContains(account, routingModel) ||
			!s.gateway.isRoutingModelSupportedByAccountWithContext(ctx, account, routingModel) {
			continue
		}
		if channel != nil && channel.RestrictModels && billingSource == BillingModelSourceUpstream &&
			s.gateway.requestableModelRestricted(ctx, &group.ID, resolveAccountUpstreamModel(ctx, account, routingModel)) {
			continue
		}
		result.HasModel = true
		// 错误策略可能把账号持久暂停，但其配置目录仍然存在；停调只影响可调度性。
		if !account.IsActive() || !account.Schedulable {
			continue
		}
		if group.RequireOAuthOnly && !account.IsOAuth() || group.RequirePrivacySet && !account.IsPrivacySet() {
			continue
		}
		if !account.IsSchedulableForModelWithContext(ctx, routingModel) || EvaluateAccountSchedulingThreshold(account, thresholds, now).ShouldPause {
			continue
		}
		if account.Platform == PlatformAnthropic && isAnthropicFableModel(resolveAccountUpstreamModel(ctx, account, routingModel)) &&
			evaluateAnthropicFableSchedulingThreshold(account, thresholds, now).ShouldPause {
			continue
		}
		if !smartRoutingAccountEndpointEligible(ctx, account, routingModel, endpoint) {
			continue
		}
		if account.IsOpenAICompatible() {
			if account.IsShadow() {
				parent, err := s.gateway.accountRepo.GetByID(ctx, *account.ParentAccountID)
				if err != nil {
					return result, fmt.Errorf("load smart routing parent account: %w", err)
				}
				if !parentHealthyForShadow(account, func(int64) *Account { return parent }) {
					continue
				}
			}
			if account.IsGrok() && s.openAI != nil && len(s.openAI.filterGrokFreeQuotaAccountsForOpenAI(ctx, []Account{*account})) == 0 {
				continue
			}
		} else if !s.gateway.isAccountSchedulableForQuota(account) ||
			!s.gateway.isAccountSchedulableForWindowCost(ctx, account, false) ||
			!s.gateway.isAccountSchedulableForRPM(ctx, account, false) {
			continue
		}
		eligible = append(eligible, account)
	}
	if len(eligible) == 0 {
		return result, nil
	}
	concurrency := s.gateway.concurrencyService
	if (platform == PlatformOpenAI || platform == PlatformGrok) && s.openAI != nil && s.openAI.concurrencyService != nil {
		concurrency = s.openAI.concurrencyService
	}
	if concurrency == nil {
		result.Schedulable = true
		return result, nil
	}
	ids := make([]int64, 0, len(eligible))
	for _, account := range eligible {
		ids = append(ids, account.ID)
	}
	counts, err := concurrency.GetAccountConcurrencyBatch(ctx, ids)
	if err != nil {
		return result, fmt.Errorf("load smart routing account concurrency: %w", err)
	}
	for _, account := range eligible {
		// 与并发服务一致，非正上限不限制；这里只读计数，真实槽由最终 handler 竞争。
		if account.Concurrency <= 0 || counts[account.ID] < account.Concurrency {
			result.Schedulable = true
			break
		}
	}
	return result, ctx.Err()
}

// ResolveSmartRoutingModels 返回与智能选组相同的有限模型目录，瞬时满载或限流不改变模型身份。
// 查询或渠道解析失败直接返回错误，调用方不得恢复为默认平台模型列表。
func (s *SmartRoutingService) ResolveSmartRoutingModels(ctx context.Context, group *Group) ([]string, error) {
	if group == nil || group.ID <= 0 || !group.IsActive() {
		return []string{}, nil
	}
	if s == nil || s.gateway == nil || s.gateway.accountRepo == nil {
		return nil, errors.New("smart routing account resolver unavailable")
	}
	forcePlatform, _ := ctx.Value(ctxkey.ForcePlatform).(string)
	if forcePlatform != "" && forcePlatform != group.Platform {
		return []string{}, nil
	}
	ctx = context.WithValue(ctx, ctxkey.Group, group)
	useMixed := forcePlatform == "" && (group.Platform == PlatformAnthropic || group.Platform == PlatformGemini)
	accounts, err := s.listSmartRoutingCatalogAccounts(ctx, group, useMixed)
	if err != nil {
		return nil, fmt.Errorf("load smart routing model catalog: %w", err)
	}
	accounts = filterRequestableModelAccounts(accounts, group.Platform)
	var channel *Channel
	if s.gateway.channelService != nil {
		channel, err = s.gateway.channelService.GetChannelForGroup(ctx, group.ID)
		if err != nil {
			return nil, fmt.Errorf("load smart routing channel: %w", err)
		}
	}
	baseModels := make([]string, 0)
	for i := range accounts {
		account := &accounts[i]
		baseModels = append(baseModels, mergeRequestableModelCandidates(account.GetConfiguredRequestModels(), []Account{*account}, nil, account.Platform)...)
	}
	candidates := mergeRequestableModelCandidates(baseModels, accounts, channel, group.Platform)
	models := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		routingModel := candidate
		billingSource := BillingModelSourceRequested
		if channel != nil {
			mapping := s.gateway.channelService.ResolveChannelMapping(ctx, group.ID, candidate)
			if strings.TrimSpace(mapping.MappedModel) != "" {
				routingModel = mapping.MappedModel
			}
			billingSource = mapping.BillingModelSource
			if channel.RestrictModels && billingSource != BillingModelSourceUpstream &&
				s.gateway.requestableModelRestricted(ctx, &group.ID, billingModelForRestriction(billingSource, candidate, routingModel)) {
				continue
			}
		}
		for i := range accounts {
			account := &accounts[i]
			if !smartRoutingAccountCatalogContains(account, routingModel) ||
				!s.gateway.isRoutingModelSupportedByAccountWithContext(ctx, account, routingModel) {
				continue
			}
			if channel != nil && channel.RestrictModels && billingSource == BillingModelSourceUpstream &&
				s.gateway.requestableModelRestricted(ctx, &group.ID, resolveAccountUpstreamModel(ctx, account, routingModel)) {
				continue
			}
			models = append(models, candidate)
			break
		}
	}
	return models, ctx.Err()
}

// listSmartRoutingCatalogAccounts 仅读取当前分组的配置目录，错误/暂停不会把已知模型变成未知型号。
// 显式 disabled 账号仍不参与目录，实际派发继续要求 active、schedulable 及全部运行时门禁。
func (s *SmartRoutingService) listSmartRoutingCatalogAccounts(ctx context.Context, group *Group, useMixed bool) ([]Account, error) {
	platform := group.Platform
	if useMixed {
		platform = ""
	}
	// 共享模型诊断查询要求 active+schedulable；这里使用固定分组配置查询，避免改变其它诊断契约。
	accounts, err := s.gateway.accountRepo.ListAllWithFilters(ctx, platform, "", "", "", group.ID, "")
	if err != nil {
		return nil, err
	}
	filtered := make([]Account, 0, len(accounts))
	for i := range accounts {
		account := &accounts[i]
		if (account.Status == StatusActive || account.Status == StatusError) &&
			s.gateway.isAccountAllowedForPlatform(account, group.Platform, useMixed) {
			filtered = append(filtered, *account)
		}
	}
	// 配置查询不指定排序，这里维持旧分组候选的优先级/ID 顺序，稳定模型列表结果。
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].Priority != filtered[j].Priority {
			return filtered[i].Priority < filtered[j].Priority
		}
		return filtered[i].ID < filtered[j].ID
	})
	return filtered, nil
}

// smartRoutingGroupEndpointEligible 对齐公开路由分派，防止探针或媒体请求选中无法承接端点的平台。
func smartRoutingGroupEndpointEligible(platform, endpoint string) bool {
	switch {
	case strings.HasSuffix(strings.TrimSuffix(endpoint, "/"), "/contents/generations/tasks"):
		return platform == PlatformOpenAI
	case strings.Contains(endpoint, "/v1beta/models/"):
		if strings.HasPrefix(endpoint, "/antigravity/") {
			return platform == PlatformAntigravity
		}
		return platform == PlatformGemini
	case strings.Contains(endpoint, "/images/batches"):
		return platform == PlatformGemini
	case strings.Contains(endpoint, "/images/"):
		return platform == PlatformOpenAI || platform == PlatformGrok
	case strings.Contains(endpoint, "/videos"):
		return platform == PlatformGrok
	case strings.HasSuffix(endpoint, "/embeddings"), strings.HasSuffix(endpoint, "/alpha/search"):
		return platform == PlatformOpenAI
	case strings.HasSuffix(endpoint, "/responses/input_tokens"):
		return platform == PlatformOpenAI || platform == PlatformGrok || platform == PlatformKimi || platform == PlatformZhipu || platform == PlatformDeepseek || platform == PlatformMiniMax || platform == PlatformOpenCodeGo
	case strings.HasSuffix(endpoint, "/messages/count_tokens"):
		return platform != PlatformAntigravity && platform != PlatformQoder
	case strings.Contains(endpoint, "/responses/"):
		return platform != PlatformQoder
	default:
		return true
	}
}

// smartRoutingAccountCatalogContains 只匹配账号目录中的具体模型，通配符不构成未知模型的目录证据。
// 无显式白名单时复用模型列表的有限平台基线，不能调用 permissive IsModelSupported 推断任意型号。
func smartRoutingAccountCatalogContains(account *Account, model string) bool {
	if account == nil || strings.TrimSpace(model) == "" {
		return false
	}
	catalog := mergeRequestableModelCandidates(account.GetConfiguredRequestModels(), []Account{*account}, nil, account.Platform)
	model = normalizeRequestedModelForLookup(account.Platform, model)
	for _, candidate := range catalog {
		if normalizeRequestedModelForLookup(account.Platform, candidate) == model {
			return true
		}
	}
	return false
}

// smartRoutingAccountEndpointEligible 保留账号类型和端点能力边界；跨平台协议准入另由路由层检查。
func smartRoutingAccountEndpointEligible(ctx context.Context, account *Account, model, endpoint string) bool {
	// Seedance 能力必须由 OpenAI API Key 账号显式声明，不能被默认文本能力替代。
	if strings.HasSuffix(strings.TrimSuffix(endpoint, "/"), "/contents/generations/tasks") {
		return account.Platform == PlatformOpenAI && isOpenAICompatibleAccountEligibleForRequest(ctx, account, PlatformOpenAI, model, false, OpenAIEndpointCapabilitySeedance)
	}
	if strings.Contains(endpoint, "/images/batches") {
		// 批量图片只由 Gemini API Key 或有效 Vertex 服务账号执行，OAuth 不能承接作业。
		return (&GeminiAPIBatchImageProvider{}).SupportsAccount(account) || (&VertexBatchImageProvider{}).SupportsAccount(account)
	}
	if !account.IsOpenAICompatible() {
		return !strings.HasSuffix(endpoint, "/embeddings") && !strings.HasSuffix(endpoint, "/alpha/search")
	}
	capability := OpenAIEndpointCapabilityTextGeneration
	if strings.Contains(endpoint, "/responses") && OpenAIImageGenerationIntentFromContext(ctx) {
		capability = OpenAIEndpointCapabilityResponses
	} else if strings.HasSuffix(endpoint, "/embeddings") {
		if account.Platform != PlatformOpenAI {
			return false
		}
		capability = OpenAIEndpointCapabilityEmbeddings
	} else if strings.HasSuffix(endpoint, "/alpha/search") {
		capability = OpenAIEndpointCapabilityAlphaSearch
	} else if strings.Contains(endpoint, "/images/") || strings.Contains(endpoint, "/videos") || strings.Contains(endpoint, "/batch/images") {
		if account.IsGrok() {
			capability = OpenAIEndpointCapabilityGrokMediaGeneration
		} else if account.Platform == PlatformOpenAI {
			capability = ""
			if !account.SupportsOpenAIImageCapability(OpenAIImagesCapabilityBasic) {
				return false
			}
		}
	}
	return isOpenAICompatibleAccountEligibleForRequest(ctx, account, account.Platform, model, strings.HasSuffix(endpoint, "/responses/compact"), capability)
}

// filterSmartRoutingAccounts 在共享调度快照的读取边界固定最终分组，不修改缓存中的完整账号集合。
func filterSmartRoutingAccounts(ctx context.Context, accounts []Account, groupID *int64) []Account {
	if !isSmartRoutingScoped(ctx) {
		return accounts
	}
	filtered := make([]Account, 0, len(accounts))
	if groupID == nil || *groupID <= 0 {
		return filtered
	}
	for i := range accounts {
		if openAIStickyAccountMatchesGroup(&accounts[i], groupID) {
			filtered = append(filtered, accounts[i])
		}
	}
	return filtered
}
