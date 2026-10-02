<template>
  <div class="space-y-3" data-testid="video-pricing-matrix">
    <p class="input-hint">{{ t('admin.channels.videoPricing.hint') }}</p>
    <p v-if="resolutions.length === 0" class="rounded-lg border border-dashed border-gray-300 p-4 text-sm text-gray-500 dark:border-dark-600 dark:text-gray-400" data-testid="video-pricing-empty">{{ t('admin.channels.videoPricing.empty') }}</p>
    <div v-for="resolution in resolutions" :key="resolution" class="grid grid-cols-1 gap-3 rounded-lg border border-gray-200 p-3 dark:border-dark-600 sm:grid-cols-[5rem_1fr_auto] sm:items-end">
      <span class="text-sm font-medium text-gray-700 dark:text-gray-200">{{ resolution }}</span>
      <div>
        <label class="input-label">{{ t('admin.channels.videoPricing.price') }} <span class="text-xs text-gray-400">{{ unit }}</span></label>
        <input class="input" type="number" min="0" step="any" :value="priceFor(resolution)" :placeholder="t('admin.channels.videoPricing.unconfigured')" :data-testid="`video-price-${resolution}`" @input="updatePrice(resolution, ($event.target as HTMLInputElement).value)" />
      </div>
      <button type="button" class="btn btn-secondary" :aria-label="`${t('common.delete')} ${resolution}`" @click="removeResolution(resolution)">×</button>
    </div>
    <div class="flex gap-2">
      <input v-model="newResolution" class="input min-w-0 flex-1" :placeholder="t('admin.channels.videoPricing.resolutionPlaceholder')" @keydown.enter.prevent="addResolution" />
      <button type="button" class="btn btn-secondary" :disabled="!canAdd" @click="addResolution">{{ t('admin.channels.videoPricing.addResolution') }}</button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { normalizeVideoResolution, videoPricesFromAPI, type VideoPriceFormEntry } from './videoPricing'
const props = defineProps<{ modelValue?: VideoPriceFormEntry[]; mode: 'video' | 'video_token' | 'video_per_request' }>()
const emit = defineEmits<{ 'update:modelValue': [values: VideoPriceFormEntry[]] }>()
const { t } = useI18n()
// 只展示已有配置和手动添加的分辨率，避免空价卡自动出现固定档位。
const prices = computed(() => videoPricesFromAPI(props.modelValue))
const resolutions = computed(() => prices.value.map(value => value.resolution))
const unit = computed(() => props.mode === 'video_token' ? '$/1M Token' : props.mode === 'video_per_request' ? t('admin.channels.videoPricing.perRequestUnit') : '$/s')
const newResolution = ref('')
const canAdd = computed(() => /^(?:[1-9]\d*p?|[24]k)$/i.test(newResolution.value.trim()) && !resolutions.value.includes(normalizeVideoResolution(newResolution.value)))
const priceFor = (resolution: string) => prices.value.find(value => value.resolution === resolution)?.price ?? ''
function updatePrice(resolution: string, price: string) {
  const next = { resolution, price: price === '' ? null : price }
  // 每个分辨率只保留一个值，原地编辑保持顺序，清空后仍保留该行。
  emit('update:modelValue', resolutions.value.includes(resolution)
    ? prices.value.map(value => value.resolution === resolution ? next : value)
    : [...prices.value, next])
}
function addResolution() {
  if (!canAdd.value) return
  updatePrice(normalizeVideoResolution(newResolution.value), '')
  newResolution.value = ''
}
function removeResolution(resolution: string) {
  emit('update:modelValue', prices.value.filter(value => value.resolution !== resolution))
}
</script>
