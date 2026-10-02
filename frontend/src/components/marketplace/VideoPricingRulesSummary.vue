<template>
  <div v-for="group in groups" :key="group.key" data-testid="video-pricing-rule-group">
    <p v-if="scoped" class="mt-3 text-xs font-medium leading-5 text-gray-600 dark:text-gray-300" data-testid="video-pricing-rule-scope">
      {{ group.labels.join(' / ') }} · {{ videoPricingUnit(group.unit, t) }}
    </p>
    <VideoImageInputPricingSummary :pricing="group.pricing" />
    <VideoTokenPrepaySummary :pricing="group.pricing" />
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { MarketplaceModelPricing, MarketplaceVideoPrice } from '@/types'
import VideoImageInputPricingSummary from './VideoImageInputPricingSummary.vue'
import VideoTokenPrepaySummary from './VideoTokenPrepaySummary.vue'
import { videoPricingEntries, videoPricingUnit } from './marketplaceVideoPricing'

const props = defineProps<{ pricing: MarketplaceModelPricing }>()
const { t } = useI18n()
const entries = computed(() => videoPricingEntries(props.pricing, t))
const scoped = computed(() => entries.value.some((entry) => entry.scoped))

// 只合并单位和附加规则完全一致的档位，避免重复说明，也避免不同档位相互借用规则。
const groups = computed(() => {
  const result = new Map<string, { key: string; labels: string[]; unit: MarketplaceVideoPrice['unit']; pricing: MarketplaceModelPricing }>()
  for (const entry of entries.value) {
    const image = entry.video_image_input_pricing
    const prepay = entry.video_token_prepay
    if (image == null && prepay == null) continue
    const key = JSON.stringify([entry.unit, image?.free_images ?? null, image?.price ?? null, prepay?.price_per_second ?? null])
    const existing = result.get(key)
    if (existing) {
      existing.labels.push(entry.label)
    } else {
      result.set(key, {
        key, labels: [entry.label], unit: entry.unit,
        pricing: { price_status: 'priced', pricing_mode: entry.unit === 'million_tokens' ? 'video_token' : entry.unit === 'request' ? 'video_per_request' : 'video', video_image_input_pricing: image, video_token_prepay: prepay },
      })
    }
  }
  return [...result.values()]
})
</script>
