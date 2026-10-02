package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

var (
	ErrVideoPricingUnavailable = errors.New("video pricing is unavailable")
	ErrVideoUsageUnavailable   = errors.New("trusted video usage is unavailable")
)

// VideoPriceTier 的价格与计费模式绑定，nil 为缺价，显式零价为免费。
type VideoPriceTier struct {
	Resolution string `json:"resolution"`
	// 仅兼容读取旧双条件价卡，新配置每个分辨率只保存一个单价。
	HasReferenceVideo bool     `json:"has_reference_video,omitempty"`
	Price             *float64 `json:"price"`
}

// VideoPriceInput 只接收适配器确认后的计费维度，不把厂商缺省值当成可信用量。
type VideoPriceInput struct {
	Group               *Group
	Model               string
	Resolution          string
	HasReferenceVideo   bool
	ReferenceImageCount *int
	DurationSeconds     float64
	Tokens              *int64
	RateMultiplier      float64
	Mode                BillingMode
}

// VideoPriceQuote 是可持久化的完整价格快照，后台结算不再读取可变价卡。
// @project-doc docs/domains/routing_and_billing.md#video_price_snapshot
type VideoPriceQuote struct {
	Version           int         `json:"version"`
	Mode              BillingMode `json:"mode"`
	Source            string      `json:"source"`
	Model             string      `json:"model"`
	Resolution        string      `json:"resolution"`
	HasReferenceVideo bool        `json:"has_reference_video"`
	Unit              string      `json:"unit"`
	BaseUnitPrice     float64     `json:"base_unit_price"`
	PriceMultiplier   float64     `json:"price_multiplier"`
	UnitPrice         float64     `json:"unit_price"`
	RateMultiplier    float64     `json:"rate_multiplier"`
	// 图片附加费冻结原始单价与输入数量，不参与视频价卡和资金来源倍率。
	ImageInputPricing   *VideoImageInputPricing `json:"image_input_pricing,omitempty"`
	ReferenceImageCount *int                    `json:"reference_image_count,omitempty"`
	// 预扣单价保持原始配置；最终费用仍按视频价格快照和可信用量计算。
	TokenPrepay  *VideoTokenPrepayConfig `json:"token_prepay,omitempty"`
	FallbackUsed bool                    `json:"fallback_used,omitempty"`
}

// NormalizeVideoPriceResolution 保留厂商原生 2K/4K 档位，不隐式换算成较便宜的数字档位。
func NormalizeVideoPriceResolution(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "2k" || value == "4k" {
		return value, nil
	}
	digits := strings.TrimSuffix(value, "p")
	n, err := strconv.Atoi(digits)
	if err != nil || n <= 0 || n > 65536 || strconv.Itoa(n) != digits {
		return "", fmt.Errorf("invalid video pricing resolution %q", value)
	}
	return strconv.Itoa(n) + "p", nil
}

func cloneVideoPriceTiers(prices []VideoPriceTier) []VideoPriceTier {
	if prices == nil {
		return nil
	}
	cloned := append([]VideoPriceTier{}, prices...)
	for i := range cloned {
		if cloned[i].Price != nil {
			price := *cloned[i].Price
			cloned[i].Price = &price
		}
	}
	return cloned
}

func hasVideoTierPrice(prices []VideoPriceTier) bool {
	for _, tier := range prices {
		if tier.Price != nil {
			return true
		}
	}
	return false
}

// 视频三种计费单位共用分辨率单价，按次使用独立模式避免混入聊天按次或历史秒价。
func isVideoMatrixBillingMode(mode BillingMode) bool {
	return mode == BillingModeVideo || mode == BillingModeVideoToken || mode == BillingModeVideoPerRequest
}

