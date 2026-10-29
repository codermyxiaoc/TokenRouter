//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// 管理端真实创建和编辑均要保留独立 Video 身份及共享凭据，删除废弃预留上限；
// 旧端点配置在保存时迁移为 video_model_protocols 并清理旧字段，迁移规则与运行时换算一致。
func TestVideoAdminReadinessAccountCreateAndUpdate(t *testing.T) {
	ctx := context.Background()
	repo := &accountServiceAdminTestRepo{accountServiceTestRepo: &accountServiceTestRepo{}}
	svc := &adminServiceImpl{accountRepo: repo}
	credentials := videoFixtureAccount(VideoEndpointCompat).Credentials
	credentials["video_endpoints"] = []string{"compat", "seedance"}
	credentials["video_model_bindings"] = map[string]any{"upstream-video": []string{"compat", "seedance"}}
	credentials["video_base_urls"] = map[string]string{"seedance": "https://ark.example"}
	credentials["model_mapping"] = map[string]any{"client-video": "upstream-video"}
	credentials["video_max_output_tokens"] = 100000
	account, err := svc.CreateAccount(ctx, &CreateAccountInput{Name: "video-ready", Platform: PlatformVideo, Type: AccountTypeAPIKey,
		Credentials: credentials, SkipDefaultGroupBind: true})
	require.NoError(t, err)
	require.Equal(t, PlatformVideo, account.Platform)
	require.Equal(t, AccountTypeAPIKey, account.Type)
	require.NotContains(t, account.Credentials, "video_max_output_tokens")
	for _, key := range videoLegacyCredentialKeys {
		require.NotContains(t, account.Credentials, key)
	}
	cfg, err := account.VideoConfiguration()
	require.NoError(t, err)
	// 多端点模型按旧 /v1/videos 优先级迁移：compat 优先于原生端点。
	binding, bound := cfg.Protocol("upstream-video")
	require.True(t, bound)
	// compat 的原样协议保存时改绑为通用扁平格式模板。
	require.Equal(t, "profile:generic-flat", videoProtocolLabel(binding))
	// 通用编辑接口提交完整凭据对象；只提交旧字段时同样迁移，未勾选的兼容入口不会被保留为隐式绑定。
	editedCredentials := mergeMap(nil, account.Credentials)
	delete(editedCredentials, "video_model_protocols")
	editedCredentials["video_endpoints"] = []string{"seedance"}
	editedCredentials["video_model_bindings"] = map[string]any{"upstream-video": "seedance"}
	editedCredentials["video_base_urls"] = map[string]string{"seedance": "https://ark.example"}
	updated, err := svc.UpdateAccount(ctx, account.ID, &UpdateAccountInput{Credentials: editedCredentials})
	require.NoError(t, err)
	require.Equal(t, "fixture-secret", updated.GetCredential("api_key"))
	require.Equal(t, "upstream-video", updated.GetMappedModel("client-video"))
	cfg, err = updated.VideoConfiguration()
	require.NoError(t, err)
	for model := range cfg.ModelProtocols {
		binding, bound := cfg.Protocol(model)
		require.True(t, bound)
		require.Equal(t, VideoEndpointSeedance, binding.Endpoint)
		require.Equal(t, "https://ark.example", binding.BaseURL)
	}
	for _, key := range videoLegacyCredentialKeys {
		require.NotContains(t, updated.Credentials, key)
	}
	// 新格式编辑直接保存绑定；模板协议与内置协议同样按模型精确匹配。
	editedCredentials = mergeMap(nil, updated.Credentials)
	editedCredentials["video_model_protocols"] = map[string]any{"upstream-video": map[string]any{"protocol": "wan.wan_v1"}}
	updated, err = svc.UpdateAccount(ctx, account.ID, &UpdateAccountInput{Credentials: editedCredentials})
	require.NoError(t, err)
	cfg, err = updated.VideoConfiguration()
	require.NoError(t, err)
	binding, bound = cfg.Protocol("upstream-video")
	require.True(t, bound)
	require.Equal(t, VideoEndpointWan, binding.Endpoint)
	_, bound = cfg.Protocol("other-video")
	require.False(t, bound)
	stored, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, updated.Credentials, stored.Credentials)
}

