package service

import (
	"log/slog"
	"strings"
)

// ServiceTierBillingResolution 描述请求档位与上游实际档位之间的计费决策。
type ServiceTierBillingResolution struct {
	Requested  string // 请求发送的档位，空值表示未指定
	Observed   string // 上游响应声明的档位，空值表示未声明
	Billing    string // 实际用于计费和日志的档位
	Downgraded bool   // 计费档位是否低于请求档位
}

// ResolveBillingServiceTier 只信任上游把价格降档的声明，绝不因响应内容升档收费。
func ResolveBillingServiceTier(requested, observed string) ServiceTierBillingResolution {
	requested = normalizeBillingServiceTier(requested)
	observed = normalizeBillingServiceTier(observed)
	resolution := ServiceTierBillingResolution{Requested: requested, Observed: observed, Billing: requested}
	if observed == "" || observed == requested {
		return resolution
	}
	observedRank, known := serviceTierCostRank(observed)
	if !known {
		return resolution
	}
	requestedRank, _ := serviceTierCostRank(requested)
	if observedRank >= requestedRank {
		return resolution
	}
	resolution.Billing = observed
	resolution.Downgraded = true
	return resolution
}

// serviceTierCostRank 按相对成本排序，数值越低表示价格越低；未知档位不参与决策。
func serviceTierCostRank(tier string) (rank int, known bool) {
	switch normalizeBillingServiceTier(tier) {
	case "flex":
		return 0, true
	case "", "default", "standard", "auto", "scale":
		return 1, true
	case "priority", "fast":
		return 2, true
	default:
		return 1, false
	}
}

// ResolveOpenAIServiceTierBilling 按凭据类型应用上游 service tier 计费契约。
// 公共 OpenAI 响应的档位声明可降低计费；私有 ChatGPT Codex 常将有效 Fast 回显为
// default，因此 OAuth 类凭据保留最终出站档位。
func ResolveOpenAIServiceTierBilling(account *Account, requested, observed string) ServiceTierBillingResolution {
	if account != nil && account.IsOpenAIOAuthLike() && codexOAuthResponseTierIsNonAuthoritative(observed) {
		return ServiceTierBillingResolution{
			Requested: normalizeBillingServiceTier(requested),
			Observed:  normalizeBillingServiceTier(observed),
			Billing:   normalizeBillingServiceTier(requested),
		}
	}
	return ResolveBillingServiceTier(requested, observed)
}

func codexOAuthResponseTierIsNonAuthoritative(observed string) bool {
	switch normalizeBillingServiceTier(observed) {
	case "default":
		return true
	default:
		return false
	}
}

// ApplyOpenAIServiceTierBillingResolution 仅在上游档位对当前凭据有权威性时降档。
func ApplyOpenAIServiceTierBillingResolution(account *Account, result *OpenAIForwardResult) ServiceTierBillingResolution {
	if result == nil {
		return ServiceTierBillingResolution{}
	}
	resolution := ResolveOpenAIServiceTierBilling(account, stringValueOrEmpty(result.ServiceTier), result.UpstreamResponseServiceTier)
	if resolution.Downgraded {
		billing := resolution.Billing
		result.ServiceTier = &billing
	}
	return resolution
}

// ApplyForwardServiceTierBillingResolution 是通用 Anthropic 转发结果的对应处理。
func ApplyForwardServiceTierBillingResolution(result *ForwardResult) ServiceTierBillingResolution {
	if result == nil {
		return ServiceTierBillingResolution{}
	}
	requested := stringValueOrEmpty(result.ServiceTier)
	if requested == "" {
		requested = strings.TrimSpace(result.Usage.Speed)
	}
	resolution := ResolveBillingServiceTier(requested, result.UpstreamResponseServiceTier)
	if resolution.Downgraded {
		billing := resolution.Billing
		result.ServiceTier = &billing
		// fork 的通用计费路径历史上从 Usage.Speed 取档位；同步更新它，
		// 让费用、Usage Log 和结果元数据保持同一口径。
		if result.Usage.Speed != "" {
			result.Usage.Speed = billing
		}
	}
	return resolution
}

// logServiceTierBillingDowngrade 为每次降档计费留下审计日志。
func logServiceTierBillingDowngrade(component string, account *Account, requestID string, resolution ServiceTierBillingResolution) {
	if !resolution.Downgraded {
		return
	}
	attrs := []any{
		"component", component,
		"request_id", strings.TrimSpace(requestID),
		"requested_tier", resolution.Requested,
		"response_tier", resolution.Observed,
		"billed_tier", resolution.Billing,
	}
	if account != nil {
		attrs = append(attrs, "platform", account.Platform, "account_id", account.ID)
	}
	slog.Info("billing.service_tier_downgraded", attrs...)
}
