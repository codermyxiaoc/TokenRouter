//go:build unit

package repository

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

func creativeTextBillingUnitCommand() service.UsageBillingCommand {
	group := int64(4)
	cmd := service.UsageBillingCommand{CreativeTextExecutionID: "11111111-1111-4111-8111-111111111111", RequestID: "creative_text:11111111-1111-4111-8111-111111111111",
		UserID: 1, ActorUserID: 1, APIKeyID: 2, AccountID: 3, GroupID: &group, Model: "grok-4.5", APIKeyBillingMode: service.APIKeyBillingModeBalance,
		BaseAmountUSD: 0.25, BillableAmountUSD: 0.25, BalanceRateMultiplier: 1, InputTokens: 10, OutputTokens: 20}
	cmd.Normalize()
	return cmd
}

func creativeTextBillingUnitEvidence(cmd service.UsageBillingCommand) creativeTextBillingEvidenceRecord {
	return creativeTextBillingEvidenceRecord{Version: 1, Command: cmd, Usage: service.UsageLog{
		UserID: cmd.ActorUserID, BillingUserID: cmd.UserID, APIKeyID: cmd.APIKeyID, AccountID: cmd.AccountID,
		GroupID: cmd.GroupID, RequestID: cmd.RequestID, Model: cmd.Model}}
}

func TestCreativeTextBillingCommandScopeAndLegacyFingerprint(t *testing.T) {
	cmd := creativeTextBillingUnitCommand()
	require.NoError(t, validateCreativeTextBillingCommand(&cmd))
	old := cmd
	old.CreativeTextExecutionID, old.RequestFingerprint = "", ""
	old.Normalize()
	require.Equal(t, cmd.RequestFingerprint, old.RequestFingerprint, "新增回执字段不能更改旧命令指纹")
	for name, mutate := range map[string]func(*service.UsageBillingCommand){
		"缺少执行ID":    func(c *service.UsageBillingCommand) { c.CreativeTextExecutionID = "" },
		"非法UUID":    func(c *service.UsageBillingCommand) { c.CreativeTextExecutionID = "../foreign" },
		"请求ID不匹配":   func(c *service.UsageBillingCommand) { c.RequestID = "other-request" },
		"付款人与操作人不同": func(c *service.UsageBillingCommand) { c.ActorUserID++ },
		"团队身份":      func(c *service.UsageBillingCommand) { id := int64(9); c.TeamID = &id },
		"无分组":       func(c *service.UsageBillingCommand) { c.GroupID = nil },
		"旧视频混用":     func(c *service.UsageBillingCommand) { c.LegacyVideoTaskID = 1 },
		"图片混用":      func(c *service.UsageBillingCommand) { c.ImageCount = 1 },
		"语音混用":      func(c *service.UsageBillingCommand) { c.MediaType = "audio" },
		"未持久化的订阅范围": func(c *service.UsageBillingCommand) { id := int64(9); c.SubscriptionScopeID = &id },
		"负金额":       func(c *service.UsageBillingCommand) { c.BaseAmountUSD = -1 },
		"非有限金额":     func(c *service.UsageBillingCommand) { c.BaseAmountUSD = math.Inf(1) },
		"无指纹":       func(c *service.UsageBillingCommand) { c.RequestFingerprint = "" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := cmd
			mutate(&changed)
			require.ErrorIs(t, validateCreativeTextBillingCommand(&changed), service.ErrUsageBillingRequestConflict)
		})
	}
	// 原普通文本/图片/视频命令不进入专属画布校验。
	require.NoError(t, validateCreativeTextBillingCommand(&service.UsageBillingCommand{RequestID: "ordinary", ImageCount: 1}))
	require.NoError(t, validateCreativeTextBillingCommand(&service.UsageBillingCommand{RequestID: "grok-video:old", LegacyVideoTaskID: 9}))
}

func TestCreativeTextBillingEvidenceCannotChangeAmountsOrOwnership(t *testing.T) {
	cmd := creativeTextBillingUnitCommand()
	evidence := creativeTextBillingUnitEvidence(cmd)
	raw, err := json.Marshal(evidence)
	require.NoError(t, err)
	require.NoError(t, validateCreativeTextBillingEvidence(raw, cmd.UserID, "text", &cmd))
	require.ErrorIs(t, validateCreativeTextBillingEvidence(raw, cmd.UserID+1, "text", &cmd), service.ErrUsageBillingRequestConflict)
	require.ErrorIs(t, validateCreativeTextBillingEvidence(raw, cmd.UserID, "audio", &cmd), service.ErrUsageBillingRequestConflict)
	changed := cmd
	changed.BaseAmountUSD = 9 // 故意保留旧指纹，整命令比较仍必须拒绝。
	require.ErrorIs(t, validateCreativeTextBillingEvidence(raw, cmd.UserID, "text", &changed), service.ErrUsageBillingRequestConflict)
	for name, mutate := range map[string]func(*creativeTextBillingEvidenceRecord){
		"版本错误":   func(e *creativeTextBillingEvidenceRecord) { e.Version = 2 },
		"账户错误":   func(e *creativeTextBillingEvidenceRecord) { e.Command.AccountID++ },
		"金额错误":   func(e *creativeTextBillingEvidenceRecord) { e.Command.BaseAmountUSD++ },
		"日志归属错误": func(e *creativeTextBillingEvidenceRecord) { e.Usage.UserID++ },
		"日志请求错误": func(e *creativeTextBillingEvidenceRecord) { e.Usage.RequestID += "wrong" },
		"关联密钥对象": func(e *creativeTextBillingEvidenceRecord) { e.Usage.APIKey = &service.APIKey{Key: "test-only"} },
		"关联用户对象": func(e *creativeTextBillingEvidenceRecord) { e.Usage.User = &service.User{} },
		"保存用户代理": func(e *creativeTextBillingEvidenceRecord) { value := "private"; e.Usage.UserAgent = &value },
		"混入媒体日志": func(e *creativeTextBillingEvidenceRecord) { e.Usage.VideoCount = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			modified := creativeTextBillingUnitEvidence(cmd)
			mutate(&modified)
			bytes, err := json.Marshal(modified)
			require.NoError(t, err)
			require.ErrorIs(t, validateCreativeTextBillingEvidence(bytes, cmd.UserID, "text", &cmd), service.ErrUsageBillingRequestConflict)
		})
	}
	for _, bad := range [][]byte{nil, []byte("null"), append(append([]byte(nil), raw...), []byte(" {}")...), make([]byte, 64*1024+1)} {
		require.ErrorIs(t, validateCreativeTextBillingEvidence(bad, cmd.UserID, "text", &cmd), service.ErrUsageBillingRequestConflict)
	}
}
