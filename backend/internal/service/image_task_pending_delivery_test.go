package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// imageTaskChargeRecorderStub 模拟只读账本：charged 表示该请求 ID 已扣费，err 表示账本不可查询。
type imageTaskChargeRecorderStub struct {
	charged   map[string]bool
	err       error
	requestID string
	apiKeyID  int64
}

func (r *imageTaskChargeRecorderStub) UsageBillingRequestRecorded(_ context.Context, requestID string, apiKeyID int64) (bool, error) {
	r.requestID, r.apiKeyID = requestID, apiKeyID
	if r.err != nil {
		return false, r.err
	}
	return r.charged[requestID], nil
}

// pendingFailingStore 拒绝写入带待交付结果的记录，模拟扣费前数据库不可写。
type pendingFailingStore struct {
	imageTaskMemoryStore
}

func (s *pendingFailingStore) Save(ctx context.Context, task *ImageTaskRecord, ttl time.Duration) error {
	if len(task.PendingResult) > 0 {
		return errors.New("database unavailable")
	}
	return s.imageTaskMemoryStore.Save(ctx, task, ttl)
}

// 扣费前先落库待交付结果：扣费时任务仍是 processing、不对外返回结果，但记录里已有可恢复的结果与扣费请求 ID。
func TestImageTaskStagesPendingDeliveryBeforeCharge(t *testing.T) {
	result := json.RawMessage(`{"data":[{"url":"https://cdn.test/a.png"}]}`)
	owner := ImageTaskOwner{UserID: 1, APIKeyID: 2}

	t.Run("staged_then_completed", func(t *testing.T) {
		store := &imageTaskMemoryStore{}
		svc := NewImageTaskServiceWithOptions(store, time.Hour, time.Minute)
		created, err := svc.Create(context.Background(), owner)
		require.NoError(t, err)
		delivered, err := svc.CompleteDeliveredWithBilling(context.Background(), created.ID, http.StatusOK, result, nil, "client:req-1", func(context.Context) error {
			record, getErr := store.Get(context.Background(), created.ID)
			require.NoError(t, getErr)
			require.Equal(t, ImageTaskStatusProcessing, record.Status)
			require.JSONEq(t, string(result), string(record.PendingResult), "扣费前必须已落库待交付结果")
			require.Equal(t, "client:req-1", record.BillingRequestID)
			public, getErr := svc.Get(context.Background(), owner, created.ID)
			require.NoError(t, getErr)
			require.Empty(t, public.Result, "待交付结果不能在扣费前对外返回")
			require.Empty(t, public.ImageURL)
			return nil
		})
		require.NoError(t, err)
		require.True(t, delivered)
		record, err := store.Get(context.Background(), created.ID)
		require.NoError(t, err)
		require.Equal(t, ImageTaskStatusCompleted, record.Status)
		require.Empty(t, record.PendingResult, "完成后不再保留待交付副本")
		require.Equal(t, "client:req-1", record.BillingRequestID)
	})

	t.Run("stage_failure_never_charges", func(t *testing.T) {
		store := &pendingFailingStore{}
		svc := NewImageTaskServiceWithOptions(store, time.Hour, time.Minute)
		created, err := svc.Create(context.Background(), owner)
		require.NoError(t, err)
		charged := false
		delivered, err := svc.CompleteDeliveredWithBilling(context.Background(), created.ID, http.StatusOK, result, nil, "client:req-2", func(context.Context) error {
			charged = true
			return nil
		})
		require.ErrorIs(t, err, ErrImageTaskBillingUnavailable)
		require.False(t, delivered)
		require.False(t, charged, "无法落库可恢复记录时不能扣费")
		got, err := svc.Get(context.Background(), owner, created.ID)
		require.NoError(t, err)
		require.Equal(t, ImageTaskStatusFailed, got.Status)
		require.Contains(t, string(got.Error), "no charge was applied")
	})

	t.Run("not_processing_never_charges", func(t *testing.T) {
		store := &imageTaskMemoryStore{}
		svc := NewImageTaskServiceWithOptions(store, time.Hour, time.Minute)
		created, err := svc.Create(context.Background(), owner)
		require.NoError(t, err)
		// 维护器已判为中断终态，迟到的执行者不能再扣费。
		require.NoError(t, svc.Fail(context.Background(), created.ID, http.StatusServiceUnavailable, json.RawMessage(`{"type":"execution_interrupted"}`)))
		charged := false
		_, err = svc.CompleteDeliveredWithBilling(context.Background(), created.ID, http.StatusOK, result, nil, "client:req-3", func(context.Context) error {
			charged = true
			return nil
		})
		require.ErrorIs(t, err, ErrImageTaskBillingUnavailable)
		require.False(t, charged)
	})
}

