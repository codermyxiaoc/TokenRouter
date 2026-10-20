package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

func TestImageObjectCleanupStoreCheckDetectsMissingMigration(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	missing := errors.New("relation image_object_cleanup does not exist")
	mock.ExpectQuery(`SELECT id,object_key,storage_ciphertext,expires_at,next_attempt_at,`).
		WillReturnError(missing)
	require.ErrorIs(t, NewImageObjectCleanupStore(db).Check(context.Background()), missing)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestImageObjectCleanupStoreBoundsClaimAndFencesOldOwner(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	store := NewImageObjectCleanupStore(db)
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	id := strings.Repeat("a", 64)
	mock.ExpectQuery(`(?s)WITH due AS.*FOR UPDATE SKIP LOCKED.*UPDATE image_object_cleanup`).
		WithArgs(now, imageObjectCleanupMaxBatch, sqlmock.AnyArg(), now.Add(imageObjectCleanupMaxLease)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "object_key", "storage_ciphertext", "expires_at", "lease_token", "attempts"}))
	records, err := store.ClaimDue(context.Background(), now, 10000, 24*time.Hour)
	require.NoError(t, err)
	require.Empty(t, records)
	mock.ExpectExec(`DELETE FROM image_object_cleanup WHERE id=\$1 AND lease_token=\$2`).
		WithArgs(id, "old-token").WillReturnResult(sqlmock.NewResult(0, 0))
	require.ErrorIs(t, store.Complete(context.Background(), id, "old-token"), service.ErrImageObjectCleanupLeaseLost)
	mock.ExpectExec(`(?s)UPDATE image_object_cleanup.*WHERE id=\$1 AND lease_token=\$2`).
		WithArgs(id, "old-token", now.Add(time.Minute), "cleanup_failed").
		WillReturnResult(sqlmock.NewResult(0, 0))
	require.ErrorIs(t, store.Retry(context.Background(), id, "old-token", now.Add(time.Minute),
		"https://storage.test?X-Amz-Signature=secret"), service.ErrImageObjectCleanupLeaseLost)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestImageObjectCleanupStoreRejectsInvalidInputWithoutDatabaseCalls(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	store := NewImageObjectCleanupStore(db)
	base := service.ImageObjectCleanupRecord{ID: strings.Repeat("b", 64), ObjectKey: "images/one.png",
		StorageCiphertext: "encrypted-config", ExpiresAt: time.Now().Add(time.Hour)}
	for _, modify := range []func(*service.ImageObjectCleanupRecord){
		func(r *service.ImageObjectCleanupRecord) { r.ID = "not-a-hash" },
		func(r *service.ImageObjectCleanupRecord) { r.ObjectKey = "" },
		func(r *service.ImageObjectCleanupRecord) { r.ObjectKey = strings.Repeat("图", 342) },
		func(r *service.ImageObjectCleanupRecord) { r.StorageCiphertext = "" },
		func(r *service.ImageObjectCleanupRecord) {
			r.StorageCiphertext = strings.Repeat("c", imageObjectCleanupMaxCiphertext+1)
		},
		func(r *service.ImageObjectCleanupRecord) { r.ExpiresAt = time.Time{} },
	} {
		record := base
		modify(&record)
		_, err := store.Schedule(context.Background(), record)
		require.Error(t, err)
	}
	require.ErrorIs(t, store.Complete(context.Background(), base.ID, ""), service.ErrImageObjectCleanupLeaseLost)
	_, err = store.ClaimDue(context.Background(), time.Now(), 1, 0)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestImageObjectCleanupErrorCodeCannotPersistRawDiagnostics(t *testing.T) {
	for _, code := range []string{"cleanup_snapshot_invalid", "cleanup_decrypt_failed", "cleanup_storage_unavailable",
		"cleanup_delete_failed", "cleanup_access_denied", "cleanup_timeout", "cleanup_complete_failed", "cleanup_failed"} {
		require.Equal(t, code, safeImageObjectCleanupErrorCode(code))
	}
	for _, raw := range []string{"", "sk_secret", "AccessDenied: secret", "https://store.test/signed?secret=123"} {
		require.Equal(t, "cleanup_failed", safeImageObjectCleanupErrorCode(raw))
	}
}
