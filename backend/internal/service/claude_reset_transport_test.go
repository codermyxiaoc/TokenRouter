package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

// 通过共享传输桩验证保护标记，避免测试消耗真实上游额度。
type claudeResetTransportStub struct {
	HTTPUpstream
	t     *testing.T
	calls int
}

func (u *claudeResetTransportStub) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	u.calls++
	require.Equal(u.t, int64(27), id)
	require.Equal(u.t, 3, concurrency)
	require.Empty(u.t, proxy)
	require.True(u.t, HTTPUpstreamRedirectsDisabled(req.Context()))
	require.True(u.t, HTTPUpstreamPublicHostsOnly(req.Context()))
	require.Equal(u.t, "Bearer synthetic-token", req.Header.Get("Authorization"))
	deadline, ok := req.Context().Deadline()
	require.True(u.t, ok)
	require.LessOrEqual(u.t, time.Until(deadline), 25*time.Second)
	require.Nil(u.t, profile)
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
}

func TestClaudeResetUsesAccountTransportAndDisablesRedirects(t *testing.T) {
	account := &Account{ID: 27, Concurrency: 3, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Credentials: map[string]any{"scope": "user:profile"}}
	s := NewClaudeResetCreditService(nil, nil, nil, nil)
	s.accounts, s.tokens = resetAccountStub{account}, resetTokenStub{}
	upstream := &claudeResetTransportStub{t: t}
	s.ConfigureTransport(upstream, nil)
	_, err := s.Query(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, 1, upstream.calls)
}

func TestClaudeResetMissingProxyDoesNotFallBackToDirect(t *testing.T) {
	proxyID := int64(9)
	s := NewClaudeResetCreditService(nil, nil, nil, nil)
	s.accounts = resetAccountStub{&Account{ID: 27, ProxyID: &proxyID, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Credentials: map[string]any{"scope": "user:profile"}}}
	s.do = func(*http.Request, string) (*http.Response, error) {
		t.Fatal("代理不可用时不得发起直连")
		return nil, nil
	}
	_, err := s.Query(context.Background(), 27)
	require.ErrorContains(t, err, "proxy unavailable")
}
