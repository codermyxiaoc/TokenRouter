//go:build unit

package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 详情与收费合同分离，并在复制、分组JSON存储后保留每个模型的独立开关。
func TestModelDetailsValidationCloneAndCompatibility(t *testing.T) {
	card := ChannelModelPricing{Platform: PlatformVideo, Models: []string{"a", "b"}, ModelDetails: map[string]ModelDetails{
		"a": {Enabled: true, Description: strings.Repeat("中", 2000)}, "b": {Description: "未公开草稿"},
	}}
	require.NoError(t, validatePricingEntries([]ChannelModelPricing{card}))
	require.False(t, card.HasEffectivePricing())
	require.False(t, hasExplicitPricingPrice(card))
	clone := card.Clone()
	clone.ModelDetails["a"] = ModelDetails{Description: "changed"}
	require.True(t, card.ModelDetails["a"].Enabled)
	encoded, err := json.Marshal(card)
	require.NoError(t, err)
	var decoded ChannelModelPricing
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.Equal(t, card.ModelDetails, decoded.ModelDetails)
	var legacy ChannelModelPricing
	require.NoError(t, json.Unmarshal([]byte(`{"models":["old"]}`), &legacy))
	require.Nil(t, legacy.ModelDetails)
	require.Error(t, validateAccountStatsPricingEntries([]ChannelModelPricing{card}))

	for _, tc := range []struct {
		name   string
		change func(*ChannelModelPricing)
	}{
		{"platform", func(p *ChannelModelPricing) { p.Platform = PlatformOpenAI }},
		{"foreign-model", func(p *ChannelModelPricing) { p.ModelDetails["other"] = ModelDetails{} }},
		{"duplicate-key", func(p *ChannelModelPricing) { p.ModelDetails[" A "] = ModelDetails{} }},
		{"duplicate-model", func(p *ChannelModelPricing) { p.Models = append(p.Models, " A ") }},
		{"empty-model", func(p *ChannelModelPricing) { p.Models = append(p.Models, " ") }},
		{"too-long", func(p *ChannelModelPricing) {
			p.ModelDetails["a"] = ModelDetails{Description: strings.Repeat("中", 2001)}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := card.Clone()
			tc.change(&bad)
			require.Error(t, validatePricingEntries([]ChannelModelPricing{bad}))
		})
	}
	require.NoError(t, validatePricingEntries([]ChannelModelPricing{{Platform: PlatformVideo, Models: []string{"a"}, ModelDetails: map[string]ModelDetails{"a": {Enabled: true, Description: "  "}}}}))
	normalized, err := normalizeGroupModelPricing(PlatformVideo, []ChannelModelPricing{card})
	require.NoError(t, err)
	require.Equal(t, card.ModelDetails, normalized[0].ModelDetails)
	_, err = normalizeGroupModelPricing(PlatformOpenAI, []ChannelModelPricing{card})
	require.Error(t, err)
}

