//go:build unit

package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadImageStorageFromEnv(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv("IMAGE_STORAGE_ENABLED", "true")
	t.Setenv("IMAGE_STORAGE_ENDPOINT", "https://acct.r2.cloudflarestorage.com")
	t.Setenv("IMAGE_STORAGE_BUCKET", "my-images")
	t.Setenv("IMAGE_STORAGE_ACCESS_KEY_ID", "ak")
	t.Setenv("IMAGE_STORAGE_SECRET_ACCESS_KEY", "sk")
	t.Setenv("IMAGE_STORAGE_PUBLIC_BASE_URL", "https://cdn.example.com")

	cfg, err := Load()
	require.NoError(t, err)

	require.True(t, cfg.ImageStorage.Enabled)
	require.Equal(t, "https://acct.r2.cloudflarestorage.com", cfg.ImageStorage.Endpoint)
	require.Equal(t, "my-images", cfg.ImageStorage.Bucket)
	require.Equal(t, "ak", cfg.ImageStorage.AccessKeyID)
	require.Equal(t, "sk", cfg.ImageStorage.SecretAccessKey)
	require.Equal(t, "https://cdn.example.com", cfg.ImageStorage.PublicBaseURL)

	require.True(t, cfg.ImageStorage.IsConfigured())
	require.True(t, cfg.ImageStorage.Active(), "async image tasks must be active when every credential is supplied via env")
}
