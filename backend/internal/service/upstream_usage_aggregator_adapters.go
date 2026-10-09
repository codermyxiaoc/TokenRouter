package service

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

const (
	UpstreamUsageAdapterCline       = "cline"
	UpstreamUsageAdapterCommandCode = "command_code"
)

type clineUsageAdapter struct{}

func (*clineUsageAdapter) Name() string { return UpstreamUsageAdapterCline }

type commandCodeUsageAdapter struct{}

func (*commandCodeUsageAdapter) Name() string { return UpstreamUsageAdapterCommandCode }

// 官方只读账户接口固定域名；中继账号必须选择通用查询协议，不能外发中继凭据。
func providerUsageJSON(ctx context.Context, client *upstreamUsageHTTPClient, target string) (gjson.Result, int, error) {
	body, status, err := client.getURL(ctx, target, true)
	if err != nil {
		return gjson.Result{}, status, err
	}
	if err := validateCNUsageStatus(status); err != nil {
		return gjson.Result{}, status, err
	}
	if !gjson.ValidBytes(body) {
		return gjson.Result{}, status, ErrUpstreamUsageInvalidResponse
	}
	return gjson.ParseBytes(body), status, nil
}

func clineUsageData(value gjson.Result) (gjson.Result, error) {
	if success := value.Get("success"); success.Exists() {
		if success.Type != gjson.True {
			return gjson.Result{}, ErrUpstreamUsageInvalidResponse
		}
		return value.Get("data"), nil
	}
	return value, nil
}

