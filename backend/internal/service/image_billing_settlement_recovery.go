package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"
)

// ImageBillingSettlementEvidence 仅在已确认图片输出后保存原结算命令和展示元数据。
// 禁止存放请求正文、图片、API Key、关联账号对象或上游响应。
type ImageBillingSettlementEvidence struct {
	Version int                 `json:"version"`
	Command UsageBillingCommand `json:"command"`
	Usage   UsageLog            `json:"usage"`
}

type ImageBillingSettlementRecovered struct {
	UserID      int64
	ActorUserID int64
	APIKeyID    int64
	AccountID   int64
}

// ImageBillingSettlementRepository 为已确认生成的原预占提供持久结算重试，不具有生成能力。
type ImageBillingSettlementRepository interface {
	SaveImageBillingSettlementEvidence(context.Context, *ImageBillingSettlementEvidence) error
	RecoverImageBillingSettlements(context.Context, time.Time, int) ([]ImageBillingSettlementRecovered, error)
}

// NormalizeImageBillingSettlementEvidence 创建无关联对象的安全副本，同时限制持久化大小。
func NormalizeImageBillingSettlementEvidence(input *ImageBillingSettlementEvidence) (*ImageBillingSettlementEvidence, []byte, error) {
	if input == nil || input.Version != 1 {
		return nil, nil, ErrImageBillingReservationInvalid
	}
	value := *input
	cmd, log := &value.Command, &value.Usage
	if cmd.RequestID == "" || cmd.UserID <= 0 || cmd.ActorUserID <= 0 || cmd.APIKeyID <= 0 || cmd.AccountID <= 0 ||
		cmd.ImageCount <= 0 || log.ImageCount != cmd.ImageCount || log.RequestID != cmd.RequestID ||
		log.BillingUserID != cmd.UserID || log.UserID != cmd.ActorUserID || log.APIKeyID != cmd.APIKeyID || log.AccountID != cmd.AccountID ||
		log.Model != cmd.Model || !imageSettlementSameID(log.GroupID, cmd.GroupID) || !imageSettlementSameID(log.TeamID, cmd.TeamID) ||
		math.IsNaN(cmd.BaseAmountUSD) || math.IsInf(cmd.BaseAmountUSD, 0) || cmd.BaseAmountUSD < 0 {
		return nil, nil, ErrImageBillingReservationInvalid
	}
	log.User, log.APIKey, log.Account, log.Group, log.Subscription = nil, nil, nil, nil, nil
	log.VideoBilling, log.BillingSubscriptions = nil, nil
	log.UserAgent, log.IPAddress, log.SessionID = nil, nil, nil
	cmd.LegacyVideoTaskID = 0
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > 64*1024 {
		return nil, nil, ErrImageBillingReservationInvalid
	}
	return &value, encoded, nil
}

func imageSettlementSameID(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

// ApplyImageBillingSettlementResult 使用账本最终分配修复费用，不能以首次结算失败写入的零额覆盖真实扣款。
func ApplyImageBillingSettlementResult(log *UsageLog, result *UsageBillingApplyResult) {
	applyUsageBillingResultToUsageLog(log, result)
	if log == nil || result == nil {
		return
	}
	log.ActualCost = result.SubscriptionAmountUSD + result.BalanceAmountUSD
	if log.TotalCost > 0 {
		log.RateMultiplier = log.ActualCost / log.TotalCost
	}
}

// RecoverAsyncImageBilling 只补捕获和使用记录；不会拉取图片或调用任何上游模型。
func (s *OpenAIGatewayService) RecoverAsyncImageBilling(ctx context.Context, invalidateAuth ...func(context.Context, int64)) error {
	if s == nil {
		return nil
	}
	repo, ok := s.usageBillingRepo.(ImageBillingSettlementRepository)
	if !ok {
		return nil
	}
	recovered, recoverErr := repo.RecoverImageBillingSettlements(ctx, time.Now().UTC(), 25)
	for _, item := range recovered {
		if s.billingCacheService != nil {
			if err := s.billingCacheService.InvalidateUserBalance(ctx, item.UserID); err != nil {
				recoverErr = errors.Join(recoverErr, err)
			}
			if err := s.billingCacheService.InvalidateAPIKeyRateLimit(ctx, item.APIKeyID); err != nil {
				recoverErr = errors.Join(recoverErr, err)
			}
		}
		for _, invalidate := range invalidateAuth {
			if invalidate == nil {
				continue
			}
			invalidate(ctx, item.UserID)
			// 团队付款人与 Key 实际所有者可能不同，两侧鉴权快照都需失效。
			if item.ActorUserID > 0 && item.ActorUserID != item.UserID {
				invalidate(ctx, item.ActorUserID)
			}
		}
		if s.deferredService != nil {
			s.deferredService.ScheduleLastUsedUpdate(item.AccountID)
		}
	}
	return recoverErr
}
