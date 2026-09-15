<template>
  <span class="inline-flex flex-col items-start gap-1">
    <span class="inline-flex items-center whitespace-nowrap rounded-full px-2.5 py-1 text-xs font-medium" :class="color">{{ t(`tickets.${kind === 'status' ? 'statuses' : 'priorities'}.${value}`) }}</span>
    <span v-if="closeRole" data-testid="ticket-close-role" class="whitespace-nowrap text-xs text-gray-500 dark:text-gray-400">{{ t('tickets.closedBy', { role: t(`tickets.closeRoles.${closeRole}`) }) }}</span>
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { TicketClosedByRole } from '@/api/tickets'
const props = defineProps<{ kind: 'status' | 'priority'; value: string; closedByRole?: TicketClosedByRole | null }>()
const { t } = useI18n()
// 完成和撤销只使用服务端记录的角色，不能从工单归属、页面身份或最后消息猜测结单方。
const closeRole = computed(() => {
  if (props.kind !== 'status' || !['completed', 'cancelled', 'expired'].includes(props.value)) return null
  if (props.closedByRole && ['user', 'admin', 'system'].includes(props.closedByRole)) return props.closedByRole
  return props.value === 'expired' ? 'system' : 'unknown'
})
// 状态与优先级均保留文字，颜色只用作补充提示。
const color = computed(() => {
  if (['high', 'waiting_user'].includes(props.value)) return 'bg-amber-50 text-amber-700 dark:bg-amber-900/25 dark:text-amber-300'
  if (props.value === 'completed') return 'bg-emerald-50 text-emerald-700 dark:bg-emerald-900/25 dark:text-emerald-300'
  if (['pending', 'normal'].includes(props.value)) return 'bg-blue-50 text-blue-700 dark:bg-blue-900/25 dark:text-blue-300'
  return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
})
</script>
