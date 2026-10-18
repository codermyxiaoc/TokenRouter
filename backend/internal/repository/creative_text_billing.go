package repository

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"math"
	"reflect"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
)

// 此结构仅用于核验服务端保存的文本结算快照，不接收外部请求或供应商正文。
type creativeTextBillingEvidenceRecord struct {
	Version int                         `json:"version"`
	Command service.UsageBillingCommand `json:"command"`
	Usage   service.UsageLog            `json:"usage"`
}

// 专属请求前缀和执行 UUID 必须成对出现，防止移除关联字段后绕开持久证据校验。
func validateCreativeTextBillingCommand(cmd *service.UsageBillingCommand) error {
	if cmd == nil || cmd.CreativeTextExecutionID == "" && !strings.HasPrefix(cmd.RequestID, "creative_text:") {
		return nil
	}
	id, err := uuid.Parse(cmd.CreativeTextExecutionID)
	if err != nil || id == uuid.Nil || id.String() != cmd.CreativeTextExecutionID || cmd.RequestID != "creative_text:"+id.String() ||
		cmd.UserID <= 0 || cmd.ActorUserID != cmd.UserID || cmd.APIKeyID <= 0 || cmd.AccountID <= 0 || cmd.GroupID == nil || *cmd.GroupID <= 0 ||
		cmd.TeamID != nil || cmd.LegacyVideoTaskID != 0 || cmd.ImageCount != 0 || cmd.MediaType != "" && cmd.MediaType != "text" ||
		cmd.SubscriptionScopeID != nil || cmd.PreserveZeroRateAllocation || strings.TrimSpace(cmd.Model) == "" || strings.TrimSpace(cmd.RequestFingerprint) == "" {
		return service.ErrUsageBillingRequestConflict
	}
	for _, value := range []float64{cmd.BillableAmountUSD, cmd.BaseAmountUSD, cmd.SubscriptionRateMultiplier, cmd.SubscriptionRateMultiplierScale, cmd.BalanceRateMultiplier, cmd.APIKeyQuotaCost, cmd.APIKeyRateLimitCost, cmd.AccountQuotaCost} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return service.ErrUsageBillingRequestConflict
		}
	}
	if cmd.InputTokens < 0 || cmd.OutputTokens < 0 || cmd.CacheCreationTokens < 0 || cmd.CacheReadTokens < 0 {
		return service.ErrUsageBillingRequestConflict
	}
	return nil
}

// 比较整份冻结命令而不是信任传入指纹，避免金额或倍率被改写却沿用原指纹。
func validateCreativeTextBillingEvidence(raw []byte, owner int64, kind string, cmd *service.UsageBillingCommand) error {
	if err := validateCreativeTextBillingCommand(cmd); err != nil {
		return err
	}
	if cmd == nil || cmd.CreativeTextExecutionID == "" || owner != cmd.UserID || kind != "text" || len(raw) == 0 || len(raw) > 64*1024 {
		return service.ErrUsageBillingRequestConflict
	}
	var evidence creativeTextBillingEvidenceRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&evidence) != nil || decoder.Decode(new(any)) != io.EOF || evidence.Version != 1 {
		return service.ErrUsageBillingRequestConflict
	}
	evidence.Command.Normalize()
	if err := validateCreativeTextBillingCommand(&evidence.Command); err != nil || !reflect.DeepEqual(evidence.Command, *cmd) {
		return service.ErrUsageBillingRequestConflict
	}
	u := &evidence.Usage
	if u.UserID != owner || u.BillingUserID != owner || u.APIKeyID != cmd.APIKeyID || u.AccountID != cmd.AccountID ||
		u.RequestID != cmd.RequestID || u.Model != cmd.Model || !reflect.DeepEqual(u.GroupID, cmd.GroupID) || u.TeamID != nil ||
		u.ImageCount != 0 || u.VideoCount != 0 || u.User != nil || u.APIKey != nil || u.Account != nil || u.Group != nil || u.Subscription != nil ||
		u.VideoBilling != nil || len(u.BillingSubscriptions) != 0 || u.UserAgent != nil || u.IPAddress != nil || u.SessionID != nil {
		return service.ErrUsageBillingRequestConflict
	}
	return nil
}

// 在当前资金事务中锁住证据行，避免校验通过后另一恢复器替换证据或先写入回执。
func lockCreativeTextBillingEvidence(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand) ([]byte, error) {
	var owner int64
	var kind string
	var evidence, receipt []byte
	err := tx.QueryRowContext(ctx, `SELECT user_id,kind,text_billing_evidence,text_billing_result FROM creative_canvas_executions WHERE id=$1 FOR UPDATE`, cmd.CreativeTextExecutionID).Scan(&owner, &kind, &evidence, &receipt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrUsageBillingRequestConflict
	}
	if err != nil {
		return nil, err
	}
	if err := validateCreativeTextBillingEvidence(evidence, owner, kind, cmd); err != nil {
		return nil, err
	}
	return receipt, nil
}

func replayCreativeTextBillingReceipt(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand) (*service.UsageBillingApplyResult, error) {
	raw, err := lockCreativeTextBillingEvidence(ctx, tx, cmd)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, errors.New("文本已扣费但原扣费回执缺失，需人工核对")
	}
	var receipt service.UsageBillingApplyResult
	if err := json.Unmarshal(raw, &receipt); err != nil || !receipt.Applied {
		return nil, errors.New("文本原扣费回执损坏，需人工核对")
	}
	receipt.Applied = false
	return &receipt, nil
}

func saveCreativeTextBillingReceipt(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand, result *service.UsageBillingApplyResult) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	updated, err := tx.ExecContext(ctx, `UPDATE creative_canvas_executions SET text_billing_result=$1 WHERE id=$2 AND user_id=$3 AND kind='text' AND text_billing_result IS NULL`, string(raw), cmd.CreativeTextExecutionID, cmd.UserID)
	if err != nil {
		return err
	}
	affected, err := updated.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return service.ErrUsageBillingRequestConflict
	}
	return nil
}
