//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type stubSettingRepo struct {
	mu     sync.Mutex
	values map[string]string
}

func newStubSettingRepo() *stubSettingRepo {
	return &stubSettingRepo{values: map[string]string{}}
}

func (r *stubSettingRepo) Get(context.Context, string) (*Setting, error) { return nil, nil }
func (r *stubSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.values[key], nil
}

func (r *stubSettingRepo) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values[key] = value
	return nil
}
func (r *stubSettingRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (r *stubSettingRepo) SetMultiple(context.Context, map[string]string) error { return nil }
func (r *stubSettingRepo) GetAll(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
func (r *stubSettingRepo) Delete(context.Context, string) error { return nil }

type reversibleEncryptor struct{}

func (reversibleEncryptor) Encrypt(plaintext string) (string, error) {
	return "enc:" + plaintext, nil
}

func (reversibleEncryptor) Decrypt(ciphertext string) (string, error) {
	rest, ok := strings.CutPrefix(ciphertext, "enc:")
	if !ok {
		return "", errors.New("not encrypted")
	}
	return rest, nil
}

type recordingStorage struct{ saved []string }

func (s *recordingStorage) Save(_ context.Context, key, _ string, _ []byte) (string, error) {
	s.saved = append(s.saved, key)
	return "https://cdn.example.com/" + key, nil
}

func newImageStorageFixture(t *testing.T, fallback config.ImageStorageConfig) (*ImageStorageSettingService, *stubSettingRepo, *[]config.ImageStorageConfig) {
	return newImageStorageFixtureWithKey(t, fallback, true)
}

func newImageStorageFixtureWithKey(t *testing.T, fallback config.ImageStorageConfig, encryptionKeyConfigured bool) (*ImageStorageSettingService, *stubSettingRepo, *[]config.ImageStorageConfig) {
	t.Helper()
	repo := newStubSettingRepo()
	encryptor := reversibleEncryptor{}
	backup := NewBackupService(repo, &config.Config{
		Totp: config.TotpConfig{EncryptionKeyConfigured: encryptionKeyConfigured},
	}, encryptor, nil, nil)

	var built []config.ImageStorageConfig
	factory := func(_ context.Context, cfg *config.ImageStorageConfig) (ImageStorage, error) {
		built = append(built, *cfg)
		return &recordingStorage{}, nil
	}
	return NewImageStorageSettingService(repo, encryptor, backup, factory, fallback), repo, &built
}

func seedBackupS3(t *testing.T, repo *stubSettingRepo, cfg BackupS3Config) {
	t.Helper()
	cfg.SecretAccessKey = "enc:" + cfg.SecretAccessKey
	data, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, repo.Set(context.Background(), settingKeyBackupS3Config, string(data)))
}

func TestImageStorageSettingsToggleTakesEffectWithoutRestart(t *testing.T) {
	svc, repo, built := newImageStorageFixture(t, config.ImageStorageConfig{})
	ctx := context.Background()
	seedBackupS3(t, repo, BackupS3Config{
		Endpoint: "https://acct.r2.cloudflarestorage.com", Region: "auto",
		Bucket: "backup-bucket", AccessKeyID: "ak", SecretAccessKey: "sk",
		Prefix: "backups/",
	})

	uploader, enabled := svc.resolve()
	require.False(t, enabled, "disabled until an admin turns it on")
	require.Nil(t, uploader)

	_, err := svc.Update(ctx, ImageStorageSettings{Enabled: true, ReuseBackupS3: true})
	require.NoError(t, err)

	uploader, enabled = svc.resolve()
	require.True(t, enabled, "saving the setting must enable the feature immediately")
	require.NotNil(t, uploader)

	_, err = svc.Update(ctx, ImageStorageSettings{Enabled: false, ReuseBackupS3: true})
	require.NoError(t, err)
	_, enabled = svc.resolve()
	require.False(t, enabled, "turning it back off must also apply immediately")

	require.Len(t, *built, 1, "the S3 client is built only when the feature is on")
}

func TestImageStorageSettingsReuseBackupCredentials(t *testing.T) {
	svc, repo, built := newImageStorageFixture(t, config.ImageStorageConfig{})
	ctx := context.Background()
	seedBackupS3(t, repo, BackupS3Config{
		Endpoint: "https://acct.r2.cloudflarestorage.com", Region: "wnam",
		Bucket: "backup-bucket", AccessKeyID: "backup-ak", SecretAccessKey: "backup-sk",
		Prefix: "backups/", ForcePathStyle: true,
	})

	_, err := svc.Update(ctx, ImageStorageSettings{Enabled: true, ReuseBackupS3: true, Prefix: "images"})
	require.NoError(t, err)
	_, enabled := svc.resolve()
	require.True(t, enabled)

	require.Len(t, *built, 1)
	got := (*built)[0]
	require.Equal(t, "https://acct.r2.cloudflarestorage.com", got.Endpoint)
	require.Equal(t, "wnam", got.Region)
	require.Equal(t, "backup-ak", got.AccessKeyID)
	require.Equal(t, "backup-sk", got.SecretAccessKey, "the backup secret must be decrypted before use")
	require.True(t, got.ForcePathStyle)
	require.Equal(t, "backup-bucket", got.Bucket, "an empty bucket falls back to the backup bucket")
	require.Equal(t, "images/", got.Prefix, "images stay under their own prefix so they never collide with backups/")

	raw, err := repo.GetValue(ctx, settingKeyImageStorageConfig)
	require.NoError(t, err)
	require.NotContains(t, raw, "backup-sk")
	require.NotContains(t, raw, "enc:")
}

func TestImageStorageSettingsOwnCredentialsAreEncryptedAndMasked(t *testing.T) {
	svc, repo, built := newImageStorageFixture(t, config.ImageStorageConfig{})
	ctx := context.Background()

	saved, err := svc.Update(ctx, ImageStorageSettings{
		Enabled: true, Bucket: "my-images",
		Endpoint:    "https://acct.r2.cloudflarestorage.com",
		AccessKeyID: "ak", SecretAccessKey: "super-secret",
	})
	require.NoError(t, err)
	require.Empty(t, saved.SecretAccessKey, "the response must never echo the secret back")

	raw, err := repo.GetValue(ctx, settingKeyImageStorageConfig)
	require.NoError(t, err)
	require.NotContains(t, raw, `"secret_access_key":"super-secret"`, "the secret must be encrypted at rest")
	require.Contains(t, raw, "enc:super-secret")

	fetched, err := svc.Get(ctx)
	require.NoError(t, err)
	require.Empty(t, fetched.SecretAccessKey)
	require.True(t, svc.SecretConfigured(ctx))

	_, enabled := svc.resolve()
	require.True(t, enabled)
	require.Equal(t, "super-secret", (*built)[0].SecretAccessKey, "the stored secret must be decrypted before use")

	_, err = svc.Update(ctx, ImageStorageSettings{
		Enabled: true, Bucket: "my-images",
		Endpoint: "https://acct.r2.cloudflarestorage.com", AccessKeyID: "ak",
	})
	require.NoError(t, err)
	svc.resolve()
	require.Equal(t, "super-secret", (*built)[1].SecretAccessKey)
}

func TestImageStorageSettingsRejectSecretWithEphemeralKey(t *testing.T) {
	svc, repo, built := newImageStorageFixtureWithKey(t, config.ImageStorageConfig{}, false)
	ctx := context.Background()

	_, err := svc.Update(ctx, ImageStorageSettings{
		Enabled: true, Bucket: "my-images",
		Endpoint:    "https://acct.r2.cloudflarestorage.com",
		AccessKeyID: "ak", SecretAccessKey: "super-secret",
	})
	require.ErrorIs(t, err, ErrSecretEncryptionKeyNotConfigured)

	raw, _ := repo.GetValue(ctx, settingKeyImageStorageConfig)
	require.Empty(t, raw, "nothing must be persisted when the secret is rejected")
	require.Empty(t, *built)

	seedBackupS3(t, repo, BackupS3Config{
		Endpoint: "https://acct.r2.cloudflarestorage.com", Region: "auto",
		Bucket: "backup-bucket", AccessKeyID: "ak", SecretAccessKey: "sk", Prefix: "backups/",
	})
	_, err = svc.Update(ctx, ImageStorageSettings{Enabled: true, ReuseBackupS3: true})
	require.NoError(t, err)
}

func TestImageStorageSettingsIncompleteStaysDisabled(t *testing.T) {
	svc, _, built := newImageStorageFixture(t, config.ImageStorageConfig{})
	ctx := context.Background()

	_, err := svc.Update(ctx, ImageStorageSettings{Enabled: true, Bucket: "my-images"})
	require.NoError(t, err)

	_, enabled := svc.resolve()
	require.False(t, enabled, "missing credentials must not enable the feature")
	require.Empty(t, *built, "no client is built from an incomplete configuration")
}

func TestImageStorageSettingsFallBackToConfigFile(t *testing.T) {
	svc, _, built := newImageStorageFixture(t, config.ImageStorageConfig{
		Enabled: true, Endpoint: "https://acct.r2.cloudflarestorage.com", Region: "auto",
		Bucket: "yaml-bucket", AccessKeyID: "yaml-ak", SecretAccessKey: "yaml-sk",
		Prefix: "images/", MaxDownloadByte: 1024,
	})

	_, enabled := svc.resolve()
	require.True(t, enabled, "config.yaml still enables the feature when nothing is stored yet")
	require.Equal(t, "yaml-bucket", (*built)[0].Bucket)

	fetched, err := svc.Get(context.Background())
	require.NoError(t, err)
	require.True(t, fetched.Enabled)
	require.Equal(t, "yaml-bucket", fetched.Bucket)
	require.Empty(t, fetched.SecretAccessKey)
}

// 首次在后台编辑 YAML 配置时留空密钥，不得把已配置的密钥清空。
func TestImageStorageSettingsRetainsFallbackSecretOnFirstSave(t *testing.T) {
	svc, repo, built := newImageStorageFixture(t, config.ImageStorageConfig{
		Enabled: true, Bucket: "yaml-bucket", AccessKeyID: "yaml-ak", SecretAccessKey: "yaml-secret",
	})
	ctx := context.Background()
	settings, err := svc.Get(ctx)
	require.NoError(t, err)
	require.Empty(t, settings.SecretAccessKey)
	require.NoError(t, svc.TestConnection(ctx, *settings))
	require.Equal(t, "yaml-secret", (*built)[0].SecretAccessKey)
	_, err = svc.Update(ctx, *settings)
	require.NoError(t, err)
	raw, err := repo.GetValue(ctx, settingKeyImageStorageConfig)
	require.NoError(t, err)
	require.Contains(t, raw, `"secret_access_key":"enc:yaml-secret"`)
	_, enabled := svc.resolve()
	require.True(t, enabled)
	require.Equal(t, "yaml-secret", (*built)[1].SecretAccessKey)
}

// 超出 S3 签名期限的配置在任何显式入口都应被拒绝，且不能先覆盖原有可用配置。
func TestImageStorageSettingsRejectExcessivePresignExpiry(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(strconv.FormatBool(enabled), func(t *testing.T) {
			svc, repo, built := newImageStorageFixture(t, config.ImageStorageConfig{})
			ctx := context.Background()
			good := ImageStorageSettings{Enabled: true, Bucket: "images", AccessKeyID: "ak", SecretAccessKey: "secret", PresignExpiry: 24}
			_, err := svc.Update(ctx, good)
			require.NoError(t, err)
			before, err := repo.GetValue(ctx, settingKeyImageStorageConfig)
			require.NoError(t, err)
			bad := good
			bad.Enabled, bad.PresignExpiry = enabled, 169
			_, err = svc.Update(ctx, bad)
			require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
			require.Equal(t, "IMAGE_STORAGE_PRESIGN_EXPIRY_INVALID", infraerrors.Reason(err))
			require.Contains(t, infraerrors.Message(err), "1 and 168")
			err = svc.TestConnection(ctx, bad)
			require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
			after, err := repo.GetValue(ctx, settingKeyImageStorageConfig)
			require.NoError(t, err)
			require.Equal(t, before, after)
			require.Empty(t, *built, "无效参数不能创建客户端或探测存储")
		})
	}
}

