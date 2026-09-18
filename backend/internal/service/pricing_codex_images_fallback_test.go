package service

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// 图片日期快照必须先查同产品目录价，避免绕过管理员已有价格或显式零价。
func TestCodexImages25PricingPrefersSameProductCatalog(t *testing.T) {
	for _, model := range []string{"gpt-image-2.5-flare", "gpt-image-2.5-sunburst"} {
		for _, inputPrice := range []float64{0, 0.123} {
			t.Run(model+"_"+strconv.FormatFloat(inputPrice, 'g', -1, 64), func(t *testing.T) {
				basePrice := &LiteLLMModelPricing{InputCostPerToken: inputPrice}
				svc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{model: basePrice}}
				require.Same(t, basePrice, svc.GetModelPricing(model))
				require.Same(t, basePrice, svc.GetModelPricing(model+"-2026-09-08"))
			})
		}
		t.Run(model+"_exact_snapshot", func(t *testing.T) {
			basePrice := &LiteLLMModelPricing{InputCostPerToken: 0.123}
			exactPrice := &LiteLLMModelPricing{InputCostPerToken: 0.456}
			svc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
				model: basePrice, model + "-2026-09-08": exactPrice,
			}}
			require.Same(t, exactPrice, svc.GetModelPricing(model+"-2026-09-08"))
		})
	}
}

// 同产品目录确实缺失时继续采用新图片模型自己的兜底，不借用旧图片模型价格。
func TestCodexImages25PricingUsesStaticOnlyWhenCatalogMissing(t *testing.T) {
	svc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"gpt-image-2": {InputCostPerToken: 0.123},
	}}
	for _, model := range []string{"gpt-image-2.5-flare", "gpt-image-2.5-sunburst"} {
		for _, suffix := range []string{"", "-2026-09-08"} {
			t.Run(model+suffix, func(t *testing.T) {
				require.Same(t, openAIGPTImage25FallbackPricing, svc.GetModelPricing(model+suffix))
			})
		}
	}
}
