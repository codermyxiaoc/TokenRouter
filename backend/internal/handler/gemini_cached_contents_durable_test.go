//go:build unit

package handler

import (
	"context"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
)

// cachedDurableBillingRepo 将上游处理测试接到本站持久预留契约；真实锁和事务另由仓储集成测试覆盖。
type cachedDurableBillingRepo struct {
	service.UsageBillingRepository
	mu                  sync.Mutex
	balance             float64
	reservations        map[string]*service.ImageBillingReservation
	captureError        error
	reconciliationError error
	captureCalls        int
	releaseCalls        int
	reconciliationCalls int
}

func (r *cachedDurableBillingRepo) Apply(context.Context, *service.UsageBillingCommand) (*service.UsageBillingApplyResult, error) {
	return &service.UsageBillingApplyResult{Applied: true}, nil
}
func (r *cachedDurableBillingRepo) ReserveImageBilling(ctx context.Context, c *service.ImageBillingReserveCommand) (*service.ImageBillingReservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reservations == nil {
		r.reservations = map[string]*service.ImageBillingReservation{}
	}
	if previous := r.reservations[c.Hold.RequestID]; previous != nil {
		cp := *previous
		cp.Applied = false
		return &cp, nil
	}
	amount := c.Hold.BaseAmountUSD * c.Hold.BalanceRateMultiplier
	if amount > r.balance {
		return nil, service.ErrBatchImageInsufficientBalance
	}
	r.balance -= amount
	hold := c.Hold
	hold.HoldAmount = amount
	hold.BalanceHoldAmount = amount
	result := &service.ImageBillingReservation{ID: hold.RequestID, State: service.ImageBillingReserved, Applied: true, Hold: hold, Quote: c.Quote, ExpiresAt: c.ExpiresAt}
	r.reservations[result.ID] = result
	return result, nil
}
func (r *cachedDurableBillingRepo) CaptureImageBilling(_ context.Context, id string, c *service.UsageBillingCommand, base float64) (*service.UsageBillingApplyResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.captureCalls++
	if r.captureError != nil {
		return nil, r.captureError
	}
	v := r.reservations[id]
	if v == nil {
		return nil, service.ErrImageBillingReservationNotFound
	}
	if v.State == service.ImageBillingCaptured {
		return &service.UsageBillingApplyResult{Applied: false}, nil
	}
	amount := base * v.Hold.BalanceRateMultiplier
	r.balance += v.Hold.HoldAmount - amount
	v.State = service.ImageBillingCaptured
	rate := v.Hold.BalanceRateMultiplier
	return &service.UsageBillingApplyResult{Applied: true, BalanceAmountUSD: amount, EffectiveRateMultiplier: &rate}, nil
}
func (r *cachedDurableBillingRepo) ReleaseImageBilling(_ context.Context, id string, _ int64, _ int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.releaseCalls++
	v := r.reservations[id]
	if v != nil && v.State == service.ImageBillingReserved {
		r.balance += v.Hold.HoldAmount
		v.State = service.ImageBillingReleased
	}
	return nil
}
func (r *cachedDurableBillingRepo) MarkImageBillingReconciliation(_ context.Context, id string, _ int64, _ int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reconciliationCalls++
	if r.reconciliationError != nil {
		return r.reconciliationError
	}
	if v := r.reservations[id]; v != nil {
		v.State = service.ImageBillingReconciliation
	}
	return nil
}
func (r *cachedDurableBillingRepo) ReconcileExpiredImageBilling(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}
