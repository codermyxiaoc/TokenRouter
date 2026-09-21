package config

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestPluginConfigDefaultsAndEnvironment(t *testing.T) {
	resetViperWithJWTSecret(t)
	cfg, err := Load()
	require.NoError(t, err)
	require.False(t, cfg.Plugins.AllowUnsigned)
	require.Equal(t, int64(128*1024*1024), cfg.Plugins.MaxUploadBytes)
	require.Equal(t, int64(256*1024*1024), cfg.Plugins.MaxUncompressedBytes)
	require.Equal(t, 15, cfg.Plugins.StartTimeoutSeconds)
	t.Setenv("PLUGINS_START_TIMEOUT_SECONDS", "30")
	t.Setenv("PLUGINS_DATA_DIR", "custom-plugin-data")
	cfg, err = Load()
	require.NoError(t, err)
	require.Equal(t, 30, cfg.Plugins.StartTimeoutSeconds)
	require.Equal(t, "custom-plugin-data", cfg.Plugins.DataDir)
}

func TestPluginConfigRejectsInvalidLimits(t *testing.T) {
	for _, key := range []string{"PLUGINS_MAX_UPLOAD_BYTES", "PLUGINS_MAX_UNCOMPRESSED_BYTES", "PLUGINS_START_TIMEOUT_SECONDS"} {
		t.Run(key, func(t *testing.T) {
			resetViperWithJWTSecret(t)
			t.Setenv(key, "-1")
			_, err := Load()
			require.Error(t, err)
		})
	}
}
