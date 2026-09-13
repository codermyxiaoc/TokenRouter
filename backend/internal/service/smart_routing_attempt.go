package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type smartRoutingAttemptContextKey struct{}

// SmartRoutingSessionClaimCache 保持原有数字 owner 格式，另用随机 token 保护本次新建的临时归属。
type SmartRoutingSessionClaimCache interface {
	ClaimSmartRoutingSessionOwner(context.Context, int64, string, string, int64, string, time.Duration) (bool, error)
	FinishSmartRoutingSessionOwner(context.Context, int64, string, string, int64, string, bool) error
}

type smartRoutingSessionClaim struct {
	cache               SmartRoutingSessionClaimCache
	userID              int64
	source, sessionHash string
	groupID             int64
}

// SmartRoutingAttemptState 只属于一次组内执行；已提交用量或其它副作用后不得跨组重放。
// @project-doc docs/domains/smart_routing_api_keys.md#request_selection
type SmartRoutingAttemptState struct {
	mu                  sync.Mutex
	token               string
	nonReplayableReason string
	claims              []smartRoutingSessionClaim
	finishOnce          sync.Once
	finishErr           error
}

func WithSmartRoutingAttempt(ctx context.Context) (context.Context, *SmartRoutingAttemptState) {
	state := &SmartRoutingAttemptState{token: uuid.NewString()}
	return context.WithValue(ctx, smartRoutingAttemptContextKey{}, state), state
}

func smartRoutingAttemptFromContext(ctx context.Context) *SmartRoutingAttemptState {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(smartRoutingAttemptContextKey{}).(*SmartRoutingAttemptState)
	return state
}

func (s *SmartRoutingAttemptState) CanReplay() bool {
	return s != nil && s.NonReplayableReason() == ""
}

func (s *SmartRoutingAttemptState) NonReplayableReason() string {
	if s == nil {
		return "missing_attempt_state"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nonReplayableReason
}

// MarkSmartRoutingAttemptNonReplayable 必须在异步用量任务入队前同步调用，避免后台执行晚于跨组决策。
func MarkSmartRoutingAttemptNonReplayable(ctx context.Context, reason string) {
	state := smartRoutingAttemptFromContext(ctx)
	if state == nil {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "side_effect_committed"
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.nonReplayableReason == "" {
		state.nonReplayableReason = reason
	}
}

func (s *SmartRoutingAttemptState) ownsSessionClaim(userID int64, source, hash string, groupID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, claim := range s.claims {
		if claim.userID == userID && claim.source == source && claim.sessionHash == hash && claim.groupID == groupID {
			return true
		}
	}
	return false
}

func (s *SmartRoutingAttemptState) addSessionClaim(claim smartRoutingSessionClaim) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claims = append(s.claims, claim)
}

// FinishSmartRoutingAttempt 在成功/不可重放时提交临时归属；安全换组前只释放仍属于本 token 的新归属。
// 调用方应提供有界清理 Context，清理失败时不能继续跨组；重复调用不会重复释放。
func FinishSmartRoutingAttempt(ctx context.Context, state *SmartRoutingAttemptState, replay bool) error {
	if state == nil {
		return nil
	}
	state.finishOnce.Do(func() {
		state.mu.Lock()
		release := replay && state.nonReplayableReason == ""
		claims := append([]smartRoutingSessionClaim(nil), state.claims...)
		token := state.token
		state.mu.Unlock()
		var failures []error
		for _, claim := range claims {
			if err := claim.cache.FinishSmartRoutingSessionOwner(ctx, claim.userID, claim.source, claim.sessionHash, claim.groupID, token, release); err != nil {
				failures = append(failures, err)
			}
		}
		state.finishErr = errors.Join(failures...)
		if state.finishErr != nil {
			// 清理结果不确定时保留失败状态，后续重复收尾不能将其误判成可重放。
			state.mu.Lock()
			if state.nonReplayableReason == "" {
				state.nonReplayableReason = "session_claim_cleanup_failed"
			}
			state.mu.Unlock()
		}
	})
	return state.finishErr
}
