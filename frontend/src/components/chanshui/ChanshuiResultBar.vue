<template>
  <div>
    <div class="overflow-x-auto pb-1" tabindex="0" :aria-label="t('chanshui.history')">
      <div class="grid h-10 min-w-[600px] grid-cols-[repeat(60,minmax(0,1fr))] items-center gap-1" data-result-bar>
        <span v-for="slot in 60 - ordered.length" :key="`empty-${slot}`" class="h-7 rounded-[2px] bg-gray-100 dark:bg-dark-700" aria-hidden="true" data-empty-slot />
        <button
          v-for="run in ordered" :key="run.id" type="button"
          class="h-7 min-w-0 rounded-[2px] transition hover:opacity-75 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary-500"
          :class="runColor(run)" :data-run-id="run.id" :title="label(run)" :aria-label="label(run)" @click="emit('select', run)"
        />
      </div>
    </div>
    <p class="mt-1 text-xs text-gray-400">{{ ordered.length ? t('chanshui.recentResults', { count: ordered.length }) : t('chanshui.noRuns') }}</p>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ChanshuiRun } from '@/api/admin/chanshui'
import { formatDateTime } from '@/utils/format'
import { recentRuns, runColor, runLabelKey } from './results'

const props = defineProps<{ runs: ChanshuiRun[] }>()
const emit = defineEmits<{ select: [run: ChanshuiRun] }>()
const { t } = useI18n()
// 固定保留 60 个槽位，单次结果也只占一格；空槽没有状态、详情或失败含义。
const ordered = computed(() => recentRuns(props.runs))
function label(run: ChanshuiRun) {
  const score = typeof run.score === 'number' && Number.isFinite(run.score)
    ? t(run.score_complete ? 'chanshui.scoreValue' : 'chanshui.partialScore', { score: run.score })
    : t('chanshui.noScore')
  return `${formatDateTime(run.created_at)} · ${t(runLabelKey(run))} · ${score}`
}
</script>
