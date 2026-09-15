//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

// subscriptionDisplayGroupRepo 保留共享分组，验证展示覆盖不会改动原始倍率。
type subscriptionDisplayGroupRepo struct {
	GroupRepository
	groups []Group
}

func (r *subscriptionDisplayGroupRepo) ListActive(context.Context) ([]Group, error) {
	return r.groups, nil
}

func (r *subscriptionDisplayGroupRepo) GetByIDLite(_ context.Context, id int64) (*Group, error) {
	for i := range r.groups {
		if r.groups[i].ID == id {
			return &r.groups[i], nil
		}
	}
	return nil, ErrGroupNotFound
}

// TestEnrichSubscriptionPlanGroups_Rates 验证展示元数据、每份订阅独立覆盖、零值与已删除分组的展示边界。
func TestEnrichSubscriptionPlanGroups_Rates(t *testing.T) {
	t.Parallel()
	repo := &subscriptionDisplayGroupRepo{groups: []Group{
		{ID: 1, Name: "默认两倍", Platform: PlatformOpenAI, DisplayBrand: "anthropic", RateMultiplier: 2},
		{ID: 2, Name: "默认折扣", Platform: PlatformGemini, RateMultiplier: 0.8},
		{ID: 3, Name: "免费分组", RateMultiplier: 0},
	}}
	svc := NewSubscriptionService(repo, nil, nil, nil, nil)
	subscriptions := []UserSubscription{
		{Plan: &SubscriptionPlan{GroupIDs: []int64{1, 2, 3, 4}, GroupRateMultipliers: map[int64]float64{1: 0.5, 2: 0}}},
		{Plan: &SubscriptionPlan{GroupIDs: []int64{1, 4}, GroupRateMultipliers: map[int64]float64{1: 1.3, 4: 0.7}}},
		{Plan: &SubscriptionPlan{}},
		{},
	}

	svc.EnrichSubscriptionPlanGroups(context.Background(), subscriptions)

	first := subscriptions[0].Plan
	require.True(t, first.GroupsRestricted)
	require.Len(t, first.ApplicableGroups, 4)
	require.Equal(t, "默认两倍", first.ApplicableGroups[0].Name)
	require.Equal(t, PlatformOpenAI, first.ApplicableGroups[0].Platform)
	require.Equal(t, "anthropic", first.ApplicableGroups[0].DisplayBrand)
	require.Equal(t, PlatformGemini, first.ApplicableGroups[1].Platform)
	require.Empty(t, first.ApplicableGroups[1].DisplayBrand)
	require.Equal(t, 0.5, *first.ApplicableGroups[0].RateMultiplier)
	// 计划零值不符合现有计费配置规则，应回退；分组本身为零则必须保留免费倍率。
	require.Equal(t, 0.8, *first.ApplicableGroups[1].RateMultiplier)
	require.NotNil(t, first.ApplicableGroups[2].RateMultiplier)
	require.Zero(t, *first.ApplicableGroups[2].RateMultiplier)
	require.Equal(t, int64(4), first.ApplicableGroups[3].ID)
	require.Empty(t, first.ApplicableGroups[3].Name)
	require.Empty(t, first.ApplicableGroups[3].Platform)
	require.Empty(t, first.ApplicableGroups[3].DisplayBrand)
	require.Nil(t, first.ApplicableGroups[3].RateMultiplier)
	require.Equal(t, 1.3, *subscriptions[1].Plan.ApplicableGroups[0].RateMultiplier)
	require.Equal(t, 0.7, *subscriptions[1].Plan.ApplicableGroups[1].RateMultiplier)
	require.False(t, subscriptions[2].Plan.GroupsRestricted)
	require.Empty(t, subscriptions[2].Plan.ApplicableGroups)
	require.Equal(t, 2.0, repo.groups[0].RateMultiplier)
}

