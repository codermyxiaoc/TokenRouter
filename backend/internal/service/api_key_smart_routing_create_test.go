package service

import (
	"context"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

// 创建替身只记录持久化边界，非法配置不得抵达此处。
type smartRoutingCreateRepo struct {
	APIKeyRepository
	created *APIKey
}

func (r *smartRoutingCreateRepo) Create(_ context.Context, key *APIKey) error {
	key.ID = 100
	r.created = key
	return nil
}

type smartRoutingSubscriptionRepo struct {
	UserSubscriptionRepository
	subscription *UserSubscription
}

func (r *smartRoutingSubscriptionRepo) GetByID(_ context.Context, _ int64) (*UserSubscription, error) {
	return r.subscription, nil
}

func TestSmartRoutingCreateValidatesModesAndSubscription(t *testing.T) {
	groupOne := &Group{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, IsExclusive: true}
	groupTwo := &Group{ID: 2, Platform: PlatformAnthropic, Status: StatusActive, IsExclusive: true}
	user := &User{ID: 20, AllowedGroups: []int64{1, 2}, GroupRestrictionsLoaded: true}
	repo := &smartRoutingCreateRepo{}
	subscriptions := &smartRoutingSubscriptionRepo{subscription: &UserSubscription{
		ID: 30, UserID: user.ID, Status: SubscriptionStatusActive,
		StartsAt: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(time.Hour),
		Plan: &SubscriptionPlan{ID: 30, GroupIDs: []int64{1}},
	}}
	svc := NewAPIKeyService(repo, &compositeUserRepoStub{user: user}, &compositeGroupRepoStub{groups: map[int64]*Group{1: groupOne, 2: groupTwo}}, subscriptions, nil, nil, &config.Config{})
	groupID := int64(1)
	for _, request := range []CreateAPIKeyRequest{
		{Name: "invalid", SmartRouting: true, IsComposite: true, SmartRoutingGroupIDs: []int64{1}},
		{Name: "invalid", SmartRouting: true, GroupID: &groupID, SmartRoutingGroupIDs: []int64{1}},
		{Name: "invalid", SmartRoutingGroupIDs: []int64{1}},
	} {
		_, err := svc.Create(context.Background(), user.ID, request)
		require.ErrorIs(t, err, ErrSmartRoutingConflict)
		require.Nil(t, repo.created)
	}
	_, err := svc.Create(context.Background(), user.ID, CreateAPIKeyRequest{
		Name: "restricted", SmartRouting: true, SmartRoutingGroupIDs: []int64{1, 2},
		BillingMode: APIKeyBillingModeSubscription, PreferredSubscriptionID: &subscriptions.subscription.ID,
	})
	require.ErrorIs(t, err, ErrPreferredSubscriptionGroup)
	require.Nil(t, repo.created)

	created, err := svc.Create(context.Background(), user.ID, CreateAPIKeyRequest{Name: "valid", SmartRouting: true, SmartRoutingGroupIDs: []int64{2, 1}})
	require.NoError(t, err)
	require.True(t, created.SmartRouting)
	require.Equal(t, []int64{2, 1}, created.SmartRoutingGroupIDs())
	require.Nil(t, created.GroupID)
	// 指定订阅更新也要复验整个有序列表，不能仅检查第一候选。
	updateRepo := &compositeAPIKeyRepoStub{key: created}
	svc.apiKeyRepo = updateRepo
	mode := APIKeyBillingModeSubscription
	_, err = svc.Update(context.Background(), created.ID, user.ID, UpdateAPIKeyRequest{BillingMode: &mode, PreferredSubscriptionID: &subscriptions.subscription.ID})
	require.ErrorIs(t, err, ErrPreferredSubscriptionGroup)
	require.Empty(t, updateRepo.updatedFields)
}
