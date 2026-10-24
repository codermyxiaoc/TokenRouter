package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/pkg/chanshui"
	"github.com/stretchr/testify/require"
)

// 与实际仓储一样，每次读取返回独立账号快照，避免内存替身掩盖并发配置变化。
type chanshuiSecurityAccountReader struct {
	ChanshuiAccountReader
	first, current *Account
	reads          int
}

func (r *chanshuiSecurityAccountReader) GetByID(context.Context, int64) (*Account, error) {
	r.reads++
	if r.reads == 1 {
		return r.first, nil
	}
	return r.current, nil
}

func chanshuiSecurityCloneAccount(t *testing.T, a *Account) *Account {
	t.Helper()
	copy := *a
	data, err := json.Marshal(a.Credentials)
	require.NoError(t, err)
	copy.Credentials = nil
	require.NoError(t, json.Unmarshal(data, &copy.Credentials))
	return &copy
}

func TestChanshuiSecurityDisabledWhileDNSPendingCannotSubmit(t *testing.T) {
	s, repo, client, _ := newChanshuiFixture()
	enabled := true
	s.enabled = func(context.Context) bool { return enabled }
	s.resolvePublicHost = func(string) error { enabled = false; return nil }
	s.execute(context.Background(), chanshuiQueued())
	require.Zero(t, client.creates)
	require.Equal(t, "error", repo.run.Status)
	require.NotContains(t, repo.run.ErrorMessage, "sk-")
}

func TestChanshuiSecurityChangedAccountWhileDNSPendingCannotSubmit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Account)
	}{
		{"停用", func(a *Account) { a.Status = StatusDisabled; a.Schedulable = false }},
		{"替换密钥", func(a *Account) { a.Credentials["api_key"] = "sk-rotated-private" }},
		{"替换地址", func(a *Account) { a.Credentials["base_url"] = "https://changed.example/v1" }},
		{"替换映射", func(a *Account) { a.Credentials["model_mapping"] = map[string]any{"local-alias": "other-model"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, repo, client, a := newChanshuiFixture()
			reader := &chanshuiSecurityAccountReader{first: chanshuiSecurityCloneAccount(t, a), current: chanshuiSecurityCloneAccount(t, a)}
			s.accounts = reader
			s.resolvePublicHost = func(string) error { tc.mutate(reader.current); return nil }
			s.execute(context.Background(), chanshuiQueued())
			require.Zero(t, client.creates)
			require.Equal(t, "error", repo.run.Status)
			require.NotContains(t, repo.run.ErrorMessage, "sk-rotated")
			require.NotContains(t, repo.run.ErrorMessage, "changed.example")
		})
	}
}

func TestChanshuiSecurityUnsafeEndpointsAndUnsupportedAccounts(t *testing.T) {
	for _, rawURL := range []string{
		"ftp://public.example/v1", "http://localhost/v1", "http://127.0.0.1/v1", "http://[::1]/v1", "https://localhost/v1", "https://127.0.0.1/v1", "https://[::1]/v1",
		"https://10.1.2.3/v1", "https://169.254.169.254/v1", "https://user:secret@public.example/v1",
		"https://public.example/v1?api_key=sk-private", "https://public.example/v1#secret", "https://public.example\r\n/v1",
	} {
		t.Run(rawURL, func(t *testing.T) {
			_, _, _, a := newChanshuiFixture()
			a.Credentials["base_url"] = rawURL
			base, key, reason := chanshuiAccountEndpoint(a, "chat")
			require.Empty(t, base)
			require.Empty(t, key)
			require.NotEmpty(t, reason)
			require.NotContains(t, reason, rawURL)
			require.NotContains(t, reason, "sk-private")
		})
	}
	for _, mutate := range []func(*Account){
		func(a *Account) { a.Type = AccountTypeOAuth },
		func(a *Account) { a.Credentials["api_key"] = "" },
		func(a *Account) { a.Credentials["api_key"] = "invalid\nkey" },
		func(a *Account) { id := int64(1); a.ParentAccountID = &id; delete(a.Credentials, "api_key") },
		func(a *Account) { a.Platform = PlatformGemini; delete(a.Credentials, "base_url") },
	} {
		_, _, _, a := newChanshuiFixture()
		mutate(a)
		base, key, reason := chanshuiAccountEndpoint(a, "chat")
		require.Empty(t, base)
		require.Empty(t, key)
		require.NotEmpty(t, reason)
	}
}

func TestChanshuiSecurityDNSFailureNeverSubmitsOrLeaksReason(t *testing.T) {
	s, repo, client, _ := newChanshuiFixture()
	s.resolvePublicHost = func(string) error { return errors.New("private.example resolves to 127.0.0.1; sk-private") }
	s.execute(context.Background(), chanshuiQueued())
	require.Zero(t, client.creates)
	require.Equal(t, "error", repo.run.Status)
	for _, private := range []string{"private.example", "127.0.0.1", "sk-private"} {
		require.NotContains(t, repo.run.ErrorMessage, private)
	}
}

func TestChanshuiSecurityAutoProtocolUsesMatchingAccountEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name, platform, model, fixed, requested, wantProtocol, wantBase string
	}{
		{"CommandCodeClaude", PlatformCommandCode, "anthropic/claude-sonnet-5", APIProtocolAdaptive, "auto", "anthropic", "https://anthropic.example/v1"},
		{"CommandCodeGPT", PlatformCommandCode, "gpt-6", APIProtocolAdaptive, "auto", "openai", "https://responses.example/v1"},
		{"CommandCodeDeepSeek", PlatformCommandCode, "deepseek/deepseek-v4-flash", APIProtocolAdaptive, "auto", "chat", "https://chat.example/v1"},
		{"CommandCodeFixedChat", PlatformCommandCode, "claude-sonnet-5", APIProtocolChatCompletions, "auto", "chat", "https://chat.example/v1"},
		{"ExplicitProtocol", PlatformCommandCode, "claude-sonnet-5", APIProtocolChatCompletions, "anthropic", "anthropic", "https://anthropic.example/v1"},
		{"CNAdaptiveClaude", PlatformMiniMax, "claude-sonnet-5", APIProtocolAdaptive, "auto", "anthropic", "https://anthropic.example/v1"},
		{"CNAdaptiveGPT", PlatformMiniMax, "gpt-6", APIProtocolAdaptive, "auto", "openai", "https://responses.example/v1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, repo, client, a := newChanshuiFixture()
			a.Platform = tc.platform
			a.Credentials["api_protocol"] = tc.fixed
			a.Credentials["model_mapping"] = map[string]any{"local-alias": tc.model}
			a.Credentials["api_base_urls"] = map[string]any{APIProtocolAnthropic: "https://anthropic.example/v1", APIProtocolChatCompletions: "https://chat.example/v1", APIProtocolResponses: "https://responses.example/v1"}
			repo.config.Protocol = tc.requested
			client.create = func(in chanshui.CreateRequest) {
				require.Equal(t, tc.wantProtocol, in.Protocol)
				require.Equal(t, tc.wantBase, in.BaseURL)
				require.Equal(t, tc.model, in.Model)
			}
			s.execute(context.Background(), chanshuiQueued())
			require.Equal(t, 1, client.creates)
		})
	}
}
