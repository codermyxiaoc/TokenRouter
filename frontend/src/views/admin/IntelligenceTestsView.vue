<template>
  <AppLayout>
    <div class="space-y-6">
      <div class="flex flex-wrap items-start justify-between gap-3"><div><h1 class="page-title">{{ t('intelligence.adminTitle') }}</h1><p class="page-description">{{ t('intelligence.adminDescription') }}</p></div><div class="flex gap-2"><button type="button" class="btn btn-secondary btn-sm" :disabled="loading" @click="load">{{ t('common.refresh') }}</button><button type="button" class="btn btn-primary btn-sm" @click="openForm()">{{ t('intelligence.add') }}</button></div></div>
      <p v-if="!moduleEnabled" class="rounded-lg bg-amber-50 p-4 text-sm text-amber-800 dark:bg-amber-900/20 dark:text-amber-200">{{ t('intelligence.disabledHint') }}</p>
      <p v-if="error" role="alert" class="rounded-lg bg-red-50 p-4 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300">{{ error }}</p>
      <div v-if="loading && !configs.length" role="status" class="py-16 text-center text-gray-500">{{ t('common.loading') }}</div>
      <div v-if="!loading && !error && !configs.length" class="card p-12 text-center"><h2 class="font-semibold">{{ t('intelligence.adminEmpty') }}</h2><p class="mt-2 text-sm text-gray-500">{{ t('intelligence.adminEmptyHint') }}</p></div>
      <section v-for="group in groupedConfigs" :key="group.id" class="card overflow-hidden">
        <div class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-100 p-4 dark:border-dark-700 sm:px-6"><div class="min-w-0"><h2 class="break-words text-lg font-semibold">{{ group.name }}</h2><p class="mt-1 text-xs text-gray-500">{{ t('intelligence.configuredModels', { count: group.configs.length }) }}</p></div><button type="button" class="btn btn-secondary btn-sm" @click="openForm(undefined, group.id)">{{ t('intelligence.add') }}</button></div>
        <div class="divide-y divide-gray-100 dark:divide-dark-700">
          <article v-for="config in group.configs" :key="config.id" class="flex flex-col gap-4 p-4 sm:px-6 lg:flex-row lg:items-center lg:justify-between" :data-config-id="config.id">
            <div class="min-w-0"><div class="flex flex-wrap items-center gap-2"><h3 class="break-all font-semibold">{{ config.model }}</h3><span class="rounded bg-primary-50 px-2 py-0.5 text-xs text-primary-700 dark:bg-primary-900/30 dark:text-primary-300">{{ t(`intelligence.${config.benchmark}`) }}</span><span v-if="!config.enabled" class="text-xs text-gray-400">{{ t('common.inactive') }}</span></div><p class="mt-2 text-xs text-gray-500">{{ config.protocol === 'responses' ? 'Responses' : 'Chat Completions' }} · {{ config.schedule_enabled ? t('intelligence.everyMinutes', { minutes: config.interval_minutes }) : t('intelligence.manual') }}</p><p v-if="config.latest_run" class="mt-2 inline-flex items-center gap-2 text-xs text-gray-500"><span class="h-2 w-2 rounded-full" :class="resultColor(config.latest_run)" />{{ t(`intelligence.statuses.${resultStatus(config.latest_run)}`) }} · {{ formatDateTime(config.latest_run.created_at) }}</p></div>
            <div class="flex flex-wrap gap-2"><button type="button" class="btn btn-primary btn-sm" :disabled="!moduleEnabled || !config.enabled || isBusy(config) || runningIDs.has(config.id)" @click="runTest(config)">{{ t('intelligence.run') }}</button><button type="button" class="btn btn-secondary btn-sm" @click="openHistory(config)">{{ t('intelligence.history') }}</button><button type="button" class="btn btn-secondary btn-sm" :disabled="isBusy(config)" @click="openForm(config)">{{ t('common.edit') }}</button><button type="button" class="btn btn-secondary btn-sm text-red-600" :disabled="isBusy(config)" @click="deleting = config">{{ t('common.delete') }}</button></div>
          </article>
        </div>
      </section>

      <BaseDialog :show="showForm" :title="t(editingID ? 'intelligence.edit' : 'intelligence.add')" width="wide" :close-on-escape="!saving" @close="closeForm">
        <form id="intelligence-config-form" class="space-y-5" @submit.prevent="save">
          <p v-if="formError" role="alert" class="text-sm text-red-600">{{ formError }}</p>
          <div class="grid gap-4 sm:grid-cols-2"><div><label for="intelligence-group" class="input-label">{{ t('intelligence.group') }}</label><Select id="intelligence-group" v-model="form.group_id" :options="groupOptions" :disabled="saving || !!editingID" searchable :placeholder="t('intelligence.groupPlaceholder')" /></div><div><label for="intelligence-model" class="input-label">{{ t('intelligence.model') }}</label><Select id="intelligence-model" v-model="form.model" :options="modelOptions" :disabled="saving || !!editingID || !form.group_id || modelsLoading" searchable creatable :placeholder="t('intelligence.modelPlaceholder')" /></div></div>
          <div class="grid gap-4 sm:grid-cols-2"><div><label for="intelligence-benchmark" class="input-label">{{ t('intelligence.benchmark') }}</label><Select id="intelligence-benchmark" v-model="form.benchmark" :options="benchmarkOptions" :disabled="saving || !!editingID" /></div><div><label for="intelligence-protocol" class="input-label">{{ t('intelligence.protocol') }}</label><Select id="intelligence-protocol" v-model="form.protocol" :options="protocolOptions" :disabled="saving || form.benchmark === 'candy'" /><p v-if="form.benchmark === 'candy'" class="mt-2 text-xs text-gray-500">{{ t('intelligence.candyProtocolHint') }}</p></div></div>
          <div>
            <label for="intelligence-base-url" class="input-label">{{ t('intelligence.baseUrl') }}</label>
            <div class="flex flex-col gap-2 sm:flex-row">
              <input id="intelligence-base-url" v-model="form.base_url" type="url" required class="input min-w-0 flex-1" :disabled="saving" placeholder="https://your-site.example/v1" />
              <button type="button" class="btn btn-secondary shrink-0" :disabled="saving" @click="useCurrentSite">{{ t('intelligence.useCurrentSite') }}</button>
            </div>
            <p class="mt-2 text-xs text-gray-500">{{ t('intelligence.baseUrlHint') }}</p>
          </div>
          <div>
            <label for="intelligence-key-select" class="input-label">{{ t('intelligence.selectApiKey') }}</label>
            <div class="flex items-start gap-2">
              <Select id="intelligence-key-select" class="min-w-0 flex-1" :model-value="selectedKeyID" :options="apiKeyOptions" :disabled="saving || !form.group_id || keysLoading" searchable clearable :placeholder="t(!form.group_id ? 'intelligence.groupPlaceholder' : keysLoading ? 'common.loading' : 'intelligence.selectApiKeyPlaceholder')" :empty-text="t('intelligence.noApiKeys')" @update:model-value="selectApiKey" />
              <button type="button" class="btn btn-secondary shrink-0" :disabled="saving || !form.group_id || keysLoading" @click="loadAPIKeys">{{ t('common.refresh') }}</button>
            </div>
            <p class="mt-2 text-xs text-gray-500">{{ t('intelligence.selectApiKeyHint') }}</p>
            <p v-if="keysError" role="alert" class="mt-2 text-xs text-red-600">{{ keysError }}</p>
            <p v-else-if="form.group_id && !keysLoading && !apiKeys.length" class="mt-2 text-xs text-gray-500">{{ t('intelligence.noApiKeys') }}</p>
            <label for="intelligence-api-key" class="input-label mt-3">{{ t('intelligence.apiKey') }}</label>
            <input id="intelligence-api-key" v-model="form.api_key" type="password" autocomplete="new-password" class="input w-full" :required="!storedKey" :disabled="saving" :placeholder="t(storedKey ? 'intelligence.apiKeyKeep' : 'intelligence.apiKeyRequired')" @input="selectedKeyID = null" />
            <p class="mt-2 text-xs text-gray-500">{{ t('intelligence.apiKeyHint') }}</p>
            <p class="mt-2 text-xs text-gray-500">{{ t('intelligence.apiKeyFallbackHint') }}</p>
          </div>
          <div class="grid gap-4 sm:grid-cols-2"><div><label for="intelligence-reasoning" class="input-label">{{ t('intelligence.reasoning') }}</label><Select id="intelligence-reasoning" v-model="form.reasoning_effort" :options="reasoningOptions" :disabled="saving" /></div><div><label for="intelligence-tier" class="input-label">{{ t('intelligence.tier') }}</label><Select id="intelligence-tier" v-model="form.service_tier" :options="tierOptions" :disabled="saving" /></div></div>
          <div class="flex items-center justify-between gap-4 border-t border-gray-100 pt-4 dark:border-dark-700"><label for="intelligence-config-enabled" class="input-label">{{ t('intelligence.enabled') }}</label><Toggle id="intelligence-config-enabled" v-model="form.enabled" :disabled="saving" /></div>
          <div class="flex items-center justify-between gap-4"><div><label for="intelligence-schedule" class="input-label">{{ t('intelligence.schedule') }}</label><p class="mt-1 text-xs text-gray-500">{{ t('intelligence.scheduleHint') }}</p></div><Toggle id="intelligence-schedule" v-model="form.schedule_enabled" :disabled="saving" /></div>
          <div v-if="form.schedule_enabled"><label for="intelligence-interval" class="input-label">{{ t('intelligence.interval') }}</label><input id="intelligence-interval" v-model.number="form.interval_minutes" type="number" min="5" max="10080" step="1" required class="input w-full" :disabled="saving" /></div>
        </form>
        <template #footer><button type="button" class="btn btn-secondary" :disabled="saving" @click="closeForm">{{ t('common.cancel') }}</button><button type="submit" form="intelligence-config-form" class="btn btn-primary" :disabled="saving || !valid">{{ t(saving ? 'common.processing' : 'intelligence.save') }}</button></template>
      </BaseDialog>

      <BaseDialog :show="!!deleting" :title="t('intelligence.remove')" :close-on-escape="!removing" @close="!removing && (deleting = null)"><p class="text-sm">{{ t('intelligence.removeConfirm') }}</p><p class="mt-2 break-all font-semibold">{{ deleting?.group_name }} · {{ deleting?.model }}</p><template #footer><button type="button" class="btn btn-secondary" :disabled="removing" @click="deleting = null">{{ t('common.cancel') }}</button><button type="button" class="btn btn-primary" :disabled="removing" @click="remove">{{ t('common.delete') }}</button></template></BaseDialog>
      <BaseDialog :show="!!historyConfig" :title="`${t('intelligence.history')} · ${historyConfig?.model || ''}`" width="wide" @close="closeHistory"><p v-if="historyLoading" role="status" class="py-8 text-center text-gray-500">{{ t('common.loading') }}</p><p v-else-if="historyError" role="alert" class="text-sm text-red-600">{{ historyError }}</p><template v-else><IntelligenceResultBar :runs="history" @select="openRun" /><div class="mt-4 divide-y divide-gray-100 dark:divide-dark-700"><button v-for="run in history" :key="run.id" type="button" class="flex w-full items-center justify-between gap-4 py-3 text-left text-sm hover:text-primary-500" @click="openRun(run)"><span>{{ formatDateTime(run.created_at) }}</span><span class="inline-flex items-center gap-2"><span class="h-2.5 w-2.5 rounded-full" :class="resultColor(run)" />{{ t(`intelligence.statuses.${resultStatus(run)}`) }}</span></button></div></template></BaseDialog>
      <IntelligenceRunDialog :show="showDetail" :run="selectedRun" :loading="detailLoading" :error="detailError" @close="closeDetail" />
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import Toggle from '@/components/common/Toggle.vue'
import IntelligenceResultBar from '@/components/intelligence/IntelligenceResultBar.vue'
import IntelligenceRunDialog from '@/components/intelligence/IntelligenceRunDialog.vue'
import { resultColor, resultStatus } from '@/components/intelligence/results'
import { intelligenceAPI, type IntelligenceConfig, type IntelligenceConfigInput, type IntelligenceRun } from '@/api/intelligence'
import * as groupsAPI from '@/api/admin/groups'
import { keysAPI } from '@/api/keys'
import type { AdminGroup, ApiKey } from '@/types'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import { formatDateTime } from '@/utils/format'
import { provideIntelligenceResults } from '@/components/intelligence/useIntelligenceResults'

