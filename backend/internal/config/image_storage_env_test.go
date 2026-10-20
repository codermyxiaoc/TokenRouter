//go:build unit

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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
	require.Equal(t, ImageStorageDefaultRetentionHours, cfg.ImageStorage.RetentionHours)

	require.True(t, cfg.ImageStorage.IsConfigured())
	require.True(t, cfg.ImageStorage.Active(), "async image tasks must be active when every credential is supplied via env")
}

// 文件和环境变量均独立加载保存期；零值兼容默认，但非法值不允许启用图片存储。
func TestLoadImageStorageRetention(t *testing.T) {
	for _, source := range []string{"env", "yaml"} {
		for _, hours := range []int{0, 1, 72, 8760, -1, 8761} {
			t.Run(source+"/"+strconv.Itoa(hours), func(t *testing.T) {
				resetViperWithJWTSecret(t)
				t.Setenv("IMAGE_STORAGE_ENABLED", "true")
				t.Setenv("IMAGE_STORAGE_BUCKET", "images")
				t.Setenv("IMAGE_STORAGE_ACCESS_KEY_ID", "ak")
				t.Setenv("IMAGE_STORAGE_SECRET_ACCESS_KEY", "sk")
				if source == "env" {
					t.Setenv("IMAGE_STORAGE_RETENTION_HOURS", strconv.Itoa(hours))
				} else {
					path := filepath.Join(t.TempDir(), "config.yaml")
					require.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf("image_storage:\n  retention_hours: %d\n", hours)), 0o600))
					t.Setenv("CONFIG_FILE", path)
				}
				cfg, err := Load()
				require.NoError(t, err)
				want := hours
				if want == 0 {
					want = ImageStorageDefaultRetentionHours
				}
				require.Equal(t, want, cfg.ImageStorage.RetentionHours)
				require.Equal(t, 24, cfg.ImageStorage.PresignExpiry)
				if hours < 0 || hours > ImageStorageMaxRetentionHours {
					require.ErrorContains(t, cfg.ImageStorage.ValidateRetentionHours(), "1 and 8760")
					require.False(t, cfg.ImageStorage.Active())
				} else {
					require.NoError(t, cfg.ImageStorage.ValidateRetentionHours())
					require.True(t, cfg.ImageStorage.Active())
				}
			})
		}
	}
}

// YAML 小数必须报错，不能截断成更短的图片保存期；环境变量同样不接受小数。
func TestLoadImageStorageRejectsFractionalRetention(t *testing.T) {
	for _, source := range []string{"yaml", "env"} {
		t.Run(source, func(t *testing.T) {
			resetViperWithJWTSecret(t)
			if source == "env" {
				t.Setenv("IMAGE_STORAGE_RETENTION_HOURS", "1.5")
			} else {
				path := filepath.Join(t.TempDir(), "config.yaml")
				require.NoError(t, os.WriteFile(path, []byte("image_storage:\n  retention_hours: 1.5\n"), 0o600))
				t.Setenv("CONFIG_FILE", path)
			}
			_, err := Load()
			require.ErrorContains(t, err, "retention_hours")
		})
	}
}

// 启动仍允许禁用或待修正的存储配置，实际启用判断必须拒绝无效期限。
func TestLoadImageStoragePresignExpiryGuard(t *testing.T) {
	for _, source := range []string{"env", "yaml"} {
		for _, enabled := range []bool{false, true} {
			t.Run(source+"/"+strconv.FormatBool(enabled), func(t *testing.T) {
				resetViperWithJWTSecret(t)
				if source == "env" {
					t.Setenv("IMAGE_STORAGE_ENABLED", strconv.FormatBool(enabled))
					t.Setenv("IMAGE_STORAGE_BUCKET", "images")
					t.Setenv("IMAGE_STORAGE_ACCESS_KEY_ID", "ak")
					t.Setenv("IMAGE_STORAGE_SECRET_ACCESS_KEY", "sk")
					t.Setenv("IMAGE_STORAGE_PRESIGN_EXPIRY_HOURS", "169")
				} else {
					path := filepath.Join(t.TempDir(), "config.yaml")
					require.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf("image_storage:\n  enabled: %t\n  bucket: images\n  access_key_id: ak\n  secret_access_key: sk\n  presign_expiry_hours: 169\n", enabled)), 0o600))
					t.Setenv("CONFIG_FILE", path)
				}
				cfg, err := Load()
				require.NoError(t, err)
				require.True(t, cfg.ImageStorage.IsConfigured())
				require.Equal(t, 169, cfg.ImageStorage.PresignExpiry)
				require.ErrorContains(t, cfg.ImageStorage.ValidatePresignExpiry(), "1 and 168")
				require.False(t, cfg.ImageStorage.Active())
			})
		}
	}
}
