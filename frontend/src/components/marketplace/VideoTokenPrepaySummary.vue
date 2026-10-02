<template>
  <div v-if="rule" class="mt-3 rounded-lg bg-gray-50 p-3 text-xs text-gray-600 dark:bg-dark-800 dark:text-gray-300" data-testid="video-token-prepay-summary">
    <p class="font-medium">{{ t('marketplace.videoTokenPrepay.title') }}</p>
    <p>{{ t('marketplace.videoTokenPrepay.price', { price: formatVideoTaskAmount(rule.price_per_second) }) }}</p>
    <p class="mt-1 text-gray-500 dark:text-gray-400">{{ t('marketplace.videoTokenPrepay.hint') }}</p>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { MarketplaceModelPricing } from '@/types'
import { formatVideoTaskAmount } from '@/components/media/mediaTaskCost'
const props = defineProps<{ pricing: MarketplaceModelPricing }>()
const { t } = useI18n()
// 预扣独立展示固定 USD 原价，不能作为最终 Token 单价或重复乘倍率。
const rule = computed(() => {
  const value = props.pricing.video_token_prepay
  return props.pricing.pricing_mode === 'video_token' && value != null &&
    Number.isFinite(value.price_per_second) && value.price_per_second >= 0 ? value : null
})
</script>
