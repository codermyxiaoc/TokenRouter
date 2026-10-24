package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/chanshui"
	"github.com/stretchr/testify/require"
)

// 候选目录用于选择提示，不能因补默认型号而绕过账号最终白名单。
func TestChanshuiModelCandidatesRespectAccountConfiguration(t *testing.T) {
	account := func(credentials map[string]any) *Account {
		return &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: credentials}
	}
	t.Run("无映射Claude显示默认候选", func(t *testing.T) {
		models := chanshuiModelCandidates(account(map[string]any{}))
		require.Contains(t, models, "claude-sonnet-4-6")
		require.Contains(t, models, "claude-opus-4-6")
	})
	t.Run("独立白名单不依赖映射", func(t *testing.T) {
		a := account(map[string]any{"model_whitelist": []any{"claude-private"}})
		require.Equal(t, []string{"claude-private"}, chanshuiModelCandidates(a))
	})
	t.Run("映射后校验白名单", func(t *testing.T) {
		a := account(map[string]any{
			"model_mapping":   map[string]any{"friendly": "claude-private", "blocked": "claude-blocked"},
			"model_whitelist": []any{"claude-private"},
		})
		require.Equal(t, []string{"claude-private", "friendly"}, chanshuiModelCandidates(a))
	})
	t.Run("不受限别名与平台目录同时显示", func(t *testing.T) {
		a := account(map[string]any{"model_mapping": map[string]any{"friendly": "claude-private"}, "model_whitelist": []any{}})
		models := chanshuiModelCandidates(a)
		require.Contains(t, models, "friendly")
		require.Contains(t, models, "claude-sonnet-4-6")
	})
	t.Run("通配映射展开后仍限制最终白名单", func(t *testing.T) {
		a := account(map[string]any{"model_mapping": map[string]any{"claude-sonnet-*": "claude-private"}, "model_whitelist": []any{"claude-private"}})
		models := chanshuiModelCandidates(a)
		require.Contains(t, models, "claude-sonnet-4-6")
		require.NotContains(t, models, "claude-opus-4-6")
		for _, model := range models {
			require.True(t, model == "claude-private" || strings.HasPrefix(model, "claude-sonnet-"))
			require.NotContains(t, model, "*")
		}
	})
	t.Run("媒体白名单不能回填文本默认型号", func(t *testing.T) {
		a := account(map[string]any{"model_whitelist": []any{"gpt-image-2"}})
		require.Empty(t, chanshuiModelCandidates(a))
	})
	t.Run("未知平台不套用Claude默认目录", func(t *testing.T) {
		a := account(map[string]any{})
		a.Platform = "future-provider"
		require.Empty(t, chanshuiModelCandidates(a))
	})
}

// 列表替身保留所有平台与状态，用于验证检测没有误用正常调度账号过滤。
type chanshuiTargetAccounts struct {
	ChanshuiAccountReader
	accounts []Account
	filters  []string
	groupID  int64
}

func (r *chanshuiTargetAccounts) ListAllWithFilters(_ context.Context, platform, accountType, status, search string, groupID int64, privacy string) ([]Account, error) {
	r.filters = []string{platform, accountType, status, search, privacy}
	r.groupID = groupID
	return r.accounts, nil
}

func TestChanshuiTargetsIncludeEveryAPIKeyPlatformAndState(t *testing.T) {
	s, _, _, fixture := newChanshuiFixture()
	reader := &chanshuiTargetAccounts{}
	for i, platform := range []string{PlatformAnthropic, PlatformOpenAI, PlatformGemini, PlatformQoder, PlatformTypeSafe, PlatformVideo, "future-provider"} {
		a := chanshuiSecurityCloneAccount(t, fixture)
		a.ID = int64(i + 1)
		a.Platform = platform
		a.Status = StatusDisabled
		a.Schedulable = false
		reader.accounts = append(reader.accounts, *a)
	}
	oauth := *fixture
	oauth.Type = AccountTypeOAuth
	reader.accounts = append(reader.accounts, oauth)
	missingKey := chanshuiSecurityCloneAccount(t, fixture)
	missingKey.ID = 99
	delete(missingKey.Credentials, "api_key")
	reader.accounts = append(reader.accounts, *missingKey)
	s.accounts = reader
	targets, err := s.Targets(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"", AccountTypeAPIKey, "", "", ""}, reader.filters)
	require.Zero(t, reader.groupID)
	require.Len(t, targets, 8)
	for _, target := range targets[:7] {
		require.True(t, target.Supported, target.Platform)
		require.NotEmpty(t, target.Warning)
		require.Empty(t, target.Reason)
	}
	require.False(t, targets[7].Supported)
	require.Contains(t, targets[7].Reason, "账号自身")
	raw, err := json.Marshal(targets)
	require.NoError(t, err)
	for _, secret := range []string{"sk-upstream-secret", "upstream.example", "credentials", "base_url"} {
		require.NotContains(t, string(raw), secret)
	}
}

func TestChanshuiPublicEndpointPreservesSavedSchemeAndOwnCredentials(t *testing.T) {
	for _, platform := range []string{PlatformOpenAI, PlatformAnthropic, PlatformGemini, PlatformTypeSafe, "future-provider"} {
		for _, scheme := range []string{"http", "https"} {
			t.Run(platform+scheme, func(t *testing.T) {
				_, _, _, a := newChanshuiFixture()
				a.Platform = platform
				a.Status = StatusDisabled
				a.Schedulable = false
				past := time.Now().Add(-time.Hour)
				a.ExpiresAt = &past
				parent, proxy := int64(22), int64(33)
				a.ParentAccountID, a.ProxyID = &parent, &proxy
				a.Credentials["base_url"] = scheme + "://relay.example/v1"
				a.Credentials["header_override_enabled"] = true
				a.Credentials["header_overrides"] = map[string]any{"X-Private": "secret-header"}
				base, key, reason := chanshuiAccountEndpoint(a, "chat")
				require.Empty(t, reason)
				require.Equal(t, scheme+"://relay.example/v1", base)
				require.Equal(t, "sk-upstream-secret", key)
				warning := chanshuiAccountWarning(a)
				require.Contains(t, warning, "仅配置的自动恢复规则")
				require.Contains(t, warning, "不使用本站代理或自定义请求头")
				require.NotContains(t, warning, "secret-header")
			})
		}
	}
}

