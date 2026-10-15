package config

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// 创建预算支持纯环境变量部署，非法输入必须在启动时拒绝。
func TestVideoCreateTimeoutConfiguration(t *testing.T) {
	resetViperWithJWTSecret(t)
	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, 180, cfg.Gateway.VideoCreateTimeoutSeconds)
	for _, value := range []string{"0", "1", "300", "600"} {
		t.Setenv("GATEWAY_VIDEO_CREATE_TIMEOUT_SECONDS", value)
		cfg, err = Load()
		require.NoError(t, err)
		want, _ := strconv.Atoi(value)
		require.Equal(t, want, cfg.Gateway.VideoCreateTimeoutSeconds)
	}
	for _, value := range []string{"-1", "601"} {
		t.Setenv("GATEWAY_VIDEO_CREATE_TIMEOUT_SECONDS", value)
		_, err = Load()
		require.ErrorContains(t, err, "gateway.video_create_timeout_seconds")
	}
}