// TestAPIKeyService_AvailableGroupsSubscriptionRates 验证筛选范围、最终覆盖和原分组数据相互隔离。
func TestAPIKeyService_AvailableGroupsSubscriptionRates(t *testing.T) {
	t.Parallel()
	const userID int64 = 7
	repo := &subscriptionDisplayGroupRepo{groups: []Group{
		{ID: 1, RateMultiplier: 2, Status: StatusActive},
		{ID: 2, RateMultiplier: 0.8, Status: StatusActive},
		{ID: 3, RateMultiplier: 0, Status: StatusActive},
		{ID: 4, RateMultiplier: 3, Status: StatusActive, IsExclusive: true},
		{ID: 5, RateMultiplier: 4, Status: StatusActive},
	}}
	first := activeBillingModeSubscription(101, userID, 1, 2, 3, 4)
	first.Plan.GroupRateMultipliers = map[int64]float64{1: 0.5, 2: 0, 4: 0.1}
	second := activeBillingModeSubscription(102, userID, 1)
	second.Plan.GroupRateMultipliers = map[int64]float64{1: 1.3}
	global := activeBillingModeSubscription(103, userID)
	global.Plan.GroupRateMultipliers = map[int64]float64{1: 0.6}
	svc := NewAPIKeyService(nil, &billingModeUserRepoStub{users: map[int64]*User{
		userID: {ID: userID, Status: StatusActive},
	}}, repo, &billingModeSubscriptionRepoStub{subscriptions: map[int64]*UserSubscription{
		first.ID: first, second.ID: second, global.ID: global,
	}}, nil, nil, nil)

	for _, tc := range []struct {
		name           string
		subscriptionID *int64
		wantIDs        []int64
		wantRates      []float64
	}{
		{name: "首份订阅覆盖与缺省", subscriptionID: &first.ID, wantIDs: []int64{1, 2, 3}, wantRates: []float64{0.5, 0.8, 0}},
		{name: "切换到不同倍率的订阅", subscriptionID: &second.ID, wantIDs: []int64{1}, wantRates: []float64{1.3}},
		{name: "未限制分组的套餐", subscriptionID: &global.ID, wantIDs: []int64{1, 2, 3, 5}, wantRates: []float64{0.6, 0.8, 0, 4}},
		{name: "未指定订阅保留历史倍率和范围", wantIDs: []int64{1, 2, 3, 5}, wantRates: []float64{2, 0.8, 0, 4}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			groups, err := svc.GetAvailableGroupsForScopeWithSubscription(context.Background(), userID, "personal", tc.subscriptionID)
			require.NoError(t, err)
			require.Len(t, groups, len(tc.wantIDs))
			for i := range groups {
				require.Equal(t, tc.wantIDs[i], groups[i].ID)
				require.Equal(t, tc.wantRates[i], groups[i].RateMultiplier)
			}
		})
	}
	require.Equal(t, 2.0, repo.groups[0].RateMultiplier)
	require.Equal(t, 0.8, repo.groups[1].RateMultiplier)
}

// TestAPIKeyService_AvailableGroupsSubscriptionRatesTeamOwner 验证团队只展示付款 Owner 的权限与订阅倍率。
func TestAPIKeyService_AvailableGroupsSubscriptionRatesTeamOwner(t *testing.T) {
	t.Parallel()
	const memberID, ownerID int64 = 7, 8
	ownerSubscription := activeBillingModeSubscription(101, ownerID, 1, 2)
	ownerSubscription.Plan.GroupRateMultipliers = map[int64]float64{1: 0.4, 2: 0.6}
	memberSubscription := activeBillingModeSubscription(102, memberID, 1)
	memberSubscription.Plan.GroupRateMultipliers = map[int64]float64{1: 0.1}
	userRepo := &billingModeUserRepoStub{users: map[int64]*User{
		memberID: {ID: memberID, Status: StatusActive, AllowedGroups: []int64{2}},
		ownerID:  {ID: ownerID, Status: StatusActive, AllowedGroups: []int64{1}},
	}}
	svc := NewAPIKeyService(nil, userRepo, &subscriptionDisplayGroupRepo{groups: []Group{
		{ID: 1, RateMultiplier: 2, Status: StatusActive, IsExclusive: true},
		{ID: 2, RateMultiplier: 3, Status: StatusActive, IsExclusive: true},
	}}, &billingModeSubscriptionRepoStub{subscriptions: map[int64]*UserSubscription{
		ownerSubscription.ID: ownerSubscription, memberSubscription.ID: memberSubscription,
	}}, nil, nil, &config.Config{Team: config.TeamConfig{Enabled: true}})
	svc.SetTeamRepository(&fakeTeamRepository{teamContext: &TeamContext{
		Team:       &Team{ID: 11, Status: TeamStatusActive},
		Membership: &TeamMembership{TeamID: 11, UserID: memberID, Role: TeamRoleMember},
		Owner:      &TeamMembership{TeamID: 11, UserID: ownerID, Role: TeamRoleOwner},
	}})

	groups, err := svc.GetAvailableGroupsForScopeWithSubscription(context.Background(), memberID, "team", &ownerSubscription.ID)
	require.NoError(t, err)
	require.Len(t, groups, 1)
	require.Equal(t, int64(1), groups[0].ID)
	require.Equal(t, 0.4, groups[0].RateMultiplier)
	require.Equal(t, []int64{ownerID}, userRepo.requested)

	_, err = svc.GetAvailableGroupsForScopeWithSubscription(context.Background(), memberID, "team", &memberSubscription.ID)
	require.ErrorIs(t, err, ErrPreferredSubscriptionInvalid)
	_, err = svc.GetAvailableGroupsForScopeWithSubscription(context.Background(), memberID, "personal", &ownerSubscription.ID)
	require.ErrorIs(t, err, ErrPreferredSubscriptionInvalid)
}