// validateVideoPriceTiers 防止条件重复、缺价和非有限数进入热路径。
func validateVideoPriceTiers(pricing ChannelModelPricing) error {
	if price := pricing.VideoFallbackPrice; price != nil {
		if pricing.Platform != PlatformVideo || !isVideoMatrixBillingMode(pricing.BillingMode) {
			return errors.New("video_fallback_price requires video platform and a video billing mode")
		}
		if !finiteNonNegative(*price) {
			return errors.New("video_fallback_price must be finite and non-negative")
		}
	}
	if p := pricing.VideoTokenPrepay; p != nil {
		if pricing.Platform != PlatformVideo || pricing.BillingMode != BillingModeVideoToken {
			return errors.New("video_token_prepay requires video platform and video_token mode")
		}
		if p.PricePerSecond == nil || !finiteNonNegative(*p.PricePerSecond) {
			return errors.New("video_token_prepay requires a finite non-negative price_per_second")
		}
	}
	if p := pricing.VideoImageInputPricing; p != nil {
		if pricing.Platform != PlatformVideo || !isVideoMatrixBillingMode(pricing.BillingMode) {
			return errors.New("video_image_input_pricing requires video platform and a video billing mode")
		}
		if p.FreeImages < 0 || p.Price == nil || !finiteNonNegative(*p.Price) {
			return errors.New("video image input pricing requires non-negative free_images and a finite non-negative price")
		}
	}
	if len(pricing.VideoPrices) == 0 {
		if (pricing.BillingMode == BillingModeVideoToken || pricing.BillingMode == BillingModeVideoPerRequest) && pricing.VideoFallbackPrice == nil {
			return errors.New("video_token and video_per_request require video_prices or video_fallback_price")
		}
		return nil
	}
	if !isVideoMatrixBillingMode(pricing.BillingMode) {
		return errors.New("video_prices requires a video billing mode")
	}
	seen := make(map[string]bool, len(pricing.VideoPrices))
	for _, tier := range pricing.VideoPrices {
		resolution, err := NormalizeVideoPriceResolution(tier.Resolution)
		if err != nil {
			return err
		}
		// 旧同分辨率的有/无参考两行仍可读取；新无条件行的重复配置继续拒绝。
		key := fmt.Sprintf("%s:%t", resolution, tier.HasReferenceVideo)
		if seen[key] {
			return fmt.Errorf("duplicate video price tier %s", key)
		}
		seen[key] = true
		if tier.Price == nil || !finiteNonNegative(*tier.Price) {
			return fmt.Errorf("video price %s must be present, finite and non-negative", key)
		}
	}
	return nil
}

func finiteNonNegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

// calculateVideoMatrixCost 让旧统一入口识别视频矩阵，但不借用聊天 Token 或缺省时长。
func calculateVideoMatrixCost(input CostInput, resolved *ResolvedPricing) (*CostBreakdown, error) {
	if resolved == nil || resolved.channelPricing == nil {
		return nil, ErrVideoPricingUnavailable
	}
	resolutionValue := input.VideoResolution
	if resolutionValue == "" && resolved.Mode == BillingModeVideo {
		resolutionValue = input.SizeTier
	}
	resolution := ""
	if strings.TrimSpace(resolutionValue) != "" || resolved.Mode != BillingModeVideoPerRequest {
		var err error
		resolution, err = NormalizeVideoPriceResolution(resolutionValue)
		if err != nil {
			return nil, ErrVideoPricingUnavailable
		}
	}
	quote, err := quoteVideoConfigured(VideoPriceInput{Group: input.Group, Model: input.Model,
		Resolution: resolution, HasReferenceVideo: input.HasReferenceVideo, ReferenceImageCount: input.VideoReferenceImageCount, RateMultiplier: input.RateMultiplier,
		Mode: resolved.Mode}, resolved.channelPricing, resolved.Source)
	if err != nil {
		return nil, err
	}
	return quote.Calculate(input.UsageUnits, input.VideoTokens)
}

// QuoteVideo 按分组价卡、分组专属秒价、渠道价卡解析；不同单位之间永不回退。
func (r *ModelPricingResolver) QuoteVideo(ctx context.Context, input VideoPriceInput) (*VideoPriceQuote, error) {
	resolution := ""
	if strings.TrimSpace(input.Resolution) != "" {
		var err error
		resolution, err = NormalizeVideoPriceResolution(input.Resolution)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrVideoPricingUnavailable, err)
		}
	}
	input.Resolution = resolution
	input.Model = strings.TrimSpace(input.Model)
	if input.Model == "" || !finiteNonNegative(input.RateMultiplier) || (input.Mode != "" && !isVideoMatrixBillingMode(input.Mode)) {
		return nil, ErrVideoPricingUnavailable
	}
	if groupPricing := matchGroupModelPricing(input.Group, input.Model); groupPricing != nil {
		return quoteVideoConfigured(input, groupPricing, PricingSourceGroup)
	}
	// 旧分组专属字段只表达按秒合同；显式 Token 或按次模式不能借这些字段获取价格。
	if (input.Mode == "" || input.Mode == BillingModeVideo) && input.Group != nil && resolution != "" {
		if price := explicitGroupVideoSecondPrice(input.Group, input.Model, resolution); price != nil {
			return newVideoPriceQuote(input, BillingModeVideo, PricingSourceGroup, *price, 1)
		}
	}
	if r != nil && input.Group != nil && input.Group.ID > 0 {
		if pricing := r.lookupChannelPricingNormalized(ctx, input.Group.ID, input.Model); pricing != nil {
			return quoteVideoConfigured(input, pricing, PricingSourceChannel)
		}
	}
	return nil, fmt.Errorf("%w: model=%s resolution=%s", ErrVideoPricingUnavailable, input.Model, resolution)
}

