<template>
  <BaseDialog :show="show" :title="t('keys.ccsImport.title')" width="normal" @close="emit('close')">
    <form id="ccs-import-form" class="space-y-4" @submit.prevent="confirmImport">
      <fieldset>
        <legend class="input-label">{{ t('keys.ccsImport.app') }}</legend>
        <!-- 沿用使用密钥弹窗的分段选择样式，保留原生单选的键盘操作。 -->
        <div class="grid grid-cols-3 gap-1 rounded-control bg-gray-100 p-1 dark:bg-dark-700">
          <label v-for="option in apps" :key="option.value" class="relative min-w-0 cursor-pointer">
            <input v-model="app" type="radio" name="ccs-app" :value="option.value" class="peer sr-only" />
            <span class="block rounded-control px-3 py-2 text-center text-sm font-medium text-gray-600 transition-colors hover:text-gray-900 peer-checked:bg-white peer-checked:text-primary-700 peer-checked:shadow-sm peer-focus-visible:ring-2 peer-focus-visible:ring-black/10 dark:text-dark-300 dark:hover:text-white dark:peer-checked:bg-dark-800 dark:peer-checked:text-primary-300 dark:peer-focus-visible:ring-primary-500/30">
              {{ option.label }}
            </span>
          </label>
        </div>
      </fieldset>

      <div>
        <label for="ccs-provider-name" class="input-label">{{ t('keys.ccsImport.name') }}</label>
        <input
          id="ccs-provider-name"
          v-model="draft.providerName"
          type="text"
          class="input"
          :class="{ 'input-error': submitted && !draft.providerName.trim() }"
          :aria-invalid="submitted && !draft.providerName.trim()"
          :placeholder="t('keys.ccsImport.namePlaceholder')"
          autocomplete="off"
        />
      </div>

      <div>
        <label for="ccs-main-model" class="input-label">
          {{ t('keys.ccsImport.mainModel') }} <span class="text-red-500">*</span>
        </label>
        <Select
          id="ccs-main-model"
          :model-value="draft.model"
          :aria-label="t('keys.ccsImport.mainModel')"
          :options="modelOptions"
          :error="submitted && !draft.model.trim()"
          :disabled="loadingModels"
          :placeholder="modelPlaceholder"
          :search-placeholder="t('keys.ccsImport.modelSearchPlaceholder')"
          searchable
          clearable
          @update:model-value="setField('model', $event)"
        />
        <p v-if="loadingModels" class="input-hint" role="status">{{ t('keys.ccsImport.loadingModels') }}</p>
        <div v-else-if="modelsFailed" class="mt-2 flex items-center justify-between gap-3">
          <p class="text-xs text-red-600 dark:text-red-400" role="status">{{ t('keys.ccsImport.modelsFailed') }}</p>
          <button type="button" class="btn btn-secondary btn-sm shrink-0" data-testid="ccs-retry-models" @click="loadModels">
            {{ t('keys.ccsImport.retryModels') }}
          </button>
        </div>
        <p v-else-if="!modelOptions.length" class="input-hint" role="status">{{ t('keys.ccsImport.modelsEmpty') }}</p>
      </div>

      <!-- 分档模型只对 Claude 生效，各应用保留独立草稿，避免切换时误带配置。 -->
      <template v-if="app === 'claude'">
        <div v-for="field in claudeModelFields" :key="field.key">
          <label :for="`ccs-${field.key}`" class="input-label">{{ t(field.label) }}</label>
          <Select
            :id="`ccs-${field.key}`"
            :model-value="draft[field.key]"
            :aria-label="t(field.label)"
            :options="modelOptions"
            :disabled="loadingModels"
            :placeholder="modelPlaceholder"
            :search-placeholder="t('keys.ccsImport.modelSearchPlaceholder')"
            searchable
            clearable
            @update:model-value="setField(field.key, $event)"
          />
        </div>
      </template>
      <p v-if="submitted && validationError" class="text-sm text-red-600 dark:text-red-400" role="alert">{{ validationError }}</p>
    </form>
    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" @click="emit('close')">{{ t('common.cancel') }}</button>
        <button type="submit" form="ccs-import-form" class="btn btn-primary" data-testid="ccs-confirm" :disabled="!apiKey || loadingModels || modelsFailed || !modelOptions.length">
          {{ t('keys.ccsImport.open') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onUnmounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ApiKey, MarketplaceGroup } from '@/types'
import { getMarketplaceModels } from '@/api/marketplace'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import type { CcSwitchApp, CcSwitchImportSelection } from '@/utils/ccswitchImport'

const props = defineProps<{ show: boolean; apiKey: ApiKey | null }>()
const emit = defineEmits<{
  (event: 'close'): void
  (event: 'confirm', selection: CcSwitchImportSelection): void
}>()
const { t } = useI18n()
const apps: Array<{ value: CcSwitchApp; label: string }> = [
  { value: 'claude', label: 'Claude' },
  { value: 'codex', label: 'Codex' },
  { value: 'gemini', label: 'Gemini' }
]
const claudeModelFields = [
  { key: 'haikuModel', label: 'keys.ccsImport.haikuModel' },
  { key: 'sonnetModel', label: 'keys.ccsImport.sonnetModel' },
  { key: 'opusModel', label: 'keys.ccsImport.opusModel' }
] as const
type Draft = Required<Omit<CcSwitchImportSelection, 'app'>>
const emptyDraft = (label: string): Draft => ({
  providerName: `My ${label}`, model: '', haikuModel: '', sonnetModel: '', opusModel: ''
})
const drafts = reactive<Record<CcSwitchApp, Draft>>({
  claude: emptyDraft('Claude'), codex: emptyDraft('Codex'), gemini: emptyDraft('Gemini')
})
const app = ref<CcSwitchApp>('claude')
const draft = computed(() => drafts[app.value])
const submitted = ref(false)
const modelGroups = ref<MarketplaceGroup[]>([])
const loadingModels = ref(false)
const modelsFailed = ref(false)
let modelRequest: AbortController | null = null
// 复用模型广场的全站目录，不按当前密钥或所选应用缩小列表；重复 ID 只展示一次。
const modelOptions = computed(() => {
  const models = new Map<string, { value: string; label: string; description: string }>()
  for (const group of modelGroups.value) {
    for (const model of group.models) {
      if (model.id && !models.has(model.id)) {
        models.set(model.id, { value: model.id, label: model.id, description: model.display_name })
      }
    }
  }
  return [...models.values()]
})
const modelPlaceholder = computed(() => t(loadingModels.value ? 'keys.ccsImport.loadingModels' : 'keys.ccsImport.modelPlaceholder'))
const validationError = computed(() => {
  if (!draft.value.providerName.trim()) return t('keys.ccsImport.nameRequired')
  if (!draft.value.model.trim()) return t('keys.ccsImport.modelRequired')
  return ''
})

function setField(field: keyof Draft, value: string | number | boolean | null) {
  draft.value[field] = typeof value === 'string' ? value : ''
}

function cancelModelRequest() {
  modelRequest?.abort()
  modelRequest = null
}

// 关闭或换密钥时取消旧请求，避免迟到的结果覆盖新弹窗；失败后可在原处重试。
async function loadModels() {
  cancelModelRequest()
  if (!props.show || !props.apiKey) return
  const request = new AbortController()
  modelRequest = request
  loadingModels.value = true
  modelsFailed.value = false
  try {
    const groups = await getMarketplaceModels(request.signal)
    if (modelRequest === request) modelGroups.value = groups
  } catch {
    if (modelRequest === request) modelsFailed.value = true
  } finally {
    if (modelRequest === request) {
      loadingModels.value = false
      modelRequest = null
    }
  }
}

watch(() => [props.show, props.apiKey] as const, ([show, key]) => {
  cancelModelRequest()
  modelGroups.value = []
  loadingModels.value = false
  modelsFailed.value = false
  submitted.value = false
  for (const option of apps) drafts[option.value] = emptyDraft(option.label)
  if (!show || !key) return
  const platform = key.group?.platform || key.smart_routing_groups?.[0]?.platform || key.composite_groups?.[0]?.group?.platform
  app.value = platform === 'openai' || platform === 'grok' ? 'codex' : platform === 'gemini' ? 'gemini' : 'claude'
  void loadModels()
}, { immediate: true })
watch(app, () => { submitted.value = false })
onUnmounted(cancelModelRequest)

function confirmImport() {
  submitted.value = true
  if (!props.show || !props.apiKey || loadingModels.value || modelsFailed.value || !modelOptions.value.length || validationError.value) return
  const selection: CcSwitchImportSelection = {
    app: app.value, providerName: draft.value.providerName.trim(), model: draft.value.model.trim()
  }
  if (app.value === 'claude') {
    for (const field of claudeModelFields) {
      const value = draft.value[field.key].trim()
      if (value) selection[field.key] = value
    }
  }
  emit('confirm', selection)
}
</script>
