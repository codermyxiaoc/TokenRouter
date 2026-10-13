package handler

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/handler/dto"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

func TestLocalizedMarketplaceGroups(t *testing.T) {
	visible := []service.ModelMarketplaceGroup{
		{
			ID: 1, Name: "标准", Description: "号池", Platform: "anthropic",
			I18n: service.GroupI18n{"en": {Name: "Standard", Description: "Pro + Max pool"}},
		},
		{ID: 2, Name: "无译文", Description: "原描述", Platform: "openai"},
	}

	english := dto.ModelMarketplaceGroupsFromService(visible, "en")
	require.Equal(t, "Standard", english[0].Name)
	require.Equal(t, "Pro + Max pool", english[0].Description)
	require.Equal(t, "无译文", english[1].Name)
	require.Equal(t, "原描述", english[1].Description)

	chinese := dto.ModelMarketplaceGroupsFromService(visible, "zh")
	require.Equal(t, "标准", chinese[0].Name)
	require.Equal(t, "号池", chinese[0].Description)

	// service 层对象不被改写，同一批分组可继续按别的语言输出
	require.Equal(t, "标准", visible[0].Name)

	raw, err := json.Marshal(english[0])
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.NotContains(t, decoded, "i18n")
}

func TestMarketplaceLanguageVaryPreservesOrigin(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Writer.Header().Set("Vary", "Origin")
	varyMarketplaceLanguage(c)
	varyMarketplaceLanguage(c)
	require.Equal(t, []string{"Origin", "Accept-Language"}, c.Writer.Header().Values("Vary"))
}
