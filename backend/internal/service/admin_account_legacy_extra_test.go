package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/pkg/xai"
	"github.com/stretchr/testify/require"
)

func TestCreateAccountDiscardsForgedBillingProbeSnapshot(t *testing.T) {
	repo := &accountServiceTestRepo{}
	created, err := (&adminServiceImpl{accountRepo: repo}).CreateAccount(context.Background(), &CreateAccountInput{
		Name:                 "upstream",
		Platform:             PlatformOpenAI,
		Type:                 AccountTypeAPIKey,
		Credentials:          map[string]any{"api_key": "sk-test"},
		SkipDefaultGroupBind: true,
		Extra: map[string]any{
			UpstreamBillingProbeEnabledExtraKey: true,
			UpstreamBillingProbeExtraKey:        map[string]any{"status": "ok"},
			"custom":                            "value",
		},
	})

	require.NoError(t, err)
	require.Equal(t, true, created.Extra[UpstreamBillingProbeEnabledExtraKey])
	require.NotContains(t, created.Extra, UpstreamBillingProbeExtraKey)
	require.Equal(t, "value", created.Extra["custom"])
}

func TestUpdateAccountDiscardsForgedBillingProbeSnapshot(t *testing.T) {
	accountID := int64(110)
	repo := &accountServiceTestRepo{accounts: map[int64]*Account{
		accountID: {
			ID:       accountID,
			Platform: PlatformOpenAI,
			Type:     AccountTypeAPIKey,
			Status:   StatusActive,
			Extra: map[string]any{
				UpstreamBillingProbeEnabledExtraKey: true,
				UpstreamBillingProbeExtraKey:        map[string]any{"status": "ok"},
			},
		},
	}}

	updated, err := (&adminServiceImpl{accountRepo: repo}).UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		Extra: map[string]any{
			UpstreamBillingProbeEnabledExtraKey: false,
			UpstreamBillingProbeExtraKey:        map[string]any{"status": "forged"},
			"custom":                            "value",
		},
	})

	require.NoError(t, err)
	require.Equal(t, true, updated.Extra[UpstreamBillingProbeEnabledExtraKey])
	require.NotContains(t, updated.Extra, UpstreamBillingProbeExtraKey)
	require.Equal(t, "value", updated.Extra["custom"])
}

func TestBulkUpdateAccountsDiscardsForgedBillingProbeSnapshot(t *testing.T) {
	repo := &accountServiceTestRepo{}
	result, err := (&adminServiceImpl{accountRepo: repo}).BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
		AccountIDs: []int64{1},
		Extra: map[string]any{
			UpstreamBillingProbeEnabledExtraKey: true,
			UpstreamBillingProbeExtraKey:        map[string]any{"status": "ok"},
			"custom":                            "value",
		},
	})

	require.NoError(t, err)
	require.Equal(t, 1, result.Success)
	require.Len(t, repo.bulkUpdates, 1)
	require.NotContains(t, repo.bulkUpdates[0].Extra, UpstreamBillingProbeEnabledExtraKey)
	require.NotContains(t, repo.bulkUpdates[0].Extra, UpstreamBillingProbeExtraKey)
	require.Equal(t, "value", repo.bulkUpdates[0].Extra["custom"])
}

func TestUpdateAccountPreservesGrokBillingSnapshotForUnrelatedEdit(t *testing.T) {
	accountID := int64(112)
	billing := &xai.BillingSummary{
		StatusCode:       http.StatusForbidden,
		WeeklyStatusCode: http.StatusForbidden,
	}
	repo := &accountServiceTestRepo{accounts: map[int64]*Account{
		accountID: {
			ID:       accountID,
			Platform: PlatformGrok,
			Type:     AccountTypeOAuth,
			Status:   StatusActive,
			Extra:    map[string]any{grokBillingExtraKey: billing},
		},
	}}

	updated, err := (&adminServiceImpl{accountRepo: repo}).UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		Extra: map[string]any{"custom": "value"},
	})

	require.NoError(t, err)
	require.Equal(t, billing, updated.Extra[grokBillingExtraKey])
	require.Equal(t, "value", updated.Extra["custom"])
	eligible, reason := updated.GrokMediaGenerationEligibility()
	require.False(t, eligible)
	require.Equal(t, "billing_forbidden", reason)
}

// 目录观察与上游能力快照只由同步写入：编辑表单携带的旧值或注入值被丢弃，以账号现有值为准；
// 账号原来没有时也不会凭表单写入。
func TestUpdateAccountKeepsSyncManagedCatalogSnapshots(t *testing.T) {
	accountID := int64(113)
	observed := map[string]any{"models": []any{"grok-4.6"}, "fetched_at": "2026-10-30T00:00:00Z"}
	repo := &accountServiceTestRepo{accounts: map[int64]*Account{
		accountID: {ID: accountID, Platform: PlatformGrok, Type: AccountTypeOAuth, Status: StatusActive, Extra: map[string]any{grokObservedModelsExtraKey: observed}},
	}}
	updated, err := (&adminServiceImpl{accountRepo: repo}).UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		Extra: map[string]any{
			grokObservedModelsExtraKey:    map[string]any{"models": []any{"forged"}},
			UpstreamModelMetadataExtraKey: map[string]any{"forged": true},
			"custom":                      "value",
		},
	})
	require.NoError(t, err)
	require.Equal(t, observed, updated.Extra[grokObservedModelsExtraKey])
	require.NotContains(t, updated.Extra, UpstreamModelMetadataExtraKey)
	require.Equal(t, "value", updated.Extra["custom"])
}