// 创建及编辑不得允许非 API Key 身份绕过 Video 凭据校验。
func TestVideoAdminReadinessRejectsNonAPIKey(t *testing.T) {
	for _, kind := range []string{AccountTypeOAuth, AccountTypeSetupToken, AccountTypeUpstream, AccountTypeBedrock, AccountTypeServiceAccount} {
		for _, operation := range []string{"create", "update"} {
			t.Run(kind+"/"+operation, func(t *testing.T) {
				ctx := context.Background()
				repo := &accountServiceAdminTestRepo{accountServiceTestRepo: &accountServiceTestRepo{}}
				svc := &adminServiceImpl{accountRepo: repo}
				var err error
				if operation == "create" {
					_, err = svc.CreateAccount(ctx, &CreateAccountInput{Name: "invalid-video", Platform: PlatformVideo, Type: kind,
						Credentials: videoFixtureAccount(VideoEndpointCompat).Credentials, SkipDefaultGroupBind: true})
					require.Empty(t, repo.accounts)
				} else {
					account := videoFixtureAccount(VideoEndpointCompat)
					require.NoError(t, repo.Create(ctx, account))
					_, err = svc.UpdateAccount(ctx, account.ID, &UpdateAccountInput{Type: kind})
					require.Equal(t, AccountTypeAPIKey, repo.accounts[account.ID].Type)
				}
				require.Error(t, err)
				require.Equal(t, "VIDEO_ACCOUNT_INVALID", infraerrors.Reason(err))
			})
		}
	}
}

// 三条写入口都须先执行双向分组隔离；跳过混合渠道提示不能绕过硬性约束。
func TestVideoAdminReadinessGroupIsolationAtWrites(t *testing.T) {
	for _, accountPlatform := range []string{PlatformVideo, PlatformOpenAI} {
		groupPlatform := PlatformVideo
		if accountPlatform == PlatformVideo {
			groupPlatform = PlatformOpenAI
		}
		for _, operation := range []string{"create", "update", "bulk"} {
			t.Run(accountPlatform+"/"+operation, func(t *testing.T) {
				ctx := context.Background()
				repo := &accountServiceAdminTestRepo{accountServiceTestRepo: &accountServiceTestRepo{}}
				svc := &adminServiceImpl{accountRepo: repo, groupRepo: &groupRepoStubForAdmin{getByID: &Group{ID: 5, Platform: groupPlatform}}}
				ids := []int64{5}
				account := videoFixtureAccount(VideoEndpointCompat)
				account.Platform = accountPlatform
				var err error
				switch operation {
				case "create":
					_, err = svc.CreateAccount(ctx, &CreateAccountInput{Name: "isolated", Platform: accountPlatform, Type: AccountTypeAPIKey,
						Credentials: account.Credentials, GroupIDs: ids, SkipMixedChannelCheck: true})
					require.Empty(t, repo.accounts)
				case "update":
					require.NoError(t, repo.Create(ctx, account))
					_, err = svc.UpdateAccount(ctx, account.ID, &UpdateAccountInput{GroupIDs: &ids, SkipMixedChannelCheck: true})
				case "bulk":
					require.NoError(t, repo.Create(ctx, account))
					_, err = svc.BulkUpdateAccounts(ctx, &BulkUpdateAccountsInput{AccountIDs: []int64{account.ID}, GroupIDs: &ids, SkipMixedChannelCheck: true})
					require.Empty(t, repo.bulkUpdates)
				}
				require.Equal(t, "VIDEO_GROUP_PLATFORM_MISMATCH", infraerrors.Reason(err))
			})
		}
	}
}