// 保存的历史值及环境/YAML 回落均必须在创建存储客户端之前验证。
func TestImageStorageSettingsInvalidPresignNeverEnables(t *testing.T) {
	for _, persisted := range []bool{false, true} {
		for _, enabled := range []bool{false, true} {
			t.Run(strconv.FormatBool(persisted)+"/"+strconv.FormatBool(enabled), func(t *testing.T) {
				fallback := config.ImageStorageConfig{Enabled: enabled, Bucket: "images", AccessKeyID: "ak", SecretAccessKey: "secret", PresignExpiry: 169}
				svc, repo, built := newImageStorageFixture(t, fallback)
				if persisted {
					raw, err := json.Marshal(settingsFromConfig(fallback))
					require.NoError(t, err)
					require.NoError(t, repo.Set(context.Background(), settingKeyImageStorageConfig, string(raw)))
				}
				uploader, active := svc.resolve()
				require.False(t, active)
				require.Nil(t, uploader)
				require.Empty(t, *built)
				// 无效的旧配置仍可读取修正，不得导致整个应用无法启动。
				got, err := svc.Get(context.Background())
				require.NoError(t, err)
				require.Equal(t, 169, got.PresignExpiry)
			})
		}
	}
}

func TestImageStorageSettingsPresignBoundsAndLegacyDefault(t *testing.T) {
	for _, hours := range []int{-1, 0, 1, 24, 168} {
		t.Run(strconv.Itoa(hours), func(t *testing.T) {
			svc, _, built := newImageStorageFixture(t, config.ImageStorageConfig{})
			ctx := context.Background()
			input := ImageStorageSettings{Enabled: true, Bucket: "images", AccessKeyID: "ak", SecretAccessKey: "secret", PresignExpiry: hours}
			got, err := svc.Update(ctx, input)
			require.NoError(t, err)
			want := hours
			if want <= 0 {
				want = config.ImageStorageDefaultPresignExpiryHours
			}
			require.Equal(t, want, got.PresignExpiry)
			require.NoError(t, svc.TestConnection(ctx, input))
			require.Len(t, *built, 1)
			require.Equal(t, want, (*built)[0].PresignExpiry)
			_, active := svc.resolve()
			require.True(t, active)
		})
	}
}