const { t } = useI18n()
const results = provideIntelligenceResults(true)
const appStore = useAppStore()
const moduleEnabled = computed(() => appStore.cachedPublicSettings?.intelligence_enabled === true)
const configs = ref<IntelligenceConfig[]>([])
const groups = ref<AdminGroup[]>([])
const loading = ref(false)
const error = ref('')
const showForm = ref(false)
const editingID = ref<number>()
const storedKey = ref(false)
const saving = ref(false)
const formError = ref('')
const models = ref<string[]>([])
const modelsLoading = ref(false)
const apiKeys = ref<ApiKey[]>([])
const selectedKeyID = ref<number | null>(null)
const keysLoading = ref(false)
const keysError = ref('')
const runningIDs = reactive(new Set<number>())
const deleting = ref<IntelligenceConfig | null>(null)
const removing = ref(false)
const historyConfig = ref<IntelligenceConfig | null>(null)
const history = ref<IntelligenceRun[]>([])
const historyLoading = ref(false)
const historyError = ref('')
const showDetail = ref(false)
const selectedRun = ref<IntelligenceRun | null>(null)
const detailLoading = ref(false)
const detailError = ref('')
let listController: AbortController | null = null
let historyController: AbortController | null = null
let detailController: AbortController | null = null
let keysController: AbortController | null = null
let modelRevision = 0
let disposed = false
let refreshTimer: ReturnType<typeof setInterval> | undefined

