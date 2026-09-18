package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// 提升缺省容量不能覆盖存量部署者的配置，环境变量仍应优先于 YAML。
func TestLoadOpenAIWSConnectionFactorsRespectOverrides(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		oauthEnv, apiKeyEnv   string
		wantOAuth, wantAPIKey float64
	}{
		{name: "保留存量显式一倍", wantOAuth: 1, wantAPIKey: 1},
		{name: "环境变量覆盖文件", oauthEnv: "2.5", apiKeyEnv: "3", wantOAuth: 2.5, wantAPIKey: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetViperWithJWTSecret(t)
			path := filepath.Join(t.TempDir(), "config.yaml")
			require.NoError(t, os.WriteFile(path, []byte("gateway:\n  openai_ws:\n    oauth_max_conns_factor: 1.0\n    apikey_max_conns_factor: 1.0\n"), 0o600))
			t.Setenv("CONFIG_FILE", path)
			t.Setenv("GATEWAY_OPENAI_WS_OAUTH_MAX_CONNS_FACTOR", tc.oauthEnv)
			t.Setenv("GATEWAY_OPENAI_WS_APIKEY_MAX_CONNS_FACTOR", tc.apiKeyEnv)
			cfg, err := Load()
			require.NoError(t, err)
			require.Equal(t, tc.wantOAuth, cfg.Gateway.OpenAIWS.OAuthMaxConnsFactor)
			require.Equal(t, tc.wantAPIKey, cfg.Gateway.OpenAIWS.APIKeyMaxConnsFactor)
		})
	}
}
