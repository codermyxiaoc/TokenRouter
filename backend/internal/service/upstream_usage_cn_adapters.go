package service

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// CN 用量适配器只读查询国产供应商自己的余额/窗口接口；它们不写账号 Extra，
// 也不参与调度。周期监控若启用，另由 CNProviderBalanceCheckService 写入快照。

type kimiCodingUsageAdapter struct{}

func (*kimiCodingUsageAdapter) Name() string { return UpstreamUsageAdapterKimiCoding }
func (*kimiCodingUsageAdapter) Query(ctx context.Context, client *upstreamUsageHTTPClient) (*UpstreamUsageInfo, error) {
	endpoint, err := cnUsageEndpoint(client.baseURL, "/v1/usages", true)
	if err != nil {
		return nil, ErrUpstreamUsageConfigInvalid.WithCause(err)
	}
	body, status, err := client.getURL(ctx, endpoint, true)
	if err != nil {
		return nil, err
	}
	if err := validateCNUsageStatus(status); err != nil {
		return nil, err
	}
	tiers := parseKimiUsageTiers(body)
	return cnUsageLimits(PlatformKimi, tiers)
}

type zhipuCodingUsageAdapter struct{}

func (*zhipuCodingUsageAdapter) Name() string { return UpstreamUsageAdapterZhipuCoding }
func (*zhipuCodingUsageAdapter) Query(ctx context.Context, client *upstreamUsageHTTPClient) (*UpstreamUsageInfo, error) {
	endpoint, err := cnUsageEndpoint(client.baseURL, "/api/monitor/usage/quota/limit", false)
	if err != nil {
		return nil, ErrUpstreamUsageConfigInvalid.WithCause(err)
	}
	// 智谱 Coding Plan 额度接口使用裸 API key，不是 Bearer token。
	teamHeaders := map[string]string{"Authorization": client.apiKey}
	organization := strings.TrimSpace(client.account.GetCredential("zhipu_organization"))
	if organization != "" {
		if strings.ContainsAny(organization, "\r\n") {
			return nil, ErrUpstreamUsageConfigInvalid
		}
		// 团队版接口通过 type=2 和组织请求头区分个人 Coding Plan。
		parsed, parseErr := url.Parse(endpoint)
		if parseErr != nil || parsed.Scheme == "" || parsed.Host == "" {
			return nil, ErrUpstreamUsageConfigInvalid
		}
		query := parsed.Query()
		query.Set("type", "2")
		parsed.RawQuery = query.Encode()
		endpoint = parsed.String()
		teamHeaders["bigmodel-organization"] = organization
		if project := strings.TrimSpace(client.account.GetCredential("zhipu_project")); project != "" {
			if strings.ContainsAny(project, "\r\n") {
				return nil, ErrUpstreamUsageConfigInvalid
			}
			teamHeaders["bigmodel-project"] = project
		}
	}
	body, status, err := client.getURLWithHeaders(ctx, endpoint, teamHeaders)
	if err != nil {
		return nil, err
	}
	if err := validateCNUsageStatus(status); err != nil {
		return nil, err
	}
	if success := gjson.GetBytes(body, "success"); success.Exists() && !success.Bool() {
		return nil, ErrUpstreamUsageInvalidResponse
	}
	tiers := parseZhipuTokenTiers(gjson.GetBytes(body, "data"))
	return cnUsageLimits(PlatformZhipu, tiers)
}

// @project-doc docs/interfaces/minimax_upstream.md#minimax_usage_and_limits
// minimaxCodingUsageAdapter 只替换固定用量路径，保留管理员配置的主机，避免把中转密钥发往官方站点。
type minimaxCodingUsageAdapter struct{}

func (*minimaxCodingUsageAdapter) Name() string { return UpstreamUsageAdapterMiniMaxCoding }
func (*minimaxCodingUsageAdapter) Query(ctx context.Context, client *upstreamUsageHTTPClient) (*UpstreamUsageInfo, error) {
	endpoint, err := cnUsageEndpoint(client.baseURL, "/v1/api/openplatform/coding_plan/remains", false)
	if err != nil {
		return nil, ErrUpstreamUsageConfigInvalid.WithCause(err)
	}
	body, status, err := client.getURLWithHeaders(ctx, endpoint, map[string]string{
		"Authorization":   "Bearer " + client.apiKey,
		"Content-Type":    "application/json",
		"Accept-Language": "en-US,en",
	})
	if err != nil {
		return nil, err
	}
	if err := validateCNUsageStatus(status); err != nil {
		return nil, err
	}
	// 上游也会在 HTTP 200 内返回业务错误；缺少明确成功状态不能覆盖最近成功快照。
	code := gjson.GetBytes(body, "base_resp.status_code")
	value, ok := cnParseF64(code.Value())
	if !gjson.ValidBytes(body) || !ok || value != 0 {
		return nil, ErrUpstreamUsageInvalidResponse
	}
	return cnUsageLimits(PlatformMiniMax, parseMiniMaxUsageTiers(body))
}

type kimiBalanceUsageAdapter struct{}