// 保存期影响删除计划，显式非法值必须拒绝，且不得改写已生效设置或创建存储客户端。
func TestImageStorageSettingsRetentionValidation(t *testing.T) {
	for _, hours := range []int{-1, 8761} {
		t.Run(strconv.Itoa(hours), func(t *testing.T) {
			svc, repo, built := newImageStorageFixture(t, config.ImageStorageConfig{})
			ctx := context.Background()
			good := ImageStorageSettings{Enabled: true, Bucket: "images", AccessKeyID: "ak", SecretAccessKey: "secret", RetentionHours: 48}
			_, err := svc.Update(ctx, good)
			require.NoError(t, err)
			before, err := repo.GetValue(ctx, settingKeyImageStorageConfig)
			require.NoError(t, err)
			bad := good
			bad.RetentionHours = hours
			_, err = svc.Update(ctx, bad)
			require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
			require.Equal(t, "IMAGE_STORAGE_RETENTION_INVALID", infraerrors.Reason(err))
			require.Contains(t, infraerrors.Message(err), "1 and 8760")
			require.Equal(t, http.StatusBadRequest, infraerrors.Code(svc.TestConnection(ctx, bad)))
			after, err := repo.GetValue(ctx, settingKeyImageStorageConfig)
			require.NoError(t, err)
			require.Equal(t, before, after)
			require.Empty(t, *built)
		})
	}
}

