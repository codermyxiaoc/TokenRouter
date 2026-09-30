package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 创建频率沿用配置加载优先级，显式 0 关闭，非法负数启动时拒绝。
func TestAPIKeyCreateConfiguration(t *testing.T) {
	resetViperWithJWTSecret(t)
	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, 60, cfg.APIKeyCreate.MaxPerUserPerHour)
	t.Setenv("API_KEY_CREATE_MAX_PER_USER_PER_HOUR", "0")
	cfg, err = Load()
	require.NoError(t, err)
	require.Zero(t, cfg.APIKeyCreate.MaxPerUserPerHour)
	cfg.APIKeyCreate.MaxPerUserPerHour = -1
	require.ErrorContains(t, cfg.Validate(), "api_key_create.max_per_user_per_hour")
}