func (*kimiBalanceUsageAdapter) Name() string { return UpstreamUsageAdapterKimiBalance }
func (*kimiBalanceUsageAdapter) Query(ctx context.Context, client *upstreamUsageHTTPClient) (*UpstreamUsageInfo, error) {
	endpoint, err := cnUsageEndpoint(client.baseURL, "/v1/users/me/balance", false)
	if err != nil {
		return nil, ErrUpstreamUsageConfigInvalid.WithCause(err)
	}
	body, status, err := client.getURL(ctx, endpoint, true)
	if err != nil {
		return nil, err
	}
	if err := validateCNUsageStatus(status); err != nil {
		return nil, err
	}
	if code := gjson.GetBytes(body, "code"); code.Exists() && code.Int() != 0 {
		return nil, ErrUpstreamUsageInvalidResponse
	}
	value, ok := cnParseF64(gjson.GetBytes(body, "data.available_balance").Value())
	if !ok || !validFiniteNumber(value) {
		return nil, ErrUpstreamUsageInvalidResponse
	}
	return &UpstreamUsageInfo{
		Provider: PlatformKimi,
		Mode:     "balance",
		Unit:     "CNY",
		Balance:  &UpstreamUsageAmount{Remaining: &value},
	}, nil
}

type deepseekBalanceUsageAdapter struct{}

func (*deepseekBalanceUsageAdapter) Name() string { return UpstreamUsageAdapterDeepseekBalance }
func (*deepseekBalanceUsageAdapter) Query(ctx context.Context, client *upstreamUsageHTTPClient) (*UpstreamUsageInfo, error) {
	endpoint, err := cnUsageEndpoint(client.baseURL, "/user/balance", false)
	if err != nil {
		return nil, ErrUpstreamUsageConfigInvalid.WithCause(err)
	}
	body, status, err := client.getURL(ctx, endpoint, true)
	if err != nil {
		return nil, err
	}
	if err := validateCNUsageStatus(status); err != nil {
		return nil, err
	}
	var balances []UpstreamUsageBalanceEntry
	available := true
	if raw := gjson.GetBytes(body, "is_available"); raw.Exists() {
		available = raw.Bool()
	}
	balanceInfos := gjson.GetBytes(body, "balance_infos")
	if !balanceInfos.Exists() || !balanceInfos.IsArray() {
		return nil, ErrUpstreamUsageInvalidResponse
	}
	balanceInfos.ForEach(func(_, item gjson.Result) bool {
		currency := strings.ToUpper(strings.TrimSpace(item.Get("currency").String()))
		totalBalance := item.Get("total_balance")
		value, ok := cnParseF64(totalBalance.Value())
		if !totalBalance.Exists() || !ok || !validFiniteNumber(value) {
			return true
		}
		if currency == "" {
			currency = "CNY"
		}
		balances = append(balances, UpstreamUsageBalanceEntry{Currency: currency, Remaining: value})
		return true
	})
	if len(balances) == 0 {
		return nil, ErrUpstreamUsageInvalidResponse
	}
	primary := balances[0].Remaining
	return &UpstreamUsageInfo{
		Provider:  PlatformDeepseek,
		Mode:      "balance",
		Unit:      balances[0].Currency,
		Balance:   &UpstreamUsageAmount{Remaining: &primary},
		Balances:  balances,
		Available: &available,
	}, nil
}

func cnUsageLimits(provider string, tiers []CNQuotaTier) (*UpstreamUsageInfo, error) {
	if len(tiers) == 0 {
		return nil, ErrUpstreamUsageInvalidResponse
	}
	limits := make([]UpstreamUsageLimit, 0, len(tiers))
	for _, tier := range tiers {
		name := strings.TrimSpace(tier.Window)
		if name == "" || !validFiniteNumber(tier.UsedPercent) || tier.UsedPercent < 0 {
			return nil, ErrUpstreamUsageInvalidResponse
		}
		remaining := 100 - tier.UsedPercent
		if remaining < 0 {
			remaining = 0
		}
		var resetAt *time.Time
		if parsed, err := time.Parse(time.RFC3339, tier.ResetAt); err == nil && !parsed.IsZero() {
			utc := parsed.UTC()
			resetAt = &utc
		}
		used := tier.UsedPercent
		limit := float64(100)
		limits = append(limits, UpstreamUsageLimit{Name: name, Used: &used, Limit: &limit, Remaining: &remaining, ResetAt: resetAt})
	}
	return &UpstreamUsageInfo{Provider: provider, Mode: "limits", Unit: "PERCENT", Limits: limits}, nil
}

func validateCNUsageStatus(status int) error {
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == 401 || status == 403:
		return ErrUpstreamUsageAuthFailed
	case status == 429:
		return ErrUpstreamUsageRateLimited
	case status == 404 || status == 405:
		return ErrUpstreamUsageUnsupported
	default:
		return ErrUpstreamUsageInvalidResponse
	}
}

// cnUsageEndpoint 从已通过策略校验的账号 Base URL 派生固定路径。主机始终来自账号配置，
// 不从响应或任意用户输入推断，也不会把凭据发往其它主机。
func cnUsageEndpoint(base, path string, preserveCodingPrefix bool) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(base))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("invalid base URL")
	}
	prefix := ""
	if preserveCodingPrefix && strings.Contains(strings.ToLower(parsed.Path), "/coding") {
		prefix = "/coding"
	}
	parsed.Path = strings.TrimRight(prefix, "/") + "/" + strings.TrimLeft(path, "/")
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.User = nil
	return strings.TrimRight(parsed.String(), "/"), nil
}
