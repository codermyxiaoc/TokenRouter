<template>
  <section class="mt-4 space-y-3 rounded-lg border border-gray-200 p-3 dark:border-dark-600" data-testid="video-model-details">
    <h4 class="text-sm font-medium text-gray-700 dark:text-gray-200">{{ t('admin.channels.modelDetails.title') }}</h4>
    <p class="input-hint">{{ t(scope === 'group' ? 'admin.channels.modelDetails.groupHint' : 'admin.channels.modelDetails.channelHint') }}</p>
    <p v-if="models.length === 0" class="input-hint">{{ t('admin.channels.modelDetails.empty') }}</p>
    <div v-for="model in models" :key="model" class="space-y-2 rounded-lg border border-gray-200 bg-white p-3 dark:border-dark-600 dark:bg-dark-800" :data-model-detail="model">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div class="min-w-0 flex-1">
          <p class="break-all text-sm font-medium text-gray-700 dark:text-gray-200">{{ model }}</p>
          <span v-if="scope === 'group' && !hasDetail(model)" class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.channels.modelDetails.inherited') }}</span>
        </div>
        <div class="flex items-center gap-2">
          <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.channels.modelDetails.enabled') }}</span>
          <Toggle :model-value="detail(model)?.enabled === true" :aria-label="`${model} ${t('admin.channels.modelDetails.enabled')}`" @update:model-value="setEnabled(model, $event)" />
        </div>
      </div>
      <label v-if="detail(model)?.enabled" class="block">
        <span class="input-label">{{ t('admin.channels.modelDetails.description') }}</span>
        <textarea class="input min-h-24 resize-y" rows="3" :value="detail(model)?.description" :placeholder="t('admin.channels.modelDetails.placeholder')" @input="setDescription(model, $event)" />
        <span class="input-hint block text-right">{{ Array.from(detail(model)?.description || '').length }} / {{ MODEL_DETAIL_MAX_LENGTH }}</span>
      </label>
      <button v-if="scope === 'group' && hasDetail(model)" type="button" class="text-xs text-primary-500 hover:text-primary-600" data-testid="inherit-model-detail" @click="reset(model)">{{ t('admin.channels.modelDetails.reset') }}</button>
    </div>
  </section>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'
import type { ModelDisplayDetail } from '@/api/admin/channels'
import { copyModelDetails, MODEL_DETAIL_MAX_LENGTH } from './modelDetails'

const props = withDefaults(defineProps<{
  models: string[]
  modelValue?: Record<string, ModelDisplayDetail>
  scope?: 'group' | 'channel'
}>(), { scope: 'channel' })
const emit = defineEmits<{ 'update:modelValue': [value: Record<string, ModelDisplayDetail> | undefined] }>()
const { t } = useI18n()
const hasDetail = (model: string) => !!props.modelValue && Object.prototype.hasOwnProperty.call(props.modelValue, model)
const detail = (model: string) => hasDetail(model) ? props.modelValue?.[model] : undefined

// 显式关闭仍保留草稿；只有重置才删除键，让分组重新继承渠道说明。
function setEnabled(model: string, enabled: boolean) {
  emit('update:modelValue', { ...copyModelDetails(props.models, props.modelValue), [model]: { enabled, description: detail(model)?.description || '' } })
}

function setDescription(model: string, event: Event) {
  const target = event.target as HTMLTextAreaElement
  // 与服务端按 Unicode 字符计数一致，避免中文和表情的长度边界不一致。
  const description = Array.from(target.value).slice(0, MODEL_DETAIL_MAX_LENGTH).join('')
  target.value = description
  emit('update:modelValue', { ...copyModelDetails(props.models, props.modelValue), [model]: { enabled: detail(model)?.enabled === true, description } })
}

function reset(model: string) {
  const next = { ...props.modelValue }
  delete next[model]
  emit('update:modelValue', copyModelDetails(props.models, next))
}
</script>
