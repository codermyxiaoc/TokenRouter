package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/enttest"
	"github.com/TokenFlux/TokenRouter/ent/paymentorder"
	"github.com/TokenFlux/TokenRouter/ent/usersubscription"
	"github.com/TokenFlux/TokenRouter/internal/payment"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// walletSubscriptionRepo 用真实数据库保存权益，保证回滚断言覆盖整个事务而非内存桩。
type walletSubscriptionRepo struct {
	userSubRepoNoop
	client    *dbent.Client
	latestErr error
}

func (r *walletSubscriptionRepo) subscriptions(ctx context.Context) *dbent.UserSubscriptionClient {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return tx.UserSubscription
	}
	return r.client.UserSubscription
}

func (r *walletSubscriptionRepo) Create(ctx context.Context, sub *UserSubscription) error {
	if dbent.TxFromContext(ctx) == nil {
		return errors.New("余额支付发放订阅必须使用调用方事务")
	}
	row, err := r.subscriptions(ctx).Create().
		SetUserID(sub.UserID).SetPlanID(sub.PlanID).
		SetStartsAt(sub.StartsAt).SetExpiresAt(sub.ExpiresAt).SetStatus(sub.Status).
		SetNillableDailyLimitUsd(sub.DailyLimitUSD).
		SetNillableWeeklyLimitUsd(sub.WeeklyLimitUSD).
		SetNillableMonthlyLimitUsd(sub.MonthlyLimitUSD).
		SetNillableSourceOrderID(sub.SourceOrderID).SetAssignedAt(sub.AssignedAt).
		SetNotes(sub.Notes).Save(ctx)
	if err == nil {
		sub.ID = row.ID
	}
	return err
}

func (r *walletSubscriptionRepo) GetByID(ctx context.Context, id int64) (*UserSubscription, error) {
	row, err := r.subscriptions(ctx).Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return walletSubscriptionFromRow(row), nil
}

func (r *walletSubscriptionRepo) GetLatestByUserIDAndPlanID(ctx context.Context, userID, planID int64) (*UserSubscription, error) {
	if r.latestErr != nil {
		return nil, r.latestErr
	}
	row, err := r.subscriptions(ctx).Query().
		Where(usersubscription.UserIDEQ(userID), usersubscription.PlanIDEQ(planID)).
		Order(dbent.Desc(usersubscription.FieldExpiresAt)).First(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, ErrSubscriptionNotFound
		}
		return nil, err
	}
	return walletSubscriptionFromRow(row), nil
}

func (r *walletSubscriptionRepo) ListBySourceOrderID(ctx context.Context, orderID int64) ([]UserSubscription, error) {
	rows, err := r.subscriptions(ctx).Query().Where(usersubscription.SourceOrderIDEQ(orderID)).All(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]UserSubscription, 0, len(rows))
	for _, row := range rows {
		result = append(result, *walletSubscriptionFromRow(row))
	}
	return result, nil
}

func walletSubscriptionFromRow(row *dbent.UserSubscription) *UserSubscription {
	return &UserSubscription{
		ID: row.ID, UserID: row.UserID, PlanID: row.PlanID,
		StartsAt: row.StartsAt, ExpiresAt: row.ExpiresAt, Status: row.Status,
		DailyLimitUSD: row.DailyLimitUsd, WeeklyLimitUSD: row.WeeklyLimitUsd,
		MonthlyLimitUSD: row.MonthlyLimitUsd, SourceOrderID: row.SourceOrderID,
	}
}

type walletPaymentFixture struct {
	client   *dbent.Client
	service  *PaymentService
	settings *paymentConfigSettingRepoStub
	user     *dbent.User
	plan     *dbent.SubscriptionPlan
	repo     *walletSubscriptionRepo
}

