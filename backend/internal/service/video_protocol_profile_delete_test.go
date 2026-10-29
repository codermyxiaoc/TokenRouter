//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// videoProfileAccountsStub 只返回预设的视频账号，用于删除前的绑定检查。
type videoProfileAccountsStub struct {
	AccountRepository
	accounts []Account
}

func (s *videoProfileAccountsStub) ListByPlatform(_ context.Context, platform string) ([]Account, error) {
	if platform != PlatformVideo {
		return nil, nil
	}
	return s.accounts, nil
}

// 删除模板：启用版本须先停用；被账号绑定或锁定的版本拒绝删除；删除整个预置模板后不会被启动补齐或回退到内置定义。
func TestVideoProtocolProfileDelete(t *testing.T) {
	repo := &videoProfileMemoryRepo{}
	settings := &videoProfileAISettingStub{}
	bound := Account{Name: "relay-a", Platform: PlatformVideo, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"api_key": "k", "base_url": "https://relay.example",
		"video_model_protocols": map[string]any{"m": map[string]any{"protocol": "profile:vendor-x@1"}},
	}}
	accounts := &videoProfileAccountsStub{accounts: []Account{bound}}
	svc := ProvideVideoProtocolProfileService(repo, settings, accounts)
	spec := videoTestProfileSpec()
	for i := 0; i < 2; i++ {
		_, err := svc.Save(t.Context(), VideoProtocolProfileInput{ProfileKey: "vendor-x", Name: "X", Spec: spec, Activate: i == 1}, 1)
		require.NoError(t, err)
	}
	// v2 启用中，不能直接删除。
	err := svc.Delete(t.Context(), "vendor-x", 2)
	require.Equal(t, http.StatusConflict, infraerrors.Code(err))
	// v1 被账号锁定，不能删除，错误中列出账号名称。
	err = svc.Delete(t.Context(), "vendor-x", 1)
	require.Equal(t, http.StatusConflict, infraerrors.Code(err))
	require.Contains(t, infraerrors.Message(err), "relay-a")
	// 解除绑定后可删除单个版本与整个模板。
	accounts.accounts = nil
	require.NoError(t, svc.Delete(t.Context(), "vendor-x", 1))
	_, err = svc.Resolve(t.Context(), "vendor-x", 2)
	require.NoError(t, err)
	require.NoError(t, svc.Delete(t.Context(), "vendor-x", 0))
	_, err = svc.Resolve(t.Context(), "vendor-x", 0)
	require.Error(t, err)
	require.ErrorIs(t, svc.Delete(t.Context(), "vendor-x", 0), ErrVideoProtocolProfileNotFound)

	// 整个模板仍被不锁版本的绑定使用时拒绝删除。
	_, err = EnsureVideoProtocolPresets(t.Context(), svc)
	require.NoError(t, err)
	accounts.accounts = []Account{{Name: "relay-b", Platform: PlatformVideo, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"api_key": "k", "base_url": "https://relay.example",
		"video_model_protocols": map[string]any{"*": map[string]any{"protocol": "profile:metaso-h3"}},
	}}}
	err = svc.Delete(t.Context(), "metaso-h3", 0)
	require.Equal(t, http.StatusConflict, infraerrors.Code(err))
	accounts.accounts = nil
	require.NoError(t, svc.SetStatus(t.Context(), "metaso-h3", 1, VideoProtocolProfileDisabled))
	require.NoError(t, svc.Delete(t.Context(), "metaso-h3", 0))
	// 已删除的预置模板不回退到内置定义，启动补齐也不再创建。
	_, err = svc.Resolve(t.Context(), "metaso-h3", 0)
	require.Error(t, err)
	created, err := EnsureVideoProtocolPresets(t.Context(), svc)
	require.NoError(t, err)
	require.Zero(t, created)
}