// 公开说明按分组覆盖渠道，绑定多个模型时不会复用同一行其他模型的说明。
func TestMarketplaceModelDescriptionPrecedenceAndIsolation(t *testing.T) {
	detailCard := func(platform, model, text string, enabled bool) ChannelModelPricing {
		return ChannelModelPricing{Platform: platform, Models: []string{model}, ModelDetails: map[string]ModelDetails{model: {Enabled: enabled, Description: text}}}
	}
	channel := Channel{ID: 7, Status: StatusActive, GroupIDs: []int64{11}, ModelPricing: []ChannelModelPricing{
		detailCard(PlatformVideo, "a", "渠道A", true), detailCard(PlatformVideo, "b", "渠道B", true),
		detailCard(PlatformVideo, "seed*", "Seedance通配", true), detailCard(PlatformOpenAI, "text", "不能串平台", true),
	}, ModelMapping: map[string]map[string]string{PlatformVideo: {"public": "a"}}}
	channels := &ChannelService{}
	channels.cache.Store(populateChannelCache([]Channel{channel}, map[int64]string{11: PlatformVideo, 12: PlatformVideo}))
	svc := &ModelMarketplaceService{gatewayService: &GatewayService{channelService: channels}}
	group := &Group{ID: 11, Platform: PlatformVideo, ModelPricing: []ChannelModelPricing{{Platform: PlatformVideo, Models: []string{"a", "b"}, ModelDetails: map[string]ModelDetails{"a": {Enabled: true, Description: " 分组A "}}}}}
	ctx := context.Background()
	require.Equal(t, "分组A", svc.marketplaceModelDescription(ctx, group, marketplaceModelDef{ID: "a"}))
	require.Equal(t, "渠道B", svc.marketplaceModelDescription(ctx, group, marketplaceModelDef{ID: "b"}))
	require.Equal(t, "Seedance通配", svc.marketplaceModelDescription(ctx, group, marketplaceModelDef{ID: "seedance-2.5"}))
	require.Equal(t, "分组A", svc.marketplaceModelDescription(ctx, group, marketplaceModelDef{ID: "public", PricingModel: "a"}))
	require.Equal(t, "分组A", svc.marketplaceModelDescription(ctx, group, marketplaceModelDef{ID: "public"}))
	require.Empty(t, svc.marketplaceModelDescription(ctx, group, marketplaceModelDef{ID: "public", PricingModel: "a", PricingAmbiguous: true}))
	require.Empty(t, svc.marketplaceModelDescription(ctx, group, marketplaceModelDef{ID: "text"}))
	require.Empty(t, svc.marketplaceModelDescription(ctx, &Group{ID: 12, Platform: PlatformVideo}, marketplaceModelDef{ID: "a"}))
	require.Empty(t, svc.marketplaceModelDescription(ctx, &Group{ID: 11, Platform: PlatformOpenAI}, marketplaceModelDef{ID: "a"}))
	for _, detail := range []ModelDetails{{Description: "关闭草稿"}, {Enabled: true, Description: "  "}} {
		group.ModelPricing[0].ModelDetails["a"] = detail
		require.Empty(t, svc.marketplaceModelDescription(ctx, group, marketplaceModelDef{ID: "a"}))
	}
	delete(group.ModelPricing[0].ModelDetails, "a")
	require.Equal(t, "渠道A", svc.marketplaceModelDescription(ctx, group, marketplaceModelDef{ID: "a"}))
	models := svc.buildPublicModelsForGroup(ctx, group, []marketplaceModelDef{{ID: "a"}, {ID: "b"}})
	require.Equal(t, "渠道A", models[0].ModelDescription)
	require.Equal(t, "unpriced", models[0].Pricing.PriceStatus)
}

// 精确匹配优先于通配，且说明的多模型映射只跟随命中的模型键。
func TestModelDetailsMatchingAndSafeProjection(t *testing.T) {
	entries := []ChannelModelPricing{
		{Platform: PlatformVideo, Models: []string{"seed*"}, ModelDetails: map[string]ModelDetails{"seed*": {Enabled: true, Description: "wild"}}},
		{Platform: PlatformVideo, Models: []string{"seed", "b"}, ModelDetails: map[string]ModelDetails{"SEED": {Enabled: true, Description: "exact"}}},
	}
	d, ok := matchModelDetails(entries, " SEED ")
	require.True(t, ok)
	require.Equal(t, "exact", d.Description)
	_, ok = matchModelDetails(entries, "b")
	require.False(t, ok)
	require.Empty(t, publicModelDescription(ModelDetails{Enabled: true, Description: strings.Repeat("x", 2001)}))
	require.Equal(t, "<script>alert(1)</script>", publicModelDescription(ModelDetails{Enabled: true, Description: "<script>alert(1)</script>"})) // 纯文本交给前端转义，不作为HTML渲染。
}