// 每种视频价卡经分组创建、编辑和 JSON 回读后保留零价、固定图片费及各自计费模式。
func TestVideoAdminReadinessGroupPricingRoundTrip(t *testing.T) {
	for _, mode := range []BillingMode{BillingModeVideo, BillingModeVideoToken, BillingModeVideoPerRequest} {
		t.Run(string(mode), func(t *testing.T) {
			ctx := context.Background()
			zero, price, fixed, rate := 0., 23., .15, .5
			card := ChannelModelPricing{Models: []string{"video-model"}, BillingMode: mode,
				VideoPrices:        []VideoPriceTier{{Resolution: "480p", Price: &zero}, {Resolution: "768p", Price: &price}},
				VideoFallbackPrice: &price, VideoImageInputPricing: &VideoImageInputPricing{FreeImages: 5, Price: &fixed}}
			if mode == BillingModeVideoToken {
				card.VideoTokenPrepay = &VideoTokenPrepayConfig{PricePerSecond: &fixed}
			}
			repo := &groupRepoStubForAdmin{}
			svc := &adminServiceImpl{groupRepo: repo}
			group, err := svc.CreateGroup(ctx, &CreateGroupInput{Name: "video-pricing", Platform: PlatformVideo, RateMultiplier: 2,
				VideoRateIndependent: true, VideoRateMultiplier: &rate, ModelPricing: []ChannelModelPricing{card}})
			require.NoError(t, err)
			require.Equal(t, PlatformVideo, group.ModelPricing[0].Platform)
			require.Equal(t, mode, group.ModelPricing[0].BillingMode)
			require.Zero(t, *group.ModelPricing[0].VideoPrices[0].Price)
			require.True(t, group.VideoRateIndependent)
			require.Equal(t, rate, group.VideoRateMultiplier)
			repo.getByID = group
			cards := group.ModelPricing
			updated, err := svc.UpdateGroup(ctx, 1, &UpdateGroupInput{ModelPricing: &cards})
			require.NoError(t, err)
			raw, err := json.Marshal(updated)
			require.NoError(t, err)
			var decoded Group
			require.NoError(t, json.Unmarshal(raw, &decoded))
			require.Equal(t, updated.ModelPricing, decoded.ModelPricing)
			require.Equal(t, mode, decoded.ModelPricing[0].BillingMode)
		})
	}
}

// 渠道真实服务完成创建、编辑和缓存重建后，调用链读取的仍是 Video 三模式价卡。
func TestVideoAdminReadinessChannelCreateUpdateAndRuntimeLookup(t *testing.T) {
	for _, mode := range []BillingMode{BillingModeVideo, BillingModeVideoToken, BillingModeVideoPerRequest} {
		t.Run(string(mode), func(t *testing.T) {
			ctx := context.Background()
			price, fallback, fixed, prepay := 23., 42., .15, .3
			cards := []ChannelModelPricing{{Platform: PlatformVideo, Models: []string{"upstream-video"}, BillingMode: mode,
				VideoPrices: []VideoPriceTier{{Resolution: "768p", Price: &price}}, VideoFallbackPrice: &fallback,
				VideoImageInputPricing: &VideoImageInputPricing{FreeImages: 5, Price: &fixed}}}
			if mode == BillingModeVideoToken {
				cards[0].VideoTokenPrepay = &VideoTokenPrepayConfig{PricePerSecond: &prepay}
			}
			var persisted *Channel
			clone := func(ch *Channel) *Channel {
				raw, err := json.Marshal(ch)
				require.NoError(t, err)
				var copy Channel
				require.NoError(t, json.Unmarshal(raw, &copy))
				return &copy
			}
			repo := &mockChannelRepository{
				createFn:  func(_ context.Context, ch *Channel) error { ch.ID = 77; persisted = clone(ch); return nil },
				updateFn:  func(_ context.Context, ch *Channel) error { persisted = clone(ch); return nil },
				getByIDFn: func(context.Context, int64) (*Channel, error) { return clone(persisted), nil },
				listAllFn: func(context.Context) ([]Channel, error) { return []Channel{*clone(persisted)}, nil },
				getGroupPlatformsFn: func(context.Context, []int64) (map[int64]string, error) {
					return map[int64]string{5: PlatformVideo}, nil
				},
			}
			svc := newTestChannelService(repo)
			created, err := svc.Create(ctx, &CreateChannelInput{Name: "video-channel", GroupIDs: []int64{5}, ModelPricing: cards,
				ModelMapping: map[string]map[string]string{PlatformVideo: {"client-video": "upstream-video"}}, RestrictModels: true})
			require.NoError(t, err)
			require.Equal(t, cards, created.ModelPricing)
			require.Equal(t, "upstream-video", svc.ResolveChannelMapping(ctx, 5, "client-video").MappedModel)
			lookup := svc.GetChannelModelPricing(ctx, 5, "upstream-video")
			require.NotNil(t, lookup)
			require.Equal(t, mode, lookup.BillingMode)
			require.Equal(t, price, *lookup.VideoPrices[0].Price)
			zero := 0.
			cards[0].VideoPrices[0].Price = &zero
			_, err = svc.Update(ctx, created.ID, &UpdateChannelInput{ModelPricing: &cards})
			require.NoError(t, err)
			lookup = svc.GetChannelModelPricing(ctx, 5, "upstream-video")
			require.NotNil(t, lookup)
			require.Zero(t, *lookup.VideoPrices[0].Price, "更新后不能继续命中旧缓存价格")
			require.Equal(t, fixed, *lookup.VideoImageInputPricing.Price)
		})
	}
}

