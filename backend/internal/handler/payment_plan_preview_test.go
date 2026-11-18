package handler

import (
	"encoding/json"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 预览卡片只能展示当前目录与自定义列表都允许的型号，且保留零倍率与本地化名称。
func TestPlanPreviewGroupProjection(t *testing.T) {
	group := &service.Group{ID: 8, Name: "分组", Platform: service.PlatformOpenAI, DisplayBrand: "brand",
		I18n:             service.GroupI18n{"en": {Name: "Group"}},
		ModelsListConfig: service.GroupModelsListConfig{Enabled: true, Models: []string{"b", "not-requestable", "a"}},
	}
	projected := newPlanPreviewGroup(group, []string{"a", "b", "not-listed"}, "en")
	raw, err := json.Marshal(projected)
	require.NoError(t, err)
	require.JSONEq(t, `{"id":8,"name":"Group","display_brand":"brand","platform":"openai","rate_multiplier":0,"models":["b","a"]}`, string(raw))
	projected = newPlanPreviewGroup(group, nil, "zh")
	require.Equal(t, "分组", projected.Name)
	require.Equal(t, []string{}, projected.Models)
}
