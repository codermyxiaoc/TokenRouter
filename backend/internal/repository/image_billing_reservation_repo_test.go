//go:build unit

package repository

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/domain"
	"github.com/TokenFlux/TokenRouter/internal/service"
)

func imageBillingUnitReserveCommand() *service.ImageBillingReserveCommand {
	groupID := int64(91)
	return &service.ImageBillingReserveCommand{Hold: service.BatchImageBalanceHoldCommand{
		RequestID: "client:image-test", UserID: 2759, ActorUserID: 2759, APIKeyID: 7, GroupID: &groupID,
		APIKeyBillingMode: service.APIKeyBillingModeAuto, BaseAmountUSD: 0.12,
		SubscriptionRateMultiplier: 6.6, SubscriptionRateMultiplierScale: 1, BalanceRateMultiplier: 1,
	}, Quote: json.RawMessage(`{"model":"image-test","prices":{"1K":0.12}}`)}
}

func TestImageBillingReserveNormalizationUsesStablePriceFingerprint(t *testing.T) {
	input := imageBillingUnitReserveCommand()
	first, quote, expiry, err := normalizeImageBillingReserve(input)
	require.NoError(t, err)
	require.JSONEq(t, string(input.Quote), string(quote))
	require.WithinDuration(t, time.Now().Add(35*time.Minute), expiry, time.Second)
	// 重试不能因为时间变化或调用方意外携带分配结果而再次冻结资金。
	input.Hold.ReservedAt = time.Now().Add(-time.Hour)
	input.Hold.HoldAmount = 999
	input.Hold.RequestFingerprint = "caller-fingerprint"
	second, _, _, err := normalizeImageBillingReserve(input)
	require.NoError(t, err)
	require.Equal(t, first.RequestFingerprint, second.RequestFingerprint)
	require.Zero(t, second.HoldAmount)
	input.Quote = json.RawMessage(`{"model":"image-test","prices":{"1K":0.24}}`)
	changed, _, _, err := normalizeImageBillingReserve(input)
	require.NoError(t, err)
	require.NotEqual(t, first.RequestFingerprint, changed.RequestFingerprint)
}

func TestImageBillingReserveRejectsInvalidContracts(t *testing.T) {
	cases := map[string]func(*service.ImageBillingReserveCommand){
		"invalid_mode":   func(c *service.ImageBillingReserveCommand) { c.Hold.APIKeyBillingMode = "other" },
		"missing_owner":  func(c *service.ImageBillingReserveCommand) { c.Hold.UserID = 0 },
		"missing_group":  func(c *service.ImageBillingReserveCommand) { c.Hold.GroupID = nil },
		"negative_base":  func(c *service.ImageBillingReserveCommand) { c.Hold.BaseAmountUSD = -1 },
		"nonfinite_rate": func(c *service.ImageBillingReserveCommand) { c.Hold.BalanceRateMultiplier = math.Inf(1) },
		"video_entity":   func(c *service.ImageBillingReserveCommand) { c.Hold.VideoEntity = true },
		"expired":        func(c *service.ImageBillingReserveCommand) { c.ExpiresAt = time.Now().Add(-time.Second) },
		"invalid_quote":  func(c *service.ImageBillingReserveCommand) { c.Quote = json.RawMessage(`[]`) },
		"missing_preferred": func(c *service.ImageBillingReserveCommand) {
			c.Hold.APIKeyBillingMode = service.APIKeyBillingModeSubscription
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			input := imageBillingUnitReserveCommand()
			mutate(input)
			_, _, _, err := normalizeImageBillingReserve(input)
			require.Error(t, err)
		})
	}
}

func TestImageBillingCapturePlanKeepsPerSourceRates(t *testing.T) {
	subID := int64(1953)
	input := imageBillingUnitReserveCommand().Hold
	input.BalanceHoldAmount = 0.04298259
	input.SubscriptionHoldAllocations = []domain.BillingAllocation{{Type: domain.BillingAllocationTypeSubscription,
		SubscriptionID: &subID, AmountUSD: 0.50831488, BaseAmountUSD: 0.50831488 / 6.6, RateMultiplier: 6.6}}
	plan, err := planImageBillingCapture(&input, 0.12)
	require.NoError(t, err)
	require.InDelta(t, 0.50831488, plan.SubscriptionAmountUSD, 1e-10)
	require.InDelta(t, 0.04298259, plan.BalanceAmountUSD, 1e-10)
	require.InDelta(t, 0.55129747, plan.ActualAmountUSD, 1e-10)
	require.Empty(t, plan.SubscriptionReleases)

	partial, err := planImageBillingCapture(&input, 0.06)
	require.NoError(t, err)
	require.InDelta(t, 0.396, partial.SubscriptionAmountUSD, 1e-10)
	require.Zero(t, partial.BalanceAmountUSD)
	require.InDelta(t, 0.11231488, partial.SubscriptionReleases[0].AmountUSD, 1e-10)
}

func TestImageBillingCapturePlanPreservesFreeCoverageAndRejectsOverrun(t *testing.T) {
	subID := int64(1)
	for _, freeSource := range []string{"subscription", "balance", "zero_base"} {
		t.Run(freeSource, func(t *testing.T) {
			hold := service.BatchImageBalanceHoldCommand{BaseAmountUSD: 0.12, BalanceRateMultiplier: 1}
			actual := 0.12
			switch freeSource {
			case "subscription":
				hold.SubscriptionHoldAllocations = []domain.BillingAllocation{{Type: domain.BillingAllocationTypeSubscription,
					SubscriptionID: &subID, BaseAmountUSD: 0.12, RateMultiplier: 0}}
			case "balance":
				hold.BalanceRateMultiplier = 0
			case "zero_base":
				hold.BaseAmountUSD, actual = 0, 0
			}
			plan, err := planImageBillingCapture(&hold, actual)
			require.NoError(t, err)
			require.Zero(t, plan.ActualAmountUSD)
			_, err = planImageBillingCapture(&hold, actual+0.01)
			require.ErrorIs(t, err, service.ErrBatchImageSettlementCostExceedsHold)
		})
	}
}

func TestImageBillingReservationReconciliationDoesNotRefund(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := &usageBillingRepository{db: db}
	mock.ExpectExec(`UPDATE image_billing_reservations SET state = 'reconciliation'`).
		WithArgs("client:test", int64(9), int64(8)).WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.MarkImageBillingReconciliation(context.Background(), "client:test", 8, 9))
	now := time.Now()
	mock.ExpectExec(`(?s)WITH expired AS.*FOR UPDATE SKIP LOCKED.*UPDATE image_billing_reservations`).
		WithArgs(now, 100).WillReturnResult(sqlmock.NewResult(0, 3))
	count, err := repo.ReconcileExpiredImageBilling(context.Background(), now, 0)
	require.NoError(t, err)
	require.Equal(t, int64(3), count)
	require.NoError(t, mock.ExpectationsWereMet())
}