// 套餐配置使用隔离内存数据库，验证 Video 分组和覆盖倍率真实保存、编辑及关联同步。
func TestVideoAdminReadinessPlanGroupRates(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	_, err := client.ExecContext(ctx, `CREATE TABLE subscription_plan_groups (plan_id integer NOT NULL REFERENCES subscription_plans(id), group_id integer NOT NULL REFERENCES groups(id), rate_multiplier real, PRIMARY KEY(plan_id,group_id))`)
	require.NoError(t, err)
	group, err := client.Group.Create().SetName("video-plan-group").SetPlatform(PlatformVideo).Save(ctx)
	require.NoError(t, err)
	groupID := int64(group.ID)
	svc := &PaymentConfigService{entClient: client}
	plan, err := svc.CreatePlan(ctx, CreatePlanRequest{Name: "video-plan", Price: 1, ValidityDays: 30, ValidityUnit: "days",
		GroupIDs: []int64{groupID}, GroupRateMultipliers: map[int64]float64{groupID: .5}})
	require.NoError(t, err)
	require.Equal(t, []int64{groupID}, plan.GroupIds)
	require.Equal(t, .5, plan.GroupRateMultipliers[groupID])
	rates := map[int64]float64{groupID: .25}
	_, err = svc.UpdatePlan(ctx, int64(plan.ID), UpdatePlanRequest{GroupRateMultipliers: &rates})
	require.NoError(t, err)
	stored, err := svc.GetPlan(ctx, int64(plan.ID))
	require.NoError(t, err)
	require.Equal(t, []int64{groupID}, stored.GroupIds)
	require.Equal(t, rates, stored.GroupRateMultipliers)
	rows, err := client.QueryContext(ctx, "SELECT rate_multiplier FROM subscription_plan_groups WHERE plan_id=$1 AND group_id=$2", plan.ID, group.ID)
	require.NoError(t, err)
	defer rows.Close()
	require.True(t, rows.Next())
	var rate float64
	require.NoError(t, rows.Scan(&rate))
	require.Equal(t, .25, rate)
}

// Video Key 沿用三种资金模式，指定订阅仍需覆盖该视频分组，不能凭平台新增绕过套餐范围。
func TestVideoAdminReadinessAPIKeyFundingModes(t *testing.T) {
	for _, mode := range []string{APIKeyBillingModeAuto, APIKeyBillingModeBalance, APIKeyBillingModeSubscription} {
		t.Run(mode, func(t *testing.T) {
			const userID, groupID, preferredID int64 = 7, 11, 101
			group := &Group{ID: groupID, Platform: PlatformVideo, Status: StatusActive, IsExclusive: true}
			sub := activeBillingModeSubscription(preferredID, userID, groupID)
			svc := NewAPIKeyService(&apiKeyNameSanitizeRepoStub{}, &userRepoStub{user: &User{ID: userID, Status: StatusActive, Role: RoleUser, AllowedGroups: []int64{groupID}}},
				&compositeGroupRepoStub{groups: map[int64]*Group{groupID: group}}, &billingModeSubscriptionRepoStub{subscriptions: map[int64]*UserSubscription{preferredID: sub}}, nil, nil, nil)
			key := "sk_video_funding_mode_" + mode
			request := CreateAPIKeyRequest{Name: "video", CustomKey: &key, GroupID: &group.ID, BillingMode: mode}
			if mode == APIKeyBillingModeSubscription {
				request.PreferredSubscriptionID = &sub.ID
			}
			created, err := svc.Create(context.Background(), userID, request)
			require.NoError(t, err)
			require.Equal(t, mode, created.BillingMode)
			require.Equal(t, groupID, *created.GroupID)
			if mode == APIKeyBillingModeSubscription {
				require.Equal(t, preferredID, *created.PreferredSubscriptionID)
				sub.Plan.GroupIDs = []int64{99}
				_, err = svc.Create(context.Background(), userID, request)
				require.ErrorIs(t, err, ErrPreferredSubscriptionGroup)
			}
		})
	}
}
