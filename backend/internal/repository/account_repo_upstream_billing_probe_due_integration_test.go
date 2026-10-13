//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

func TestListDueUpstreamBillingProbeAccountsHandlesInvalidCalendarDate(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	now := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	_, err := tx.ExecContext(ctx, `
		UPDATE accounts
		SET extra = extra - 'upstream_billing_probe_enabled' - 'upstream_billing_probe'
	`)
	require.NoError(t, err)

	insert := func(name, nextProbeAt string) int64 {
		t.Helper()
		var id int64
		extra := fmt.Sprintf(`{
			"upstream_billing_probe_enabled": true,
			"upstream_billing_probe": {"status": "ok", "next_probe_at": %q}
		}`, nextProbeAt)
		err := scanSingleRow(ctx, tx, `
			INSERT INTO accounts (name, platform, type, status, extra)
			VALUES ($1, 'openai', $2, 'active', $3::jsonb)
			RETURNING id
		`, []any{name, service.AccountTypeAPIKey, extra}, &id)
		require.NoError(t, err)
		return id
	}

	invalidID := insert("probe-invalid-calendar-date", "2026-99-99T12:00:00Z")
	dueID := insert("probe-due", "2026-07-14T11:59:59Z")
	_ = insert("probe-not-due", "2026-07-14T12:00:01Z")

	accounts, err := repo.ListDueUpstreamBillingProbeAccounts(ctx, now, 20)
	require.NoError(t, err)
	require.Len(t, accounts, 2)
	require.Equal(t, invalidID, accounts[0].ID)
	require.Equal(t, dueID, accounts[1].ID)
}

func insertUpstreamBillingProbeAccount(ctx context.Context, t *testing.T, tx sqlQueryer, name, nextProbeAt string) int64 {
	t.Helper()
	var id int64
	extra := fmt.Sprintf(`{
		"upstream_billing_probe_enabled": true,
		"upstream_billing_probe": {"status": "ok", "next_probe_at": %q}
	}`, nextProbeAt)
	err := scanSingleRow(ctx, tx, `
		INSERT INTO accounts (name, platform, type, status, extra)
		VALUES ($1, 'openai', $2, 'active', $3::jsonb)
		RETURNING id
	`, []any{name, service.AccountTypeAPIKey, extra}, &id)
	require.NoError(t, err)
	return id
}

func rfc3339WithFraction(t time.Time, fraction string) string {
	return fmt.Sprintf("%s.%sZ", t.UTC().Format("2006-01-02T15:04:05"), fraction)
}

func TestListDueUpstreamBillingProbeAccountsParsesNanosecondTimestamps(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	now := time.Date(2026, time.July, 25, 17, 30, 0, 0, time.UTC)
	_, err := tx.ExecContext(ctx, `
		UPDATE accounts
		SET extra = extra - 'upstream_billing_probe_enabled' - 'upstream_billing_probe'
	`)
	require.NoError(t, err)

	notDue := now.Add(time.Hour)
	for i := 0; i < 22; i++ {
		insertUpstreamBillingProbeAccount(ctx, t, tx,
			fmt.Sprintf("probe-nano-not-due-%02d", i),
			rfc3339WithFraction(notDue.Add(time.Duration(i)*time.Second), "123456789"))
	}
	dueThird := insertUpstreamBillingProbeAccount(ctx, t, tx,
		"probe-nano-due-9digits", rfc3339WithFraction(now.Add(-time.Minute), "123456789"))
	dueFirst := insertUpstreamBillingProbeAccount(ctx, t, tx,
		"probe-nano-due-8digits", rfc3339WithFraction(now.Add(-3*time.Minute), "12345678"))
	dueSecond := insertUpstreamBillingProbeAccount(ctx, t, tx,
		"probe-nano-due-7digits", rfc3339WithFraction(now.Add(-2*time.Minute), "1234567"))

	accounts, err := repo.ListDueUpstreamBillingProbeAccounts(ctx, now, 20)
	require.NoError(t, err)
	require.Len(t, accounts, 3)
	require.Equal(t, dueFirst, accounts[0].ID)
	require.Equal(t, dueSecond, accounts[1].ID)
	require.Equal(t, dueThird, accounts[2].ID)
}

