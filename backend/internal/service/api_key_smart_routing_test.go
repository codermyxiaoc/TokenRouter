package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSmartRoutingGroupValidation(t *testing.T) {
	// 空候选、重复和越界配置必须在查询分组前拒绝，顺序不做自动排序。
	tests := []struct {
		name string
		ids  []int64
		err  error
	}{
		{"empty", nil, ErrSmartRoutingGroupsRequired},
		{"too_many", make([]int64, 11), ErrSmartRoutingTooManyGroups},
		{"duplicate", []int64{2, 2}, ErrSmartRoutingGroupDuplicate},
		{"invalid", []int64{0}, ErrGroupNotAllowed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := smartRoutingGroupInputs(tt.ids)
			require.ErrorIs(t, err, tt.err)
		})
	}
	inputs, err := smartRoutingGroupInputs([]int64{20, 3})
	require.NoError(t, err)
	require.Equal(t, int64(20), inputs[0].GroupID)
	require.Equal(t, int64(3), inputs[1].GroupID)
	maximum := make([]int64, 10)
	for index := range maximum {
		maximum[index] = int64(index + 1)
	}
	_, err = smartRoutingGroupInputs(maximum)
	require.NoError(t, err)
}

func TestSmartRoutingAPIKeyUpdateModesAndPermissions(t *testing.T) {
	groupOne := &Group{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, IsExclusive: true}
	groupTwo := &Group{ID: 2, Platform: PlatformAnthropic, Status: StatusActive, IsExclusive: true}
	groupForbidden := &Group{ID: 3, Platform: PlatformGemini, Status: StatusActive, IsExclusive: true}
	user := &User{ID: 20, Status: StatusActive, AllowedGroups: []int64{1, 2}, GroupRestrictionsLoaded: true}
	groupID := int64(1)
	repo := &compositeAPIKeyRepoStub{key: &APIKey{ID: 10, UserID: user.ID, Key: "sk-smart", Status: StatusActive, User: user, GroupID: &groupID, Group: groupOne}}
	svc := NewAPIKeyService(repo, &compositeUserRepoStub{user: user}, &compositeGroupRepoStub{groups: map[int64]*Group{1: groupOne, 2: groupTwo, 3: groupForbidden}}, nil, nil, nil, nil)
	enabled, disabled := true, false
	ids := []int64{2, 1}
	updated, err := svc.Update(context.Background(), 10, user.ID, UpdateAPIKeyRequest{SmartRouting: &enabled, SmartRoutingGroupIDs: &ids})
	require.NoError(t, err)
	require.True(t, updated.SmartRouting)
	require.False(t, updated.IsComposite)
	require.Nil(t, updated.GroupID)
	require.Equal(t, ids, updated.SmartRoutingGroupIDs())
	require.Equal(t, APIKeyUpdateFields{GroupID: true, CompositeConfiguration: true}, repo.updatedFields[0])

	// 重新排序是完整替换；禁止加入无权限分组，失败不触发持久化。
	ids = []int64{1, 2}
	updated, err = svc.Update(context.Background(), 10, user.ID, UpdateAPIKeyRequest{SmartRoutingGroupIDs: &ids})
	require.NoError(t, err)
	require.Equal(t, ids, updated.SmartRoutingGroupIDs())
	forbidden := []int64{3}
	_, err = svc.Update(context.Background(), 10, user.ID, UpdateAPIKeyRequest{SmartRoutingGroupIDs: &forbidden})
	require.ErrorIs(t, err, ErrGroupNotAllowed)
	require.Len(t, repo.updatedFields, 2)
	_, err = svc.Update(context.Background(), 10, user.ID, UpdateAPIKeyRequest{IsComposite: &enabled})
	require.ErrorIs(t, err, ErrSmartRoutingConflict)
	_, err = svc.Update(context.Background(), 10, user.ID, UpdateAPIKeyRequest{GroupID: &groupID})
	require.ErrorIs(t, err, ErrSmartRoutingConflict)
	_, err = svc.Update(context.Background(), 10, user.ID, UpdateAPIKeyRequest{SmartRouting: &disabled})
	require.ErrorIs(t, err, ErrSmartRoutingTargetRequired)
	updated, err = svc.Update(context.Background(), 10, user.ID, UpdateAPIKeyRequest{SmartRouting: &disabled, GroupID: &groupID})
	require.NoError(t, err)
	require.False(t, updated.SmartRouting)
	require.Empty(t, updated.CompositeGroups)
	require.Equal(t, groupID, *updated.GroupID)
}

func TestSmartRoutingAuthSnapshotAndSelection(t *testing.T) {
	group := &Group{ID: 2, Platform: PlatformOpenAI, Status: StatusActive, RPMLimit: 80}
	key := &APIKey{ID: 1, UserID: 20, Key: "sk-smart-cache", SmartRouting: true, User: &User{ID: 20}, CompositeGroups: []APIKeyCompositeGroup{{GroupID: 2, Group: group}}, FallbackToDefaultGroupWhenUnavailable: true}
	svc := NewAPIKeyService(nil, nil, nil, nil, nil, nil, nil)
	snapshot := svc.snapshotFromAPIKey(context.Background(), key)
	restored := svc.snapshotToAPIKey(key.Key, snapshot)
	require.True(t, restored.SmartRouting)
	require.False(t, restored.IsComposite)
	require.Equal(t, []int64{2}, restored.SmartRoutingGroupIDs())
	require.Equal(t, 80, restored.CompositeGroups[0].Group.RPMLimit)
	selected, err := svc.SelectCompositeGroupForRequest(context.Background(), restored, &restored.CompositeGroups[0])
	require.NoError(t, err)
	require.Equal(t, int64(2), *selected.GroupID)
	require.Nil(t, restored.GroupID)
	selected.CompositeGroups[0].GroupID = 9
	require.Equal(t, int64(2), restored.CompositeGroups[0].GroupID)
}
