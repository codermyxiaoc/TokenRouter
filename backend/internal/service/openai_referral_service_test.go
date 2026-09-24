package service

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/TokenFlux/TokenRouter/internal/model"
	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	"net/http"
	"strings"
	"testing"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type referralClientStub struct {
	eligibility       *OpenAIReferralEligibility
	queryErr, sendErr error
	calls             []OpenAIReferralCall
	emails            []string
}

func (s *referralClientStub) QueryEligibility(_ context.Context, call OpenAIReferralCall) (*OpenAIReferralEligibility, error) {
	s.calls = append(s.calls, call)
	return s.eligibility, s.queryErr
}
func (s *referralClientStub) SendInvite(_ context.Context, call OpenAIReferralCall, email string) error {
	s.calls = append(s.calls, call)
	s.emails = append(s.emails, email)
	return s.sendErr
}

func referralTestService(t *testing.T, plan string, client OpenAIReferralClient) (*OpenAIQuotaService, *stubQuotaAccountRepo) {
	t.Helper()
	a := &Account{ID: 100, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Credentials: map[string]any{"chatgpt_account_id": "workspace-test", "plan_type": plan}}
	repo := &stubQuotaAccountRepo{accounts: map[int64]*Account{100: a}}
	tokens := &stubQuotaTokenCache{tokens: map[string]string{OpenAITokenCacheKey(a): "test-token"}}
	svc := NewOpenAIQuotaService(stubQuotaAdminService{repo: repo}, &codexInviteResetHTTPUpstreamStub{}, NewOpenAITokenProvider(repo, tokens, nil), nil, nil)
	svc.accountRepo = repo
	svc.referralClient = client
	return svc, repo
}

func TestOpenAIReferralSend(t *testing.T) {
	for _, tc := range []struct{ plan, program string }{
		{"plus", openAIReferralConsumer}, {"team", openAIReferralWorkspace},
		{"self_serve_business_usage_based", openAIReferralWorkspace},
	} {
		t.Run(tc.plan, func(t *testing.T) {
			send, reward := 8, 3
			client := &referralClientStub{eligibility: &OpenAIReferralEligibility{
				ShouldShow: true, RemainingSendCapacity: &send, RemainingRewardCapacity: &reward,
				Grants: []OpenAIReferralGrant{{GrantType: "rate_limit_reset_credit", Amount: 1, Recipient: "referrer"}},
				Rules:  []string{"Offer rule"},
			}}
			svc, repo := referralTestService(t, tc.plan, client)
			eligibility, err := svc.QueryReferralEligibility(context.Background(), 100)
			require.NoError(t, err)
			require.Equal(t, 3, *eligibility.AvailableInvites)
			require.Equal(t, []string{"Offer rule"}, eligibility.Rules)
			require.Positive(t, eligibility.FetchedAt)
			require.NoError(t, svc.CacheReferralSnapshot(context.Background(), 100, eligibility))
			require.Equal(t, eligibility, repo.extraUpdates[100][openAIReferralSnapshotKey])
			result, err := svc.SendReferralInvite(context.Background(), 100, OpenAIReferralSendRequest{
				Email: " friend@example.com ", ProgramID: tc.program, Confirmed: true,
			})
			require.NoError(t, err)
			require.True(t, result.Sent)
			require.Equal(t, "friend@example.com", result.Email)
			require.Equal(t, []string{"friend@example.com"}, client.emails)
			require.Len(t, client.calls, 3, "send must recheck eligibility")
			for _, call := range client.calls {
				require.Equal(t, tc.program, call.ProgramID)
				require.NotNil(t, call.Do, "邀请必须复用当前账号的完整传输配置")
			}
		})
	}
}

func TestOpenAIReferralSendGuards(t *testing.T) {
	for _, tc := range []struct {
		name, email, body, reason string
		confirmed, shadow         bool
	}{
		{"invalid email", "bad-email", `{}`, "OPENAI_REFERRAL_INVALID_EMAIL", true, false},
		{"multiple emails", "a@example.com,b@example.com", `{}`, "OPENAI_REFERRAL_INVALID_EMAIL", true, false},
		{"display name", "User <a@example.com>", `{}`, "OPENAI_REFERRAL_INVALID_EMAIL", true, false},
		{"header injection", "a@example.com\r\nBcc: b@example.com", `{}`, "OPENAI_REFERRAL_INVALID_EMAIL", true, false},
		{"no consent", "a@example.com", `{"should_show":true,"remaining_send_capacity":2}`, "OPENAI_REFERRAL_CONFIRMATION_REQUIRED", false, false},
		{"ineligible", "a@example.com", `{"should_show":false,"remaining_send_capacity":2}`, "OPENAI_REFERRAL_UNAVAILABLE", true, false},
		{"exhausted", "a@example.com", `{"should_show":true,"remaining_send_capacity":0}`, "OPENAI_REFERRAL_UNAVAILABLE", true, false},
		{"unknown capacity", "a@example.com", `{"should_show":true}`, "OPENAI_REFERRAL_UNAVAILABLE", true, false},
		{"reward exhausted", "a@example.com", `{"should_show":true,"remaining_send_capacity":3,"offer_id":"credits_250","remaining_reward_capacity":0}`, "OPENAI_REFERRAL_UNAVAILABLE", true, false},
		{"unknown reward capacity", "a@example.com", `{"should_show":true,"remaining_send_capacity":3,"grants":[{}]}`, "OPENAI_REFERRAL_UNAVAILABLE", true, false},
		{"shadow", "a@example.com", `{}`, "OPENAI_REFERRAL_SHADOW_ACCOUNT", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &referralClientStub{}
			require.NoError(t, json.Unmarshal([]byte(tc.body), &client.eligibility))
			svc, repo := referralTestService(t, "plus", client)
			if tc.shadow {
				parentID := int64(200)
				repo.accounts[100].ParentAccountID = &parentID
			}
			_, err := svc.SendReferralInvite(context.Background(), 100, OpenAIReferralSendRequest{Email: tc.email, ProgramID: openAIReferralConsumer, Confirmed: tc.confirmed})
			require.Equal(t, tc.reason, infraerrors.Reason(err))
			require.Empty(t, client.emails)
		})
	}
}