func TestListDueUpstreamBillingProbeAccountsSelectsEarliestDueAcrossIDs(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	now := time.Date(2026, time.July, 25, 17, 30, 0, 0, time.UTC)
	_, err := tx.ExecContext(ctx, `
		UPDATE accounts
		SET extra = extra - 'upstream_billing_probe_enabled' - 'upstream_billing_probe'
	`)
	require.NoError(t, err)

	ids := make([]int64, 0, 25)
	for i := 0; i < 25; i++ {
		due := now.Add(-time.Duration(i+1) * time.Minute)
		ids = append(ids, insertUpstreamBillingProbeAccount(ctx, t, tx,
			fmt.Sprintf("probe-nano-order-%02d", i),
			rfc3339WithFraction(due, "123456789")))
	}

	accounts, err := repo.ListDueUpstreamBillingProbeAccounts(ctx, now, 20)
	require.NoError(t, err)
	require.Len(t, accounts, 20)
	got := make([]int64, 0, len(accounts))
	for _, account := range accounts {
		got = append(got, account.ID)
	}
	want := make([]int64, 0, 20)
	for i := 24; i >= 5; i-- {
		want = append(want, ids[i])
	}
	require.Equal(t, want, got)
}

func TestListDueUpstreamBillingProbeAccountsIncludesAllAPIKeyPlatforms(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	now := time.Date(2026, time.July, 26, 3, 0, 0, 0, time.UTC)
	_, err := tx.ExecContext(ctx, `
		UPDATE accounts
		SET extra = extra - 'upstream_billing_probe_enabled' - 'upstream_billing_probe'
	`)
	require.NoError(t, err)

	insert := func(name, platform, accountType, nextProbeAt string) int64 {
		t.Helper()
		var id int64
		extra := fmt.Sprintf(`{
			"upstream_billing_probe_enabled": true,
			"upstream_billing_probe": {"status": "ok", "next_probe_at": %q}
		}`, nextProbeAt)
		err := scanSingleRow(ctx, tx, `
			INSERT INTO accounts (name, platform, type, status, extra)
			VALUES ($1, $2, $3, 'active', $4::jsonb)
			RETURNING id
		`, []any{name, platform, accountType, extra}, &id)
		require.NoError(t, err)
		return id
	}

	openaiDue := insert("probe-openai-due", "openai", service.AccountTypeAPIKey, "2026-07-26T02:57:00Z")
	anthropicDue := insert("probe-anthropic-due", "anthropic", service.AccountTypeAPIKey, "2026-07-26T02:58:00Z")
	grokDue := insert("probe-grok-due", "grok", service.AccountTypeAPIKey, "2026-07-26T02:59:00Z")
	_ = insert("probe-grok-oauth-excluded", "grok", service.AccountTypeOAuth, "2026-07-26T02:59:00Z")
	var disabledID int64
	err = scanSingleRow(ctx, tx, `
		INSERT INTO accounts (name, platform, type, status, extra)
		VALUES ('probe-grok-disabled', 'grok', $1, 'active', '{}'::jsonb)
		RETURNING id
	`, []any{service.AccountTypeAPIKey}, &disabledID)
	require.NoError(t, err)

	accounts, err := repo.ListDueUpstreamBillingProbeAccounts(ctx, now, 20)
	require.NoError(t, err)
	require.Len(t, accounts, 3)
	require.Equal(t, openaiDue, accounts[0].ID)
	require.Equal(t, anthropicDue, accounts[1].ID)
	require.Equal(t, grokDue, accounts[2].ID)
}

// 非法平台不应占用有界候选名额，避免旧脏数据让有效账号长期得不到探测。
func TestListDueUpstreamBillingProbeAccountsSkipsInvalidPlatformsBeforeLimit(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	_, err := tx.ExecContext(ctx, `UPDATE accounts SET extra=extra-'upstream_billing_probe_enabled'`)
	require.NoError(t, err)
	for i := range 25 {
		_, err := tx.ExecContext(ctx, `INSERT INTO accounts(name,platform,type,status,extra) VALUES ($1,'invalid-platform','apikey','active','{"upstream_billing_probe_enabled":true}')`, fmt.Sprintf("不合法平台-%d", i))
		require.NoError(t, err)
	}
	valid := insertUpstreamBillingProbeAccount(ctx, t, tx, "有效账号", "2026-01-01T00:00:00Z")
	accounts, err := repo.ListDueUpstreamBillingProbeAccounts(ctx, time.Now(), 20)
	require.NoError(t, err)
	require.Len(t, accounts, 1)
	require.Equal(t, valid, accounts[0].ID)
}
