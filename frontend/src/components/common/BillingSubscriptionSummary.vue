<template>
  <div v-if="subscriptions.length" class="min-w-0 space-y-0.5" data-testid="billing-subscriptions">
    <div
      v-for="item in subscriptions"
      :key="`${item.subscription_id}:${item.plan_id ?? 0}`"
      class="flex min-w-0 items-center gap-1 text-xs text-gray-600 dark:text-gray-400"
      :title="`${getBillingSubscriptionName(item, subscriptionLabel)} · ${subscriptionLabel} #${item.subscription_id}`"
    >
      <span class="max-w-[180px] truncate">{{ getBillingSubscriptionName(item, subscriptionLabel) }}</span>
      <span v-if="item.plan_name?.trim()" class="shrink-0 text-[10px] text-gray-400 dark:text-gray-500">#{{ item.subscription_id }}</span>
    </div>
  </div>
  <span v-else-if="showEmpty" class="text-sm text-gray-400 dark:text-gray-500">—</span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { BillingSubscription } from '@/types'
import { getBillingSubscriptionName, getBillingSubscriptions } from '@/utils/billingSubscriptions'

// 用量与错误列表共用同一账单摘要，无摘要时不推断未扣费或当前绑定套餐。
const props = withDefaults(defineProps<{
  row: { billing_subscriptions?: BillingSubscription[] | null }
  showEmpty?: boolean
}>(), { showEmpty: false })
const { t } = useI18n()
const subscriptionLabel = computed(() => t('admin.usage.billingSubscription'))
const subscriptions = computed(() => getBillingSubscriptions(props.row))
</script>
