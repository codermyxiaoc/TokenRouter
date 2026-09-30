//go:build unit

package repository

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// 下载和校验文件只允许由本仓库跳到 GitHub 资产 CDN，不能沿仓库重定向取到其它项目。
func TestTokenRouterReleaseAssetRedirectSource(t *testing.T) {
	origin, err := http.NewRequest(http.MethodGet, "https://github.com/codermyxiaoc/TokenRouter/releases/download/v2.6/package.tar.gz", nil)
	require.NoError(t, err)
	check := githubReleaseAssetCheckRedirect(nil)
	for _, target := range []string{
		"https://release-assets.githubusercontent.com/github-production-release-asset/token?signature=example",
		"https://objects.githubusercontent.com/github-production-release-asset/token?signature=example",
		origin.URL.String(),
	} {
		next, err := http.NewRequest(http.MethodGet, target, nil)
		require.NoError(t, err)
		require.NoError(t, check(next, []*http.Request{origin}))
	}
	for _, target := range []string{
		"https://github.com/Wei-Shaw/sub2api/releases/download/v2.6/package.tar.gz",
		"https://github.com/TokenFlux/TokenRouter/releases/download/v2.6/package.tar.gz",
		"https://github.com/codermyxiaoc/TokenRouter/releases/download/v2.4/package.tar.gz",
		"https://evil.invalid/package.tar.gz",
		"http://objects.githubusercontent.com/package.tar.gz",
		"https://objects.githubusercontent.com.evil.invalid/package.tar.gz",
		"https://user@release-assets.githubusercontent.com/package.tar.gz",
	} {
		next, err := http.NewRequest(http.MethodGet, target, nil)
		require.NoError(t, err)
		require.Error(t, check(next, []*http.Request{origin}), target)
	}
	// 下载重定向与原有 API 认证隔离策略组合后仍不得向 CDN 转发令牌。
	cdn, err := http.NewRequest(http.MethodGet, "https://release-assets.githubusercontent.com/package", nil)
	require.NoError(t, err)
	cdn.Header.Set("Authorization", "Bearer test-secret")
	require.NoError(t, githubReleaseAssetCheckRedirect(githubAPICheckRedirect(nil))(cdn, []*http.Request{origin}))
	require.Empty(t, cdn.Header.Get("Authorization"))
	via := make([]*http.Request, 10)
	for i := range via {
		via[i] = origin
	}
	require.Error(t, check(cdn, via))
}
