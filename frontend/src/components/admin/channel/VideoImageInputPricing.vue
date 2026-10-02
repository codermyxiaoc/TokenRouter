<template>
  <section class="mt-4 space-y-3 rounded-lg border border-gray-200 p-3 dark:border-dark-600" data-testid="video-image-input-pricing">
    <div class="flex items-center justify-between gap-3">
      <span class="text-sm font-medium text-gray-700 dark:text-gray-200">{{ t('admin.channels.videoImageInputPricing.title') }}</span>
      <Toggle :model-value="modelValue != null" :aria-label="t('admin.channels.videoImageInputPricing.title')" @update:model-value="setEnabled" />
    </div>
    <p class="input-hint">{{ t('admin.channels.videoImageInputPricing.hint') }}</p>
    <div v-if="modelValue" class="grid grid-cols-1 gap-3 sm:grid-cols-2">
      <div>
        <label class="input-label">{{ t('admin.channels.videoImageInputPricing.mode') }}</label>
        <Select :model-value="mode" :options="modeOptions" :aria-label="t('admin.channels.videoImageInputPricing.mode')" @update:model-value="setMode(String($event))" />
      </div>
      <label v-if="mode === 'after_free'" class="block">
        <span class="input-label">{{ t('admin.channels.videoImageInputPricing.freeImages') }}</span>
        <input type="number" min="0" step="1" class="input" :value="modelValue.free_images" data-testid="video-image-free-count" @input="updateField('free_images', ($event.target as HTMLInputElement).value)" />
      </label>
      <label class="block">
        <span class="input-label">{{ t('admin.channels.videoImageInputPricing.price') }}</span>
        <input type="number" min="0" step="any" class="input" :value="modelValue.price" :placeholder="t('admin.channels.videoPricing.unconfigured')" data-testid="video-image-unit-price" @input="updateField('price', ($event.target as HTMLInputElement).value)" />
      </label>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import Toggle from '@/components/common/Toggle.vue'
import type { VideoImageInputPricingForm } from './videoPricing'

const props = defineProps<{ modelValue?: VideoImageInputPricingForm | null }>()
const emit = defineEmits<{ 'update:modelValue': [value: VideoImageInputPricingForm | null] }>()
const { t } = useI18n()
const mode = computed(() => props.modelValue?.free_images === 0 || props.modelValue?.free_images === '0' ? 'from_first' : 'after_free')
const modeOptions = computed(() => [
  { value: 'from_first', label: t('admin.channels.videoImageInputPricing.fromFirst') },
  { value: 'after_free', label: t('admin.channels.videoImageInputPricing.afterFree') },
])

// 新开启配置要求填写单价，避免把空白默认为显式零价。
function setEnabled(enabled: boolean) {
  emit('update:modelValue', enabled ? { free_images: 0, price: null } : null)
}

function setMode(value: string) {
  if (!props.modelValue) return
  emit('update:modelValue', { ...props.modelValue, free_images: value === 'from_first' ? 0 : Math.max(1, Number(props.modelValue.free_images) || 1) })
}

function updateField(field: keyof VideoImageInputPricingForm, value: string) {
  if (!props.modelValue) return
  emit('update:modelValue', { ...props.modelValue, [field]: value === '' ? null : value })
}
</script>
