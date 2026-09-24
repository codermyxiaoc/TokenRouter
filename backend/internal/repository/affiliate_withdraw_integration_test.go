//go:build integration

package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
	"sync"
	"testing"
	"time"
)

func mustCreateAffiliateWithdrawUser(t *testing.T, client *dbent.Client, label string) *service.User {
	t.Helper()
	return mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("affiliate-withdraw-%s-%d@example.com", label, time.Now().UnixNano()),
		PasswordHash: "hash",
		Role:         service.RoleUser,
		Status:       service.StatusActive,
		Balance:      5.5,
		Concurrency:  5,
	})
}

func mustSeedAffiliateQuota(t *testing.T, ctx context.Context, client *dbent.Client, userID int64, quota, frozen, history float64) {
	t.Helper()
	affCode := fmt.Sprintf("AFW%d-%06d", userID, time.Now().UnixNano()%1_000_000)
	_, err := client.ExecContext(ctx, `
INSERT INTO user_affiliates (user_id, aff_code, aff_quota, aff_frozen_quota, aff_history_quota, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, NOW(), NOW())`, userID, affCode, quota, frozen, history)
	require.NoError(t, err)
}

func mustCreateCommittedAffiliateWithdrawUser(t *testing.T, label string, quota, frozen, history float64) *service.User {
	t.Helper()
	client := testEntClient(t)
	u := mustCreateAffiliateWithdrawUser(t, client, label)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM users WHERE id = $1", u.ID)
	})
	mustSeedAffiliateQuota(t, context.Background(), client, u.ID, quota, frozen, history)
	return u
}

func newAffiliateWithdrawOperationID(label string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s-%d", label, time.Now().UnixNano())))
	return hex.EncodeToString(sum[:])
}

