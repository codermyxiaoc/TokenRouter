package dto

import (
	"encoding/json"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/domain"
	"github.com/stretchr/testify/require"
)

func localizableGroup() *Group {
	return &Group{
		ID: 1, Name: "标准", Description: "号池",
		I18n: domain.GroupI18n{"en": {Name: "Standard", Description: "Pro + Max pool"}},
	}
}

func TestGroupLocalized_ReplacesTextAndDropsTranslations(t *testing.T) {
	g := localizableGroup().Localized("en")

	require.Equal(t, "Standard", g.Name)
	require.Equal(t, "Pro + Max pool", g.Description)

	raw, err := json.Marshal(g)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.NotContains(t, decoded, "i18n", "user-facing output must not carry the translation table")
}

func TestGroupLocalized_KeepsOriginalWhenLanguageHasNoTranslation(t *testing.T) {
	g := localizableGroup().Localized("zh")

	require.Equal(t, "标准", g.Name)
	require.Equal(t, "号池", g.Description)
	require.Nil(t, g.I18n)
}

func TestLocalized_NilSafe(t *testing.T) {
	require.Nil(t, (*Group)(nil).Localized("en"))
	require.Nil(t, (*APIKey)(nil).Localized("en"))
	require.Nil(t, (*UsageLog)(nil).Localized("en"))
	require.Nil(t, (*UserSubscription)(nil).Localized("en"))

	// 未携带分组的记录原样返回
	require.Nil(t, (&APIKey{}).Localized("en").Group)
}

func TestLocalized_NestedGroups(t *testing.T) {
	key := (&APIKey{Group: localizableGroup()}).Localized("en")
	require.Equal(t, "Standard", key.Group.Name)

	sub := (&UserSubscription{Plan: &SubscriptionPlan{ApplicableGroups: []SubscriptionPlanGroup{{Name: "标准", I18n: domain.GroupI18n{"en": {Name: "Standard"}}}}}}).Localized("en")
	require.Equal(t, "Standard", sub.Plan.ApplicableGroups[0].Name)

	// 使用记录除自身分组外，还带着 API Key 与订阅各自的分组
	log := (&UsageLog{
		Group:        localizableGroup(),
		APIKey:       &APIKey{Group: localizableGroup()},
		Subscription: &UserSubscription{Plan: &SubscriptionPlan{ApplicableGroups: []SubscriptionPlanGroup{{Name: "标准", I18n: domain.GroupI18n{"en": {Name: "Standard"}}}}}},
	}).Localized("en")
	require.Equal(t, "Standard", log.Group.Name)
	require.Equal(t, "Standard", log.APIKey.Group.Name)
	require.Equal(t, "Standard", log.Subscription.Plan.ApplicableGroups[0].Name)
	require.Nil(t, log.APIKey.Group.I18n)
	require.Nil(t, log.Subscription.Plan.ApplicableGroups[0].I18n)
}

// 多路由与多分组套餐翻译后仍保留身份、前缀与独立倍率。
func TestLocalized_ForkRoutingAndSubscriptionGroups(t *testing.T) {
	rate := 0.0
	key := (&APIKey{Group: localizableGroup(), SmartRoutingGroups: []*Group{localizableGroup()}, CompositeGroups: []APIKeyCompositeGroup{{Prefix: "seed", Group: localizableGroup()}}}).Localized("en-US")
	require.Equal(t, "Standard", key.SmartRoutingGroups[0].Name)
	require.Equal(t, "Standard", key.CompositeGroups[0].Group.Name)
	require.Equal(t, "seed", key.CompositeGroups[0].Prefix)
	require.Equal(t, int64(1), key.CompositeGroups[0].Group.ID)
	sub := (&UserSubscription{Plan: &SubscriptionPlan{ApplicableGroups: []SubscriptionPlanGroup{{ID: 7, Name: "标准", I18n: domain.GroupI18n{"en": {Name: "Standard"}}, RateMultiplier: &rate}}}}).Localized("en")
	require.Equal(t, "Standard", sub.Plan.ApplicableGroups[0].Name)
	require.Equal(t, int64(7), sub.Plan.ApplicableGroups[0].ID)
	require.Zero(t, *sub.Plan.ApplicableGroups[0].RateMultiplier)
}