const defaultForm = (): IntelligenceConfigInput => ({ group_id: 0, model: '', benchmark: 'candy', base_url: '', api_key: '', protocol: 'responses', reasoning_effort: '', service_tier: '', enabled: true, schedule_enabled: false, interval_minutes: 60 })
const form = reactive<IntelligenceConfigInput>(defaultForm())
const groupedConfigs = computed(() => {
  const map = new Map<number, { id: number; name: string; configs: IntelligenceConfig[] }>()
  for (const config of configs.value) {
    if (!map.has(config.group_id)) map.set(config.group_id, { id: config.group_id, name: config.group_name, configs: [] })
    map.get(config.group_id)!.configs.push(config)
  }
  return [...map.values()]
})
const groupOptions = computed(() => {
  const options = groups.value.map(group => ({ value: group.id, label: group.name }))
  if (form.group_id && !options.some(option => option.value === form.group_id)) {
    const existing = configs.value.find(config => config.group_id === form.group_id)
    options.push({ value: form.group_id, label: existing?.group_name || `#${form.group_id}` })
  }
  return options
})
const modelOptions = computed(() => [...new Set([...models.value, ...(form.model ? [form.model] : [])])].map(model => ({ value: model, label: model })))
// 下拉框只显示名称与编号，密钥明文不进入选项标签或选项值。
const apiKeyOptions = computed(() => apiKeys.value.map(key => ({ value: key.id, label: `${key.name} (#${key.id})` })))
const benchmarkOptions = computed(() => [{ value: 'candy', label: t('intelligence.candy') }, { value: 'drawing', label: t('intelligence.drawing') }])
const protocolOptions = [{ value: 'responses', label: 'Responses' }, { value: 'chat_completions', label: 'Chat Completions' }]
const reasoningOptions = computed(() => [{ value: '', label: t('intelligence.default') }, ...(form.benchmark === 'candy' ? ['low', 'medium', 'high', 'xhigh', 'max', 'ultra'] : ['low', 'medium', 'high']).map(value => ({ value, label: value }))])
const tierOptions = computed(() => [{ value: '', label: t('intelligence.default') }, ...['priority', 'ultrafast'].map(value => ({ value, label: value }))])
const valid = computed(() => {
  let validURL = false
  try { const url = new URL(form.base_url.trim()); validURL = url.protocol === 'https:' && !url.username && !url.password && !url.search && !url.hash } catch { /* 输入过程中允许地址暂时不完整。 */ }
  return form.group_id > 0 && !!form.model.trim() && validURL && (storedKey.value || !!form.api_key.trim()) && Number.isInteger(form.interval_minutes) && form.interval_minutes >= 5 && form.interval_minutes <= 10080
})
const isBusy = (config: IntelligenceConfig) => !!config.latest_run && ['queued', 'submitting', 'running'].includes(config.latest_run.status)