func TestAffiliateRepository_WithdrawQuota_DeductsAndRecordsLedger(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	txCtx := dbent.NewTxContext(ctx, tx)
	client := tx.Client()
	repo := NewAffiliateRepository(client, integrationDB).(*affiliateRepository)

	u := mustCreateAffiliateWithdrawUser(t, client, "user")
	mustSeedAffiliateQuota(t, txCtx, client, u.ID, 20, 0, 30)
	operationID := newAffiliateWithdrawOperationID("deduct")

	result, err := repo.WithdrawQuota(txCtx, u.ID, 12.5, operationID)
	require.NoError(t, err)
	require.Positive(t, result.LedgerID)
	require.False(t, result.Replayed)
	require.InDelta(t, 12.5, result.Amount, 1e-9)
	require.InDelta(t, 7.5, result.AvailableQuotaAfter, 1e-9)
	require.InDelta(t, 0.0, result.FrozenQuotaAfter, 1e-9)
	require.InDelta(t, 30.0, result.HistoryQuotaAfter, 1e-9)

	require.InDelta(t, 7.5, querySingleFloat(t, txCtx, client,
		"SELECT aff_quota::double precision FROM user_affiliates WHERE user_id = $1", u.ID), 1e-9)
	require.InDelta(t, 30.0, querySingleFloat(t, txCtx, client,
		"SELECT aff_history_quota::double precision FROM user_affiliates WHERE user_id = $1", u.ID), 1e-9)
	require.InDelta(t, 5.5, querySingleFloat(t, txCtx, client,
		"SELECT balance::double precision FROM users WHERE id = $1", u.ID), 1e-9)

	records, total, err := repo.ListAffiliateTransferRecords(txCtx, service.AffiliateRecordFilter{
		Search:   u.Email,
		Page:     1,
		PageSize: 20,
		SortDesc: true,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, records, 1)
	record := records[0]
	require.Equal(t, result.LedgerID, record.LedgerID)
	require.Equal(t, "withdraw", record.Action)
	require.Equal(t, u.ID, record.UserID)
	require.InDelta(t, 12.5, record.Amount, 1e-9)
	require.True(t, record.SnapshotAvailable)
	require.InDelta(t, 5.5, *record.BalanceAfter, 1e-9)
	require.InDelta(t, 7.5, *record.AvailableQuotaAfter, 1e-9)
	require.InDelta(t, 30.0, *record.HistoryQuotaAfter, 1e-9)

	rows, err := client.QueryContext(txCtx, "SELECT operation_id FROM user_affiliate_ledger WHERE id = $1", result.LedgerID)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	require.True(t, rows.Next())
	var storedOperationID string
	require.NoError(t, rows.Scan(&storedOperationID))
	require.Equal(t, operationID, storedOperationID)
}

func TestAffiliateRepository_WithdrawQuota_ThawsMaturedQuotaFirst(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	txCtx := dbent.NewTxContext(ctx, tx)
	client := tx.Client()
	repo := NewAffiliateRepository(client, integrationDB).(*affiliateRepository)

	u := mustCreateAffiliateWithdrawUser(t, client, "thaw")
	mustSeedAffiliateQuota(t, txCtx, client, u.ID, 0, 10, 10)
	_, err := client.ExecContext(txCtx, `
INSERT INTO user_affiliate_ledger (user_id, action, amount, frozen_until, created_at, updated_at)
VALUES ($1, 'accrue', 10, NOW() - INTERVAL '1 hour', NOW(), NOW())`, u.ID)
	require.NoError(t, err)

	result, err := repo.WithdrawQuota(txCtx, u.ID, 10, newAffiliateWithdrawOperationID("thaw"))
	require.NoError(t, err)
	require.InDelta(t, 0.0, result.AvailableQuotaAfter, 1e-9)
	require.InDelta(t, 0.0, result.FrozenQuotaAfter, 1e-9)
	require.Equal(t, 1, querySingleInt(t, txCtx, client,
		"SELECT COUNT(*) FROM user_affiliate_ledger WHERE user_id = $1 AND action = 'withdraw'", u.ID))
}

func TestAffiliateRepository_WithdrawQuota_InsufficientLeavesQuotaUntouched(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewAffiliateRepository(client, integrationDB).(*affiliateRepository)

	u := mustCreateCommittedAffiliateWithdrawUser(t, "insufficient", 5, 10, 15)
	_, err := integrationDB.ExecContext(ctx, `
INSERT INTO user_affiliate_ledger (user_id, action, amount, frozen_until, created_at, updated_at)
VALUES ($1, 'accrue', 10, NOW() + INTERVAL '1 hour', NOW(), NOW())`, u.ID)
	require.NoError(t, err)
	operationID := newAffiliateWithdrawOperationID("insufficient")

	_, err = repo.WithdrawQuota(ctx, u.ID, 6, operationID)
	require.ErrorIs(t, err, service.ErrAffiliateQuotaInsufficient)

	require.InDelta(t, 5.0, querySingleFloat(t, ctx, client,
		"SELECT aff_quota::double precision FROM user_affiliates WHERE user_id = $1", u.ID), 1e-9)
	require.InDelta(t, 10.0, querySingleFloat(t, ctx, client,
		"SELECT aff_frozen_quota::double precision FROM user_affiliates WHERE user_id = $1", u.ID), 1e-9)
	require.Equal(t, 0, querySingleInt(t, ctx, client,
		"SELECT COUNT(*) FROM user_affiliate_ledger WHERE user_id = $1 AND action = 'withdraw'", u.ID))

	result, err := repo.WithdrawQuota(ctx, u.ID, 5, operationID)
	require.NoError(t, err)
	require.False(t, result.Replayed)
	require.InDelta(t, 0.0, result.AvailableQuotaAfter, 1e-9)

	noProfile := mustCreateAffiliateWithdrawUser(t, client, "no-profile")
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM users WHERE id = $1", noProfile.ID)
	})
	_, err = repo.WithdrawQuota(ctx, noProfile.ID, 1, newAffiliateWithdrawOperationID("no-profile"))
	require.ErrorIs(t, err, service.ErrAffiliateQuotaInsufficient)
	require.Equal(t, 0, querySingleInt(t, ctx, client,
		"SELECT COUNT(*) FROM user_affiliate_ledger WHERE user_id = $1", noProfile.ID))
}

