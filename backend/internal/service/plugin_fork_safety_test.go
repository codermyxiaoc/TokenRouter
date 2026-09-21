package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

// 读取过无绑定状态后，插件数据库的瞬时故障不能新增全局 OAuth 故障。
func TestPluginReconcileKnownEmptyKeepsLegacyRouting(t *testing.T) {
	repo := &pluginTokenRepository{}
	manager := &PluginManager{repo: repo, runtimes: map[int64]*pluginRuntime{}, localInstallations: map[int64]*PluginInstallation{}}
	require.NoError(t, manager.reconcileOnce(context.Background()))
	repo.listErr = errors.New("数据库暂不可用")
	require.Error(t, manager.reconcileOnce(context.Background()))
	require.False(t, manager.ShouldRouteOpenAIOAuth(&Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive}))
}

func TestPluginReconcileReadFailurePreservesRolloutScope(t *testing.T) {
	manager := &PluginManager{repo: &pluginTokenRepository{listErr: errors.New("数据库暂不可用")}, bindingStateLoaded: true}
	manager.route.Store(&pluginRoute{pluginID: 7, rolloutPercent: 25, unavailable: "原绑定"})
	require.Error(t, manager.reconcileOnce(context.Background()))
	require.Equal(t, int64(7), manager.route.Load().pluginID)
	require.Equal(t, 25, manager.route.Load().rolloutPercent)
}

type pluginScopeAccountRepository struct {
	AccountRepository
	accounts []Account
}

func (r *pluginScopeAccountRepository) ListByPlatform(context.Context, string) ([]Account, error) {
	return r.accounts, nil
}
func (r *pluginScopeAccountRepository) GetByID(_ context.Context, id int64) (*Account, error) {
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			return &r.accounts[i], nil
		}
	}
	return nil, errors.New("账号不存在")
}

// 凭据解析与账号列举使用同一范围，停用账号和影子账号均不可获取令牌。
func TestPluginAccountDirectoryRejectsDisabledShadowAndOtherScopes(t *testing.T) {
	parentID := int64(1)
	repo := &pluginScopeAccountRepository{accounts: []Account{
		{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive},
		{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: "inactive"},
		{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, ParentAccountID: &parentID},
		{ID: 4, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive},
		{ID: 5, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive},
	}}
	svc := &OpenAIGatewayService{accountRepo: repo}
	ids, err := svc.ListPluginAccounts(context.Background(), "", "")
	require.NoError(t, err)
	require.Equal(t, []int64{1}, ids)
	for _, id := range []int64{2, 3, 4, 5} {
		identity, err := svc.ResolvePluginOutboundIdentity(context.Background(), id)
		require.NoError(t, err)
		require.Nil(t, identity)
	}
	manager := &PluginManager{}
	manager.route.Store(&pluginRoute{rolloutPercent: 100})
	for i := 1; i < len(repo.accounts); i++ {
		require.False(t, manager.ShouldRouteOpenAIOAuth(&repo.accounts[i]))
	}
}

type pluginProfileHTTPUpstream struct {
	pluginRoutingHTTPUpstream
	profile *tlsfingerprint.Profile
}

func (u *pluginProfileHTTPUpstream) DoWithTLS(request *http.Request, proxy string, id int64, concurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	u.profile = profile
	return u.pluginRoutingHTTPUpstream.DoWithTLS(request, proxy, id, concurrency, profile)
}

func TestPluginUnboundPreservesSelectedTLSProfile(t *testing.T) {
	upstream := &pluginProfileHTTPUpstream{}
	svc := &OpenAIGatewayService{pluginManager: &PluginManager{}, httpUpstream: upstream}
	request, err := http.NewRequest(http.MethodPost, "https://example.com/v1/responses", nil)
	require.NoError(t, err)
	profile := &tlsfingerprint.Profile{}
	response, err := svc.doOpenAIUpstream(request, "", &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive}, profile)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	require.Same(t, profile, upstream.profile)
	require.Equal(t, 1, upstream.doWithTLSCalls)
}

func TestPluginForkVersionCompatibilityUsesActualVersion(t *testing.T) {
	manifest := testPluginManifest(nil)
	manifest.Requires.Sub2API = ">=0.1.278-ct-v1.6 <0.2.0"
	manifest.Requires.TestedSub2APIVersions = []string{"v0.1.278-ct-v1.6"}
	result := EvaluatePluginCompatibility(manifest, PluginHostInfo{Version: "0.1.278-ct-v1.6"})
	require.True(t, result.Compatible)
	require.True(t, result.Tested)
	manifest.Requires.Sub2API = ">=0.2.7"
	require.False(t, EvaluatePluginCompatibility(manifest, PluginHostInfo{Version: "0.1.278-ct-v1.6"}).Compatible)
}