func newWalletPaymentFixture(t *testing.T, balance, frozenBalance float64) *walletPaymentFixture {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "wallet.db")))
	require.NoError(t, err)
	// SQLite 以单连接串行执行事务；生产 PostgreSQL 的行锁由单独的锁探针验证。
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(entsql.OpenDB(dialect.SQLite, db))))
	t.Cleanup(func() { _ = client.Close() })
	user, err := client.User.Create().SetEmail("wallet@example.com").
		SetPasswordHash("test-password-hash").SetUsername("余额支付用户").
		SetBalance(balance).SetFrozenBalance(frozenBalance).Save(ctx)
	require.NoError(t, err)
	plan, err := client.SubscriptionPlan.Create().SetName("月度订阅").
		SetPrice(80.25).SetCurrency("CNY").SetValidityDays(30).
		SetDailyLimitUsd(12).SetMonthlyLimitUsd(300).SetForSale(true).Save(ctx)
	require.NoError(t, err)
	settings := &paymentConfigSettingRepoStub{values: map[string]string{
		SettingPaymentEnabled: "true", SettingWalletPaymentEnabled: "true",
		SettingBalanceRechargeMult: "100", SettingSubscriptionUSDToCNYRate: "7.2",
		SettingRechargeFeeRate: "15", SettingBalancePayDisabled: "true",
	}}
	repo := &walletSubscriptionRepo{client: client}
	return &walletPaymentFixture{
		client: client, user: user, plan: plan, settings: settings, repo: repo,
		// 不装配第三方渠道，成功用例同时证明余额支付无需网关实例或注册表。
		service: &PaymentService{
			entClient:       client,
			configService:   &PaymentConfigService{entClient: client, settingRepo: settings},
			subscriptionSvc: NewSubscriptionService(groupRepoNoop{}, repo, nil, client, nil),
		},
	}
}

func (f *walletPaymentFixture) request(key int) CreateOrderRequest {
	return CreateOrderRequest{
		UserID: f.user.ID, PlanID: f.plan.ID, PaymentType: "balance",
		OrderType:      payment.OrderTypeSubscription,
		IdempotencyKey: fmt.Sprintf("00000000-0000-4000-8000-%012d", key),
		ClientIP:       "127.0.0.1", SrcHost: "wallet.example.com",
	}
}

func (f *walletPaymentFixture) assertUnchanged(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	user, err := f.client.User.Get(ctx, f.user.ID)
	require.NoError(t, err)
	require.Equal(t, f.user.Balance, user.Balance)
	require.Equal(t, f.user.FrozenBalance, user.FrozenBalance)
	orders, err := f.client.PaymentOrder.Query().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, orders)
	subs, err := f.client.UserSubscription.Query().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, subs)
	audits, err := f.client.PaymentAuditLog.Query().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, audits)
}

// TestWalletPaymentCompletesAndQueuesRenewal 验证金额口径、冻结余额隔离及续费时间链。
func TestWalletPaymentCompletesAndQueuesRenewal(t *testing.T) {
	ctx := context.Background()
	f := newWalletPaymentFixture(t, 200, 500)
	before := time.Now()
	first, err := f.service.CreateOrder(ctx, f.request(1))
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, first.Status)
	require.Equal(t, "balance", first.PaymentType)
	require.Len(t, first.OutTradeNo, 26)
	require.Regexp(t, `^bal_[A-Za-z0-9_-]{22}$`, first.OutTradeNo)
	require.Equal(t, "USD", first.Currency)
	require.Equal(t, 80.25, first.Amount)
	require.Equal(t, 80.25, first.PayAmount)
	require.Zero(t, first.FeeRate)
	require.Zero(t, first.FeeFixed)
	require.Zero(t, first.FeeAmount)
	require.Empty(t, first.PayURL)
	require.Empty(t, first.QRCode)
	user, err := f.client.User.Get(ctx, f.user.ID)
	require.NoError(t, err)
	require.Equal(t, 119.75, user.Balance)
	require.Equal(t, 500.0, user.FrozenBalance)
	order, err := f.client.PaymentOrder.Get(ctx, first.OrderID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, order.Status)
	require.NotNil(t, order.PaidAt)
	require.NotNil(t, order.CompletedAt)
	require.Equal(t, f.plan.ID, *order.PlanID)
	require.Equal(t, f.plan.Price, order.PlanSnapshot.Price)
	granted, err := f.client.UserSubscription.Query().Where(usersubscription.SourceOrderIDEQ(first.OrderID)).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, SubscriptionStatusActive, granted.Status)
	require.False(t, granted.StartsAt.Before(before))
	require.True(t, granted.ExpiresAt.Equal(granted.StartsAt.AddDate(0, 0, 30)))
	require.Equal(t, 12.0, *granted.DailyLimitUsd)
	require.Equal(t, 300.0, *granted.MonthlyLimitUsd)
	audits, err := f.client.PaymentAuditLog.Query().Count(ctx)
	require.NoError(t, err)
	require.Positive(t, audits)

	second, err := f.service.CreateOrder(ctx, f.request(2))
	require.NoError(t, err)
	require.NotEqual(t, first.OrderID, second.OrderID)
	queued, err := f.client.UserSubscription.Query().Where(usersubscription.SourceOrderIDEQ(second.OrderID)).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, SubscriptionStatusPending, queued.Status)
	require.True(t, queued.StartsAt.Equal(granted.ExpiresAt))
	require.True(t, queued.ExpiresAt.Equal(queued.StartsAt.AddDate(0, 0, 30)))
	user, err = f.client.User.Get(ctx, f.user.ID)
	require.NoError(t, err)
	require.Equal(t, 39.5, user.Balance)
	require.Equal(t, 500.0, user.FrozenBalance)
}

