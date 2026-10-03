<template>
  <BaseDialog :show="show" :title="t('intelligence.detail')" width="wide" @close="emit('close')">
    <div v-if="loading" role="status" class="py-12 text-center text-gray-500">{{ t('common.loading') }}</div>
    <p v-else-if="error" role="alert" class="text-red-600">{{ error }}</p>
    <div v-else-if="run" class="space-y-5">
      <div class="flex flex-wrap items-center justify-between gap-2 text-sm"><span class="inline-flex items-center gap-2"><span class="h-2.5 w-2.5 rounded-full" :class="resultColor(run)" />{{ t(`intelligence.statuses.${resultStatus(run)}`) }}</span><span class="text-gray-500">{{ formatDateTime(run.created_at) }}</span></div>
      <dl class="grid grid-cols-2 gap-x-6 gap-y-4 rounded-lg bg-gray-50 p-4 text-sm dark:bg-dark-900 sm:grid-cols-3">
        <div class="min-w-0"><dt class="text-gray-500">{{ t('intelligence.model') }}</dt><dd class="mt-1 break-all font-medium">{{ run.model }}</dd></div>
        <div v-for="stat in stats" :key="stat.label"><dt class="text-gray-500">{{ t(`intelligence.${stat.label}`) }}</dt><dd class="mt-1 font-medium">{{ stat.value }}</dd></div>
      </dl>
      <p v-if="run.error_message" class="rounded-lg bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-900/20 dark:text-amber-200">{{ run.error_message }}</p>
      <p v-if="run.assessment_reason" class="text-sm text-gray-600 dark:text-gray-300">{{ run.assessment_reason }}</p>
      <section v-if="run.question"><h4 class="mb-2 text-sm font-semibold">{{ t('intelligence.question') }}</h4><div class="intelligence-markdown" v-html="resultMarkdown(run.question)" /></section>
      <section v-if="run.answer"><h4 class="mb-2 text-sm font-semibold">{{ t('intelligence.answer') }}</h4><div class="intelligence-markdown" v-html="resultMarkdown(run.answer)" /></section>
      <p v-if="!run.question && !run.answer && !run.html && !run.error_message" class="text-sm text-gray-500">{{ t('intelligence.noAnswer') }}</p>
      <section v-if="run.has_artifact"><h4 class="mb-2 text-sm font-semibold">{{ t('intelligence.previewTitle') }}</h4><div class="h-[min(60vh,560px)] min-h-64 overflow-hidden rounded-xl border border-gray-200 dark:border-dark-600"><IntelligenceDrawing :run-id="run.id" :title="run.model" /></div><p class="mt-2 text-xs text-gray-500">{{ t('intelligence.previewHint') }}</p></section>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import type { IntelligenceRun } from '@/api/intelligence'
import { formatDateTime } from '@/utils/format'
import IntelligenceDrawing from './IntelligenceDrawing.vue'
import { resultColor, resultMarkdown, resultStatus } from './results'

const props = defineProps<{ show: boolean; run: IntelligenceRun | null; loading: boolean; error: string }>()
const emit = defineEmits<{ close: [] }>()
const { t } = useI18n()
// 缺少统计字段时显示未知；总耗时包含排队与评测，不能用它编造 TPS。
const number = (value?: number | null) => typeof value === 'number' && Number.isFinite(value) ? value.toLocaleString() : '—'
const stats = computed(() => [
  { label: 'duration', value: typeof props.run?.duration_ms === 'number' ? `${(props.run.duration_ms / 1000).toFixed(1)} s` : '—' },
  { label: 'inputTokens', value: number(props.run?.input_tokens) },
  { label: 'outputTokens', value: number(props.run?.output_tokens) },
  { label: 'reasoningTokens', value: number(props.run?.reasoning_tokens) },
])
</script>

<style>
.intelligence-markdown { line-height: 1.8; overflow-wrap: anywhere; font-size: .875rem; }
.intelligence-markdown p + p, .intelligence-markdown ul, .intelligence-markdown ol, .intelligence-markdown pre, .intelligence-markdown table { margin-top: .75rem; }
.intelligence-markdown ul { list-style: disc; padding-left: 1.5rem; }
.intelligence-markdown ol { list-style: decimal; padding-left: 1.5rem; }
.intelligence-markdown pre { white-space: pre-wrap; padding: .75rem; background: rgb(128 128 128 / .08); border-radius: .5rem; }
.intelligence-markdown table { display: block; overflow-x: auto; border-collapse: collapse; }
.intelligence-markdown th, .intelligence-markdown td { border: 1px solid rgb(128 128 128 / .25); padding: .5rem; }
.intelligence-markdown a { color: var(--color-primary-500, #0ea5e9); }
</style>
