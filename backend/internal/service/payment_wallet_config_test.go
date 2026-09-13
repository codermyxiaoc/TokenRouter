package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// 余额支付独立于余额充值入口和外部渠道，并且只有明确开启后才可使用。
func TestPaymentConfigWalletPaymentDefaultsAndRead(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		stored string
		want   bool
	}{
		{name: "missing"},
		{name: "disabled", stored: "false"},
		{name: "invalid", stored: "invalid"},
		{name: "enabled", stored: "true", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &paymentConfigSettingRepoStub{values: map[string]string{
				SettingWalletPaymentEnabled: tc.stored,
				SettingBalancePayDisabled:   "true",
				SettingEnabledPaymentTypes:  "stripe",
			}}
			svc := NewPaymentConfigService(nil, repo, nil)
			cfg, err := svc.GetPaymentConfig(context.Background())
			require.NoError(t, err)
			require.Equal(t, tc.want, cfg.WalletPaymentEnabled)
			require.True(t, cfg.BalanceDisabled)
			require.Equal(t, []string{"stripe"}, cfg.EnabledTypes)
		})
	}
}

// 专用支付配置的部分更新必须保留省略值，显式 false 才关闭余额支付。
func TestPaymentConfigWalletPaymentPatchSemantics(t *testing.T) {
	t.Parallel()

	repo := &paymentConfigSettingRepoStub{values: map[string]string{
		SettingWalletPaymentEnabled: "false",
		SettingBalancePayDisabled:   "true",
		SettingEnabledPaymentTypes:  "stripe",
	}}
	svc := NewPaymentConfigService(nil, repo, nil)
	for _, tc := range []struct {
		body string
		want string
	}{
		{body: `{"wallet_payment_enabled":true}`, want: "true"},
		{body: `{"enabled":true}`, want: "true"},
		{body: `{"wallet_payment_enabled":false}`, want: "false"},
	} {
		var req UpdatePaymentConfigRequest
		require.NoError(t, json.Unmarshal([]byte(tc.body), &req))
		require.NoError(t, svc.UpdatePaymentConfig(context.Background(), req))
		require.Equal(t, tc.want, repo.values[SettingWalletPaymentEnabled])
		if req.WalletPaymentEnabled == nil {
			require.NotContains(t, repo.updates, SettingWalletPaymentEnabled)
		}
		require.Equal(t, "true", repo.values[SettingBalancePayDisabled])
		require.Equal(t, "stripe", repo.values[SettingEnabledPaymentTypes])
	}
}