// TestWalletPaymentRejectsInvalidRequestsWithoutSideEffects 覆盖开关、用途和幂等键准入。
func TestWalletPaymentRejectsInvalidRequestsWithoutSideEffects(t *testing.T) {
	tests := []struct {
		name   string
		change func(*walletPaymentFixture, *CreateOrderRequest)
		reason string
	}{
		{"支付总开关关闭", func(f *walletPaymentFixture, _ *CreateOrderRequest) {
			f.settings.values[SettingPaymentEnabled] = "false"
		}, "PAYMENT_DISABLED"},
		{"余额支付默认关闭", func(f *walletPaymentFixture, _ *CreateOrderRequest) {
			delete(f.settings.values, SettingWalletPaymentEnabled)
		}, "WALLET_PAYMENT_DISABLED"},
		{"余额支付显式关闭", func(f *walletPaymentFixture, _ *CreateOrderRequest) {
			f.settings.values[SettingWalletPaymentEnabled] = "false"
		}, "WALLET_PAYMENT_DISABLED"},
		{"禁止余额循环充值", func(_ *walletPaymentFixture, req *CreateOrderRequest) {
			req.OrderType = payment.OrderTypeBalance
			req.Amount = 80.25
		}, "WALLET_SUBSCRIPTION_ONLY"},
		{"空幂等键", func(_ *walletPaymentFixture, req *CreateOrderRequest) {
			req.IdempotencyKey = ""
		}, "INVALID_IDEMPOTENCY_KEY"},
		{"非法幂等键", func(_ *walletPaymentFixture, req *CreateOrderRequest) {
			req.IdempotencyKey = "invalid key / ?"
		}, "INVALID_IDEMPOTENCY_KEY"},
		{"套餐不存在", func(_ *walletPaymentFixture, req *CreateOrderRequest) {
			req.PlanID = 99999
		}, "PLAN_NOT_AVAILABLE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newWalletPaymentFixture(t, 200, 500)
			req := f.request(1)
			tt.change(f, &req)
			_, err := f.service.CreateOrder(context.Background(), req)
			require.Error(t, err)
			require.Equal(t, tt.reason, infraerrors.Reason(err))
			f.assertUnchanged(t)
		})
	}
}

// TestWalletPaymentRejectsUnavailableOrInvalidPrice 验证只有在售且正价套餐可扣款。
func TestWalletPaymentRejectsUnavailableOrInvalidPrice(t *testing.T) {
	for _, tc := range []struct {
		name    string
		price   float64
		forSale bool
		reason  string
	}{
		{"下架", 80.25, false, "PLAN_NOT_AVAILABLE"},
		{"零价", 0, true, "INVALID_AMOUNT"},
		{"负价", -80.25, true, "INVALID_AMOUNT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWalletPaymentFixture(t, 200, 500)
			_, err := f.client.SubscriptionPlan.UpdateOneID(f.plan.ID).SetPrice(tc.price).SetForSale(tc.forSale).Save(context.Background())
			require.NoError(t, err)
			_, err = f.service.CreateOrder(context.Background(), f.request(1))
			require.Error(t, err)
			require.Equal(t, tc.reason, infraerrors.Reason(err))
			f.assertUnchanged(t)
		})
	}
}

