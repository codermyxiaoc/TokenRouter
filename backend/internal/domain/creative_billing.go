package domain

// CreativeBillingModeToken 表示创作台图片按上游 token 用量结算，与 API 渠道定价为 token 时一致。
// 空值表示按张（历史任务与按张/按次定价的模型）。
const CreativeBillingModeToken = "token"

// 待核对原因：token 模式下扣费按预扣上限或缺少用量兜底，需要管理员复核。
const (
	CreativeBillingReviewTokenCostExceedsHold = "token_cost_exceeds_hold"
	CreativeBillingReviewTokenUsageMissing    = "token_usage_missing"
)

// CreativeProviderUsage 是创作台按 token 结算任务的上游用量与已计算费用。
// 执行成功时一次算定并持久化，结算与崩溃恢复都读取这里的金额，不随之后的价格变化重算，
// 保证同一请求 ID 的重复捕获金额一致。金额均为未乘倍率的基础金额（美元）。
type CreativeProviderUsage struct {
	// Reported 表示上游返回了可用的 token 用量；为 false 时按预扣上限兜底并标记待核对。
	Reported             bool `json:"reported"`
	InputTokens          int  `json:"input_tokens,omitempty"`
	ImageInputTokens     int  `json:"image_input_tokens,omitempty"`
	ImageCacheReadTokens int  `json:"image_cache_read_tokens,omitempty"`
	OutputTokens         int  `json:"output_tokens,omitempty"`
	CacheCreationTokens  int  `json:"cache_creation_tokens,omitempty"`
	CacheReadTokens      int  `json:"cache_read_tokens,omitempty"`
	ImageOutputTokens    int  `json:"image_output_tokens,omitempty"`
	// TokenBaseAmountUSD 是按 API 同一公式算出的 token 费用；ChargeBaseAmountUSD 是实际扣费基础金额
	// （不超过预扣上限，缺少用量时等于预扣上限）。
	TokenBaseAmountUSD  float64 `json:"token_base_amount_usd"`
	ChargeBaseAmountUSD float64 `json:"charge_base_amount_usd"`
	InputCost           float64 `json:"input_cost,omitempty"`
	ImageInputCost      float64 `json:"image_input_cost,omitempty"`
	OutputCost          float64 `json:"output_cost,omitempty"`
	ImageOutputCost     float64 `json:"image_output_cost,omitempty"`
	CacheCreationCost   float64 `json:"cache_creation_cost,omitempty"`
	CacheReadCost       float64 `json:"cache_read_cost,omitempty"`
	// ReviewReason 非空表示扣费需要人工核对（超出预扣或缺少用量）。
	ReviewReason string `json:"review_reason,omitempty"`
}
