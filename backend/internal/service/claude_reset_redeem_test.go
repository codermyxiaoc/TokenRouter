package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

const redeemTestOrg = "11111111-1111-4111-8111-111111111111"

type redeemAccountsStub struct{}

func (redeemAccountsStub) GetByID(_ context.Context, id int64) (*Account, error) {
	return &Account{ID: id, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Credentials: map[string]any{"scope": "user:profile"}}, nil
}

type redeemLeaseStub struct {
	mu   sync.Mutex
	keys map[string]bool
	fail bool
}

func (s *redeemLeaseStub) TryAcquireLeaderLock(_ context.Context, key, _ string, _ time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return false, errors.New("unavailable")
	}
	if s.keys == nil {
		s.keys = map[string]bool{}
	}
	if s.keys[key] {
		return false, nil
	}
	s.keys[key] = true
	return true, nil
}

func (s *redeemLeaseStub) ReleaseLeaderLock(_ context.Context, key, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.keys, key)
	return nil
}

type redeemFake struct {
	mu        sync.Mutex
	status    string // 用量响应
	claim     string // 兑换响应或网络错误
	claimHTTP int
	posts     []map[string]string
	gate      chan struct{} // 设置后让兑换等待通道关闭
	entered   chan struct{}
}

func (f *redeemFake) postCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.posts)
}

const redeemableStatus = `{"cedar_ember":{"eligible":true,"at_limit":true,"next_grant_id":"grant_next","grants":[` +
	`{"id":"grant_other","resets_left":1,"usable_now":true,"use_requires_limit":false,"clears":["five_hour"]},` +
	`{"id":"grant_next","resets_left":2,"usable_now":true,"use_requires_limit":true,"clears":["five_hour","seven_day"]}]}}`

func newRedeemService(t *testing.T, f *redeemFake) (*ClaudeResetCreditService, *inMemoryIdempotencyRepo, *redeemLeaseStub) {
	t.Helper()
	repo := newInMemoryIdempotencyRepo()
	cfg := DefaultIdempotencyConfig()
	cfg.FailedRetryBackoff = 0
	locks := &redeemLeaseStub{}
	s := &ClaudeResetCreditService{accounts: redeemAccountsStub{}, tokens: resetTokenStub{}, now: time.Now}
	s.ConfigureRedemption(NewIdempotencyCoordinator(repo, cfg), locks)
	if f.status == "" {
		f.status = redeemableStatus
	}
	s.do = func(r *http.Request, _ string) (*http.Response, error) {
		require.Equal(t, "Bearer synthetic-token", r.Header.Get("Authorization"))
		code := http.StatusOK
		var body string
		switch {
		case r.Method == http.MethodGet && r.URL.String() == claudeResetProfileURL:
			body = `{"organization":{"uuid":"` + redeemTestOrg + `"}}`
		case r.Method == http.MethodGet && r.URL.String() == claudeResetUsageURL:
			f.mu.Lock()
			body = f.status
			f.mu.Unlock()
		case r.Method == http.MethodPost:
			require.Equal(t, "https://api.anthropic.com/api/organizations/"+redeemTestOrg+"/reset_rate_limits", r.URL.String())
			var payload map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			require.Regexp(t, `^[A-Za-z0-9_-]{1,64}$`, payload["request_id"])
			f.mu.Lock()
			f.posts = append(f.posts, payload)
			f.mu.Unlock()
			if f.entered != nil {
				f.entered <- struct{}{}
			}
			if f.gate != nil {
				<-f.gate
			}
			if f.claim == "network-error" {
				return nil, errors.New("timeout private upstream details")
			}
			body = f.claim
			if f.claimHTTP != 0 {
				code = f.claimHTTP
			}
		default:
			t.Fatalf("unexpected upstream request %s %s", r.Method, r.URL)
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body))}, nil
	}
	return s, repo, locks
}

func TestClaudeResetRedeemHappyPathServerPicksNextGrant(t *testing.T) {
	f := &redeemFake{claim: `{"result":"reset","resets_left":1,"cleared":["five_hour","seven_day"]}`}
	s, _, _ := newRedeemService(t, f)
	out, err := s.Redeem(context.Background(), 1, "op-1")
	require.NoError(t, err)
	require.Equal(t, ClaudeResetOutcomeReset, out.Outcome)
	require.False(t, out.Replayed)
	require.Equal(t, []string{"five_hour", "seven_day"}, out.Cleared)
	require.NotNil(t, out.Credits)
	require.Equal(t, 1, f.postCount())
	require.Equal(t, "grant_next", f.posts[0]["grant_id"])
	require.Equal(t, "cedar_ember", f.posts[0]["program"])

	raw, err := json.Marshal(out)
	require.NoError(t, err)
	for _, secret := range []string{"grant_next", "grant_other", redeemTestOrg, "synthetic-token", f.posts[0]["request_id"], `"id"`} {
		require.NotContains(t, string(raw), secret)
	}
}

