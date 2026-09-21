package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 权益发放只重放已确认结果，任何未确认状态都不能借租约或记录过期再次执行。
func TestIdempotencyCoordinator_PreventReexecutionExistingStates(t *testing.T) {
	for _, tc := range []struct {
		name          string
		status        string
		leaseExpired  bool
		recordExpired bool
		wantReplay    bool
	}{
		{"processing", IdempotencyStatusProcessing, false, false, false},
		{"processing_expired_lease", IdempotencyStatusProcessing, true, false, false},
		{"processing_expired_record", IdempotencyStatusProcessing, true, true, false},
		{"failed_retryable", IdempotencyStatusFailedRetryable, false, false, false},
		{"failed_expired_backoff", IdempotencyStatusFailedRetryable, true, false, false},
		{"failed_expired_record", IdempotencyStatusFailedRetryable, true, true, false},
		{"succeeded", IdempotencyStatusSucceeded, true, false, true},
		{"succeeded_expired_record", IdempotencyStatusSucceeded, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newInMemoryIdempotencyRepo()
			coordinator := NewIdempotencyCoordinator(repo, DefaultIdempotencyConfig())
			opts := IdempotencyExecuteOptions{Scope: "admin.subscriptions.bulk_assign", ActorScope: "admin:1", Method: "POST", Route: "/assign", IdempotencyKey: "assign-key", Payload: map[string]any{"user_ids": []int64{1}}, RequireKey: true, PreventReexecution: true}
			fingerprint, err := BuildIdempotencyFingerprint(opts.Method, opts.Route, opts.ActorScope, opts.Payload)
			require.NoError(t, err)
			lease, expiry := time.Now().Add(time.Hour), time.Now().Add(time.Hour)
			if tc.leaseExpired {
				lease = time.Now().Add(-time.Hour)
			}
			if tc.recordExpired {
				expiry = time.Now().Add(-time.Hour)
			}
			body := `{"success_count":1}`
			_, err = repo.CreateProcessing(context.Background(), &IdempotencyRecord{Scope: opts.Scope, IdempotencyKeyHash: HashIdempotencyKey(opts.IdempotencyKey), RequestFingerprint: fingerprint, Status: tc.status, LockedUntil: &lease, ExpiresAt: expiry, ResponseBody: &body})
			require.NoError(t, err)
			calls := 0
			result, err := coordinator.Execute(context.Background(), opts, func(context.Context) (any, error) { calls++; return nil, nil })
			require.Zero(t, calls)
			if tc.wantReplay {
				require.NoError(t, err)
				require.True(t, result.Replayed)
			} else {
				require.ErrorIs(t, err, ErrIdempotencyResultUnconfirmed)
			}
		})
	}
}

// 首次执行或确认存储失败后，即便失败退避结束也不可重复执行已可能提交的发放。
func TestIdempotencyCoordinator_PreventReexecutionAfterFailure(t *testing.T) {
	for _, failure := range []string{"execute", "mark_succeeded", "marshal"} {
		t.Run(failure, func(t *testing.T) {
			repo := &markBehaviorRepo{inMemoryIdempotencyRepo: *newInMemoryIdempotencyRepo(), failMarkSucceeded: failure == "mark_succeeded"}
			coordinator := NewIdempotencyCoordinator(repo, DefaultIdempotencyConfig())
			opts := IdempotencyExecuteOptions{Scope: "assign", IdempotencyKey: "key", RequireKey: true, PreventReexecution: true}
			calls := 0
			execute := func(context.Context) (any, error) {
				calls++
				if failure == "execute" {
					return nil, errors.New("unknown transaction result")
				}
				if failure == "marshal" {
					return make(chan int), nil
				}
				return map[string]any{"success_count": 1}, nil
			}
			_, err := coordinator.Execute(context.Background(), opts, execute)
			require.ErrorIs(t, err, ErrIdempotencyResultUnconfirmed)
			for _, record := range repo.data {
				past := time.Now().Add(-time.Hour)
				record.LockedUntil = &past
			}
			_, err = coordinator.Execute(context.Background(), opts, execute)
			require.ErrorIs(t, err, ErrIdempotencyResultUnconfirmed)
			require.Equal(t, 1, calls)
		})
	}
}
