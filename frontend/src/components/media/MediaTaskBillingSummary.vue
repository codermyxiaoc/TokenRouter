<template>
  <div v-if="review" class="mt-1 space-y-1 text-xs" data-testid="billing-review-summary">
    <p :class="review.status === 'pending' ? 'text-amber-700 dark:text-amber-300' : 'text-gray-500 dark:text-gray-400'">{{ t(`mediaTasks.billingReview.statuses.${review.status}`) }}</p>
    <template v-if="review.status === 'pending'">
      <p class="text-gray-500 dark:text-gray-400">{{ t('mediaTasks.billingReview.heldTotal', { amount: amount(review.held_balance_amount + review.held_subscription_amount) }) }}</p>
      <p v-if="review.expires_at" class="whitespace-normal text-gray-500 dark:text-gray-400">{{ t('mediaTasks.billingReview.deadlineValue', { time: new Date(review.expires_at).toLocaleString() }) }}</p>
    </template>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { MediaTaskBillingReview } from '@/api/mediaTasks'
import { formatVideoTaskAmount } from './mediaTaskCost'
defineProps<{ review?: MediaTaskBillingReview }>()
const { t } = useI18n()
// 摘要只展示服务端给出的当前冻结，不能用历史预算推算。
const amount = (value: number) => formatVideoTaskAmount(value) ?? '—'
</script>
