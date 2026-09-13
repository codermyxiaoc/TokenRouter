<template>
  <span class="inline-flex items-center gap-1 whitespace-nowrap tabular-nums">
    <BalanceIcon v-if="hasCustomBalanceIcon" :size="iconSize" aria-hidden="true" />
    <span>{{ formattedAmount }}</span>
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import BalanceIcon from '@/components/common/BalanceIcon.vue'
import { useBalanceDisplay } from '@/composables/useBalanceDisplay'

const props = withDefaults(defineProps<{
  amount: number | null | undefined
  fractionDigits?: number
  iconSize?: 'xs' | 'sm' | 'md' | 'lg' | 'xl'
  useGrouping?: boolean
}>(), {
  fractionDigits: 2,
  iconSize: 'sm',
  useGrouping: true
})

const { hasCustomBalanceIcon, formatBalanceAmount } = useBalanceDisplay()

// 配置 SVG 图标时由图标承担金额标识，避免同时重复显示文本符号。
const formattedAmount = computed(() => formatBalanceAmount(props.amount, {
  fractionDigits: props.fractionDigits,
  useGrouping: props.useGrouping,
  withSymbol: !hasCustomBalanceIcon.value
}))
</script>