func TestImageStorageSettingsRetentionDefaultsAndRoundTrip(t *testing.T) {
	for _, hours := range []int{0, 1, 72, 8760} {
		t.Run(strconv.Itoa(hours), func(t *testing.T) {
			svc, _, built := newImageStorageFixture(t, config.ImageStorageConfig{})
			ctx := context.Background()
			in := ImageStorageSettings{Enabled: true, Bucket: "images", AccessKeyID: "ak", SecretAccessKey: "secret", PresignExpiry: 12, RetentionHours: hours}
			want := hours
			if want == 0 {
				want = config.ImageStorageDefaultRetentionHours
			}
			saved, err := svc.Update(ctx, in)
			require.NoError(t, err)
			require.Equal(t, want, saved.RetentionHours)
			got, err := svc.Get(ctx)
			require.NoError(t, err)
			require.Equal(t, want, got.RetentionHours)
			require.Equal(t, 12, got.PresignExpiry, "保存期不得重写签名有效期")
			require.NoError(t, svc.TestConnection(ctx, in))
			_, enabled := svc.resolve()
			require.True(t, enabled)
			require.Len(t, *built, 2)
			for _, cfg := range *built {
				require.Equal(t, want, cfg.RetentionHours)
				require.Equal(t, 12, cfg.PresignExpiry)
			}
		})
	}
}