func TestAffiliateRepository_WithdrawQuota_RetryAfterCommitReplaysFirstResult(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewAffiliateRepository(client, integrationDB).(*affiliateRepository)

	u := mustCreateCommittedAffiliateWithdrawUser(t, "retry", 100, 0, 100)
	operationID := newAffiliateWithdrawOperationID("retry")

	first, err := repo.WithdrawQuota(ctx, u.ID, 10, operationID)
	require.NoError(t, err)
	require.False(t, first.Replayed)

	retry, err := repo.WithdrawQuota(ctx, u.ID, 10, operationID)
	require.NoError(t, err)
	require.True(t, retry.Replayed)
	require.Equal(t, first.LedgerID, retry.LedgerID)
	require.Equal(t, u.ID, retry.UserID)
	require.InDelta(t, 10.0, retry.Amount, 1e-9)
	require.InDelta(t, 90.0, retry.AvailableQuotaAfter, 1e-9)
	require.InDelta(t, first.FrozenQuotaAfter, retry.FrozenQuotaAfter, 1e-9)
	require.InDelta(t, 100.0, retry.HistoryQuotaAfter, 1e-9)

	require.InDelta(t, 90.0, querySingleFloat(t, ctx, client,
		"SELECT aff_quota::double precision FROM user_affiliates WHERE user_id = $1", u.ID), 1e-9)
	require.Equal(t, 1, querySingleInt(t, ctx, client,
		"SELECT COUNT(*) FROM user_affiliate_ledger WHERE user_id = $1 AND action = 'withdraw'", u.ID))

	second, err := repo.WithdrawQuota(ctx, u.ID, 10, newAffiliateWithdrawOperationID("retry-second"))
	require.NoError(t, err)
	require.False(t, second.Replayed)
	require.NotEqual(t, first.LedgerID, second.LedgerID)
	require.InDelta(t, 80.0, querySingleFloat(t, ctx, client,
		"SELECT aff_quota::double precision FROM user_affiliates WHERE user_id = $1", u.ID), 1e-9)
	require.Equal(t, 2, querySingleInt(t, ctx, client,
		"SELECT COUNT(*) FROM user_affiliate_ledger WHERE user_id = $1 AND action = 'withdraw'", u.ID))
}

func TestAffiliateRepository_WithdrawQuota_SameOperationWaitsForInFlightRegistration(t *testing.T) {
	for _, commitFirst := range []bool{true, false} {
		name := "first rolls back"
		if commitFirst {
			name = "first commits"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			client := testEntClient(t)
			repo := NewAffiliateRepository(client, integrationDB).(*affiliateRepository)

			u := mustCreateCommittedAffiliateWithdrawUser(t, "inflight", 100, 0, 100)
			operationID := newAffiliateWithdrawOperationID("inflight")

			tx, err := client.Tx(ctx)
			require.NoError(t, err)
			t.Cleanup(func() { _ = tx.Rollback() })
			first, err := repo.WithdrawQuota(dbent.NewTxContext(ctx, tx), u.ID, 10, operationID)
			require.NoError(t, err)

			type outcome struct {
				result *service.AffiliateWithdrawResult
				err    error
			}
			waiting := make(chan outcome, 1)
			go func() {
				result, err := repo.WithdrawQuota(ctx, u.ID, 10, operationID)
				waiting <- outcome{result: result, err: err}
			}()

			select {
			case got := <-waiting:
				t.Fatalf("same operation must wait for the in-flight registration, got result=%+v err=%v", got.result, got.err)
			case <-time.After(300 * time.Millisecond):
			}

			if commitFirst {
				require.NoError(t, tx.Commit())
			} else {
				require.NoError(t, tx.Rollback())
			}

			var got outcome
			select {
			case got = <-waiting:
			case <-time.After(10 * time.Second):
				t.Fatal("waiting registration did not finish after the in-flight one ended")
			}
			require.NoError(t, got.err)
			if commitFirst {
				require.True(t, got.result.Replayed)
				require.Equal(t, first.LedgerID, got.result.LedgerID)
			} else {
				require.False(t, got.result.Replayed)
				require.NotEqual(t, first.LedgerID, got.result.LedgerID)
			}
			require.InDelta(t, 90.0, got.result.AvailableQuotaAfter, 1e-9)

			require.InDelta(t, 90.0, querySingleFloat(t, ctx, client,
				"SELECT aff_quota::double precision FROM user_affiliates WHERE user_id = $1", u.ID), 1e-9)
			require.Equal(t, 1, querySingleInt(t, ctx, client,
				"SELECT COUNT(*) FROM user_affiliate_ledger WHERE user_id = $1 AND action = 'withdraw'", u.ID))
		})
	}
}

