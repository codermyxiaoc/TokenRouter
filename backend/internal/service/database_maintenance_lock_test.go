package service

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestDatabaseHeavyMaintenanceLockRejectsConcurrentTask(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectQuery("SELECT pg_try_advisory_lock").
		WithArgs(databaseHeavyMaintenanceLockID).
		WillReturnRows(sqlmock.NewRows([]string{"pg_try_advisory_lock"}).AddRow(false))

	release, acquired, err := tryAcquireDatabaseHeavyMaintenanceLock(context.Background(), db)
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	if acquired || release != nil {
		t.Fatalf("acquired = %v release = %v, want busy", acquired, release != nil)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDatabaseHeavyMaintenanceLockReleasesSession(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectQuery("SELECT pg_try_advisory_lock").
		WithArgs(databaseHeavyMaintenanceLockID).
		WillReturnRows(sqlmock.NewRows([]string{"pg_try_advisory_lock"}).AddRow(true))
	mock.ExpectExec("SELECT pg_advisory_unlock").
		WithArgs(databaseHeavyMaintenanceLockID).
		WillReturnResult(sqlmock.NewResult(0, 1))

	release, acquired, err := tryAcquireDatabaseHeavyMaintenanceLock(context.Background(), db)
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	if !acquired || release == nil {
		t.Fatalf("acquired = %v release = %v, want acquired", acquired, release != nil)
	}
	release()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}
