<template>
  <div v-if="rule" class="mt-3 rounded-lg bg-gray-50 p-3 text-xs leading-5 text-gray-600 dark:bg-dark-800 dark:text-gray-300" data-testid="video-image-pricing-summary">
    <p class="font-medium">{{ t('marketplace.videoImageInputPricing.title') }}</p>
    <p>{{ t(rule.free_images === 0 ? 'marketplace.videoImageInputPricing.fromFirst' : 'marketplace.videoImageInputPricing.afterFree', { count: rule.free_images, price: formatVideoTaskAmount(rule.price) }) }}</p>
    <p class="mt-1 text-gray-500 dark:text-gray-400">{{ t('marketplace.videoImageInputPricing.fixedHint') }}</p>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { MarketplaceModelPricing } from '@/types'
import { formatVideoTaskAmount } from '@/components/media/mediaTaskCost'

const props = defineProps<{ pricing: MarketplaceModelPricing }>()
const { t } = useI18n()
// 后端下发的是固定美元单价，不转换为站内余额单位，也不在前端叠乘倍率。
const rule = computed(() => {
  if (!['video', 'video_token', 'video_per_request'].includes(props.pricing.pricing_mode)) return null
  const value = props.pricing.video_image_input_pricing
  return value && Number.isSafeInteger(value.free_images) && value.free_images >= 0 && Number.isFinite(value.price) && value.price >= 0 ? value : null
})
</script>