func TestAffiliateRepository_WithdrawQuota_ConcurrentSameOperationDeductsOnce(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewAffiliateRepository(client, integrationDB).(*affiliateRepository)

	u := mustCreateCommittedAffiliateWithdrawUser(t, "concurrent", 100, 0, 100)
	operationID := newAffiliateWithdrawOperationID("concurrent")

	const workers = 8
	results := make([]*service.AffiliateWithdrawResult, workers)
	errs := make([]error, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = repo.WithdrawQuota(ctx, u.ID, 10, operationID)
		}(i)
	}
	close(start)
	wg.Wait()

	executed := 0
	for i := 0; i < workers; i++ {
		require.NoError(t, errs[i])
		require.Equal(t, results[0].LedgerID, results[i].LedgerID)
		require.InDelta(t, 90.0, results[i].AvailableQuotaAfter, 1e-9)
		if !results[i].Replayed {
			executed++
		}
	}
	require.Equal(t, 1, executed)
	require.InDelta(t, 90.0, querySingleFloat(t, ctx, client,
		"SELECT aff_quota::double precision FROM user_affiliates WHERE user_id = $1", u.ID), 1e-9)
	require.Equal(t, 1, querySingleInt(t, ctx, client,
		"SELECT COUNT(*) FROM user_affiliate_ledger WHERE user_id = $1 AND action = 'withdraw'", u.ID))
}

func TestAffiliateRepository_WithdrawQuota_RejectsOperationReuseWithDifferentRequest(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewAffiliateRepository(client, integrationDB).(*affiliateRepository)

	u := mustCreateCommittedAffiliateWithdrawUser(t, "conflict", 100, 0, 100)
	other := mustCreateCommittedAffiliateWithdrawUser(t, "conflict-other", 100, 0, 100)
	operationID := newAffiliateWithdrawOperationID("conflict")

	_, err := repo.WithdrawQuota(ctx, u.ID, 10, operationID)
	require.NoError(t, err)

	_, err = repo.WithdrawQuota(ctx, u.ID, 11, operationID)
	require.ErrorIs(t, err, service.ErrIdempotencyKeyConflict)
	_, err = repo.WithdrawQuota(ctx, other.ID, 10, operationID)
	require.ErrorIs(t, err, service.ErrIdempotencyKeyConflict)

	require.InDelta(t, 90.0, querySingleFloat(t, ctx, client,
		"SELECT aff_quota::double precision FROM user_affiliates WHERE user_id = $1", u.ID), 1e-9)
	require.InDelta(t, 100.0, querySingleFloat(t, ctx, client,
		"SELECT aff_quota::double precision FROM user_affiliates WHERE user_id = $1", other.ID), 1e-9)
	require.Equal(t, 1, querySingleInt(t, ctx, client,
		"SELECT COUNT(*) FROM user_affiliate_ledger WHERE user_id IN ($1, $2) AND action = 'withdraw'", u.ID, other.ID))
}
