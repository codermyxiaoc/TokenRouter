package service

import (
	"context"
	"strconv"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
)

// MaxSmartRoutingGroups 限制单个请求需要检查的候选分组数量。
const MaxSmartRoutingGroups = 10

var (
	ErrSmartRoutingGroupsRequired = infraerrors.BadRequest("SMART_ROUTING_GROUPS_REQUIRED", "smart routing requires at least one group")
	ErrSmartRoutingTooManyGroups  = infraerrors.BadRequest("SMART_ROUTING_TOO_MANY_GROUPS", "smart routing supports at most 10 groups")
	ErrSmartRoutingGroupDuplicate = infraerrors.BadRequest("SMART_ROUTING_GROUP_DUPLICATE", "smart routing groups must be unique")
	ErrSmartRoutingConflict       = infraerrors.BadRequest("SMART_ROUTING_CONFIGURATION_CONFLICT", "smart routing cannot use composite mode, composite_groups or group_id")
	ErrSmartRoutingTargetRequired = infraerrors.BadRequest("SMART_ROUTING_TARGET_GROUP_REQUIRED", "disabling smart routing requires a target group")
)

// SmartRoutingGroupIDs 返回候选的配置顺序；调用方修改结果不会改变鉴权快照。
func (k *APIKey) SmartRoutingGroupIDs() []int64 {
	ids := []int64{}
	if k == nil || !k.SmartRouting {
		return ids
	}
	for _, binding := range k.CompositeGroups {
		ids = append(ids, binding.GroupID)
	}
	return ids
}

// smartRoutingGroupInputs 复用有序分组关联，内部前缀仅满足现有存储约束，不参与模型解析。
func smartRoutingGroupInputs(groupIDs []int64) ([]APIKeyCompositeGroupInput, error) {
	if len(groupIDs) == 0 {
		return nil, ErrSmartRoutingGroupsRequired
	}
	if len(groupIDs) > MaxSmartRoutingGroups {
		return nil, ErrSmartRoutingTooManyGroups
	}
	inputs := make([]APIKeyCompositeGroupInput, 0, len(groupIDs))
	seen := make(map[int64]struct{}, len(groupIDs))
	for _, id := range groupIDs {
		if id <= 0 {
			return nil, ErrGroupNotAllowed
		}
		if _, exists := seen[id]; exists {
			return nil, ErrSmartRoutingGroupDuplicate
		}
		seen[id] = struct{}{}
		inputs = append(inputs, APIKeyCompositeGroupInput{GroupID: id, Prefix: "route_" + strconv.FormatInt(id, 10)})
	}
	return inputs, nil
}

// prepareSmartRoutingGroups 沿用普通与复合 Key 的用户/团队权限、订阅分组校验。
// @project-doc docs/domains/smart_routing_api_keys.md#configuration
func (s *APIKeyService) prepareSmartRoutingGroups(ctx context.Context, user *User, groupIDs []int64) ([]APIKeyCompositeGroup, error) {
	inputs, err := smartRoutingGroupInputs(groupIDs)
	if err != nil {
		return nil, err
	}
	return s.prepareCompositeGroups(ctx, user, inputs)
}
