import type { ModelDisplayDetail } from '@/api/admin/channels'
import type { PricingFormEntry } from './types'

export const MODEL_DETAIL_MAX_LENGTH = 2000

// 创建独立副本并移除已删除模型的说明；不自动创建关闭项，以保留分组继承语义。
export function copyModelDetails(
  models: string[],
  details?: Record<string, ModelDisplayDetail> | null,
): Record<string, ModelDisplayDetail> | undefined {
  const entries = models.flatMap(model => {
    if (!details || !Object.prototype.hasOwnProperty.call(details, model)) return []
    const value = details[model]
    return [[model, { enabled: value.enabled, description: value.description }]] as [string, ModelDisplayDetail][]
  })
  return entries.length > 0 ? Object.fromEntries(entries) : undefined
}

// 仅说明条目不参与计费；任一显式价格、倍率或收费配置均保留原价卡语义。
export function isModelDetailsOnly(entry: PricingFormEntry): boolean {
  const configured = (value: number | string | null | undefined) => value != null && value !== ''
  return copyModelDetails(entry.models, entry.model_details) !== undefined &&
    ['token', 'video', 'video_token', 'video_per_request'].includes(entry.billing_mode) &&
    ![
      entry.price_multiplier, entry.fast_mode_multiplier, entry.fast_multiplier, entry.flex_multiplier,
      entry.max_reasoning_effort_multiplier, entry.input_price, entry.output_price,
      entry.cache_write_price, entry.cache_write_1h_price, entry.cache_read_price,
      entry.image_input_price, entry.image_output_price, entry.per_request_price, entry.video_fallback_price,
      ...Object.values(entry.reasoning_effort_multipliers || {}),
    ].some(configured) &&
    !entry.intervals?.length && !entry.video_prices?.length && !entry.time_pricing?.periods.length &&
    entry.video_token_prepay == null && entry.video_image_input_pricing == null
}