func TestChanshuiProtocolEndpointsNeverFallbackToAnotherProvider(t *testing.T) {
	for _, platform := range []string{PlatformMiniMax, PlatformOpenAI, PlatformGemini, "future-provider"} {
		t.Run(platform, func(t *testing.T) {
			s, _, _, a := newChanshuiFixture()
			a.Platform = platform
			delete(a.Credentials, "base_url")
			a.Credentials["api_base_urls"] = map[string]any{APIProtocolAnthropic: "https://only-anthropic.example/v1"}
			base, _, reason := chanshuiAccountEndpoint(a, "chat")
			require.Empty(t, base)
			require.Contains(t, reason, "所选协议")
			base, _, reason = chanshuiAccountEndpoint(a, "anthropic")
			require.Empty(t, reason)
			require.Equal(t, "https://only-anthropic.example/v1", base)
			targets, err := s.Targets(context.Background())
			require.NoError(t, err)
			require.True(t, targets[0].Supported, "列表不应禁止之后选择可用的显式协议")
		})
	}
	for _, platform := range []string{PlatformGemini, PlatformTypeSafe, PlatformVideo, "future-provider"} {
		_, _, _, a := newChanshuiFixture()
		a.Platform = platform
		delete(a.Credentials, "base_url")
		base, key, reason := chanshuiAccountEndpoint(a, "chat")
		require.Empty(t, base)
		require.Empty(t, key)
		require.NotEmpty(t, reason)
	}
}

func TestChanshuiOpenAIResponsesOnlyAccountRemainsSelectable(t *testing.T) {
	s, repo, client, a := newChanshuiFixture()
	delete(a.Credentials, "base_url")
	a.Credentials["api_base_urls"] = map[string]any{APIProtocolResponses: "https://responses-only.example/v1"}
	repo.config.Protocol = "auto"
	targets, err := s.Targets(context.Background())
	require.NoError(t, err)
	require.True(t, targets[0].Supported)
	input := chanshuiInput()
	input.Protocol = "auto"
	_, err = s.SaveConfig(context.Background(), 0, input)
	require.NoError(t, err)
	client.create = func(in chanshui.CreateRequest) {
		require.Equal(t, "openai", in.Protocol)
		require.Equal(t, "https://responses-only.example/v1", in.BaseURL)
	}
	s.execute(context.Background(), chanshuiQueued())
	require.Equal(t, 1, client.creates)
}

func TestChanshuiManualDiagnosisDoesNotResumePausedAccounts(t *testing.T) {
	for _, state := range []string{"disabled", "unschedulable", "expired"} {
		t.Run(state, func(t *testing.T) {
			s, repo, client, a := newChanshuiFixture()
			switch state {
			case "disabled":
				a.Status = StatusDisabled
			case "unschedulable":
				a.Schedulable = false
			case "expired":
				past := time.Now().Add(-time.Hour)
				a.ExpiresAt = &past
			}
			snapshot := chanshuiAccountSnapshot(a)
			_, err := s.SaveConfig(context.Background(), 0, chanshuiInput())
			require.NoError(t, err)
			_, err = s.Run(context.Background(), repo.config.ID)
			require.NoError(t, err)
			run := chanshuiQueued()
			run.Trigger = "manual"
			s.execute(context.Background(), run)
			require.Equal(t, 1, client.creates)
			require.Equal(t, "running", repo.run.Status)
			require.Equal(t, snapshot, chanshuiAccountSnapshot(a))
			for _, trigger := range []string{"schedule", ""} {
				run = chanshuiQueued()
				run.Trigger = trigger
				s.execute(context.Background(), run)
				require.Equal(t, 1, client.creates)
				require.Equal(t, "error", repo.run.Status)
			}
		})
	}
}

func TestChanshuiCompatibleModelsRespectExplicitAllowlistWithoutNativeCatalog(t *testing.T) {
	for _, platform := range []string{PlatformQoder, PlatformTypeSafe, PlatformDeepseek, "future-provider"} {
		t.Run(platform, func(t *testing.T) {
			s, repo, client, a := newChanshuiFixture()
			a.Platform = platform
			a.Credentials["model_mapping"] = map[string]any{"local-alias": "custom-text-model"}
			a.Credentials["model_whitelist"] = []any{"custom-text-model"}
			client.create = func(in chanshui.CreateRequest) {
				require.Equal(t, "custom-text-model", in.Model)
				require.Equal(t, "chat", in.Protocol)
			}
			_, err := s.SaveConfig(context.Background(), 0, chanshuiInput())
			require.NoError(t, err)
			_, err = s.Run(context.Background(), repo.config.ID)
			require.NoError(t, err)
			s.execute(context.Background(), chanshuiQueued())
			require.Equal(t, 1, client.creates)
			a.Credentials["model_whitelist"] = []any{"different-text-model"}
			_, err = s.SaveConfig(context.Background(), 0, chanshuiInput())
			require.Error(t, err)
			_, err = s.Run(context.Background(), repo.config.ID)
			require.Error(t, err)
			s.execute(context.Background(), chanshuiQueued())
			require.Equal(t, 1, client.creates)
		})
	}
}