func explicitGroupVideoSecondPrice(group *Group, model, resolution string) *float64 {
	// 不调用 Grok 的默认档位函数，新增分辨率也不会误取已有档位的价格。
	if tiers, ok := group.VideoModelPrices[CanonicalGrokImagineVideoPriceFamily(model)]; ok {
		if price, found := tiers[resolution]; found {
			return &price
		}
	}
	switch resolution {
	case "480p":
		return group.VideoPrice480P
	case "720p":
		return group.VideoPrice720P
	case "1080p":
		return group.VideoPrice1080P
	}
	return nil
}

func quoteVideoConfigured(input VideoPriceInput, pricing *ChannelModelPricing, source string) (*VideoPriceQuote, error) {
	mode := pricing.BillingMode
	if !isVideoMatrixBillingMode(mode) || (input.Mode != "" && input.Mode != mode) {
		return nil, fmt.Errorf("%w: configured mode %q cannot price requested video mode %q", ErrVideoPricingUnavailable, mode, input.Mode)
	}
	if err := validateVideoPriceTiers(*pricing); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrVideoPricingUnavailable, err)
	}
	// 只有不区分分辨率的按次合同允许省略分辨率，不能借省略参数绕过已配置的高价档位。
	if input.Resolution == "" && (mode != BillingModeVideoPerRequest || len(pricing.VideoPrices) != 0 || pricing.VideoFallbackPrice == nil) {
		return nil, ErrVideoPricingUnavailable
	}
	priceMultiplier := 1.0
	if pricing.PriceMultiplier != nil {
		priceMultiplier = *pricing.PriceMultiplier
	}
	// 所有旧秒价和新矩阵都经同一构造器，防止附加费仅在部分报价分支生效。
	build := func(price float64) (*VideoPriceQuote, error) {
		quote, err := newVideoPriceQuote(input, mode, source, price, priceMultiplier)
		if err != nil {
			return nil, err
		}
		quote.TokenPrepay = pricing.VideoTokenPrepay.Clone()
		if pricing.VideoImageInputPricing != nil {
			if input.ReferenceImageCount == nil || *input.ReferenceImageCount < 0 {
				return nil, fmt.Errorf("%w: reference image count is unknown", ErrVideoUsageUnavailable)
			}
			count := *input.ReferenceImageCount
			quote.ReferenceImageCount = &count
			quote.ImageInputPricing = pricing.VideoImageInputPricing.Clone()
			if _, err := quote.FixedImageInputCost(); err != nil {
				return nil, err
			}
		}
		return quote, nil
	}
	var legacyPrice *float64
	for _, tier := range pricing.VideoPrices {
		resolution, _ := NormalizeVideoPriceResolution(tier.Resolution)
		if resolution != input.Resolution {
			continue
		}
		// 每个分辨率只取一个单价，与请求是否携带参考视频无关。
		// 旧双行优先原无参考行；只有含参考行的配置也保留其单价，显式零不能被覆盖。
		if !tier.HasReferenceVideo {
			return build(*tier.Price)
		}
		legacyPrice = tier.Price
	}
	if legacyPrice != nil {
		return build(*legacyPrice)
	}
	// 只有未配置该分辨率才使用兜底价，输入素材不会改变已匹配的档位。
	if pricing.VideoFallbackPrice != nil {
		quote, err := build(*pricing.VideoFallbackPrice)
		if err == nil {
			quote.FallbackUsed = true
		}
		return quote, err
	}
	// 分辨率价卡是完整合同，缺档不能穿透到历史平铺价格。
	if len(pricing.VideoPrices) > 0 || mode == BillingModeVideoToken || mode == BillingModeVideoPerRequest {
		return nil, ErrVideoPricingUnavailable
	}
	// 历史显式秒价仍有效，继续按分辨率和真实秒数计算。
	for _, tier := range pricing.Intervals {
		resolution, err := NormalizeVideoPriceResolution(tier.TierLabel)
		if err == nil && resolution == input.Resolution && tier.PerRequestPrice != nil {
			return build(*tier.PerRequestPrice)
		}
	}
	if pricing.PerRequestPrice != nil {
		return build(*pricing.PerRequestPrice)
	}
	return nil, ErrVideoPricingUnavailable
}

