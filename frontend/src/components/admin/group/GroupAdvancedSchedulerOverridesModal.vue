<template>
  <BaseDialog
    :show="show"
    :title="t('admin.groups.advancedSchedulerOverrides.title')"
    width="wide"
    :z-index="60"
    @close="emit('close')"
  >
    <form id="advanced-scheduler-overrides-form" class="space-y-5" @submit.prevent="handleSave">
      <p class="text-sm leading-6 text-gray-500 dark:text-gray-400">
        {{ t('admin.groups.advancedSchedulerOverrides.description') }}
      </p>

      <div class="grid gap-4 sm:grid-cols-2">
        <div>
          <label class="input-label">{{ t('admin.groups.advancedSchedulerOverrides.stickyWeighted') }}</label>
          <Select v-model="draft.stickyWeighted" :options="booleanOptions" />
        </div>
        <div>
          <label class="input-label">{{ t('admin.groups.advancedSchedulerOverrides.subscriptionPriority') }}</label>
          <Select v-model="draft.subscriptionPriority" :options="booleanOptions" />
        </div>
        <div>
          <label class="input-label">{{ t('admin.groups.advancedSchedulerOverrides.stickyEscapeEnabled') }}</label>
          <Select v-model="draft.stickyEscapeEnabled" :options="booleanOptions" />
        </div>
      </div>

      <div class="border-t border-gray-200 pt-4 dark:border-dark-600">
        <div class="mb-3">
          <h4 class="text-sm font-semibold text-gray-900 dark:text-white">
            {{ t('admin.groups.advancedSchedulerOverrides.weightsTitle') }}
          </h4>
          <p class="input-hint">{{ t('admin.groups.advancedSchedulerOverrides.weightsHint') }}</p>
        </div>
        <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          <div v-for="field in numericFields" :key="field.key">
            <label class="input-label">{{ t(`admin.groups.advancedSchedulerOverrides.fields.${field.key}`) }}</label>
            <input
              v-model="draft[field.key]"
              :type="field.integer ? 'number' : 'number'"
              :min="field.integer ? 1 : 0"
              :step="field.integer ? 1 : 'any'"
              class="input"
              inputmode="decimal"
              :placeholder="t('admin.groups.advancedSchedulerOverrides.inheritPlaceholder')"
            />
            <p class="input-hint">{{ t('admin.groups.advancedSchedulerOverrides.inheritHint') }}</p>
          </div>
        </div>
      </div>

      <p v-if="errorMessage" class="text-sm text-red-600 dark:text-red-400">{{ errorMessage }}</p>
    </form>

    <template #footer>
      <div class="flex flex-wrap items-center justify-between gap-3 pt-4">
        <button
          type="button"
          class="btn btn-ghost"
          data-test="advanced-scheduler-overrides-reset"
          @click="resetToInherit"
        >
          <Icon name="refresh" size="sm" />
          {{ t('admin.groups.advancedSchedulerOverrides.reset') }}
        </button>
        <div class="flex gap-3">
          <button type="button" class="btn btn-secondary" @click="emit('close')">
            {{ t('common.cancel') }}
          </button>
          <button
            type="submit"
            form="advanced-scheduler-overrides-form"
            class="btn btn-primary"
            data-test="advanced-scheduler-overrides-save"
          >
            <Icon name="check" size="sm" />
            {{ t('common.save') }}
          </button>
        </div>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'
import type { GroupAdvancedSchedulerOverrides } from '@/types'

const props = defineProps<{
  show: boolean
  modelValue: GroupAdvancedSchedulerOverrides
}>()

const emit = defineEmits<{
  close: []
  save: [value: GroupAdvancedSchedulerOverrides]
}>()

const { t } = useI18n()
type BooleanDraft = 'inherit' | 'true' | 'false'
type NumericKey =
  | 'ewmaErrorRateAlpha'
  | 'ewmaTTFTAlpha'
  | 'stickyEscapeTTFTMs'
  | 'stickyEscapeErrorRate'
  | 'lbTopK'
  | 'weightPriority'
  | 'weightLoad'
  | 'weightQueue'
  | 'weightErrorRate'
  | 'weightTTFT'
  | 'weightReset'
  | 'weightQuotaHeadroom'
  | 'weightPreviousResponse'
  | 'weightSessionSticky'
type NumericOverrideKey = Exclude<keyof GroupAdvancedSchedulerOverrides, 'sticky_weighted_enabled' | 'subscription_priority_enabled' | 'sticky_escape_enabled'>

// 数字输入会写回 number，空字符串仍保留“未设置即继承”的语义。
const draft = reactive<Record<NumericKey, string | number> & {
  stickyWeighted: BooleanDraft
  subscriptionPriority: BooleanDraft
  stickyEscapeEnabled: BooleanDraft
}>({
  stickyWeighted: 'inherit',
  subscriptionPriority: 'inherit',
  stickyEscapeEnabled: 'inherit',
  ewmaErrorRateAlpha: '',
  ewmaTTFTAlpha: '',
  stickyEscapeTTFTMs: '',
  stickyEscapeErrorRate: '',
  lbTopK: '',
  weightPriority: '',
  weightLoad: '',
  weightQueue: '',
  weightErrorRate: '',
  weightTTFT: '',
  weightReset: '',
  weightQuotaHeadroom: '',
  weightPreviousResponse: '',
  weightSessionSticky: '',
})
const errorMessage = ref('')

