package service

import "math"

// UsageVideoBillingDetails 仅包含历史计价事实，禁止附带供应商响应、素材地址或账号凭据。
type UsageVideoBillingDetails struct {
	Mode                        string   `json:"mode"`
	Unit                        string   `json:"unit"`
	UnitPrice                   float64  `json:"unit_price"`
	Resolution                  string   `json:"resolution"`
	HasReferenceVideo           bool     `json:"has_reference_video"`
	DurationSeconds             float64  `json:"duration_seconds"`
	Tokens                      *int64   `json:"tokens,omitempty"`
	ReferenceImageCount         *int     `json:"reference_image_count,omitempty"`
	ReferenceImageFreeCount     *int     `json:"reference_image_free_count,omitempty"`
	BillableReferenceImageCount *int     `json:"billable_reference_image_count,omitempty"`
	ReferenceImageUnitPrice     *float64 `json:"reference_image_unit_price,omitempty"`
	ReferenceImageCost          *float64 `json:"reference_image_cost,omitempty"`
}

// Clone 隔离返回 DTO 的指针字段，展示层不能意外修改已加载的历史快照。
func (details *UsageVideoBillingDetails) Clone() *UsageVideoBillingDetails {
	if details == nil {
		return nil
	}
	result := *details
	result.Tokens = cloneUsageVideoValue(details.Tokens)
	result.ReferenceImageCount = cloneUsageVideoValue(details.ReferenceImageCount)
	result.ReferenceImageFreeCount = cloneUsageVideoValue(details.ReferenceImageFreeCount)
	result.BillableReferenceImageCount = cloneUsageVideoValue(details.BillableReferenceImageCount)
	result.ReferenceImageUnitPrice = cloneUsageVideoValue(details.ReferenceImageUnitPrice)
	result.ReferenceImageCost = cloneUsageVideoValue(details.ReferenceImageCost)
	return &result
}

func cloneUsageVideoValue[T any](value *T) *T {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

// BuildUsageVideoBillingDetails 从已结算日志与任务快照生成只读明细，不使用当前模型价卡反推历史。
// @project-doc docs/domains/routing_and_billing.md#usage_video_pricing
func BuildUsageVideoBillingDetails(log *UsageLog, quote *VideoPriceQuote, metadata VideoRequestMetadata) *UsageVideoBillingDetails {
	if log == nil || quote == nil || !validUsageVideoAmount(quote.UnitPrice) {
		return nil
	}
	if (quote.Mode != BillingModeVideo || quote.Unit != "second") && (quote.Mode != BillingModeVideoToken || quote.Unit != "million_tokens") && (quote.Mode != BillingModeVideoPerRequest || quote.Unit != "request") {
		return nil
	}
	duration := metadata.DurationSeconds
	if !validUsageVideoAmount(duration) {
		if quote.Mode != BillingModeVideoPerRequest {
			return nil
		}
		// 按次无需上游时长，自动时长占位不能使单次价格及固定图片费详情消失。
		duration = 0
	}
	resolution := quote.Resolution
	if resolution == "" && quote.Mode == BillingModeVideoPerRequest {
		resolution = metadata.Resolution
	}
	details := &UsageVideoBillingDetails{
		Mode: string(quote.Mode), Unit: quote.Unit, UnitPrice: quote.UnitPrice,
		Resolution: resolution, HasReferenceVideo: quote.HasReferenceVideo,
		DurationSeconds: duration,
	}
	if metadata.Tokens != nil && *metadata.Tokens >= 0 {
		value := *metadata.Tokens
		details.Tokens = &value
	}
	// 未配置附加费也展示已确认的输入图片数量；历史未知计数保持缺失。
	count := metadata.ReferenceImageCount
	if quote.ReferenceImageCount != nil {
		count = quote.ReferenceImageCount
	}
	if count != nil && *count >= 0 {
		value := *count
		details.ReferenceImageCount = &value
	}
	pricing := quote.ImageInputPricing
	if pricing != nil && pricing.FreeImages >= 0 && pricing.Price != nil && validUsageVideoAmount(*pricing.Price) &&
		quote.ReferenceImageCount != nil && *quote.ReferenceImageCount >= 0 && validUsageVideoAmount(log.ImageInputCost) {
		// 固定图片费必须拥有完整创建快照，不能用当前设置或轮询返回的图片数量补算。
		free, billable := pricing.FreeImages, max(0, *quote.ReferenceImageCount-pricing.FreeImages)
		price, amount := *pricing.Price, log.ImageInputCost
		details.ReferenceImageFreeCount = &free
		details.BillableReferenceImageCount = &billable
		details.ReferenceImageUnitPrice = &price
		details.ReferenceImageCost = &amount
	}
	return details
}

func validUsageVideoAmount(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
