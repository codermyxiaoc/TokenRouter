<template>
  <section class="space-y-4 rounded-xl border border-violet-200 bg-violet-50/40 p-4 dark:border-violet-900/50 dark:bg-violet-950/20" data-testid="video-account-fields">
    <div>
      <h3 class="text-sm font-semibold text-gray-900 dark:text-gray-100">{{ t('admin.accounts.video.title') }}</h3>
      <p class="input-hint">{{ t('admin.accounts.video.description') }}</p>
    </div>
    <div class="flex flex-wrap gap-3">
      <label v-for="endpoint in VIDEO_ENDPOINTS" :key="endpoint" class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
        <input type="checkbox" :checked="modelValue.endpoints.includes(endpoint)" :data-testid="`video-endpoint-${endpoint}`" class="rounded border-gray-300 text-violet-600" @change="toggleEndpoint(endpoint, ($event.target as HTMLInputElement).checked)" />
        {{ endpointLabel(endpoint) }}
      </label>
    </div>
    <div v-for="endpoint in modelValue.endpoints" :key="endpoint">
      <label class="input-label break-all">{{ endpointUrlLabel(endpoint) }} · {{ t('admin.accounts.video.endpointUrl') }}</label>
      <input class="input" type="url" :value="modelValue.baseUrls[endpoint] || ''" :data-testid="`video-endpoint-url-${endpoint}`" placeholder="https://api.example.com" @input="patch({ baseUrls: { ...modelValue.baseUrls, [endpoint]: ($event.target as HTMLInputElement).value } })" />
      <p class="input-hint">{{ t('admin.accounts.video.endpointUrlHint') }}</p>
    </div>
    <div class="space-y-3">
      <div class="flex items-center justify-between gap-2">
        <label class="input-label mb-0">{{ t('admin.accounts.video.modelBindings') }}</label>
        <button type="button" class="btn btn-secondary btn-sm" :disabled="!modelValue.endpoints.length" data-testid="video-add-binding" @click="addBinding">{{ t('common.add') }}</button>
      </div>
      <p class="input-hint">{{ t('admin.accounts.video.modelBindingsHint') }}</p>
      <p class="input-hint">{{ t('admin.accounts.video.routingHint') }}</p>
      <div v-for="(binding, index) in modelValue.bindings" :key="index" class="space-y-2 rounded-lg border border-gray-200 p-3 dark:border-dark-600">
        <div class="flex flex-wrap gap-2">
          <input :value="binding.model" class="input min-w-0 flex-1" :data-testid="`video-binding-model-${index}`" :placeholder="t('admin.accounts.video.modelPlaceholder')" @input="updateBinding(index, { model: ($event.target as HTMLInputElement).value })" />
          <Select :model-value="binding.endpoint" :options="endpointOptions" :data-testid="`video-binding-endpoint-${index}`" class="min-w-36 flex-1" @update:model-value="updateBinding(index, { endpoint: $event as VideoEndpoint })" />
          <button type="button" class="btn btn-secondary text-red-600" :data-testid="`video-remove-binding-${index}`" :aria-label="t('common.delete')" @click="patch({ bindings: modelValue.bindings.filter((_, row) => row !== index) })">×</button>
        </div>
        <Select v-if="binding.endpoint === 'kling'" :model-value="binding.path" :options="pathOptions" :data-testid="`video-binding-path-${index}`" @update:model-value="updateBinding(index, { path: String($event) })" />
      </div>
      <p v-if="modelValue.endpoints.includes('kling')" class="input-hint">{{ t('admin.accounts.video.klingPathHint') }}</p>
    </div>
    <div class="grid gap-3 sm:grid-cols-2">
      <div><label class="input-label">{{ t('admin.accounts.video.maxPendingTasks') }}</label><input class="input" type="number" min="1" max="1000" step="1" :value="modelValue.maxPendingTasks" @input="patch({ maxPendingTasks: ($event.target as HTMLInputElement).value })" /></div>
      <div><label class="input-label">{{ t('admin.accounts.video.maxDurationSeconds') }}</label><input class="input" type="number" min="0" step="any" :value="modelValue.maxDurationSeconds" @input="patch({ maxDurationSeconds: ($event.target as HTMLInputElement).value })" /></div>
    </div>
    <p class="input-hint">{{ t('admin.accounts.video.budgetHint') }}</p>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import { VIDEO_ENDPOINTS, VIDEO_MODEL_PATHS, type VideoAccountForm, type VideoEndpoint, type VideoModelBindingForm } from './videoAccountConfig'