const booleanOptions = [
  { value: 'inherit', label: t('admin.groups.advancedSchedulerOverrides.inherit') },
  { value: 'true', label: t('admin.groups.advancedSchedulerOverrides.enabled') },
  { value: 'false', label: t('admin.groups.advancedSchedulerOverrides.disabled') },
]

const numericFields: Array<{ key: NumericKey; integer?: boolean }> = [
  { key: 'ewmaErrorRateAlpha' },
  { key: 'ewmaTTFTAlpha' },
  { key: 'stickyEscapeTTFTMs', integer: true },
  { key: 'stickyEscapeErrorRate' },
  { key: 'lbTopK', integer: true },
  { key: 'weightPriority' },
  { key: 'weightLoad' },
  { key: 'weightQueue' },
  { key: 'weightErrorRate' },
  { key: 'weightTTFT' },
  { key: 'weightReset' },
  { key: 'weightQuotaHeadroom' },
  { key: 'weightPreviousResponse' },
  { key: 'weightSessionSticky' },
]

const resetToInherit = () => {
  draft.stickyWeighted = 'inherit'
  draft.subscriptionPriority = 'inherit'
  draft.stickyEscapeEnabled = 'inherit'
  for (const field of numericFields) draft[field.key] = ''
  errorMessage.value = ''
}

const hydrate = (value: GroupAdvancedSchedulerOverrides | undefined) => {
  resetToInherit()
  if (!value) return
  if (value.sticky_weighted_enabled !== undefined) draft.stickyWeighted = String(value.sticky_weighted_enabled) as BooleanDraft
  if (value.subscription_priority_enabled !== undefined) draft.subscriptionPriority = String(value.subscription_priority_enabled) as BooleanDraft
  if (value.sticky_escape_enabled !== undefined) draft.stickyEscapeEnabled = String(value.sticky_escape_enabled) as BooleanDraft
  const mapping: Array<[NumericKey, keyof GroupAdvancedSchedulerOverrides]> = [
    ['ewmaErrorRateAlpha', 'ewma_error_rate_alpha'],
    ['ewmaTTFTAlpha', 'ewma_ttft_alpha'],
    ['stickyEscapeTTFTMs', 'sticky_escape_ttft_ms'],
    ['stickyEscapeErrorRate', 'sticky_escape_error_rate'],
    ['lbTopK', 'lb_top_k'],
    ['weightPriority', 'weight_priority'],
    ['weightLoad', 'weight_load'],
    ['weightQueue', 'weight_queue'],
    ['weightErrorRate', 'weight_error_rate'],
    ['weightTTFT', 'weight_ttft'],
    ['weightReset', 'weight_reset'],
    ['weightQuotaHeadroom', 'weight_quota_headroom'],
    ['weightPreviousResponse', 'weight_previous_response'],
    ['weightSessionSticky', 'weight_session_sticky'],
  ]
  for (const [draftKey, sourceKey] of mapping) {
    const raw = value[sourceKey]
    if (raw !== undefined) draft[draftKey] = String(raw)
  }
}

watch(() => [props.show, props.modelValue] as const, ([show]) => {
  if (show) hydrate(props.modelValue)
}, { immediate: true, deep: true })

const handleSave = () => {
  errorMessage.value = ''
  const value: GroupAdvancedSchedulerOverrides = {}
  if (draft.stickyWeighted !== 'inherit') value.sticky_weighted_enabled = draft.stickyWeighted === 'true'
  if (draft.subscriptionPriority !== 'inherit') value.subscription_priority_enabled = draft.subscriptionPriority === 'true'
  if (draft.stickyEscapeEnabled !== 'inherit') value.sticky_escape_enabled = draft.stickyEscapeEnabled === 'true'
  for (const field of numericFields) {
    const raw = String(draft[field.key] ?? '').trim()
    if (!raw) continue
    const parsed = Number(raw)
    const isAlpha = field.key === 'ewmaErrorRateAlpha' || field.key === 'ewmaTTFTAlpha'
    const isRate = field.key === 'stickyEscapeErrorRate'
    const valid = isAlpha
      ? Number.isFinite(parsed) && parsed > 0 && parsed <= 1
      : isRate
        ? Number.isFinite(parsed) && parsed >= 0 && parsed <= 1
        : Number.isFinite(parsed) && parsed >= 0 && (!field.integer || (Number.isInteger(parsed) && parsed > 0))
    if (!valid) {
      errorMessage.value = t('admin.groups.advancedSchedulerOverrides.invalidValue')
      return
    }
    const keyMap: Record<NumericKey, NumericOverrideKey> = {
      ewmaErrorRateAlpha: 'ewma_error_rate_alpha',
      ewmaTTFTAlpha: 'ewma_ttft_alpha',
      stickyEscapeTTFTMs: 'sticky_escape_ttft_ms',
      stickyEscapeErrorRate: 'sticky_escape_error_rate',
      lbTopK: 'lb_top_k',
      weightPriority: 'weight_priority',
      weightLoad: 'weight_load',
      weightQueue: 'weight_queue',
      weightErrorRate: 'weight_error_rate',
      weightTTFT: 'weight_ttft',
      weightReset: 'weight_reset',
      weightQuotaHeadroom: 'weight_quota_headroom',
      weightPreviousResponse: 'weight_previous_response',
      weightSessionSticky: 'weight_session_sticky',
    }
    value[keyMap[field.key]] = parsed
  }
  emit('save', value)
}
</script>
