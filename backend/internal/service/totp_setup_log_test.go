//go:build unit

package service

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"
)

// totpSetupLogUserRepoStub 记录启用流程写入的密文与启用状态。
type totpSetupLogUserRepoStub struct {
	UserRepository
	secret  string
	enabled bool
}

func (s *totpSetupLogUserRepoStub) UpdateTotpSecret(_ context.Context, _ int64, secret *string) error {
	s.secret = *secret
	return nil
}

func (s *totpSetupLogUserRepoStub) EnableTotp(context.Context, int64) error {
	s.enabled = true
	return nil
}

// totpSetupLogCacheStub 只提供启用流程用到的 setup 会话读取与删除。
type totpSetupLogCacheStub struct {
	TotpCache
	session *TotpSetupSession
}

func (c *totpSetupLogCacheStub) GetSetupSession(context.Context, int64) (*TotpSetupSession, error) {
	return c.session, nil
}

func (c *totpSetupLogCacheStub) DeleteSetupSession(context.Context, int64) error {
	c.session = nil
	return nil
}

// totpSetupLogEncryptor 是可逆的加密替身，保证走到“解密回读成功”的日志分支。
type totpSetupLogEncryptor struct{}

func (totpSetupLogEncryptor) Encrypt(plaintext string) (string, error) {
	return "enc:" + plaintext, nil
}

func (totpSetupLogEncryptor) Decrypt(ciphertext string) (string, error) {
	return strings.TrimPrefix(ciphertext, "enc:"), nil
}

// 成功启用 TOTP 时，debug 日志不得包含密钥的任何前缀片段，也不得保留 decrypted_prefix 字段。
func TestCompleteSetupDebugLogsOmitSecretPrefix(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	key, err := totp.Generate(totp.GenerateOpts{Issuer: "test", AccountName: "user@example.com"})
	require.NoError(t, err)
	secret := key.Secret()
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)

	userRepo := &totpSetupLogUserRepoStub{}
	cache := &totpSetupLogCacheStub{session: &TotpSetupSession{Secret: secret, SetupToken: "setup-token"}}
	settingSvc := NewSettingService(&totpVMSettingRepoStub{values: map[string]string{SettingKeyTotpEnabled: "true"}}, nil)
	svc := NewTotpService(userRepo, totpSetupLogEncryptor{}, cache, settingSvc, nil, nil)

	require.NoError(t, svc.CompleteSetup(context.Background(), 9, code, "setup-token"))
	require.True(t, userRepo.enabled)

	logs := buf.String()
	require.Contains(t, logs, "totp_complete_setup_verified", "必须真正走到解密回读成功分支")
	require.NotContains(t, logs, "decrypted_prefix")
	require.NotContains(t, logs, secret[:4])
}