func TestClaudeResetRedeemNonRedeemableSendsNoPost(t *testing.T) {
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	cases := map[string]string{
		"not at limit":   strings.Replace(redeemableStatus, `"at_limit":true`, `"at_limit":false`, 1),
		"ineligible":     strings.Replace(redeemableStatus, `"eligible":true`, `"eligible":false`, 1),
		"cooldown":       strings.Replace(redeemableStatus, `"eligible":true,`, `"eligible":true,"cooldown_until":"`+future+`",`, 1),
		"no next grant":  strings.Replace(redeemableStatus, `"next_grant_id":"grant_next"`, `"next_grant_id":"missing"`, 1),
		"blocked":        strings.Replace(redeemableStatus, `"clears":["five_hour","seven_day"]`, `"clears":["five_hour","seven_day"],"blocking":["x"]`, 1),
		"paused":         strings.Replace(redeemableStatus, `"id":"grant_next",`, `"id":"grant_next","paused":true,`, 1),
		"not usable now": strings.Replace(redeemableStatus, `"id":"grant_next","resets_left":2,"usable_now":true`, `"id":"grant_next","resets_left":2,"usable_now":false`, 1),
		"expired":        strings.Replace(redeemableStatus, `"id":"grant_next",`, `"id":"grant_next","ends_at":"2000-01-01T00:00:00Z",`, 1),
		"not started":    strings.Replace(redeemableStatus, `"id":"grant_next",`, `"id":"grant_next","starts_at":"`+future+`",`, 1),
		"no resets left": strings.Replace(redeemableStatus, `"id":"grant_next","resets_left":2`, `"id":"grant_next","resets_left":0`, 1),
		"no program":     `{"cedar_ember":null}`,
	}
	for name, status := range cases {
		t.Run(name, func(t *testing.T) {
			require.NotEqual(t, redeemableStatus, status)
			f := &redeemFake{status: status, claim: `{"result":"reset"}`}
			s, _, _ := newRedeemService(t, f)
			_, err := s.Redeem(context.Background(), 1, "op-1")
			require.Error(t, err)
			require.Equal(t, "CLAUDE_RESET_NOT_AVAILABLE", infraerrors.Reason(err))
			require.Zero(t, f.postCount())
		})
	}
}

func TestClaudeResetRedeemRequiresKeyAndStores(t *testing.T) {
	f := &redeemFake{claim: `{"result":"reset"}`}
	s, _, locks := newRedeemService(t, f)
	_, err := s.Redeem(context.Background(), 1, "  ")
	require.ErrorIs(t, err, ErrIdempotencyKeyRequired)
	locks.fail = true
	_, err = s.Redeem(context.Background(), 1, "op-1")
	require.Error(t, err)
	unconfigured := &ClaudeResetCreditService{accounts: redeemAccountsStub{}, tokens: resetTokenStub{}, now: time.Now}
	_, err = unconfigured.Redeem(context.Background(), 1, "op-2")
	require.ErrorIs(t, err, ErrIdempotencyStoreUnavail)
	require.Zero(t, f.postCount())
}

func TestClaudeResetRedeemSameKeyReplaysWithoutSecondPost(t *testing.T) {
	f := &redeemFake{claim: `{"result":"reset"}`}
	s, _, _ := newRedeemService(t, f)
	first, err := s.Redeem(context.Background(), 1, "op-1")
	require.NoError(t, err)
	again, err := s.Redeem(context.Background(), 1, "op-1")
	require.NoError(t, err)
	require.True(t, again.Replayed)
	require.Equal(t, first.Outcome, again.Outcome)
	require.Equal(t, 1, f.postCount())

	// 新确认必须使用新的上游操作标识。
	_, err = s.Redeem(context.Background(), 1, "op-2")
	require.NoError(t, err)
	require.Equal(t, 2, f.postCount())
	require.NotEqual(t, f.posts[0]["request_id"], f.posts[1]["request_id"])
}

