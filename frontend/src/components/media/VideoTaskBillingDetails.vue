<template>
  <section class="mt-5 rounded-lg border border-violet-200 p-4 dark:border-violet-800" data-testid="video-task-billing">
    <h3 class="mb-3 font-medium text-gray-900 dark:text-gray-100">{{ t('mediaTasks.videoBilling.title') }}</h3>
    <p v-if="billing.status === 'reconciliation'" class="mb-3 text-sm text-amber-700 dark:text-amber-300">{{ t('mediaTasks.videoBilling.reconciliationHint') }}</p>
    <p v-if="billing.status === 'released'" class="mb-3 text-sm text-green-700 dark:text-green-300" data-testid="video-billing-released-hint">{{ t('mediaTasks.videoBilling.releasedHint') }}</p>
    <p v-if="billing.token_prepay" class="mb-3 text-sm text-gray-500 dark:text-gray-400" data-testid="video-token-prepay-hint">{{ t('mediaTasks.videoBilling.tokenPrepayHint') }}</p>
    <dl class="grid grid-cols-1 gap-3 text-sm sm:grid-cols-2">
      <div v-for="field in fields" :key="field.key" class="min-w-0">
        <dt class="text-xs text-gray-500">{{ t(`mediaTasks.videoBilling.${field.key}`) }}</dt>
        <dd class="mt-1 break-all text-gray-900 dark:text-gray-100">{{ field.value }}</dd>
      </div>
    </dl>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { VideoTaskBilling } from '@/api/mediaTasks'
import { formatVideoTaskAmount } from './mediaTaskCost'
const props = defineProps<{ billing: VideoTaskBilling }>()
const { t } = useI18n()
// 显式零消费仍展示；待对账或未返回实际金额时不误显示为免费。
const amount = (value: number | undefined) => formatVideoTaskAmount(value) ?? t('mediaTasks.videoBilling.unknown')
const fields = computed(() => {
  const b = props.billing
  // 不做金额预留仍沿用后端状态机；界面显示待结算，避免将零预留理解成免费。
  const statusKey = b.deferred_billing && (b.status === 'pending' || b.status === 'reserved')
    ? 'deferredStatus' : b.status === 'reserved' ? 'reservedStatus' : b.status
  return [
    { key: 'status', value: t(`mediaTasks.videoBilling.${statusKey}`) },
    { key: 'resolution', value: b.resolution || '—' },
    // 历史参考视频标记是输入元数据，不再显示为独立定价条件。
    { key: 'unitPrice', value: `${amount(b.unit_price)} / ${b.unit === 'million_tokens' ? '1M Token' : b.unit === 'request' ? t('mediaTasks.videoBilling.requestUnit') : 's'}` },
    // 按次只展示已结算的一次任务，不把失败或待对账计为已收费。
    ...(b.unit === 'request' ? [{ key: 'requestCount', value: b.status === 'settled' ? 1 : '—' }] : []),
    { key: 'tokens', value: b.tokens == null ? '—' : b.tokens.toLocaleString() },
    // 后端用 0 表示未取得时长，不把缺失用量展示成零时长。
    { key: b.unit === 'request' ? 'generatedDuration' : 'duration', value: b.duration_seconds > 0 ? b.duration_seconds : '—' },
    // 结算或释放后保留原预算作核对依据，不能把历史金额继续标为当前冻结。
    { key: ['settled', 'released'].includes(b.status) ? 'originalReserved' : 'reserved', value: b.deferred_billing ? t(`mediaTasks.videoBilling.${['settled', 'released'].includes(b.status) ? 'notReserved' : 'deferredHint'}`) : amount(b.reserved_amount) },
    { key: 'actual', value: b.status === 'released' ? t('mediaTasks.videoBilling.notCharged') : amount(b.status === 'settled' ? b.actual_amount : undefined) },
    // 固定预扣秒价与终态 Token 单价分别展示，不用预扣推算实际消费。
    ...(b.token_prepay ? [
      { key: 'prepayPricePerSecond', value: amount(b.prepay_price_per_second) },
      { key: 'prepayDurationSeconds', value: b.prepay_duration_seconds != null && b.prepay_duration_seconds > 0 ? b.prepay_duration_seconds : '—' },
    ] : []),
    // 图片费用以服务端快照为准，未结算状态不把估算金额当作实扣。
    ...(b.reference_image_unit_price != null ? [
      { key: 'referenceImageCount', value: b.reference_image_count ?? '—' },
      { key: 'referenceImageFreeCount', value: b.reference_image_free_count ?? '—' },
      { key: 'billableReferenceImageCount', value: b.billable_reference_image_count ?? '—' },
      { key: 'referenceImageUnitPrice', value: amount(b.reference_image_unit_price) },
      { key: 'referenceImageCost', value: b.status === 'released' ? t('mediaTasks.videoBilling.notCharged') : amount(b.status === 'settled' ? b.reference_image_cost : undefined) },
    ] : []),
    { key: 'pricingSource', value: ['group', 'channel'].includes(b.pricing_source) ? t(`mediaTasks.videoBilling.sources.${b.pricing_source}`) : b.pricing_source || '—' },
  ]
})
</script>