// 扣费报错但账本显示已提交（回执丢失）时照常交付；账本确认未扣费才提示未收费，无法确认时不能断言未收费。
func TestImageTaskSettleErrorChecksLedger(t *testing.T) {
	result := json.RawMessage(`{"data":[{"url":"https://cdn.test/a.png"}]}`)
	owner := ImageTaskOwner{UserID: 1, APIKeyID: 2}
	run := func(t *testing.T, recorder *imageTaskChargeRecorderStub) (*ImageTask, bool, error) {
		t.Helper()
		svc := NewImageTaskServiceWithOptions(&imageTaskMemoryStore{}, time.Hour, time.Minute)
		svc.chargeRecorder = recorder
		created, err := svc.Create(context.Background(), owner)
		require.NoError(t, err)
		delivered, err := svc.CompleteDeliveredWithBilling(context.Background(), created.ID, http.StatusOK, result, nil, "client:req", func(context.Context) error {
			return errors.New("commit acknowledgement lost")
		})
		got, getErr := svc.Get(context.Background(), owner, created.ID)
		require.NoError(t, getErr)
		return got, delivered, err
	}

	t.Run("captured_despite_error", func(t *testing.T) {
		recorder := &imageTaskChargeRecorderStub{charged: map[string]bool{"client:req": true}}
		got, delivered, err := run(t, recorder)
		require.NoError(t, err)
		require.True(t, delivered)
		require.Equal(t, ImageTaskStatusCompleted, got.Status)
		require.Equal(t, "https://cdn.test/a.png", got.ImageURL)
		require.Equal(t, int64(2), recorder.apiKeyID, "按任务所属 Key 核对账本")
	})

	t.Run("not_captured", func(t *testing.T) {
		got, delivered, err := run(t, &imageTaskChargeRecorderStub{charged: map[string]bool{}})
		require.ErrorIs(t, err, ErrImageTaskBillingUnavailable)
		require.False(t, delivered)
		require.Equal(t, ImageTaskStatusFailed, got.Status)
		require.Contains(t, string(got.Error), "no charge was applied")
	})

	t.Run("ledger_unknown", func(t *testing.T) {
		// 账本无法确认时不判失败：任务保持 processing 并保留待交付结果，由维护器按账本补交或判为中断。
		svc := NewImageTaskServiceWithOptions(&imageTaskMemoryStore{}, time.Hour, time.Minute)
		svc.chargeRecorder = &imageTaskChargeRecorderStub{err: errors.New("ledger down")}
		created, err := svc.Create(context.Background(), owner)
		require.NoError(t, err)
		delivered, err := svc.CompleteDeliveredWithBilling(context.Background(), created.ID, http.StatusOK, result, nil, "client:req", func(context.Context) error {
			return errors.New("commit acknowledgement lost")
		})
		require.ErrorIs(t, err, ErrImageTaskBillingUnavailable)
		require.False(t, delivered)
		record, err := svc.store.Get(context.Background(), created.ID)
		require.NoError(t, err)
		require.Equal(t, ImageTaskStatusProcessing, record.Status)
		require.JSONEq(t, string(result), string(record.PendingResult), "待交付结果必须保留供维护器补交")
		require.Equal(t, "client:req", record.BillingRequestID)
		public, err := svc.Get(context.Background(), owner, created.ID)
		require.NoError(t, err)
		require.Empty(t, public.ImageURL, "未确认扣费前不对外返回结果")
	})

	t.Run("ledger_unknown_without_staging", func(t *testing.T) {
		// 没有扣费请求 ID（未落库待交付结果）时沿用旧流程：判失败，且不能断言未扣费。
		svc := NewImageTaskServiceWithOptions(&imageTaskMemoryStore{}, time.Hour, time.Minute)
		svc.chargeRecorder = &imageTaskChargeRecorderStub{err: errors.New("ledger down")}
		created, err := svc.Create(context.Background(), owner)
		require.NoError(t, err)
		_, err = svc.CompleteDeliveredWithBilling(context.Background(), created.ID, http.StatusOK, result, nil, "", func(context.Context) error {
			return errors.New("ledger unavailable")
		})
		require.ErrorIs(t, err, ErrImageTaskBillingUnavailable)
		got, err := svc.Get(context.Background(), owner, created.ID)
		require.NoError(t, err)
		require.Equal(t, ImageTaskStatusFailed, got.Status)
	})
}

// 滚动升级期间旧版本重写记录会丢掉请求指纹：指纹为空时按原任务重放，不误报 409；指纹不同仍判冲突。
func TestImageTaskReplayToleratesMissingPayloadHash(t *testing.T) {
	owner := ImageTaskOwner{UserID: 1, APIKeyID: 2}
	store := &idempotentMemoryStore{}
	svc := NewImageTaskServiceWithOptions(store, time.Hour, time.Minute)
	task, _, err := svc.CreateObservedIdempotent(context.Background(), owner, nil, ImageTaskIdempotency{Key: "k", PayloadHash: "hash-a"})
	require.NoError(t, err)
	_, _, err = svc.ReplayIdempotent(context.Background(), owner, ImageTaskIdempotency{Key: "k", PayloadHash: "hash-b"})
	require.ErrorIs(t, err, ErrImageTaskIdempotencyConflict)
	record, err := store.Get(context.Background(), task.ID)
	require.NoError(t, err)
	record.PayloadHash = ""
	require.NoError(t, store.Save(context.Background(), record, time.Hour))
	replay, found, err := svc.ReplayIdempotent(context.Background(), owner, ImageTaskIdempotency{Key: "k", PayloadHash: "hash-a"})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, task.ID, replay.ID)
	_, found, err = svc.ReplayIdempotent(context.Background(), ImageTaskOwner{UserID: 1, APIKeyID: 3}, ImageTaskIdempotency{Key: "k", PayloadHash: "hash-a"})
	require.NoError(t, err)
	require.False(t, found, "其它 Key 查不到该任务")
}

// idempotentMemoryStore 在内存存储上按 API Key 与幂等键查找任务。
type idempotentMemoryStore struct {
	imageTaskMemoryStore
}

func (s *idempotentMemoryStore) FindIdempotent(ctx context.Context, apiKeyID int64, key string) (*ImageTaskRecord, error) {
	// 内存存储只保存一个任务，按 API Key 与幂等键比对即可。
	task, err := s.imageTaskMemoryStore.Get(ctx, "")
	if err == nil && task.APIKeyID == apiKeyID && task.IdempotencyKey == key {
		return task, nil
	}
	return nil, ErrImageTaskNotFound
}
