//go:build unit

package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseCyberPolicyUserAllowlist(t *testing.T) {
	ids, err := ParseCyberPolicyUserAllowlist("12, 34\n12\t56")
	require.NoError(t, err)
	require.Len(t, ids, 3)
	for _, raw := range []string{"0", "-1", "12,invalid", "9223372036854775808", strings.Repeat("1", 16385)} {
		ids, err := ParseCyberPolicyUserAllowlist(raw)
		require.Error(t, err)
		require.Nil(t, ids)
	}
	ids, err = ParseCyberPolicyUserAllowlist("")
	require.NoError(t, err)
	require.Empty(t, ids)
}

func TestCyberPolicyAllowlistCoversAllUserKeysAndRefreshes(t *testing.T) {
	repo := &fakeSettingRepo{vals: map[string]string{SettingKeyCyberPolicyUserAllowlist: "12"}}
	settings := &SettingService{settingRepo: repo}
	svc := &OpenAIGatewayService{settingService: settings}
	ctx := context.Background()
	// 会话屏蔽未开启时仍适用白名单。
	require.True(t, svc.CyberPolicyLogOnly(ctx, &APIKey{ID: 1, UserID: 12}))
	require.True(t, svc.CyberPolicyLogOnly(ctx, &APIKey{ID: 2, UserID: 12}))
	require.False(t, svc.CyberPolicyLogOnly(ctx, &APIKey{ID: 3, UserID: 34}))
	require.False(t, svc.CyberPolicyLogOnly(ctx, nil))
	// owner 在名单内不能豁免成员；成员在名单内也不要求 owner 同时入名单。
	require.False(t, svc.CyberPolicyLogOnly(ctx, &APIKey{UserID: 12, ActorUser: &User{ID: 34}}))
	require.True(t, svc.CyberPolicyLogOnly(ctx, &APIKey{UserID: 34, ActorUser: &User{ID: 12}}))
	repo.vals[SettingKeyCyberPolicyUserAllowlist] = "34"
	settings.cyberSessionBlockCache.Store(&cachedCyberSessionBlockRuntime{})
	require.False(t, svc.CyberPolicyLogOnly(ctx, &APIKey{ID: 1, UserID: 12}))
	require.True(t, svc.CyberPolicyLogOnly(ctx, &APIKey{ID: 3, UserID: 34}))
}

// 白名单 Cyber 拒绝保存为审核事实，不进入 fork 的封禁计数表。
func TestCyberWarningAllowlistPreservesEvidenceWithoutSideEffects(t *testing.T) {
	repo := &banCountArgsTestRepo{}
	svc := NewContentModerationService(
		&contentModerationTestSettingRepo{values: map[string]string{SettingKeyRiskControlEnabled: "true", SettingKeyCyberPolicyUserAllowlist: "12"}},
		repo, nil, nil, nil, nil, &EmailService{},
	)
	warning, err := svc.RecordCyberWarning(context.Background(), ContentModerationCyberWarningInput{
		UserID: 12, UserEmail: "test@example.com", Model: "gpt-5",
		WarningText: "cyber_policy", ResponseBody: []byte(`{"error":{"code":"cyber_policy"}}`),
	})
	require.NoError(t, err)
	require.NotNil(t, warning)
	logs := repo.snapshotLogs()
	require.Len(t, logs, 1)
	require.Equal(t, ContentModerationModeCyberLogOnly, logs[0].Mode)
	require.Equal(t, ContentModerationActionAllow, logs[0].Action)
	require.True(t, logs[0].Flagged)
	require.Contains(t, logs[0].Error, "cyber_policy")
	require.False(t, logs[0].AutoBanned)
	require.False(t, logs[0].EmailSent)
	require.Zero(t, logs[0].ViolationCount)
	require.Empty(t, repo.snapshotCountCalls())
	require.Empty(t, repo.cyberWarnings)
	// 移除白名单后恢复既有 CyberWarning 流程，历史仅审计记录不追罚。
	svc.settingRepo.(*contentModerationTestSettingRepo).values[SettingKeyCyberPolicyUserAllowlist] = ""
	_, err = svc.refreshRuntimeSnapshot(context.Background())
	require.NoError(t, err)
	svc.emailService = nil
	_, err = svc.RecordCyberWarning(context.Background(), ContentModerationCyberWarningInput{
		UserID: 12, Model: "gpt-5", WarningText: "cyber_policy",
		ResponseBody: []byte(`{"error":{"code":"cyber_policy"}}`),
	})
	require.NoError(t, err)
	require.Len(t, repo.cyberWarnings, 1)
	require.Equal(t, 1, repo.cyberWarnings[0].ViolationCount)
	require.Len(t, repo.snapshotLogs(), 1)
}

// 用计数探针确认白名单路径没有触发普通风控惩罚。
type banCountArgsTestRepo struct {
	contentModerationTestRepo
	countCalls []int64
}

func (r *banCountArgsTestRepo) CountFlaggedByUserSince(ctx context.Context, userID int64, since time.Time) (int, error) {
	r.countCalls = append(r.countCalls, userID)
	return r.contentModerationTestRepo.CountFlaggedByUserSince(ctx, userID, since)
}
func (r *banCountArgsTestRepo) snapshotCountCalls() []int64 { return r.countCalls }