const props = defineProps<{ modelValue: VideoAccountForm }>()
const emit = defineEmits<{ 'update:modelValue': [value: VideoAccountForm] }>()
const { t } = useI18n()
// 两种 OpenAI Videos 使用路径区分，名称复用到复选框和模型选择框。
const endpointLabel = (endpoint: VideoEndpoint) => t(`admin.accounts.video.endpoints.${endpoint}`)
// 地址覆盖标题展示实际创建路径；Kling 按已配置模板展示，未配置时列出可选模板。
const nativeEndpointPaths: Partial<Record<VideoEndpoint, string>> = {
  seedance: '/api/v3/contents/generations/tasks',
  wan: '/api/v1/services/aigc/video-generation/video-synthesis',
  minimax: '/v2/video_generation'
}
function endpointUrlLabel(endpoint: VideoEndpoint) {
  let path = nativeEndpointPaths[endpoint]
  if (endpoint === 'kling') {
    const configured = [...new Set([
      ...props.modelValue.bindings.filter(binding => binding.endpoint === 'kling').map(binding => binding.path),
      ...Object.values(props.modelValue.unboundModelPaths || {})
    ].filter(value => (VIDEO_MODEL_PATHS as readonly string[]).includes(value)))]
    path = (configured.length ? configured : VIDEO_MODEL_PATHS).join(' · ')
  }
  return path ? t('admin.accounts.video.endpointWithPath', { name: endpointLabel(endpoint), path }) : endpointLabel(endpoint)
}
const endpointOptions = computed(() => props.modelValue.endpoints.map(value => ({ value, label: endpointLabel(value) })))
const pathOptions = computed(() => [{ value: '', label: t('admin.accounts.video.nativePath') }, ...VIDEO_MODEL_PATHS.map(value => ({ value, label: value }))])
const patch = (value: Partial<VideoAccountForm>) => emit('update:modelValue', { ...props.modelValue, ...value })
// 绑定只能从用户已勾选的端点中初始化，空配置不能隐式补入兼容端点。
function addBinding() {
  const endpoint = props.modelValue.endpoints[0]
  if (!endpoint) return
  patch({ bindings: [...props.modelValue.bindings, { model: '', endpoint, path: '' }] })
}
// 取消端点时同时移除其专属绑定，避免隐藏的旧绑定继续影响上游选择。
function toggleEndpoint(endpoint: VideoEndpoint, checked: boolean) {
  patch({ endpoints: checked ? [...props.modelValue.endpoints, endpoint] : props.modelValue.endpoints.filter(value => value !== endpoint), bindings: checked ? props.modelValue.bindings : props.modelValue.bindings.filter(binding => binding.endpoint !== endpoint) })
}
function updateBinding(index: number, value: Partial<VideoModelBindingForm>) {
  patch({ bindings: props.modelValue.bindings.map((binding, row) => {
    if (row !== index) return binding
    const next = { ...binding, ...value }
    if (next.endpoint !== 'kling') next.path = ''
    // 独立路径转为显式 Kling 绑定时先回填；用户主动清空路径时不从旧值恢复。
    else if (!next.path && ('model' in value || 'endpoint' in value)) {
      next.path = props.modelValue.unboundModelPaths?.[next.model.trim()] || ''
    }
    return next
  }) })
}
</script>
