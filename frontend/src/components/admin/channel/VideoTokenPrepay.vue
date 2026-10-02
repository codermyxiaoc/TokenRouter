<template>
  <section class="mt-4 space-y-3 rounded-lg border border-gray-200 p-3 dark:border-dark-600" data-testid="video-token-prepay">
    <div class="flex items-center justify-between gap-3">
      <span class="text-sm font-medium text-gray-700 dark:text-gray-200">{{ t('admin.channels.videoTokenPrepay.title') }}</span>
      <Toggle :model-value="modelValue != null" :aria-label="t('admin.channels.videoTokenPrepay.title')" @update:model-value="setEnabled" />
    </div>
    <p class="input-hint">{{ t('admin.channels.videoTokenPrepay.hint') }}</p>
    <label v-if="modelValue != null" class="block">
      <span class="input-label">{{ t('admin.channels.videoTokenPrepay.price') }}</span>
      <input class="input" type="number" min="0" step="any" data-testid="video-token-prepay-price" :value="modelValue.price_per_second" @input="emit('update:modelValue', { price_per_second: ($event.target as HTMLInputElement).value })" />
    </label>
  </section>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'
import type { VideoTokenPrepayForm } from './videoPricing'
defineProps<{ modelValue?: VideoTokenPrepayForm | null }>()
const emit = defineEmits<{ 'update:modelValue': [value: VideoTokenPrepayForm | null] }>()
const { t } = useI18n()
// 开启时要求管理员明确填写单价，关闭不保留隐藏配置。
const setEnabled = (enabled: boolean) => emit('update:modelValue', enabled ? { price_per_second: null } : null)
</script>