func newVideoPriceQuote(input VideoPriceInput, mode BillingMode, source string, price, multiplier float64) (*VideoPriceQuote, error) {
	rate := resolveVideoRateMultiplier(&APIKey{Group: input.Group}, input.RateMultiplier)
	if !finiteNonNegative(price) || !finiteNonNegative(multiplier) || !finiteNonNegative(rate) || !finiteNonNegative(price*multiplier) {
		return nil, ErrVideoPricingUnavailable
	}
	unit := "second"
	if mode == BillingModeVideoToken {
		unit = "million_tokens"
	} else if mode == BillingModeVideoPerRequest {
		unit = "request"
	}
	return &VideoPriceQuote{Version: 1, Mode: mode, Source: source, Model: input.Model, Resolution: input.Resolution,
		HasReferenceVideo: input.HasReferenceVideo, Unit: unit, BaseUnitPrice: price, PriceMultiplier: multiplier,
		UnitPrice: price * multiplier, RateMultiplier: rate}, nil
}

// FixedImageInputCost 只收取超出免费张数的部分；旧任务缺少该快照时保持不收费。
func (q *VideoPriceQuote) FixedImageInputCost() (float64, error) {
	if q == nil {
		return 0, ErrVideoPricingUnavailable
	}
	p := q.ImageInputPricing
	if p == nil {
		return 0, nil
	}
	if p.FreeImages < 0 || p.Price == nil || !finiteNonNegative(*p.Price) || q.ReferenceImageCount == nil || *q.ReferenceImageCount < 0 {
		return 0, ErrVideoPricingUnavailable
	}
	cost := float64(max(0, *q.ReferenceImageCount-p.FreeImages)) * *p.Price
	if !finiteNonNegative(cost) {
		return 0, ErrVideoPricingUnavailable
	}
	return cost, nil
}

// Calculate 只使用冻结快照；仅视频基础成本应用倍率，图片固定附加费始终按原价相加。
func (q *VideoPriceQuote) Calculate(durationSeconds float64, tokens *int64) (*CostBreakdown, error) {
	if q == nil || q.Version != 1 || !finiteNonNegative(q.UnitPrice) || !finiteNonNegative(q.RateMultiplier) {
		return nil, ErrVideoPricingUnavailable
	}
	var units float64
	switch q.Mode {
	case BillingModeVideoPerRequest:
		// 一条完成任务只计一次；厂商时长、输出 Token 和轮询次数不改变数量。
		units = 1
	case BillingModeVideoToken:
		if tokens == nil || *tokens < 0 {
			return nil, ErrVideoUsageUnavailable
		}
		units = float64(*tokens) / 1_000_000
	case BillingModeVideo:
		if !finiteNonNegative(durationSeconds) || durationSeconds <= 0 {
			return nil, ErrVideoUsageUnavailable
		}
		units = durationSeconds
	default:
		return nil, ErrVideoPricingUnavailable
	}
	imageCost, err := q.FixedImageInputCost()
	if err != nil {
		return nil, err
	}
	videoCost := units * q.UnitPrice
	total := videoCost + imageCost
	actual := videoCost*q.RateMultiplier + imageCost
	if !finiteNonNegative(total) || !finiteNonNegative(actual) {
		return nil, ErrVideoUsageUnavailable
	}
	return &CostBreakdown{OutputCost: videoCost, ImageInputCost: imageCost, TotalCost: total, ActualCost: actual, BillingMode: string(q.Mode)}, nil
}

