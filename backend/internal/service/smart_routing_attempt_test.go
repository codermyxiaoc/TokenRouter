package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type smartRoutingAttemptCacheStub struct {
	GatewayCache
	owner             int64
	token             string
	releases, commits int
	finishErr         error
	finishCalls       int
}

func (s *smartRoutingAttemptCacheStub) ClaimSmartRoutingSessionOwner(_ context.Context, _ int64, _, _ string, groupID int64, token string, _ time.Duration) (bool, error) {
	if s.owner != 0 {
		return false, nil
	}
	s.owner, s.token = groupID, token
	return true, nil
}
func (s *smartRoutingAttemptCacheStub) FinishSmartRoutingSessionOwner(_ context.Context, _ int64, _, _ string, groupID int64, token string, release bool) error {
	s.finishCalls++
	if s.finishErr != nil {
		return s.finishErr
	}
	if s.token != token {
		return nil
	}
	if release && s.owner == groupID {
		s.owner = 0
		s.releases++
	} else {
		s.commits++
	}
	s.token = ""
	return nil
}
func (s *smartRoutingAttemptCacheStub) GetSessionOwnerGroupID(context.Context, int64, string, string) (int64, error) {
	s.token = ""
	if s.owner == 0 {
		return 0, errors.New("missing owner")
	}
	return s.owner, nil
}
func (s *smartRoutingAttemptCacheStub) RefreshSessionOwnerTTL(context.Context, int64, string, string, time.Duration) error {
	s.token = ""
	return nil
}

func TestSmartRoutingAttemptSessionRollbackAndUsageProtection(t *testing.T) {
	for _, outcome := range []string{"retry", "success", "usage"} {
		t.Run(outcome, func(t *testing.T) {
			cache := &smartRoutingAttemptCacheStub{}
			ctx, state := WithSmartRoutingAttempt(context.Background())
			groupID := int64(10)
			key := &APIKey{SmartRouting: true, GroupID: &groupID, Group: &Group{ID: groupID, SessionIsolationEnabled: true}}
			require.NoError(t, ensureSessionIsolation(ctx, cache, key, 1, "openai", "session", time.Hour))
			require.True(t, state.CanReplay())
			// 同一 attempt 再检查自己新建的归属，不应当作其它请求提交。
			require.NoError(t, ensureSessionIsolation(ctx, cache, key, 1, "openai", "session", time.Hour))
			if outcome == "usage" {
				MarkSmartRoutingAttemptNonReplayable(ctx, "usage_submitted")
				require.False(t, state.CanReplay())
				require.Equal(t, "usage_submitted", state.NonReplayableReason())
			}
			require.NoError(t, FinishSmartRoutingAttempt(context.Background(), state, outcome != "success"))
			require.NoError(t, FinishSmartRoutingAttempt(context.Background(), state, true))
			if outcome == "retry" {
				require.Zero(t, cache.owner)
				require.Equal(t, 1, cache.releases)
			} else {
				require.Equal(t, int64(10), cache.owner)
				require.Equal(t, 1, cache.commits)
			}
		})
	}
}

func TestSmartRoutingAttemptPreservesExistingIsolatedSession(t *testing.T) {
	cache := &smartRoutingAttemptCacheStub{owner: 10}
	ctx, state := WithSmartRoutingAttempt(context.Background())
	groupID := int64(20)
	key := &APIKey{SmartRouting: true, GroupID: &groupID, Group: &Group{ID: groupID, SessionIsolationEnabled: true}}
	require.ErrorIs(t, ensureSessionIsolation(ctx, cache, key, 1, "openai", "session", time.Hour), ErrSessionIsolationConflict)
	require.NoError(t, FinishSmartRoutingAttempt(context.Background(), state, true))
	require.Equal(t, int64(10), cache.owner)
	require.Zero(t, cache.releases)
}

func TestSmartRoutingAttemptCleanupFailureRemainsNonReplayable(t *testing.T) {
	cleanupErr := errors.New("Redis unavailable during session cleanup")
	cache := &smartRoutingAttemptCacheStub{finishErr: cleanupErr}
	ctx, state := WithSmartRoutingAttempt(context.Background())
	groupID := int64(10)
	key := &APIKey{SmartRouting: true, GroupID: &groupID, Group: &Group{ID: groupID, SessionIsolationEnabled: true}}
	require.NoError(t, ensureSessionIsolation(ctx, cache, key, 1, "openai", "session", time.Hour))
	require.ErrorIs(t, FinishSmartRoutingAttempt(context.Background(), state, true), cleanupErr)
	require.False(t, state.CanReplay())
	require.Equal(t, "session_claim_cleanup_failed", state.NonReplayableReason())
	// 首次清理失败必须保留结果，重复收尾不能悄悄放行下一候选。
	require.ErrorIs(t, FinishSmartRoutingAttempt(context.Background(), state, true), cleanupErr)
	require.Equal(t, 1, cache.finishCalls)
	require.Equal(t, int64(10), cache.owner)
}

type smartRoutingCooldownCacheStub struct {
	GatewayCache
	remaining     time.Duration
	err           error
	reads, writes int
}

func (s *smartRoutingCooldownCacheStub) GetSmartRoutingCooldown(context.Context, int64, int64) (time.Duration, error) {
	s.reads++
	return s.remaining, s.err
}
func (s *smartRoutingCooldownCacheStub) CooldownSmartRoutingGroup(context.Context, int64, int64, time.Duration) error {
	s.writes++
	return s.err
}

func TestSmartRoutingCooldownServiceFailsOpenAndSkipsZeroWrite(t *testing.T) {
	cache := &smartRoutingCooldownCacheStub{err: errors.New("Redis unavailable")}
	svc := NewSmartRoutingService(&GatewayService{cache: cache}, nil)
	remaining, err := svc.GetSmartRoutingCooldown(context.Background(), 1, 2)
	require.NoError(t, err)
	require.Zero(t, remaining)
	cache.remaining = time.Second
	remaining, err = svc.GetSmartRoutingCooldown(context.Background(), 1, 2)
	require.NoError(t, err)
	require.Equal(t, time.Second, remaining)
	require.NoError(t, svc.CooldownSmartRoutingGroup(context.Background(), 1, 2, 0))
	require.Zero(t, cache.writes)
	require.NoError(t, svc.CooldownSmartRoutingGroup(context.Background(), 1, 2, time.Minute))
	require.Equal(t, 1, cache.writes)
}
