package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 计数扫描独立于 SMTP 和用量请求，并且失败时仍应继续维护订阅过期状态。
type subscriptionScheduledCounterStub struct {
	userSubRepoNoop
	calls []string
	err   error
}

func (r *subscriptionScheduledCounterStub) RefreshScheduledResetCounts(ctx context.Context, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.calls = append(r.calls, "count")
	return r.err
}

func (r *subscriptionScheduledCounterStub) BatchUpdateExpiredStatus(context.Context) (int64, error) {
	r.calls = append(r.calls, "expire")
	return 0, nil
}

func TestSubscriptionScheduledResetRuntime_NoUsageOrSMTPRequired(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "count_failure"}[fail], func(t *testing.T) {
			repo := &subscriptionScheduledCounterStub{}
			if fail {
				repo.err = errors.New("temporary database failure")
			}
			svc := NewSubscriptionExpiryService(repo, time.Minute)
			svc.runOnce()
			require.Equal(t, []string{"count", "expire"}, repo.calls)
		})
	}
}
