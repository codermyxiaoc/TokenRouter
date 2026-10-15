package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// SmartRoutingContinuationBinding 只包含已验证租户所有权的响应路由，不能由客户端直接指定。
type SmartRoutingContinuationBinding struct {
	GroupID   int64
	AccountID int64
}

// ResolveSmartRoutingContinuation 在当前已授权候选中查找真实归属，避免无关首组故障阻断后继组的续接。
func (s *SmartRoutingService) ResolveSmartRoutingContinuation(ctx context.Context, key *APIKey, responseID string) (SmartRoutingContinuationBinding, error) {
	var result SmartRoutingContinuationBinding
	if s == nil || s.openAI == nil || key == nil || key.User == nil || strings.TrimSpace(responseID) == "" {
		return result, nil
	}
	// 多候选查询共用总预算，Redis 故障不能把续接预选放大成逐组长等待。
	lookupCtx, cancel := context.WithTimeout(WithSmartRoutingScope(ctx), time.Second)
	defer cancel()
	store := s.openAI.getOpenAIWSStateStore()
	for _, binding := range key.CompositeGroups {
		if err := lookupCtx.Err(); err != nil {
			return SmartRoutingContinuationBinding{}, err
		}
		group := binding.Group
		if group == nil || !group.IsActive() || group.Platform != PlatformOpenAI || !key.User.CanBindGroup(group.ID, group.IsExclusive) {
			continue
		}
		allowed, err := s.openAI.ValidateOpenAIHTTPResponseOwner(lookupCtx, group.ID, responseID, key.User.ID, key.ID)
		if errors.Is(err, redis.Nil) {
			continue
		}
		if err != nil {
			return result, err
		}
		if !allowed {
			continue
		}
		if result.GroupID != 0 && result.GroupID != group.ID {
			return SmartRoutingContinuationBinding{}, errors.New("ambiguous smart routing continuation ownership")
		}
		accountID, err := store.GetResponseAccount(lookupCtx, group.ID, responseID)
		if err != nil {
			return result, err
		}
		if accountID <= 0 {
			return result, errors.New("smart routing continuation account binding unavailable")
		}
		result = SmartRoutingContinuationBinding{GroupID: group.ID, AccountID: accountID}
	}
	return result, nil
}
