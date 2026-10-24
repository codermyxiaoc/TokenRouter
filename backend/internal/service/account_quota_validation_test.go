//go:build unit

package service

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccountQuotaLimitNormalizationPreservesFiniteAndDisabledValues(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  any
		want any
	}{
		{"number", 1.25, 1.25}, {"integer", 12, float64(12)}, {"int64", int64(13), float64(13)},
		{"float32", float32(1.5), 1.5}, {"json_number", json.Number("2.5"), 2.5},
		{"numeric_string", " 12.5 ", 12.5}, {"zero_clears", 0, float64(0)},
		{"negative_disables", -3, float64(-3)}, {"negative_string", "-2.5", -2.5}, {"null_clears", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, field := range []string{"quota_limit", "quota_daily_limit", "quota_weekly_limit"} {
				extra := map[string]any{field: tc.raw, "unrelated": true}
				require.NoError(t, normalizeAccountQuotaLimits(extra))
				require.Equal(t, tc.want, extra[field])
				require.Equal(t, true, extra["unrelated"])
			}
		})
	}
	require.NoError(t, normalizeAccountQuotaLimits(nil))
	empty := map[string]any{}
	require.NoError(t, normalizeAccountQuotaLimits(empty))
	require.Empty(t, empty)
}

// 所有管理员写入入口必须在保存前拒绝脏值，批量请求也不能部分成功。
func TestAdminAccountQuotaRejectsMalformedBeforeAnyWrite(t *testing.T) {
	for _, accountType := range []string{AccountTypeAPIKey, AccountTypeBedrock, AccountTypeServiceAccount} {
		for _, bad := range []struct {
			name  string
			value any
		}{
			{"nan", math.NaN()}, {"positive_inf", math.Inf(1)}, {"negative_inf", math.Inf(-1)},
			{"nan_string", "NaN"}, {"inf_string", "Infinity"}, {"overflow_string", "1e309"},
			{"text", "invalid"}, {"empty_string", ""}, {"boolean", true},
			{"object", map[string]any{"value": 10}}, {"array", []any{10}},
		} {
			t.Run(accountType+"/"+bad.name, func(t *testing.T) {
				for _, operation := range []string{"create", "update", "extra", "bulk"} {
					repo := &deprecatedAccountExtraRepoStub{account: &Account{
						ID: 1, Name: "original", Platform: PlatformGemini, Type: accountType,
						Extra: map[string]any{"quota_limit": 100.0},
					}}
					svc := &adminServiceImpl{accountRepo: repo}
					extra := map[string]any{"quota_daily_limit": bad.value}
					var err error
					switch operation {
					case "create":
						_, err = svc.CreateAccount(context.Background(), &CreateAccountInput{Platform: PlatformGemini, Type: accountType, Extra: extra, SkipDefaultGroupBind: true})
					case "update":
						_, err = svc.UpdateAccount(context.Background(), 1, &UpdateAccountInput{Name: "must-not-change", Extra: extra})
					case "extra":
						err = svc.UpdateAccountExtra(context.Background(), 1, extra)
					case "bulk":
						var result *BulkUpdateAccountsResult
						result, err = svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{1, 2}, Extra: extra})
						require.Nil(t, result)
					}
					require.ErrorContains(t, err, "quota_daily_limit", operation)
					require.Nil(t, repo.createdAccount, operation)
					require.Zero(t, repo.updateExtraCalls, operation)
					require.Zero(t, repo.bulkUpdateCalls, operation)
					require.Equal(t, "original", repo.account.Name, operation)
					require.Equal(t, map[string]any{"quota_limit": 100.0}, repo.account.Extra, operation)
				}
			})
		}
	}
}

// 上限校验只针对本次补丁，不借编辑名称或其它运行字段回写历史异常上限。
func TestAdminAccountQuotaUnrelatedUpdatesDoNotRejectHistoricalValues(t *testing.T) {
	repo := &deprecatedAccountExtraRepoStub{account: &Account{
		ID: 1, Platform: PlatformGemini, Type: AccountTypeServiceAccount,
		Extra: map[string]any{"quota_limit": "legacy-invalid"},
	}}
	svc := &adminServiceImpl{accountRepo: repo}
	_, err := svc.UpdateAccount(context.Background(), 1, &UpdateAccountInput{Name: "renamed"})
	require.NoError(t, err)
	require.Equal(t, "legacy-invalid", repo.account.Extra["quota_limit"])
	require.NoError(t, svc.UpdateAccountExtra(context.Background(), 1, map[string]any{"other": true}))
	require.Equal(t, map[string]any{"other": true}, repo.lastExtraUpdates)
	result, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{1}, Extra: map[string]any{"other": true}})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, map[string]any{"other": true}, repo.lastBulkExtraUpdate)
}

func TestAdminAccountQuotaVertexAndAPIKeyShareWriteContract(t *testing.T) {
	for _, accountType := range []string{AccountTypeAPIKey, AccountTypeServiceAccount} {
		t.Run(accountType, func(t *testing.T) {
			repo := &deprecatedAccountExtraRepoStub{}
			svc := &adminServiceImpl{accountRepo: repo}
			account, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
				Platform: PlatformGemini, Type: accountType, SkipDefaultGroupBind: true,
				Extra: map[string]any{"quota_limit": "100", "quota_daily_limit": "10.5", "quota_weekly_limit": 50},
			})
			require.NoError(t, err)
			require.Equal(t, 100.0, account.Extra["quota_limit"])
			require.Equal(t, 10.5, account.Extra["quota_daily_limit"])
			require.Equal(t, 50.0, account.Extra["quota_weekly_limit"])
			account, err = svc.UpdateAccount(context.Background(), account.ID, &UpdateAccountInput{Extra: map[string]any{"quota_limit": -1, "quota_daily_limit": nil, "quota_weekly_limit": 0}})
			require.NoError(t, err)
			require.False(t, account.HasAnyQuotaLimit())
		})
	}
}