// 升级前数据库、文件及备份复用配置的缺省保存期一致，读取不额外持久化历史对象。
func TestImageStorageSettingsLegacyRetentionAndReuse(t *testing.T) {
	for _, source := range []string{"stored", "fallback", "reuse"} {
		t.Run(source, func(t *testing.T) {
			fallback := config.ImageStorageConfig{Enabled: true, Bucket: "images", AccessKeyID: "ak", SecretAccessKey: "secret"}
			svc, repo, built := newImageStorageFixture(t, fallback)
			ctx := context.Background()
			if source == "stored" {
				require.NoError(t, repo.Set(ctx, settingKeyImageStorageConfig, `{"enabled":true,"bucket":"images","access_key_id":"ak","secret_access_key":"enc:secret"}`))
			}
			if source == "reuse" {
				seedBackupS3(t, repo, BackupS3Config{Bucket: "images", AccessKeyID: "ak", SecretAccessKey: "secret"})
				_, err := svc.Update(ctx, ImageStorageSettings{Enabled: true, ReuseBackupS3: true, RetentionHours: 72})
				require.NoError(t, err)
			}
			want := 24
			if source == "reuse" {
				want = 72
			}
			got, err := svc.Get(ctx)
			require.NoError(t, err)
			require.Equal(t, want, got.RetentionHours)
			_, enabled := svc.resolve()
			require.True(t, enabled)
			require.Equal(t, want, (*built)[0].RetentionHours)
			if source == "stored" {
				raw, err := repo.GetValue(ctx, settingKeyImageStorageConfig)
				require.NoError(t, err)
				require.NotContains(t, raw, "retention_hours")
			}
		})
	}
}

func TestImageStorageSettingsInvalidRetentionNeverEnables(t *testing.T) {
	for _, hours := range []int{-1, 8761} {
		for _, persisted := range []bool{false, true} {
			t.Run(strconv.Itoa(hours)+"/"+strconv.FormatBool(persisted), func(t *testing.T) {
				cfg := config.ImageStorageConfig{Enabled: true, Bucket: "images", AccessKeyID: "ak", SecretAccessKey: "secret", RetentionHours: hours}
				svc, repo, built := newImageStorageFixture(t, cfg)
				if persisted {
					raw, err := json.Marshal(settingsFromConfig(cfg))
					require.NoError(t, err)
					require.NoError(t, repo.Set(context.Background(), settingKeyImageStorageConfig, string(raw)))
				}
				_, enabled := svc.resolve()
				require.False(t, enabled)
				require.Empty(t, *built)
			})
		}
	}
}