async function load() {
  listController?.abort()
  const controller = new AbortController()
  listController = controller
  loading.value = true
  error.value = ''
  try { const result = await intelligenceAPI.configs(controller.signal); if (!controller.signal.aborted) configs.value = result }
  catch (err) { if (!controller.signal.aborted) error.value = extractApiErrorMessage(err, t('intelligence.loadFailed')) }
  finally { if (!controller.signal.aborted) loading.value = false }
}
async function loadGroups() {
  try { const result = await groupsAPI.getAll(); if (!disposed) groups.value = result }
  catch (err) { if (!disposed) error.value = extractApiErrorMessage(err, t('intelligence.loadFailed')) }
}
async function loadModels() {
  const revision = ++modelRevision
  models.value = []
  if (!form.group_id) { modelsLoading.value = false; return }
  modelsLoading.value = true
  try { const result = await groupsAPI.getModelsListCandidates(form.group_id); if (revision === modelRevision && !disposed) models.value = result }
  catch (err) { if (revision === modelRevision && !disposed) formError.value = extractApiErrorMessage(err, t('intelligence.loadFailed')) }
  finally { if (revision === modelRevision && !disposed) modelsLoading.value = false }
}
function useCurrentSite() { form.base_url = `${window.location.origin}/v1` }
function canUseKey(key: ApiKey, groupID: number) {
  // 普通 Key 默认开启停用分组自动降级，不应因此被当成复合或智能路由 Key 排除。
  return key.group_id === groupID && key.status === 'active' && !!key.key && !key.team_id && key.scope !== 'team'
    && !key.is_composite && !key.smart_routing && !key.composite_groups?.length && !key.smart_routing_group_ids?.length
    && (!key.expires_at || Date.parse(key.expires_at) > Date.now())
    && (key.quota <= 0 || key.quota_used < key.quota)
}
function clearAPIKeys() {
  keysController?.abort()
  keysController = null
  apiKeys.value = []
  selectedKeyID.value = null
  keysLoading.value = false
  keysError.value = ''
}
async function loadAPIKeys() {
  // 分页取完所选分组的个人 Key；切换分组或关闭弹窗后忽略旧请求。
  const hadSelectedKey = selectedKeyID.value !== null
  clearAPIKeys()
  if (hadSelectedKey) form.api_key = ''
  const groupID = form.group_id
  if (!showForm.value || !groupID) return
  const controller = new AbortController()
  keysController = controller
  keysLoading.value = true
  const collected = new Map<number, ApiKey>()
  try {
    for (let page = 1; ; page++) {
      const result = await keysAPI.list(page, 100, { scope: 'personal', group_id: groupID, status: 'active', sort_by: 'id', sort_order: 'asc' }, { signal: controller.signal })
      if (controller.signal.aborted || disposed || !showForm.value || form.group_id !== groupID) return
      for (const key of result.items) if (canUseKey(key, groupID)) collected.set(key.id, key)
      if (!result.items.length || page * result.page_size >= result.total) break
    }
    apiKeys.value = [...collected.values()]
  } catch (err) {
    if (!controller.signal.aborted && !disposed) keysError.value = extractApiErrorMessage(err, t('intelligence.loadApiKeysFailed')) || t('intelligence.loadApiKeysFailed')
  } finally {
    if (!controller.signal.aborted && !disposed) keysLoading.value = false
  }
}
function selectApiKey(id: string | number | boolean | null | undefined) {
  const key = apiKeys.value.find(item => item.id === id && canUseKey(item, form.group_id))
  selectedKeyID.value = key?.id ?? null
  form.api_key = key?.key ?? ''
}
function openForm(config?: IntelligenceConfig, groupID = 0) {
  editingID.value = config?.id
  storedKey.value = config?.api_key_configured ?? false
  Object.assign(form, defaultForm(), config ? { group_id: config.group_id, model: config.model, benchmark: config.benchmark, base_url: config.base_url, protocol: config.protocol, reasoning_effort: config.reasoning_effort, service_tier: config.service_tier, enabled: config.enabled, schedule_enabled: config.schedule_enabled, interval_minutes: config.interval_minutes } : { group_id: groupID })
  formError.value = ''
  showForm.value = true
  if (!groups.value.length) void loadGroups()
  void loadModels()
  void loadAPIKeys()
}
function closeForm() { if (!saving.value) { showForm.value = false; form.api_key = ''; clearAPIKeys(); ++modelRevision } }
async function save() {
  if (!valid.value || saving.value) return
  saving.value = true
  formError.value = ''
  try {
    await intelligenceAPI.save({ ...form, model: form.model.trim(), base_url: form.base_url.trim(), api_key: form.api_key.trim() }, editingID.value)
    form.api_key = ''
    showForm.value = false
    clearAPIKeys()
    appStore.showSuccess(t('intelligence.saved'))
    await load()
  } catch (err) { formError.value = extractApiErrorMessage(err, t('intelligence.saveFailed')) }
  finally { saving.value = false }
}
async function runTest(config: IntelligenceConfig) {
  if (!moduleEnabled.value || !config.enabled || runningIDs.has(config.id) || isBusy(config)) return
  runningIDs.add(config.id)
  try { const run = await intelligenceAPI.run(config.id); config.latest_run = run; appStore.showSuccess(t('intelligence.submitted')) }
  catch (err) { appStore.showError(extractApiErrorMessage(err, t('intelligence.runFailed'))) }
  finally { runningIDs.delete(config.id) }
}
async function remove() {
  if (!deleting.value || removing.value) return
  removing.value = true
  try { await intelligenceAPI.remove(deleting.value.id); deleting.value = null; appStore.showSuccess(t('intelligence.deleted')); await load() }
  catch (err) { appStore.showError(extractApiErrorMessage(err, t('intelligence.deleteFailed'))) }
  finally { removing.value = false }
}
async function openHistory(config: IntelligenceConfig) {
  historyController?.abort()
  const controller = new AbortController()
  historyController = controller
  historyConfig.value = config
  history.value = []
  historyLoading.value = true
  historyError.value = ''
  try { const runs = await intelligenceAPI.history(config.id, controller.signal); if (!controller.signal.aborted) history.value = runs }
  catch (err) { if (!controller.signal.aborted) historyError.value = extractApiErrorMessage(err, t('intelligence.loadFailed')) }
  finally { if (!controller.signal.aborted) historyLoading.value = false }
}
function closeHistory() { historyController?.abort(); historyConfig.value = null; history.value = [] }
async function openRun(run: IntelligenceRun) {
  detailController?.abort()
  const controller = new AbortController()
  detailController = controller
  selectedRun.value = null
  detailError.value = ''
  detailLoading.value = true
  showDetail.value = true
  try { const result = await results.detail(run.id); if (!controller.signal.aborted) selectedRun.value = result }
  catch (err) { if (!controller.signal.aborted) detailError.value = extractApiErrorMessage(err, t('intelligence.loadFailed')) }
  finally { if (!controller.signal.aborted) detailLoading.value = false }
}
function closeDetail() { detailController?.abort(); showDetail.value = false; selectedRun.value = null }
watch(() => form.group_id, () => {
  // 换组立即清空手填或选择的凭据，防止旧分组密钥被提交。
  form.api_key = ''
  clearAPIKeys()
  if (showForm.value) { void loadModels(); void loadAPIKeys() }
}, { flush: 'sync' })
watch(() => form.benchmark, benchmark => {
  if (benchmark === 'candy') form.protocol = 'responses'
  if (!reasoningOptions.value.some(option => option.value === form.reasoning_effort)) form.reasoning_effort = ''
})
onMounted(() => {
  void load()
  void loadGroups()
  // 只轮询已开始的检测，页面刷新和定时器绝不提交新的付费测试。
  refreshTimer = setInterval(() => { if (!document.hidden && !loading.value && !showForm.value && configs.value.some(isBusy)) void load() }, 15000)
})
onBeforeUnmount(() => { disposed = true; listController?.abort(); historyController?.abort(); detailController?.abort(); clearAPIKeys(); if (refreshTimer) clearInterval(refreshTimer); form.api_key = '' })
</script>
