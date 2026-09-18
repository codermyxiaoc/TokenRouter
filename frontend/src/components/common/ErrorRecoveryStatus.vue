<template>
  <div v-if="recovered" class="flex flex-wrap items-center gap-1.5">
    <span
      class="inline-flex items-center rounded bg-emerald-100 px-2 py-0.5 text-xs font-medium text-emerald-800 dark:bg-emerald-900 dark:text-emerald-200"
      :title="t('usage.errors.recoveredHint')"
    >{{ t('usage.errors.recovered') }}</span>
    <span v-if="clientStatusCode != null" class="whitespace-nowrap text-xs text-gray-500 dark:text-gray-400">
      {{ t('usage.errors.finalStatus') }} {{ clientStatusCode }}
    </span>
    <span v-if="recoveredGroupName" class="inline-flex max-w-full items-center gap-1 text-xs text-emerald-700 dark:text-emerald-300">
      <span class="shrink-0">{{ t('usage.errors.recoveredTo') }}</span>
      <GroupBadge :name="recoveredGroupName" :show-rate="false" :title="recoveredGroupName" class="min-w-0 max-w-64" />
    </span>
  </div>
  <span
    v-else-if="showFinalFailure"
    class="inline-flex items-center rounded bg-red-100 px-2 py-0.5 text-xs font-medium text-red-800 dark:bg-red-900 dark:text-red-200"
  >{{ t('usage.errors.finalFailed') }}</span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import GroupBadge from './GroupBadge.vue'

const props = defineProps<{
  recovered?: boolean
  clientStatusCode?: number | null
  groupId?: number | null
  groupName?: string | null
  showFinalFailure?: boolean
}>()

const { t } = useI18n()

// 恢复状态和目标仅采信后端快照，不能用 HTTP 200 或失败分组推测历史恢复结果。
const recoveredGroupName = computed(() => props.groupName?.trim() || (props.groupId && props.groupId > 0 ? `#${props.groupId}` : ''))
</script>