// TestWalletPaymentInsufficientBalanceNeverSpendsFrozenFunds 验证冻结金额不可用于购买。
func TestWalletPaymentInsufficientBalanceNeverSpendsFrozenFunds(t *testing.T) {
	for _, balance := range []float64{80.24, 0, -1} {
		t.Run(fmt.Sprintf("balance_%.2f", balance), func(t *testing.T) {
			f := newWalletPaymentFixture(t, balance, 500)
			_, err := f.service.CreateOrder(context.Background(), f.request(1))
			require.Error(t, err)
			require.Equal(t, "INSUFFICIENT_BALANCE", infraerrors.Reason(err))
			f.assertUnchanged(t)
		})
	}
}

// TestWalletPaymentIdempotencySurvivesPlanChanges 验证响应丢失后的重试复用原订单及原价。
func TestWalletPaymentIdempotencySurvivesPlanChanges(t *testing.T) {
	ctx := context.Background()
	f := newWalletPaymentFixture(t, 200, 500)
	req := f.request(1)
	first, err := f.service.CreateOrder(ctx, req)
	require.NoError(t, err)
	auditsBefore, err := f.client.PaymentAuditLog.Query().Count(ctx)
	require.NoError(t, err)
	_, err = f.client.SubscriptionPlan.UpdateOneID(f.plan.ID).SetPrice(199).SetForSale(false).Save(ctx)
	require.NoError(t, err)
	f.settings.values[SettingWalletPaymentEnabled] = "false"
	f.settings.values[SettingPaymentEnabled] = "false"
	repeated, err := f.service.CreateOrder(ctx, req)
	require.NoError(t, err)
	require.Equal(t, first.OrderID, repeated.OrderID)
	require.Equal(t, first.OutTradeNo, repeated.OutTradeNo)
	require.Equal(t, 80.25, repeated.PayAmount)
	require.Equal(t, OrderStatusCompleted, repeated.Status)
	user, err := f.client.User.Get(ctx, f.user.ID)
	require.NoError(t, err)
	require.Equal(t, 119.75, user.Balance)
	orders, err := f.client.PaymentOrder.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, orders)
	subs, err := f.client.UserSubscription.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, subs)
	auditsAfter, err := f.client.PaymentAuditLog.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, auditsBefore, auditsAfter)

	other, err := f.client.SubscriptionPlan.Create().SetName("另一套餐").SetPrice(1).Save(ctx)
	require.NoError(t, err)
	req.PlanID = other.ID
	_, err = f.service.CreateOrder(ctx, req)
	require.Error(t, err)
	require.Equal(t, "IDEMPOTENCY_CONFLICT", infraerrors.Reason(err))
	user, err = f.client.User.Get(ctx, f.user.ID)
	require.NoError(t, err)
	require.Equal(t, 119.75, user.Balance)
}

// TestWalletPaymentLegacyOrderRetryKeepsOriginalNumber 验证旧长编号在升级后仍可恢复且不重复扣款。
func TestWalletPaymentLegacyOrderRetryKeepsOriginalNumber(t *testing.T) {
	ctx := context.Background()
	f := newWalletPaymentFixture(t, 200, 500)
	req := f.request(1)
	first, err := f.service.CreateOrder(ctx, req)
	require.NoError(t, err)
	// 还原旧版真实存储格式：60 位订单号，快照中没有完整请求摘要。
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", req.UserID, req.IdempotencyKey)))
	legacy := "bal_" + hex.EncodeToString(digest[:28])
	require.Len(t, legacy, 60)
	_, err = f.client.PaymentOrder.UpdateOneID(first.OrderID).
		SetOutTradeNo(legacy).SetRechargeCode(legacy).SetPaymentTradeNo(legacy).
		SetProviderSnapshot(map[string]any{"provider_key": PaymentTypeWallet, "currency": "USD"}).Save(ctx)
	require.NoError(t, err)
	audits, err := f.client.PaymentAuditLog.Query().Count(ctx)
	require.NoError(t, err)
	f.settings.values[SettingWalletPaymentEnabled] = "false"
	_, err = f.client.SubscriptionPlan.UpdateOneID(f.plan.ID).SetForSale(false).Save(ctx)
	require.NoError(t, err)
	repeated, err := f.service.CreateOrder(ctx, req)
	require.NoError(t, err)
	require.Equal(t, first.OrderID, repeated.OrderID)
	require.Equal(t, legacy, repeated.OutTradeNo)
	user, err := f.client.User.Get(ctx, f.user.ID)
	require.NoError(t, err)
	require.Equal(t, 119.75, user.Balance)
	require.Equal(t, 1, f.client.PaymentOrder.Query().CountX(ctx))
	require.Equal(t, 1, f.client.UserSubscription.Query().CountX(ctx))
	require.Equal(t, audits, f.client.PaymentAuditLog.Query().CountX(ctx))
}

