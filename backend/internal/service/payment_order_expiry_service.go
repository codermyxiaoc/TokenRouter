package service

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

const expiryCheckTimeout = 30 * time.Second

const (
	paymentOrderExpiryLeaderLockKey = "payment:order:expiry:leader"
	// 锁存活时间需覆盖支付状态同步、履约补偿与超时关闭阶段，避免任务中途失去主实例身份。
	paymentOrderExpiryLeaderLockTTL = 3 * time.Minute
)

// PaymentOrderExpiryService 定期处理超时支付订单。
type PaymentOrderExpiryService struct {
	paymentSvc *PaymentService
	interval   time.Duration
	stopCh     chan struct{}
	stopOnce   sync.Once
	wg         sync.WaitGroup
	lockCache  LeaderLockCache
	db         *sql.DB
	instanceID string
}

func NewPaymentOrderExpiryService(paymentSvc *PaymentService, interval time.Duration) *PaymentOrderExpiryService {
	return &PaymentOrderExpiryService{
		paymentSvc: paymentSvc,
		interval:   interval,
		stopCh:     make(chan struct{}),
		instanceID: uuid.NewString(),
	}
}

// SetLeaderLock 注入跨实例主实例锁，用于限制每轮只由一个实例同步/关闭订单。
func (s *PaymentOrderExpiryService) SetLeaderLock(lockCache LeaderLockCache, db *sql.DB) {
	if s == nil {
		return
	}
	s.lockCache = lockCache
	s.db = db
}

func (s *PaymentOrderExpiryService) Start() {
	if s == nil || s.paymentSvc == nil || s.interval <= 0 {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()

		s.runOnce()
		for {
			select {
			case <-ticker.C:
				s.runOnce()
			case <-s.stopCh:
				return
			}
		}
	}()
}

func (s *PaymentOrderExpiryService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
	s.wg.Wait()
}

func (s *PaymentOrderExpiryService) runOnce() {
	lockCtx, lockCancel := context.WithTimeout(context.Background(), 2*time.Second)
	release, ok := tryAcquireSingletonLeaderLock(lockCtx, s.lockCache, s.db, paymentOrderExpiryLeaderLockKey, s.instanceID, paymentOrderExpiryLeaderLockTTL)
	lockCancel()
	if !ok {
		return
	}
	defer release()

	reconcileCtx, cancel := context.WithTimeout(context.Background(), expiryCheckTimeout)
	recovered, err := s.paymentSvc.ReconcilePendingPaymentOrders(reconcileCtx)
	cancel()
	if err != nil {
		slog.Warn("[PaymentOrderExpiry] failed to reconcile pending payment orders", "error", err)
	} else if recovered > 0 {
		slog.Info("[PaymentOrderExpiry] reconciled paid orders", "count", recovered)
	}

	processingCtx, processingCancel := context.WithTimeout(context.Background(), expiryCheckTimeout)
	processingRecovered, err := s.paymentSvc.ReconcileProcessingOrders(processingCtx)
	processingCancel()
	if err != nil {
		slog.Warn("[PaymentOrderExpiry] failed to reconcile processing payment orders", "error", err)
	} else if processingRecovered > 0 {
		slog.Info("[PaymentOrderExpiry] reconciled paid processing orders", "count", processingRecovered)
	}

	fulfillmentCtx, fulfillmentCancel := context.WithTimeout(context.Background(), expiryCheckTimeout)
	fulfillmentRecovered, err := s.paymentSvc.ReconcilePaidFulfillmentOrders(fulfillmentCtx)
	fulfillmentCancel()
	if err != nil {
		slog.Warn("[PaymentOrderExpiry] failed to reconcile paid order fulfillment", "error", err)
	} else if fulfillmentRecovered > 0 {
		slog.Info("[PaymentOrderExpiry] reconciled paid order fulfillment", "count", fulfillmentRecovered)
	}

	expireCtx, cancel := context.WithTimeout(context.Background(), expiryCheckTimeout)
	defer cancel()
	expired, err := s.paymentSvc.ExpireTimedOutOrders(expireCtx)
	if err != nil {
		slog.Error("[PaymentOrderExpiry] failed to expire orders", "error", err)
		return
	}
	if expired > 0 {
		slog.Info("[PaymentOrderExpiry] expired timed-out orders", "count", expired)
	}
}
