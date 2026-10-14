//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 保存仅接受同所有者、同原证据，后台仅领取有成功证据的记录，未知结果不会被自动捕获。
func TestImageBillingRecoveryEvidenceOwnershipAndEmptyClaim(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &usageBillingRepository{db: db}
	evidence := &service.ImageBillingSettlementEvidence{Version: 1,
		Command: service.UsageBillingCommand{RequestID: "client:image", UserID: 1, ActorUserID: 1, APIKeyID: 2, AccountID: 3, ImageCount: 1, BaseAmountUSD: 0.12},
		Usage:   service.UsageLog{RequestID: "client:image", UserID: 1, BillingUserID: 1, APIKeyID: 2, AccountID: 3, ImageCount: 1}}
	mock.ExpectExec(`(?s)UPDATE image_billing_reservations.*user_id = \$3.*settlement_evidence = \$4::jsonb`).
		WithArgs("client:image", int64(2), int64(1), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 0))
	require.ErrorIs(t, repo.SaveImageBillingSettlementEvidence(context.Background(), evidence), service.ErrImageBillingReservationConflict)
	now := time.Now()
	mock.ExpectQuery(`(?s)WITH due AS.*settlement_evidence IS NOT NULL.*FOR UPDATE SKIP LOCKED.*RETURNING r.settlement_evidence`).
		WithArgs(now, 25).WillReturnRows(sqlmock.NewRows([]string{"settlement_evidence"}))
	recovered, err := repo.RecoverImageBillingSettlements(context.Background(), now, 0)
	require.NoError(t, err)
	require.Empty(t, recovered)
	require.NoError(t, mock.ExpectationsWereMet())
}