// TestWalletPaymentShortNumberChecksFullRequestHash 保证短编号命中后仍核对完整请求摘要。
func TestWalletPaymentShortNumberChecksFullRequestHash(t *testing.T) {
	ctx := context.Background()
	f := newWalletPaymentFixture(t, 200, 500)
	req := f.request(1)
	first, err := f.service.CreateOrder(ctx, req)
	require.NoError(t, err)
	_, err = f.client.PaymentOrder.UpdateOneID(first.OrderID).
		SetProviderSnapshot(map[string]any{"provider_key": PaymentTypeWallet, "currency": "USD", "wallet_request_hash": "different_request"}).Save(ctx)
	require.NoError(t, err)
	_, err = f.service.CreateOrder(ctx, req)
	require.Equal(t, "IDEMPOTENCY_CONFLICT", infraerrors.Reason(err))
	user, err := f.client.User.Get(ctx, f.user.ID)
	require.NoError(t, err)
	require.Equal(t, 119.75, user.Balance)
	require.Equal(t, 1, f.client.PaymentOrder.Query().CountX(ctx))
	require.Equal(t, 1, f.client.UserSubscription.Query().CountX(ctx))
}

// TestWalletPaymentRollsBackAfterPersistedGrantOrAudit 验证已执行写入后失败仍整体撤销。
func TestWalletPaymentRollsBackAfterPersistedGrantOrAudit(t *testing.T) {
	for _, failurePoint := range []string{"subscription", "audit"} {
		t.Run(failurePoint, func(t *testing.T) {
			f := newWalletPaymentFixture(t, 200, 500)
			injected := errors.New("故障注入：持久化之后失败")
			hook := func(next dbent.Mutator) dbent.Mutator {
				return dbent.MutateFunc(func(ctx context.Context, mutation dbent.Mutation) (dbent.Value, error) {
					value, err := next.Mutate(ctx, mutation)
					if err == nil && mutation.Op().Is(dbent.OpCreate) {
						return nil, injected
					}
					return value, err
				})
			}
			if failurePoint == "subscription" {
				f.client.UserSubscription.Use(hook)
			} else {
				f.client.PaymentAuditLog.Use(hook)
			}
			_, err := f.service.CreateOrder(context.Background(), f.request(1))
			require.ErrorIs(t, err, injected)
			f.assertUnchanged(t)
		})
	}
}

// TestWalletPaymentLatestSubscriptionReadFailureRollsBack 防止查询故障被误判为首次购买并立即发放。
func TestWalletPaymentLatestSubscriptionReadFailureRollsBack(t *testing.T) {
	f := newWalletPaymentFixture(t, 200, 500)
	injected := errors.New("故障注入：无法读取已有订阅")
	f.repo.latestErr = injected
	_, err := f.service.CreateOrder(context.Background(), f.request(1))
	require.ErrorIs(t, err, injected)
	f.assertUnchanged(t)
}

// TestWalletPaymentConcurrentPurchasesKeepBalanceNonnegative 验证并发调用按事务余额拒绝超额购买。
func TestWalletPaymentConcurrentPurchasesKeepBalanceNonnegative(t *testing.T) {
	f := newWalletPaymentFixture(t, 80.25, 500)
	start := make(chan struct{})
	errorsCh := make(chan error, 6)
	var done sync.WaitGroup
	for i := 1; i <= cap(errorsCh); i++ {
		done.Add(1)
		go func(key int) {
			defer done.Done()
			<-start
			_, err := f.service.CreateOrder(context.Background(), f.request(key))
			errorsCh <- err
		}(i)
	}
	close(start)
	done.Wait()
	close(errorsCh)
	successes := 0
	for err := range errorsCh {
		if err == nil {
			successes++
		} else {
			require.Equal(t, "INSUFFICIENT_BALANCE", infraerrors.Reason(err))
		}
	}
	require.Equal(t, 1, successes)
	user, err := f.client.User.Get(context.Background(), f.user.ID)
	require.NoError(t, err)
	require.Zero(t, user.Balance)
	require.Equal(t, 500.0, user.FrozenBalance)
	orders, err := f.client.PaymentOrder.Query().Where(paymentorder.StatusEQ(OrderStatusCompleted)).Count(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, orders)
	subs, err := f.client.UserSubscription.Query().Count(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, subs)
}