func (*clineUsageAdapter) Query(ctx context.Context, client *upstreamUsageHTTPClient) (*UpstreamUsageInfo, error) {
	if client == nil || client.account == nil || !client.account.IsCline() || !isOfficialProviderHost(client.baseURL, "api.cline.bot") {
		return nil, ErrUpstreamUsageUnsupported
	}
	me, _, err := providerUsageJSON(ctx, client, "https://api.cline.bot/api/v1/users/me")
	if err != nil {
		return nil, err
	}
	me, err = clineUsageData(me)
	if err != nil {
		return nil, err
	}
	userID := strings.TrimSpace(me.Get("id").String())
	if userID == "" {
		return nil, ErrUpstreamUsageInvalidResponse
	}
	balance, _, err := providerUsageJSON(ctx, client, "https://api.cline.bot/api/v1/users/"+url.PathEscape(userID)+"/balance")
	if err != nil {
		return nil, err
	}
	balance, err = clineUsageData(balance)
	if err != nil {
		return nil, err
	}
	value, ok := cnParseF64(balance.Get("balance").Value())
	if !ok || !validFiniteNumber(value) {
		return nil, ErrUpstreamUsageInvalidResponse
	}
	remaining := value / 1_000_000
	result := &UpstreamUsageInfo{Provider: PlatformCline, Mode: "wallets", Unit: "USD", Balance: &UpstreamUsageAmount{Remaining: &remaining}}
	plan, status, err := providerUsageJSON(ctx, client, "https://api.cline.bot/api/v1/users/me/plan/usage-limits")
	if status == http.StatusNotFound {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	plan, err = clineUsageData(plan)
	if err != nil {
		return nil, err
	}
	if !plan.Get("limits").IsArray() {
		return nil, ErrUpstreamUsageInvalidResponse
	}
	for _, item := range plan.Get("limits").Array() {
		window := map[string]string{"five_hour": "5h", "weekly": "weekly", "monthly": "monthly"}[item.Get("type").String()]
		if window == "" {
			continue
		}
		used, valid := cnParseF64(item.Get("percentUsed").Value())
		if !valid || !validNonNegativeNumber(used) {
			return nil, ErrUpstreamUsageInvalidResponse
		}
		total := float64(100)
		limit := UpstreamUsageLimit{Name: window, Unit: "PERCENT", Used: &used, Limit: &total}
		if reset, e := time.Parse(time.RFC3339Nano, item.Get("resetsAt").String()); e == nil {
			limit.ResetAt = &reset
		}
		result.Limits = append(result.Limits, limit)
	}
	if len(result.Limits) > 0 {
		result.Subscription = &UpstreamUsageSubscription{PlanName: "ClinePass", Limits: result.Limits}
	}
	return result, nil
}

func (*commandCodeUsageAdapter) Query(ctx context.Context, client *upstreamUsageHTTPClient) (*UpstreamUsageInfo, error) {
	if client == nil || client.account == nil || !client.account.IsCommandCode() || !isOfficialProviderHost(client.baseURL, "api.commandcode.ai") {
		return nil, ErrUpstreamUsageUnsupported
	}
	who, _, err := providerUsageJSON(ctx, client, "https://api.commandcode.ai/alpha/whoami")
	if err != nil {
		return nil, err
	}
	// 身份响应异常时不能把组织账号降格为个人账号，防止查询到另一套余额。
	org := who.Get("org")
	if !who.IsObject() || (org.Exists() && org.Type != gjson.Null && (!org.IsObject() || org.Get("id").Type != gjson.String || strings.TrimSpace(org.Get("id").String()) == "")) {
		return nil, ErrUpstreamUsageInvalidResponse
	}
	query := url.Values{}
	if org := strings.TrimSpace(who.Get("org.id").String()); org != "" {
		query.Set("orgId", org)
	}
	get := func(path string, q url.Values) (gjson.Result, error) {
		target := "https://api.commandcode.ai" + path
		if len(q) > 0 {
			target += "?" + q.Encode()
		}
		data, _, e := providerUsageJSON(ctx, client, target)
		return data, e
	}
	credits, err := get("/alpha/billing/credits", query)
	if err != nil {
		return nil, err
	}
	if !credits.Get("credits").IsObject() {
		return nil, ErrUpstreamUsageInvalidResponse
	}
	amounts := map[string]float64{}
	for _, key := range []string{"monthlyCredits", "purchasedCredits", "freeCredits"} {
		value, ok := cnParseF64(credits.Get("credits." + key).Value())
		if !ok || !validNonNegativeNumber(value) {
			return nil, ErrUpstreamUsageInvalidResponse
		}
		amounts[key] = value
	}
	remaining := amounts["monthlyCredits"] + amounts["purchasedCredits"] + amounts["freeCredits"]
	result := &UpstreamUsageInfo{Provider: PlatformCommandCode, Mode: "wallets", Unit: "USD", Balance: &UpstreamUsageAmount{Remaining: &remaining}}
	// purchased 钱包单列，用于窗口是否约束请求的判断，不改写普通总余额。
	result.Balances = []UpstreamUsageBalanceEntry{{Currency: "USD", Kind: "monthly", Remaining: amounts["monthlyCredits"]}, {Currency: "USD", Kind: "purchased", Remaining: amounts["purchasedCredits"]}, {Currency: "USD", Kind: "free", Remaining: amounts["freeCredits"]}}
	if credits.Get("windowLimits.limited").Bool() {
		for _, pair := range [][2]string{{"fiveHour", "5h"}, {"weekly", "weekly"}} {
			node := credits.Get("windowLimits." + pair[0])
			if !node.Exists() || node.Type == gjson.Null {
				continue
			}
			used, okUsed := cnParseF64(node.Get("used").Value())
			total, okTotal := cnParseF64(node.Get("cap").Value())
			if !okUsed || !okTotal || !validNonNegativeNumber(used) || !validNonNegativeNumber(total) {
				return nil, ErrUpstreamUsageInvalidResponse
			}
			if total == 0 {
				continue
			}
			limit := UpstreamUsageLimit{Name: pair[1], Unit: "USD", Used: &used, Limit: &total}
			if millis := node.Get("resetAt").Int(); millis > 0 {
				reset := time.UnixMilli(millis).UTC()
				limit.ResetAt = &reset
			}
			result.Limits = append(result.Limits, limit)
		}
	}
	subscription, err := get("/alpha/billing/subscriptions", query)
	if err != nil {
		return nil, err
	}
	if start := strings.TrimSpace(subscription.Get("data.currentPeriodStart").String()); start != "" {
		query.Set("since", start)
		summary, err := get("/alpha/usage/summary", query)
		if err != nil {
			return nil, err
		}
		node := summary.Get("totalMonthlyCredits")
		if !node.Exists() {
			node = summary.Get("totalCost")
		}
		used, valid := cnParseF64(node.Value())
		if !valid || !validNonNegativeNumber(used) {
			return nil, ErrUpstreamUsageInvalidResponse
		}
		total := used + amounts["monthlyCredits"]
		limit := UpstreamUsageLimit{Name: "monthly", Unit: "USD", Used: &used, Limit: &total}
		if end, e := time.Parse(time.RFC3339Nano, subscription.Get("data.currentPeriodEnd").String()); e == nil {
			limit.ResetAt = &end
			result.ExpiresAt = &end
		}
		result.Limits = append(result.Limits, limit)
	}
	if name := subscription.Get("data.planId").String(); name != "" {
		monthlyRemaining := amounts["monthlyCredits"]
		result.Subscription = &UpstreamUsageSubscription{PlanName: name, Remaining: &monthlyRemaining, Limits: result.Limits, ExpiresAt: result.ExpiresAt}
	}
	return result, nil
}
