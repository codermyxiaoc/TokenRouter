package service

import "strings"

// accountDeclaresCatalogModel 只约束公开目录的声明依据，不改变普通请求的未命中映射透传规则。
// 有映射而没有白名单的账号只能为命中的输入型号背书，价格条目不能替它声明其它能力。
func accountDeclaresCatalogModel(account *Account, model string) bool {
	if account == nil || strings.TrimSpace(model) == "" {
		return false
	}
	// 这些平台继续使用各自已有的透传、平台映射或站点目录合同。
	if account.IsOpenAIPassthroughEnabled() || account.Platform == PlatformAntigravity || account.Platform == PlatformQoder {
		return true
	}
	if account.Platform == PlatformVideo {
		// 视频模型的显式端点绑定同样属于目录声明；只认可精确键，不扩展端点权限或放行其它型号。
		if configuration, err := account.VideoConfiguration(); err == nil {
			if _, declared := configuration.ModelBindings[model]; declared {
				return true
			}
		}
	}
	mapping := account.GetModelMapping()
	whitelist, _ := resolveFinalModelWhitelist(account.Platform, account.Credentials, mapping)
	if len(whitelist) > 0 || len(mapping) == 0 {
		// 最终白名单和平台能力仍由后续正式支持校验决定，不能在此重复映射。
		return true
	}
	_, matched := account.ResolveMappedModel(model)
	return matched
}

// 显式渠道映射可指向账号已经声明的最终目标；仅有价格或原始模型同名不能取得这项资格。
// 目标仅用于校验，不被添加为独立公开条目，也不继续展开映射链。
func accountCatalogHasMappedTarget(account *Account, model string) bool {
	if account == nil {
		return false
	}
	model = normalizeRequestedModelForLookup(account.Platform, strings.TrimSpace(model))
	if model == "" || strings.Contains(model, "*") {
		return false
	}
	for _, target := range account.GetModelMapping() {
		target = normalizeRequestedModelForLookup(account.Platform, strings.TrimSpace(target))
		if target != "" && !strings.Contains(target, "*") && target == model {
			return true
		}
	}
	return false
}

func accountDeclaresChannelCatalogModel(account *Account, model string, channelMapped bool) bool {
	return accountDeclaresCatalogModel(account, model) || channelMapped && accountCatalogHasMappedTarget(account, model)
}

// 只从有限候选中确认请求身份；通配渠道映射不能凭空创建任意探针型号。
func requestableCatalogCandidateContains(accounts []Account, channel *Channel, platform, model string) bool {
	model = normalizeRequestedModelForLookup(platform, strings.TrimSpace(model))
	for _, candidate := range mergeRequestableModelCandidates(configuredRequestModelsFromAccounts(accounts, platform), accounts, channel, platform) {
		if normalizeRequestedModelForLookup(platform, candidate) == model {
			return true
		}
	}
	return false
}
