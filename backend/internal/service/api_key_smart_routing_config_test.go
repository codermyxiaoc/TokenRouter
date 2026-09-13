package service

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

func TestSmartRoutingCooldownCreateDefaultsAndBounds(t *testing.T) {
	// 创建时区分省略与显式 0；非法值必须在访问仓库前拒绝。
	for _, seconds := range []int{-1, MaxSmartRoutingCooldownSeconds + 1} {
		svc := &APIKeyService{}
		_, err := svc.Create(context.Background(), 1, CreateAPIKeyRequest{SmartRoutingCooldownSeconds: &seconds})
		require.ErrorIs(t, err, ErrSmartRoutingCooldownInvalid)
		_, err = svc.Update(context.Background(), 1, 1, UpdateAPIKeyRequest{SmartRoutingCooldownSeconds: &seconds})
		require.ErrorIs(t, err, ErrSmartRoutingCooldownInvalid)
	}
	for _, test := range []struct {
		name  string
		value *int
		want  int
	}{
		{name: "omitted", want: 60},
		{name: "disabled", value: new(0), want: 0},
		{name: "minimum", value: new(1), want: 1},
		{name: "maximum", value: new(3600), want: 3600},
	} {
		t.Run(test.name, func(t *testing.T) {
			group := &Group{ID: 2, Platform: PlatformOpenAI, Status: StatusActive}
			user := &User{ID: 1, AllowedGroups: []int64{2}, GroupRestrictionsLoaded: true}
			repo := &smartRoutingCreateRepo{}
			svc := NewAPIKeyService(repo, &compositeUserRepoStub{user: user}, &compositeGroupRepoStub{groups: map[int64]*Group{2: group}}, nil, nil, nil, &config.Config{})
			key, err := svc.Create(context.Background(), user.ID, CreateAPIKeyRequest{
				Name: "cooldown", SmartRouting: true, SmartRoutingGroupIDs: []int64{2}, SmartRoutingCooldownSeconds: test.value,
			})
			require.NoError(t, err)
			require.Equal(t, test.want, key.SmartRoutingCooldownSeconds)
			require.True(t, key.SmartRouting)
			require.Equal(t, []int64{2}, key.SmartRoutingGroupIDs())
		})
	}
}

func TestSmartRoutingCooldownUpdatePreservesGroupsAndUsage(t *testing.T) {
	key := &APIKey{ID: 10, UserID: 1, Key: "sk-cooldown", Status: StatusActive, SmartRouting: true, SmartRoutingCooldownSeconds: 90,
		QuotaUsed: 22, Usage5h: 7, CompositeGroups: []APIKeyCompositeGroup{{ID: 20, GroupID: 2, SortOrder: 0}}}
	repo := &compositeAPIKeyRepoStub{key: key}
	svc := NewAPIKeyService(repo, nil, nil, nil, nil, nil, nil)
	name := "renamed"
	updated, err := svc.Update(context.Background(), key.ID, key.UserID, UpdateAPIKeyRequest{Name: &name})
	require.NoError(t, err)
	require.Equal(t, 90, updated.SmartRoutingCooldownSeconds)
	require.Equal(t, APIKeyUpdateFields{Name: true}, repo.updatedFields[0])
	for _, seconds := range []int{0, 3600} {
		updated, err = svc.Update(context.Background(), key.ID, key.UserID, UpdateAPIKeyRequest{SmartRoutingCooldownSeconds: &seconds})
		require.NoError(t, err)
		require.Equal(t, seconds, updated.SmartRoutingCooldownSeconds)
		require.True(t, updated.SmartRouting)
		require.Equal(t, key.CompositeGroups, updated.CompositeGroups)
		require.Equal(t, 22.0, updated.QuotaUsed)
		require.Equal(t, 7.0, updated.Usage5h)
		require.Equal(t, APIKeyUpdateFields{SmartRoutingCooldown: true}, repo.updatedFields[len(repo.updatedFields)-1])
	}
}

func TestSmartRoutingCooldownAuthSnapshotRoundTrip(t *testing.T) {
	svc := &APIKeyService{}
	for _, seconds := range []int{0, 60, 3600} {
		t.Run(strconv.Itoa(seconds), func(t *testing.T) {
			key := &APIKey{ID: 1, UserID: 2, Key: "sk-cooldown-cache", SmartRouting: true, SmartRoutingCooldownSeconds: seconds, User: &User{ID: 2}}
			payload, err := json.Marshal(svc.snapshotFromAPIKey(context.Background(), key))
			require.NoError(t, err)
			var snapshot APIKeyAuthSnapshot
			require.NoError(t, json.Unmarshal(payload, &snapshot))
			require.Equal(t, 39, snapshot.Version)
			restored, hit, err := svc.applyAuthCacheEntry(key.Key, &APIKeyAuthCacheEntry{Snapshot: &snapshot})
			require.NoError(t, err)
			require.True(t, hit)
			require.Equal(t, seconds, restored.SmartRoutingCooldownSeconds)
			// v38 没有冷却列，不能把缺字段的零值误当成用户显式关闭冷却。
			snapshot.Version = 38
			_, hit, err = svc.applyAuthCacheEntry(key.Key, &APIKeyAuthCacheEntry{Snapshot: &snapshot})
			require.NoError(t, err)
			require.False(t, hit)
		})
	}
}