// TestWalletPaymentConcurrentRetryReturnsOneOrder 验证同键并发重试全部返回同一笔已完成交易。
func TestWalletPaymentConcurrentRetryReturnsOneOrder(t *testing.T) {
	f := newWalletPaymentFixture(t, 80.25, 500)
	start := make(chan struct{})
	type result struct {
		response *CreateOrderResponse
		err      error
	}
	results := make(chan result, 4)
	for range cap(results) {
		go func() {
			<-start
			response, err := f.service.CreateOrder(context.Background(), f.request(1))
			results <- result{response, err}
		}()
	}
	close(start)
	var orderID int64
	for range cap(results) {
		got := <-results
		require.NoError(t, got.err)
		require.Equal(t, OrderStatusCompleted, got.response.Status)
		if orderID == 0 {
			orderID = got.response.OrderID
		}
		require.Equal(t, orderID, got.response.OrderID)
	}
	user, err := f.client.User.Get(context.Background(), f.user.ID)
	require.NoError(t, err)
	require.Zero(t, user.Balance)
	orders, err := f.client.PaymentOrder.Query().Count(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, orders)
	subs, err := f.client.UserSubscription.Query().Count(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, subs)
}

// TestWalletPaymentIdempotencyIsScopedToUser 验证不同用户相同请求键不会串单。
func TestWalletPaymentIdempotencyIsScopedToUser(t *testing.T) {
	ctx := context.Background()
	f := newWalletPaymentFixture(t, 80.25, 0)
	other, err := f.client.User.Create().SetEmail("other-wallet@example.com").
		SetPasswordHash("test-password-hash").SetUsername("另一用户").SetBalance(80.25).Save(ctx)
	require.NoError(t, err)
	first, err := f.service.CreateOrder(ctx, f.request(1))
	require.NoError(t, err)
	req := f.request(1)
	req.UserID = other.ID
	second, err := f.service.CreateOrder(ctx, req)
	require.NoError(t, err)
	require.NotEqual(t, first.OrderID, second.OrderID)
	require.NotEqual(t, first.OutTradeNo, second.OutTradeNo)
	other, err = f.client.User.Get(ctx, other.ID)
	require.NoError(t, err)
	require.Zero(t, other.Balance)
	subs, err := f.client.UserSubscription.Query().Where(usersubscription.UserIDEQ(other.ID)).All(ctx)
	require.NoError(t, err)
	require.Len(t, subs, 1)
	require.Equal(t, second.OrderID, *subs[0].SourceOrderID)
}

// TestWalletPaymentLocksPostgresUserBeforeReadingOrders 保证生产事务先锁用户，再读取订单或扣款资格。
func TestWalletPaymentLocksPostgresUserBeforeReadingOrders(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	injected := errors.New("故障注入：已锁定用户")
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT .*FROM "users".*FOR UPDATE`).WithArgs(int64(42)).WillReturnError(injected)
	mock.ExpectRollback()
	svc := &PaymentService{
		entClient:       client,
		subscriptionSvc: NewSubscriptionService(groupRepoNoop{}, userSubRepoNoop{}, nil, client, nil),
		configService: &PaymentConfigService{settingRepo: &paymentConfigSettingRepoStub{values: map[string]string{
			SettingPaymentEnabled: "true", SettingWalletPaymentEnabled: "true",
		}}},
	}
	_, err = svc.CreateOrder(context.Background(), CreateOrderRequest{
		UserID: 42, PlanID: 7, PaymentType: "balance", OrderType: payment.OrderTypeSubscription,
		IdempotencyKey: "00000000-0000-4000-8000-000000000001",
	})
	require.ErrorIs(t, err, injected)
	require.NoError(t, mock.ExpectationsWereMet())
}