func TestOpenAIReferralSendStopsOnQueryError(t *testing.T) {
	client := &referralClientStub{queryErr: errors.New("eligibility unavailable")}
	svc, _ := referralTestService(t, "plus", client)
	_, err := svc.SendReferralInvite(context.Background(), 100, OpenAIReferralSendRequest{Email: "friend@example.com", ProgramID: openAIReferralConsumer, Confirmed: true})
	require.ErrorIs(t, err, client.queryErr)
	require.Empty(t, client.emails)
}

func TestOpenAIReferralSendPreservesUnknownOutcomeWithoutRetry(t *testing.T) {
	count := 2
	client := &referralClientStub{
		eligibility: &OpenAIReferralEligibility{ShouldShow: true, RemainingSendCapacity: &count},
		sendErr:     infraerrors.New(502, "OPENAI_REFERRAL_SEND_UNKNOWN", "outcome unknown"),
	}
	svc, _ := referralTestService(t, "plus", client)
	_, err := svc.SendReferralInvite(context.Background(), 100, OpenAIReferralSendRequest{Email: "friend@example.com", ProgramID: openAIReferralConsumer, Confirmed: true})
	require.ErrorIs(t, err, client.sendErr)
	require.Len(t, client.emails, 1)
}

// 邀请继承账号选择的代理、TLS 和身份；已发送请求的异常不进入额度接口的自动重试。
type referralTransportCapture struct {
	HTTPUpstream
	request            *http.Request
	proxy              string
	profile            *tlsfingerprint.Profile
	accountID          int64
	concurrency, calls int
}

func (s *referralTransportCapture) DoWithTLS(r *http.Request, proxy string, id int64, concurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	s.request = r
	s.proxy = proxy
	s.profile = profile
	s.accountID = id
	s.concurrency = concurrency
	s.calls++
	return nil, errors.New("response lost")
}
func TestOpenAIReferralTransportRetainsAccountSettingsWithoutReplay(t *testing.T) {
	proxyID, profileID := int64(4), int64(20)
	account := &Account{ID: 100, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 3, ProxyID: &proxyID,
		Credentials: map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "workspace-test"},
		Extra:       map[string]any{"enable_tls_fingerprint": true, "tls_fingerprint_profile_id": int64(10), "tls_fingerprint_router_id": int64(9)}}
	proxy := &Proxy{ID: 4, Protocol: "http", Host: "proxy.example", Port: 8080}
	upstream := &referralTransportCapture{}
	router := &openAIOAuthTokenRouterReaderStub{routers: map[int64]*model.TLSFingerprintRouter{9: {ID: 9, Enabled: true, CodexInviteResetUserAgent: " custom-quota-ua ", CodexInviteResetTLSFingerprintProfileID: &profileID}}}
	profiles := &TLSFingerprintProfileService{localCache: map[int64]*model.TLSFingerprintProfile{10: {ID: 10, Name: "account"}, 20: {ID: 20, Name: "quota-router"}}}
	svc := NewOpenAIQuotaService(codexInviteResetAdminServiceStub{account: account, proxy: proxy}, upstream, nil, profiles, router)
	svc.referralClient = &referralClientStub{}
	call, err := svc.referralCall(context.Background(), 100, openAIReferralConsumer)
	require.NoError(t, err)
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://chatgpt.com/backend-api/referrals/invite", strings.NewReader(`{}`))
	require.NoError(t, err)
	_, err = call.Do(request)
	require.Error(t, err)
	require.Equal(t, 1, upstream.calls)
	require.Equal(t, "http://proxy.example:8080", upstream.proxy)
	require.EqualValues(t, 100, upstream.accountID)
	require.Equal(t, 3, upstream.concurrency)
	require.NotNil(t, upstream.profile)
	require.Equal(t, "quota-router", upstream.profile.Name)
	require.Equal(t, "custom-quota-ua", upstream.request.Header.Get("User-Agent"))
	require.Equal(t, "Bearer oauth-token", upstream.request.Header.Get("Authorization"))
	require.Equal(t, "workspace-test", upstream.request.Header.Get("ChatGPT-Account-ID"))
	require.True(t, HTTPUpstreamRedirectsDisabled(upstream.request.Context()))
}
