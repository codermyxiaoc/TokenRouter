<template>
  <div class="space-y-1.5" data-testid="video-usage-pricing-details">
    <div v-for="field in fields" :key="field.key" class="flex items-center justify-between gap-4" :data-testid="`video-usage-${field.key}`">
      <span class="text-gray-400">{{ t(`usage.${field.key}`) }}</span>
      <span class="font-medium" :class="field.price ? 'text-sky-300' : 'text-white'">{{ field.value }}</span>
    </div>
    <p v-if="!row.video_billing" class="text-[10px] text-gray-400">{{ t('usage.videoPricingNotRecorded') }}</p>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useBalanceDisplay } from '@/composables/useBalanceDisplay'
import type { UsageLog } from '@/types'

const props = defineProps<{ row: UsageLog }>()
const { t } = useI18n()
const { formatUsdAmount } = useBalanceDisplay()
const amount = (value: number) => formatUsdAmount(value, { fractionDigits: 8 })

// 使用快照中的精确时长和定价；旧记录只展示已有字段，不从当前价格或总额反推。
const fields = computed(() => {
  const b = props.row.video_billing
  const duration = b?.duration_seconds ?? props.row.video_duration_seconds
  const resolution = b?.resolution || props.row.video_resolution
  const values: { key: string; value: string; price?: boolean }[] = []
  if (resolution) values.push({ key: 'videoResolution', value: resolution })
  if (duration != null && duration > 0) values.push({ key: 'videoDuration', value: `${duration.toLocaleString()} ${t('usage.videoSecondUnit')}` })
  if (b) {
    // 视频单价不再按参考视频输入区分，历史输入标记不作为费用条件展示。
    const unitKey = b.unit === 'second' ? 'usage.videoPerSecond' : b.unit === 'million_tokens' ? 'usage.perMillionTokens' : 'usage.videoPerRequest'
    values.push({ key: b.unit === 'second' ? 'videoSecondPrice' : b.unit === 'million_tokens' ? 'videoTokenPrice' : 'videoRequestPrice',
      value: `${amount(b.unit_price)} ${t(unitKey)}`, price: true })
    // 独立按次任务的快照只在已结算时下发，一条任务恰好计费一次。
    if (b.mode === 'video_per_request' && b.unit === 'request') values.push({ key: 'videoBillingRequests', value: '1' })
    if (b.unit === 'million_tokens' && b.tokens != null) values.push({ key: 'videoBillingTokens', value: b.tokens.toLocaleString() })
    // 已知零张、免费张数和零单价都有含义；不以真假值判断是否展示。
    for (const [key, value] of [
      ['videoReferenceImages', b.reference_image_count],
      ['videoReferenceImageFreeCount', b.reference_image_free_count],
      ['videoReferenceImageBilledCount', b.billable_reference_image_count],
    ] as const) {
      if (value != null) values.push({ key, value: `${value.toLocaleString()}${t('usage.imageUnit')}` })
    }
    if (b.reference_image_unit_price != null) values.push({ key: 'videoReferenceImagePrice', value: `${amount(b.reference_image_unit_price)} ${t('usage.videoPerReferenceImage')}`, price: true })
    if (b.reference_image_cost != null) values.push({ key: 'videoReferenceImageCost', value: amount(b.reference_image_cost) })
  }
  return values
})
</script>
