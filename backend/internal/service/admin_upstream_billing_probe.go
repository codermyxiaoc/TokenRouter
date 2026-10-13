package service

import (
	"context"
	"maps"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
)

// @project-doc docs/interfaces/upstream_billing_probe.md#upstream_billing_probe_settings
// normalizeBillingProbeSwitches 将顶层显式设置与兼容 Extra 增量规范化，禁止伪造观测结果。
func normalizeBillingProbeSwitches(extra map[string]any, probe, syncRate *bool) (*bool, *bool, error) {
	for _, field := range []struct {
		key    string
		target **bool
	}{
		{UpstreamBillingProbeEnabledExtraKey, &probe}, {UpstreamBillingRateSyncEnabledExtraKey, &syncRate},
	} {
		if value, exists := extra[field.key]; exists {
			enabled, ok := value.(bool)
			if !ok {
				return nil, nil, infraerrors.BadRequest("INVALID_UPSTREAM_BILLING_PROBE_SETTINGS", "probe and rate sync switches must be booleans")
			}
			if *field.target == nil {
				copyValue := enabled
				*field.target = &copyValue
			}
		}
	}
	if probe != nil && !*probe {
		value := false
		syncRate = &value
	} else if syncRate != nil && *syncRate {
		value := true
		probe = &value
	}
	return probe, syncRate, nil
}

// applyBillingProbeSwitches 仅管理显式开关；未携带时保留已有设置，新账号默认关闭。
func applyBillingProbeSwitches(account *Account, probe, syncRate *bool) error {
	if account == nil {
		return nil
	}
	if (probe != nil && *probe || syncRate != nil && *syncRate) && !IsUpstreamBillingProbeIdentity(account.Platform, account.Type) {
		return ErrUpstreamBillingProbeAccountInvalid
	}
	account.Extra = maps.Clone(account.Extra)
	if account.Extra == nil {
		account.Extra = map[string]any{}
	}
	if !IsUpstreamBillingProbeIdentity(account.Platform, account.Type) {
		delete(account.Extra, UpstreamBillingProbeExtraKey)
		delete(account.Extra, UpstreamBillingProbeEnabledExtraKey)
		delete(account.Extra, UpstreamBillingRateSyncEnabledExtraKey)
		return nil
	}
	if probe != nil {
		account.Extra[UpstreamBillingProbeEnabledExtraKey] = *probe
	}
	if syncRate != nil {
		account.Extra[UpstreamBillingRateSyncEnabledExtraKey] = *syncRate
	}
	return nil
}

// 持久层锁内复核显式倍率与开关，避免旧编辑请求覆盖后台同步得到的新值。
type accountBillingSettingsWriter interface {
	UpdateWithAccountBillingSettings(context.Context, *Account, *bool, *bool, *float64) error
}