// 纯说明不能遮住渠道或原通配价；显式零价及带收费配置的缺价仍遵循完整合同规则。
func TestModelDetailsOnlyPreservesVideoBilling(t *testing.T) {
	ctx := context.Background()
	price := .3
	channelCard := &ChannelModelPricing{Platform: PlatformVideo, Models: []string{"video-model"}, BillingMode: BillingModeVideo, VideoFallbackPrice: &price}
	group := &Group{ID: 12, Platform: PlatformVideo, RateMultiplier: 1}
	resolver := videoPricingTestResolver(group, channelCard)
	input := VideoPriceInput{Group: group, Model: "video-model", Resolution: "720p", RateMultiplier: 1}
	before, err := resolver.QuoteVideo(ctx, input)
	require.NoError(t, err)
	for _, mode := range []BillingMode{BillingModeVideo, BillingModeVideoToken, BillingModeVideoPerRequest} {
		t.Run(string(mode), func(t *testing.T) {
			details := ChannelModelPricing{Platform: PlatformVideo, Models: []string{"video-model"}, BillingMode: mode,
				ModelDetails: map[string]ModelDetails{"video-model": {Enabled: true, Description: "说明"}}}
			require.NoError(t, validatePricingEntries([]ChannelModelPricing{details}))
			require.True(t, details.IsModelDetailsOnly())
			group.ModelPricing = []ChannelModelPricing{details}
			after, err := resolver.QuoteVideo(ctx, input)
			require.NoError(t, err)
			require.Equal(t, before, after)
			resolved := resolver.Resolve(ctx, PricingInput{Group: group, GroupID: &group.ID, Model: "video-model"})
			require.Equal(t, PricingSourceChannel, resolved.Source)
			require.Equal(t, BillingModeVideo, resolved.Mode)
			// 历史重叠配置也先排除说明，不应跳过实际通配定价。
			wildcard := *channelCard
			wildcard.Models = []string{"video-*"}
			group.ModelPricing = []ChannelModelPricing{details, wildcard}
			after, err = resolver.QuoteVideo(ctx, input)
			require.NoError(t, err)
			require.Equal(t, PricingSourceGroup, after.Source)
			require.Equal(t, price, after.UnitPrice)
			details.ModelDetails = nil
			require.Error(t, validatePricingEntries([]ChannelModelPricing{details}))
		})
	}
	details := ChannelModelPricing{Platform: PlatformVideo, Models: []string{"video-model"}, BillingMode: BillingModeVideo,
		ModelDetails: map[string]ModelDetails{"video-model": {Enabled: true, Description: "说明"}}}
	zero := 0.0
	details.VideoFallbackPrice = &zero
	require.False(t, details.IsModelDetailsOnly())
	group.ModelPricing = []ChannelModelPricing{details}
	quote, err := resolver.QuoteVideo(ctx, input)
	require.NoError(t, err)
	require.Zero(t, quote.UnitPrice)
	require.Equal(t, PricingSourceGroup, quote.Source)
	details.VideoFallbackPrice = nil
	details.PriceMultiplier = &price
	require.False(t, details.IsModelDetailsOnly())
	require.Error(t, validatePricingEntries([]ChannelModelPricing{details}))
	group.ModelPricing = nil
	require.Error(t, func() error { _, err := videoPricingTestResolver(group, nil).QuoteVideo(ctx, input); return err }())
}

// 输入键允许已有大小写和空白归一化，但持久化后必须与models实际键完全一致。
func TestModelDetailsCanonicalKey(t *testing.T) {
	cards := []ChannelModelPricing{{Platform: PlatformVideo, Models: []string{"Seedance"}, ModelDetails: map[string]ModelDetails{" SEEDANCE ": {Enabled: true, Description: "说明"}}}}
	require.NoError(t, validatePricingEntries(cards))
	require.Contains(t, cards[0].ModelDetails, "Seedance")
	require.Len(t, cards[0].ModelDetails, 1)
}

// 纯说明不能扩大渠道模型目录或限制白名单，原未定价条目的历史准入语义继续保留。
func TestModelDetailsOnlyDoesNotGrantModelAccess(t *testing.T) {
	ctx := context.Background()
	old := ChannelModelPricing{Platform: PlatformVideo, Models: []string{"legacy"}}
	channel := Channel{ID: 7, Status: StatusActive, GroupIDs: []int64{11}, RestrictModels: true, ModelPricing: []ChannelModelPricing{old}}
	before := mergeRequestableModelCandidates([]string{"existing"}, nil, &channel, PlatformVideo)
	channel.ModelPricing = append(channel.ModelPricing,
		ChannelModelPricing{Platform: PlatformVideo, Models: []string{"secret", "wild*"}, BillingMode: BillingModeVideo, ModelDetails: map[string]ModelDetails{
			"secret": {Enabled: true, Description: "说明"}, "wild*": {Enabled: true, Description: "说明"},
		}})
	require.Equal(t, before, mergeRequestableModelCandidates([]string{"existing"}, nil, &channel, PlatformVideo))
	channels := &ChannelService{}
	channels.cache.Store(populateChannelCache([]Channel{channel}, map[int64]string{11: PlatformVideo}))
	require.True(t, channels.IsModelRestricted(ctx, 11, "secret"))
	require.True(t, channels.IsModelRestricted(ctx, 11, "wild-new-model"))
	require.False(t, channels.IsModelRestricted(ctx, 11, "legacy"))
	require.Nil(t, channels.GetChannelModelPricing(ctx, 11, "secret"))
	stored, err := channels.GetChannelForGroup(ctx, 11)
	require.NoError(t, err)
	require.Equal(t, "说明", stored.ModelPricing[1].ModelDetails["secret"].Description)
}