// VideoDisplayPricing 每个分辨率只公开一个实际单价，不把缺失档位展示成免费。
// @project-doc docs/interfaces/model_catalog_and_marketplace.md#marketplace_video_pricing
func (r *ModelPricingResolver) VideoDisplayPricing(ctx context.Context, group *Group, model string) ModelDisplayPricing {
	if group == nil {
		return unknownDisplayPricing()
	}
	resolutions := map[string]bool{"480p": true, "720p": true, "1080p": true}
	config := matchGroupModelPricing(group, model)
	if config == nil && r != nil {
		config = r.lookupChannelPricingNormalized(ctx, group.ID, model)
	}
	if config != nil {
		for _, tier := range config.VideoPrices {
			if resolution, err := NormalizeVideoPriceResolution(tier.Resolution); err == nil {
				resolutions[resolution] = true
			}
		}
		// 沿用原视频按秒层级时，也展示显式配置的非默认分辨率。
		for _, tier := range config.Intervals {
			if resolution, err := NormalizeVideoPriceResolution(tier.TierLabel); err == nil {
				resolutions[resolution] = true
			}
		}
	}
	ordered := make([]string, 0, len(resolutions))
	for resolution := range resolutions {
		ordered = append(ordered, resolution)
	}
	sort.Slice(ordered, func(i, j int) bool {
		return len(ordered[i]) < len(ordered[j]) || len(ordered[i]) == len(ordered[j]) && ordered[i] < ordered[j]
	})
	display := ModelDisplayPricing{PriceStatus: "priced"}
	zeroImages := 0
	for _, resolution := range ordered {
		quote, err := r.QuoteVideo(ctx, VideoPriceInput{Group: group, Model: model, Resolution: resolution,
			ReferenceImageCount: &zeroImages, RateMultiplier: group.RateMultiplier})
		if err != nil || quote.FallbackUsed {
			continue
		}
		price := quote.UnitPrice * quote.RateMultiplier
		if !finiteNonNegative(price) {
			continue
		}
		if display.PricingMode == "" {
			display.PricingMode = string(quote.Mode)
		}
		display.VideoPrices = append(display.VideoPrices, ModelDisplayVideoPrice{Resolution: resolution,
			Price: price, Unit: quote.Unit,
			VideoImageInputPricing: quote.ImageInputPricing.Clone(), VideoTokenPrepay: quote.TokenPrepay.Clone()})
	}
	// 广场单独展示回退价，不把它伪装成任何已配置分辨率的矩阵行。
	// 回退价保留自己的单位，混合秒价和 Token 价时也不能隐藏有效回退合同。
	if config != nil && config.VideoFallbackPrice != nil && validateVideoPriceTiers(*config) == nil {
		multiplier := 1.0
		if config.PriceMultiplier != nil {
			multiplier = *config.PriceMultiplier
		}
		quote, err := newVideoPriceQuote(VideoPriceInput{Group: group, Model: model, RateMultiplier: group.RateMultiplier},
			config.BillingMode, "", *config.VideoFallbackPrice, multiplier)
		if err == nil {
			price := quote.UnitPrice * quote.RateMultiplier
			if finiteNonNegative(price) {
				display.VideoFallbackPricing = &ModelDisplayVideoPrice{Price: price, Unit: quote.Unit,
					VideoImageInputPricing: config.VideoImageInputPricing.Clone(), VideoTokenPrepay: config.VideoTokenPrepay.Clone()}
				if display.PricingMode == "" {
					display.PricingMode = string(quote.Mode)
				}
				// 旧客户端只能由模型级模式解释标量回退价，单位不一致时不下发该兼容字段。
				if display.PricingMode == string(quote.Mode) {
					display.VideoFallbackPrice = &price
				}
			}
		}
	}
	if len(display.VideoPrices) == 0 && display.VideoFallbackPricing == nil {
		return unknownDisplayPricing()
	}
	// 仅把所有可见档位（包含回退价）共同拥有的说明提升到旧模型级字段。
	conditions := append([]ModelDisplayVideoPrice{}, display.VideoPrices...)
	if display.VideoFallbackPricing != nil {
		conditions = append(conditions, *display.VideoFallbackPricing)
	}
	display.VideoImageInputPricing = conditions[0].VideoImageInputPricing.Clone()
	display.VideoTokenPrepay = conditions[0].VideoTokenPrepay.Clone()
	for _, condition := range conditions[1:] {
		if !reflect.DeepEqual(display.VideoImageInputPricing, condition.VideoImageInputPricing) {
			display.VideoImageInputPricing = nil
		}
		if !reflect.DeepEqual(display.VideoTokenPrepay, condition.VideoTokenPrepay) {
			display.VideoTokenPrepay = nil
		}
	}
	return display
}