func TestClaudeResetRedeemDuplicateOrgAccountsConcurrentOnlyOnePost(t *testing.T) {
	f := &redeemFake{claim: `{"result":"reset"}`, gate: make(chan struct{}), entered: make(chan struct{}, 2)}
	s, _, _ := newRedeemService(t, f)
	type res struct {
		out *ClaudeResetOutcome
		err error
	}
	first := make(chan res, 1)
	go func() {
		out, err := s.Redeem(context.Background(), 1, "account-one")
		first <- res{out, err}
	}()
	<-f.entered // 账号一持有组织锁，正处于兑换中。
	_, err := s.Redeem(context.Background(), 2, "account-two")
	require.Error(t, err)
	require.Equal(t, "CLAUDE_RESET_BUSY", infraerrors.Reason(err))
	close(f.gate)
	r := <-first
	require.NoError(t, r.err)
	require.Equal(t, ClaudeResetOutcomeReset, r.out.Outcome)
	require.Equal(t, 1, f.postCount())
}

// 同组织重复账号必须共享锁和未知结果保护，不同组织不应被连带阻塞。
func TestClaudeResetRedeemParallelOrganizationsKeepUnknownFenceIsolated(t *testing.T) {
	s, _, _ := newRedeemService(t, &redeemFake{})
	const otherOrg = "22222222-2222-4222-8222-222222222222"
	started, release := make(chan struct{}, 1), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var mu sync.Mutex
	posts := map[string]int{}
	s.do = func(r *http.Request, _ string) (*http.Response, error) {
		account := r.Context().Value(claudeResetAccountContextKey{}).(*Account)
		org := redeemTestOrg
		if account.ID == 3 {
			org = otherOrg
		}
		body := redeemableStatus
		switch {
		case r.URL.String() == claudeResetProfileURL:
			body = `{"organization":{"uuid":"` + org + `"}}`
		case r.Method == http.MethodPost:
			require.Equal(t, "https://api.anthropic.com/api/organizations/"+org+"/reset_rate_limits", r.URL.String())
			mu.Lock()
			posts[org]++
			mu.Unlock()
			if org == redeemTestOrg {
				started <- struct{}{}
				<-release
				return nil, errors.New("connection lost after request was sent")
			}
			body = `{"result":"reset"}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	}
	type result struct {
		out *ClaudeResetOutcome
		err error
	}
	done := make(chan result, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { out, err := s.Redeem(ctx, 1, "parallel-one"); done <- result{out, err} }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("组织一未发起兑换")
	}
	// 原确认在途时不能发起第二次兑换；不同本地账号同样受组织锁约束。
	_, err := s.Redeem(context.Background(), 1, "parallel-one")
	require.ErrorIs(t, err, ErrIdempotencyInProgress)
	_, err = s.Redeem(context.Background(), 2, "parallel-two")
	require.Equal(t, "CLAUDE_RESET_BUSY", infraerrors.Reason(err))
	other, err := s.Redeem(context.Background(), 3, "parallel-other")
	require.NoError(t, err)
	require.Equal(t, ClaudeResetOutcomeReset, other.Outcome)
	cancel()
	releaseOnce.Do(func() { close(release) })
	first := <-done
	require.NoError(t, first.err)
	require.Equal(t, ClaudeResetOutcomeUnknown, first.out.Outcome)
	_, err = s.Redeem(context.Background(), 2, "after-disconnect")
	require.Equal(t, "CLAUDE_RESET_UNRESOLVED", infraerrors.Reason(err))
	replayed, err := s.Redeem(context.Background(), 1, "parallel-one")
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.Equal(t, ClaudeResetOutcomeUnknown, replayed.Outcome)
	other, err = s.Redeem(context.Background(), 3, "other-later")
	require.NoError(t, err)
	require.Equal(t, ClaudeResetOutcomeReset, other.Outcome)
	mu.Lock()
	require.Equal(t, map[string]int{redeemTestOrg: 1, otherOrg: 2}, posts)
	mu.Unlock()
}

// 兑换必须完成结果落库，同时不能改变订阅等业务共用的幂等超时。
func TestClaudeResetRedeemKeepsSharedTimeoutAndSurvivesClientCancel(t *testing.T) {
	f := &redeemFake{claim: `{"result":"reset","cleared":["five_hour"]}`, gate: make(chan struct{}), entered: make(chan struct{}, 1)}
	s, repo, _ := newRedeemService(t, f)
	originalTimeout := s.idempotency.cfg.ProcessingTimeout
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var closeOnce sync.Once
	release := func() { closeOnce.Do(func() { close(f.gate) }) }
	defer release()
	done := make(chan error, 1)
	go func() { _, err := s.Redeem(ctx, 1, "isolated-timeout"); done <- err }()
	select {
	case <-f.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("兑换未进入受保护请求")
	}
	operation := HashIdempotencyKey("claude-reset:1:isolated-timeout")
	record, err := repo.GetByScopeAndKeyHash(context.Background(), claudeResetOperationScope, HashIdempotencyKey(operation))
	require.NoError(t, err)
	require.NotNil(t, record)
	require.NotNil(t, record.LockedUntil)
	require.Greater(t, time.Until(*record.LockedUntil), 60*time.Second)
	require.Equal(t, originalTimeout, s.idempotency.cfg.ProcessingTimeout)
	cancel()
	release()
	require.NoError(t, <-done)
	out, err := s.Redeem(context.Background(), 1, "isolated-timeout")
	require.NoError(t, err)
	require.True(t, out.Replayed)
	require.Equal(t, ClaudeResetOutcomeReset, out.Outcome)
	require.Equal(t, 1, f.postCount())
}

func TestClaudeResetRedeemUnknownOutcomeFencesOrganization(t *testing.T) {
	for _, claim := range []string{"network-error", `{broken`, `{"result":"weird"}`, `{"result":"unavailable","reason":"stamp_indeterminate"}`, `{"result":"reset","reason":"reset_unconfirmed"}`, "http-500"} {
		t.Run(claim, func(t *testing.T) {
			f := &redeemFake{claim: claim}
			if claim == "http-500" {
				f.claim, f.claimHTTP = `{"result":"reset"}`, http.StatusInternalServerError
			}
			s, _, _ := newRedeemService(t, f)
			out, err := s.Redeem(context.Background(), 1, "op-1")
			require.NoError(t, err)
			require.Equal(t, ClaudeResetOutcomeUnknown, out.Outcome)
			require.Nil(t, out.Credits)
			require.NotContains(t, out.Reason, "private")

			// 同一确认只回放结果，不再次发送。
			again, err := s.Redeem(context.Background(), 1, "op-1")
			require.NoError(t, err)
			require.True(t, again.Replayed)
			require.Equal(t, ClaudeResetOutcomeUnknown, again.Outcome)

			// 模拟重启及新锁实例，同组织重复账号仍受持久保护记录约束。
			s.locks = &redeemLeaseStub{}
			for _, id := range []int64{1, 2} {
				_, err = s.Redeem(context.Background(), id, "op-new")
				require.Error(t, err)
				require.Equal(t, "CLAUDE_RESET_UNRESOLVED", infraerrors.Reason(err))
			}
			require.Equal(t, 1, f.postCount())

			// 超过短保护期后，未知结果仍应保持锁定。
			s.now = func() time.Time { return time.Now().Add(claudeResetUnavailableFenceTTL + time.Minute) }
			_, err = s.Redeem(context.Background(), 1, "op-new")
			require.Equal(t, "CLAUDE_RESET_UNRESOLVED", infraerrors.Reason(err))

			// 保护期结束后重新依据上游查询决定资格。
			s.now = func() time.Time { return time.Now().Add(claudeResetUnknownFenceTTL + time.Minute) }
			f.claim, f.claimHTTP = `{"result":"reset"}`, 0
			out, err = s.Redeem(context.Background(), 1, "op-later")
			require.NoError(t, err)
			require.Equal(t, ClaudeResetOutcomeReset, out.Outcome)
			require.Equal(t, 2, f.postCount())
		})
	}
}

func TestClaudeResetRedeemCrashAfterMarkerNeverResends(t *testing.T) {
	f := &redeemFake{claim: `{"result":"reset"}`}
	s, _, _ := newRedeemService(t, f)
	// 模拟持久标记写入后进程在兑换途中退出。
	orgHash := HashIdempotencyKey("claude-org:" + redeemTestOrg)
	fence, err := s.loadFence(context.Background(), orgHash)
	require.NoError(t, err)
	op := HashIdempotencyKey("claude-reset:1:op-1")
	require.NoError(t, s.persistFence(context.Background(), fence.ID, claudeResetFence{Operation: op, Outcome: ClaudeResetOutcomeUnknown, Reason: "claim_unconfirmed", At: time.Now()}))
	out, err := s.Redeem(context.Background(), 1, "op-1")
	require.NoError(t, err)
	require.Equal(t, ClaudeResetOutcomeUnknown, out.Outcome)
	require.True(t, out.Replayed)
	require.Zero(t, f.postCount())
}

func TestClaudeResetRedeemMapsUpstreamResults(t *testing.T) {
	cases := []struct {
		claim, outcome string
		http           int
	}{
		{`{"result":"reset"}`, ClaudeResetOutcomeReset, 0},
		{`{"result":"already_used"}`, ClaudeResetOutcomeAlreadyUsed, 0},
		{`{"result":"not_limited"}`, ClaudeResetOutcomeNotLimited, 0},
		{`{"result":"cooldown","cooldown_until":"2099-01-01T00:00:00Z"}`, ClaudeResetOutcomeCooldown, 0},
		{`{"result":"ineligible","reason":"tenure"}`, ClaudeResetOutcomeIneligible, 0},
		{`{"result":"unavailable","reason":"stamp_indeterminate"}`, ClaudeResetOutcomeUnknown, 0},
		{`{}`, ClaudeResetOutcomeIneligible, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.claim, func(t *testing.T) {
			f := &redeemFake{claim: tc.claim, claimHTTP: tc.http}
			s, _, _ := newRedeemService(t, f)
			out, err := s.Redeem(context.Background(), 1, "op-1")
			require.NoError(t, err)
			require.Equal(t, tc.outcome, out.Outcome)
			if tc.outcome == ClaudeResetOutcomeCooldown {
				require.NotNil(t, out.CooldownUntil)
			}
			if tc.outcome == ClaudeResetOutcomeIneligible && tc.http == 0 {
				require.Equal(t, "tenure", out.Reason)
			}
			// 明确结果不阻止后续独立确认。
			if tc.outcome != ClaudeResetOutcomeUnknown {
				_, err = s.Redeem(context.Background(), 1, "op-2")
				require.NoError(t, err)
				require.Equal(t, 2, f.postCount())
			}
		})
	}
}

func TestClaudeResetRedeemSanitizesUpstreamReason(t *testing.T) {
	f := &redeemFake{claim: `{"result":"ineligible","reason":"Bearer synthetic-token leaked <script>","cleared":["five_hour","<bad>"]}`}
	s, _, _ := newRedeemService(t, f)
	out, err := s.Redeem(context.Background(), 1, "op-1")
	require.NoError(t, err)
	require.Empty(t, out.Reason)
	require.Equal(t, []string{"five_hour"}, out.Cleared)
}

func TestClaudeResetRedeemExplicitUnavailableFencesBriefly(t *testing.T) {
	f := &redeemFake{claim: `{"result":"unavailable","reason":"grant_next"}`}
	s, _, _ := newRedeemService(t, f)
	out, err := s.Redeem(context.Background(), 1, "op-1")
	require.NoError(t, err)
	require.Equal(t, ClaudeResetOutcomeUnknown, out.Outcome)
	require.Equal(t, claudeResetReasonUnavailable, out.Reason)

	s.locks = &redeemLeaseStub{}
	_, err = s.Redeem(context.Background(), 2, "op-new")
	require.Equal(t, "CLAUDE_RESET_UPSTREAM_UNAVAILABLE", infraerrors.Reason(err))
	require.Equal(t, 1, f.postCount())

	s.now = func() time.Time { return time.Now().Add(claudeResetUnavailableFenceTTL + time.Minute) }
	f.claim = `{"result":"reset"}`
	out, err = s.Redeem(context.Background(), 1, "op-later")
	require.NoError(t, err)
	require.Equal(t, ClaudeResetOutcomeReset, out.Outcome)
	require.Equal(t, 2, f.postCount())
}

func TestClaudeResetRedeemNeverEchoesGrantIDs(t *testing.T) {
	f := &redeemFake{claim: `{"result":"reset","reason":"grant_next","cleared":["five_hour","grant_next","launch","seven_day_overage_included"]}`}
	s, _, _ := newRedeemService(t, f)
	out, err := s.Redeem(context.Background(), 1, "op-1")
	require.NoError(t, err)
	raw, err := json.Marshal(out)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "grant_next")
	require.NotContains(t, string(raw), "launch")
	require.Empty(t, out.Reason)
	require.Equal(t, []string{"five_hour", "seven_day_overage_included"}, out.Cleared)
}
