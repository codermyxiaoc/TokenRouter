<template>
  <AppLayout>
    <div v-if="moduleEnabled" class="space-y-6">
      <div class="flex flex-wrap items-start justify-between gap-3">
        <div><h1 class="page-title">{{ t('chanshui.title') }}</h1><p class="page-description">{{ t('chanshui.description') }}</p></div>
        <div class="flex gap-2"><button type="button" class="btn btn-secondary btn-sm" :disabled="loading" @click="refresh">{{ t('common.refresh') }}</button><button type="button" class="btn btn-primary btn-sm" @click="openForm()">{{ t('chanshui.add') }}</button></div>
      </div>
      <p class="rounded-lg bg-blue-50 p-4 text-sm text-blue-800 dark:bg-blue-900/20 dark:text-blue-200">{{ t('chanshui.externalNotice') }}</p>
      <p v-if="error" role="alert" class="rounded-lg bg-red-50 p-4 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300">{{ error }}</p>
      <p v-if="targetsError && !showForm" role="alert" class="rounded-lg bg-red-50 p-4 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300">{{ targetsError }}</p>
      <div v-if="loading && !configs.length" role="status" class="py-16 text-center text-gray-500">{{ t('common.loading') }}</div>
      <div v-if="!loading && !error && !configs.length" class="card p-12 text-center"><h2 class="font-semibold">{{ t('chanshui.empty') }}</h2><p class="mt-2 text-sm text-gray-500">{{ t('chanshui.emptyHint') }}</p></div>
      <article v-for="account in accountGroups" :key="account.id" class="card overflow-hidden" :data-account-id="account.id">
        <header class="border-b border-gray-100 px-5 py-4 dark:border-dark-700"><h2 class="break-words text-lg font-semibold">{{ account.name }} <span class="text-sm font-normal text-gray-400">#{{ account.id }}</span></h2></header>
        <div class="divide-y divide-gray-100 dark:divide-dark-700">
          <section v-for="config in account.configs" :key="config.id" class="min-w-0 space-y-4 p-5" :data-config-id="config.id">
            <div class="flex flex-wrap items-center justify-between gap-4">
              <div class="min-w-0"><h3 class="break-all font-semibold">{{ config.model }}</h3><p class="mt-2 text-xs text-gray-500">{{ config.protocol === 'auto' ? t('chanshui.protocolAuto') : config.protocol }} · {{ config.schedule_enabled ? t('chanshui.everyMinutes', { minutes: config.interval_minutes }) : t('chanshui.manual') }}<span v-if="!config.enabled"> · {{ t('common.inactive') }}</span></p></div>
              <div class="flex flex-wrap gap-2">
                <button type="button" class="btn btn-primary btn-sm" :disabled="!moduleEnabled || !config.enabled || busy(config) || runningIDs.has(config.id) || !!unsupported(config)" @click="runTest(config)">{{ t('chanshui.run') }}</button>
                <button type="button" class="btn btn-secondary btn-sm" @click="openHistory(config)">{{ t('chanshui.history') }}</button>
                <button type="button" class="btn btn-secondary btn-sm" :disabled="busy(config)" @click="openForm(config)">{{ t('common.edit') }}</button>
                <button type="button" class="btn btn-secondary btn-sm text-red-600" :disabled="busy(config)" @click="deleting = config">{{ t('common.delete') }}</button>
              </div>
            </div>
            <p v-if="unsupported(config)" class="text-sm text-amber-700 dark:text-amber-300">{{ unsupported(config) }}</p>
            <p v-if="config.disable_on_adulteration || config.minimum_score !== null" class="text-xs text-amber-700 dark:text-amber-300">{{ t('chanshui.ruleSummary', { adulteration: config.disable_on_adulteration ? t('chanshui.adulterationRule') : t('chanshui.noAdulterationRule'), score: config.minimum_score ?? '—' }) }}</p>
            <p v-if="config.auto_recover" class="text-xs text-gray-500">{{ t('chanshui.recoverySummary') }}</p>
            <p v-if="config.recovery_pending" class="text-xs text-blue-700 dark:text-blue-300">{{ t('chanshui.recoveryPending') }}</p>
            <button v-if="config.latest_run" type="button" class="flex flex-wrap items-center gap-2 text-left text-sm" @click="openDetail(config.latest_run.id)"><span class="h-2.5 w-2.5 rounded-full" :class="runColor(config.latest_run)" /><span>{{ runLabel(config.latest_run) }}</span><span class="text-gray-500">{{ scoreLabel(config.latest_run) }} · {{ formatDateTime(config.latest_run.created_at) }}</span><span v-if="config.latest_run.action_status !== 'none'">· {{ t(`chanshui.actions.${config.latest_run.action_status}`) }}</span></button>
            <div v-if="config.latest_run?.status === 'completed' && !config.latest_run.score_complete" class="space-y-1 text-sm text-amber-700 dark:text-amber-300"><p v-if="config.latest_run.summary" class="break-words">{{ config.latest_run.summary }}</p><button type="button" class="underline underline-offset-2" @click="openDetail(config.latest_run.id)">{{ t('chanshui.viewIncomplete') }}</button></div>
            <ChanshuiResultBar :runs="configRuns(config)" @select="openDetail($event.id)" />
          </section>
        </div>
      </article>

      <BaseDialog :show="showForm" :title="t(editingID ? 'chanshui.edit' : 'chanshui.add')" width="wide" :close-on-escape="!saving" @close="closeForm">
        <form id="chanshui-config-form" class="space-y-5" @submit.prevent="save">
          <p v-if="formError" role="alert" class="text-sm text-red-600">{{ formError }}</p>
          <p v-if="targetsError" role="alert" class="text-sm text-red-600">{{ targetsError }} <button type="button" class="underline" :disabled="targetsLoading" @click="loadTargets">{{ t('common.refresh') }}</button></p>
          <div><label for="chanshui-platform" class="input-label">{{ t('chanshui.platform') }}</label><Select id="chanshui-platform" :model-value="selectedPlatform" :options="platformOptions" :disabled="saving || !!editingID || targetsLoading" searchable :placeholder="t('chanshui.platformPlaceholder')" @update:model-value="selectPlatform" /></div>
          <div class="grid gap-4 sm:grid-cols-2">
            <div><label for="chanshui-account" class="input-label">{{ t('chanshui.account') }}</label><Select id="chanshui-account" v-model="form.account_id" :options="accountOptions" :disabled="saving || !!editingID || targetsLoading || !selectedPlatform" searchable :placeholder="t('chanshui.accountPlaceholder')" /></div>
            <div><label for="chanshui-model" class="input-label">{{ t('chanshui.model') }}</label><Select id="chanshui-model" v-model="form.model" :options="modelOptions" :disabled="saving || !!editingID || !selectedTarget?.supported" searchable creatable :placeholder="t('chanshui.modelPlaceholder')" /></div>
          </div>
          <p v-if="selectedTarget && !selectedTarget.supported" class="text-sm text-amber-700 dark:text-amber-300">{{ selectedTarget.reason || t('chanshui.unsupported') }}</p>
          <p v-if="selectedTarget?.warning" class="text-sm text-amber-700 dark:text-amber-300">{{ selectedTarget.warning }}</p>
          <p class="text-xs text-gray-500">{{ t('chanshui.accountHint') }}</p>
          <div class="grid gap-4 sm:grid-cols-2">
            <div><label for="chanshui-protocol" class="input-label">{{ t('chanshui.protocol') }}</label><Select id="chanshui-protocol" v-model="form.protocol" :options="protocolOptions" :disabled="saving || !!editingID" /><p class="mt-2 text-xs text-gray-500">{{ t('chanshui.protocolHint') }}</p></div>
            <div><label for="chanshui-timeout" class="input-label">{{ t('chanshui.timeout') }}</label><input id="chanshui-timeout" v-model.number="form.timeout_seconds" class="input w-full" type="number" min="10" max="600" step="1" required :disabled="saving" /></div>
          </div>
          <fieldset><legend class="input-label">{{ t('chanshui.sections') }}</legend><div class="mt-2 grid grid-cols-2 gap-3 sm:grid-cols-4"><label v-for="section in chanshuiSections" :key="section" class="flex items-center gap-2 text-sm"><input v-model="form.sections" :value="section" type="checkbox" :disabled="saving" :data-section="section" />{{ t(`chanshui.sectionNames.${section}`) }}</label></div></fieldset>
          <div class="flex items-center justify-between gap-4 border-t border-gray-100 pt-4 dark:border-dark-700"><label for="chanshui-enabled" class="input-label">{{ t('chanshui.enabled') }}</label><Toggle id="chanshui-enabled" v-model="form.enabled" :disabled="saving" /></div>
          <div class="flex items-center justify-between gap-4"><label for="chanshui-schedule" class="input-label">{{ t('chanshui.schedule') }}</label><Toggle id="chanshui-schedule" v-model="form.schedule_enabled" :disabled="saving || !form.enabled" /></div>
          <div v-if="form.schedule_enabled"><label for="chanshui-interval" class="input-label">{{ t('chanshui.interval') }}</label><input id="chanshui-interval" v-model.number="form.interval_minutes" type="number" min="5" max="10080" step="1" required class="input w-full" :disabled="saving" /></div>
          <fieldset class="space-y-4 rounded-lg border border-amber-200 p-4 dark:border-amber-800"><legend class="px-1 text-sm font-medium">{{ t('chanshui.disableRules') }}</legend><p class="text-xs text-gray-600 dark:text-gray-400">{{ t('chanshui.disableHint') }}</p><div class="flex items-center justify-between gap-4"><label for="chanshui-adulteration" class="input-label">{{ t('chanshui.disableAdulteration') }}</label><Toggle id="chanshui-adulteration" v-model="form.disable_on_adulteration" :disabled="saving" /></div><div><label for="chanshui-score" class="input-label">{{ t('chanshui.minimumScore') }}</label><input id="chanshui-score" v-model="minimumScore" class="input w-full" type="number" min="0" max="100" step="any" :disabled="saving" :placeholder="t('chanshui.scorePlaceholder')" /></div></fieldset>
          <div class="space-y-2 rounded-lg border border-gray-200 p-4 dark:border-dark-700"><div class="flex items-center justify-between gap-4"><label for="chanshui-recover" class="input-label">{{ t('chanshui.autoRecover') }}</label><Toggle id="chanshui-recover" v-model="form.auto_recover" :disabled="saving" /></div><p class="text-xs text-gray-500">{{ t('chanshui.autoRecoverHint') }}</p><p v-if="form.auto_recover && !recoveryValid" role="alert" class="text-sm text-amber-700 dark:text-amber-300">{{ t('chanshui.autoRecoverRequirements') }}</p></div>
          <p class="text-xs text-gray-500">{{ t('chanshui.externalNotice') }}</p>
        </form>
        <template #footer><button type="button" class="btn btn-secondary" :disabled="saving" @click="closeForm">{{ t('common.cancel') }}</button><button type="submit" form="chanshui-config-form" class="btn btn-primary" :disabled="saving || !valid">{{ t(saving ? 'common.processing' : 'common.save') }}</button></template>
      </BaseDialog>

      <BaseDialog :show="!!deleting" :title="t('chanshui.remove')" :close-on-escape="!removing" @close="!removing && (deleting = null)"><p class="text-sm">{{ t('chanshui.removeConfirm') }}</p><p class="mt-2 break-all font-semibold">{{ deleting?.account_name }} · {{ deleting?.model }}</p><p v-if="deleteError" role="alert" class="mt-2 text-sm text-red-600">{{ deleteError }}</p><template #footer><button type="button" class="btn btn-secondary" :disabled="removing" @click="deleting = null">{{ t('common.cancel') }}</button><button type="button" class="btn btn-primary" :disabled="removing" @click="remove">{{ t('common.delete') }}</button></template></BaseDialog>
      <BaseDialog :show="!!historyConfig" :title="`${t('chanshui.history')} · ${historyConfig?.model || ''}`" width="wide" @close="closeHistory">
        <p v-if="historyLoading" role="status" class="py-8 text-center text-gray-500">{{ t('common.loading') }}</p><p v-else-if="historyError" role="alert" class="text-sm text-red-600">{{ historyError }}</p>
        <template v-else><p class="mb-3 text-xs text-gray-500">{{ t('chanshui.historyHint') }}</p><ChanshuiResultBar :runs="history" @select="openDetail($event.id)" /><div class="mt-4 divide-y divide-gray-100 dark:divide-dark-700"><button v-for="run in history" :key="run.id" type="button" class="flex w-full flex-wrap items-center justify-between gap-2 py-3 text-left text-sm" @click="openDetail(run.id)"><span>{{ formatDateTime(run.created_at) }}</span><span>{{ runLabel(run) }} · {{ scoreLabel(run) }}</span><span>{{ t(`chanshui.actions.${run.action_status}`) }}</span></button></div></template>
      </BaseDialog>
      <BaseDialog :show="showDetail" :title="t('chanshui.details')" width="wide" @close="closeDetail">
        <p v-if="detailLoading" role="status" class="py-8 text-center">{{ t('common.loading') }}</p><p v-else-if="detailError" role="alert" class="text-sm text-red-600">{{ detailError }}</p>
        <div v-else-if="selectedRun" class="space-y-4">
          <div class="flex flex-wrap items-center gap-3"><span class="h-3 w-3 rounded-full" :class="runColor(selectedRun)" /><h3 class="font-semibold">{{ runLabel(selectedRun) }}</h3><span>{{ scoreLabel(selectedRun) }}</span></div>
          <p class="break-all text-sm text-gray-500">{{ selectedRun.model }} · {{ formatDateTime(selectedRun.created_at) }}</p>
          <p v-if="selectedRun.summary" class="whitespace-pre-wrap break-words text-sm">{{ selectedRun.summary }}</p>
          <p v-if="selectedRun.error_message" role="alert" class="whitespace-pre-wrap break-words text-sm text-red-600">{{ selectedRun.error_message }}</p>
          <p v-if="!selectedRun.score_complete" class="text-sm text-amber-700 dark:text-amber-300">{{ t('chanshui.incompleteHint') }}</p>
          <p v-if="unfinishedIQCount" class="text-sm text-amber-700 dark:text-amber-300">{{ t('chanshui.iqIncomplete', { count: unfinishedIQCount }) }}</p>
          <div class="rounded-lg bg-gray-50 p-3 text-sm dark:bg-dark-800"><strong>{{ t('chanshui.actionResult') }}: {{ t(`chanshui.actions.${selectedRun.action_status}`) }}</strong><p v-if="selectedRun.action_reason" class="mt-1 break-words">{{ selectedRun.action_reason }}</p></div>
          <div v-if="selectedRun.report?.fingerprint" class="space-y-1 text-sm"><h4 class="font-semibold">{{ t('chanshui.sectionNames.fingerprint') }}</h4><p>{{ selectedRun.report.fingerprint.requested_family || '—' }} → {{ selectedRun.report.fingerprint.family || '—' }}</p><p>{{ t('chanshui.samples', { count: selectedRun.report.fingerprint.samples }) }} · {{ t(selectedRun.report.fingerprint.low_confidence ? 'chanshui.lowConfidence' : 'chanshui.fingerprintObserved') }}</p></div>
          <div v-if="selectedRun.report?.probes?.length" class="overflow-x-auto"><table class="w-full text-left text-sm"><thead><tr class="border-b"><th class="py-2">{{ t('chanshui.probe') }}</th><th>{{ t('chanshui.status') }}</th><th>{{ t('chanshui.duration') }}</th><th>{{ t('chanshui.probeExplanation') }}</th></tr></thead><tbody><tr v-for="(probe, index) in selectedRun.report.probes" :key="`${probe.id}-${index}`" class="border-b border-gray-100 dark:border-dark-700"><td class="break-words py-2">{{ probe.name || probe.id }}</td><td>{{ probe.status }}</td><td>{{ typeof probe.ms === 'number' ? `${probe.ms} ms` : '—' }}</td><td class="min-w-40 break-words py-2">{{ probeExplanation(probe, selectedRun) || '—' }}</td></tr></tbody></table></div>
          <div v-if="selectedRun.report?.parts?.length" class="overflow-x-auto"><table class="w-full text-left text-sm"><thead><tr class="border-b"><th class="py-2">{{ t('chanshui.sections') }}</th><th>{{ t('chanshui.score') }}</th><th>{{ t('chanshui.contribution') }}</th></tr></thead><tbody><tr v-for="part in selectedRun.report.parts" :key="part.key" :data-part-key="part.key" class="border-b border-gray-100 dark:border-dark-700"><td class="py-2">{{ sectionLabel(part.key) }}</td><td>{{ !part.applicable ? t('chanshui.notApplicable') : hasPartScore(part, selectedRun.report) ? `${part.points} / ${part.max_points}` : t('chanshui.incomplete') }}</td><td>{{ hasPartScore(part, selectedRun.report) ? part.contribution : '—' }}</td></tr></tbody></table></div>
          <div v-if="selectedRun.report?.skipped?.length" class="space-y-1 break-words text-sm text-gray-500"><h4 class="font-semibold">{{ t('chanshui.skipped') }}</h4><p v-for="(skipped, index) in selectedRun.report.skipped" :key="`${skipped.id}-${index}`">{{ sectionLabel(skipped.id) }}: {{ skipped.reason }}</p></div>
        </div>
      </BaseDialog>
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
import ChanshuiResultBar from '@/components/chanshui/ChanshuiResultBar.vue'
import { hasPartScore, isActiveRun as active, recentRuns, runColor, runLabelKey } from '@/components/chanshui/results'
import { chanshuiAPI, chanshuiSections, type ChanshuiConfig, type ChanshuiConfigInput, type ChanshuiRun, type ChanshuiTarget } from '@/api/admin/chanshui'
import { useAppStore } from '@/stores/app'
import { useAdminSettingsStore } from '@/stores/adminSettings'
import { useRouter } from 'vue-router'
import { extractApiErrorMessage } from '@/utils/apiError'
import { formatDateTime } from '@/utils/format'
import { platformLabel } from '@/utils/platformColors'

const { t } = useI18n()
const appStore = useAppStore()
const adminSettingsStore = useAdminSettingsStore()
const router = useRouter()
// @project-doc docs/domains/chanshui_tests.md#chanshui_access
const configs = ref<ChanshuiConfig[]>([])
// 账号只展示一个标题，配置仍按编号隔离，不合并同模型不同协议的历史。
const accountGroups = computed(() => {
  const groups = new Map<number, { id: number; name: string; configs: ChanshuiConfig[] }>()
  for (const config of configs.value) {
    let group = groups.get(config.account_id)
    if (!group) {
      group = { id: config.account_id, name: config.account_name || `#${config.account_id}`, configs: [] }
      groups.set(config.account_id, group)
    }
    group.configs.push(config)
  }
  return [...groups.values()]
})
// 列表一次返回安全摘要，刷新不会为每个模型单独请求历史或详情。
function configRuns(config: ChanshuiConfig) {
  return recentRuns([...(config.recent_runs || []), ...(config.latest_run ? [config.latest_run] : [])]
    .filter(run => run.config_id === config.id))
}
const targets = ref<ChanshuiTarget[]>([])
const moduleEnabled = ref(adminSettingsStore.chanshuiEnabled === true)
const loading = ref(false)
const error = ref('')
const targetsLoading = ref(false)
const targetsError = ref('')
const showForm = ref(false)
const editingID = ref<number>()
const saving = ref(false)
const formError = ref('')
const minimumScore = ref<string | number>('')
const runningIDs = reactive(new Set<number>())
const deleting = ref<ChanshuiConfig | null>(null)
const removing = ref(false)
const deleteError = ref('')
const historyConfig = ref<ChanshuiConfig | null>(null)
const history = ref<ChanshuiRun[]>([])
const historyLoading = ref(false)
const historyError = ref('')
const showDetail = ref(false)
const selectedRun = ref<ChanshuiRun | null>(null)
// 旧记录没有保存明确原因时，也能从安全摘要识别已结束却未执行的推理题。
const unfinishedIQCount = computed(() => selectedRun.value?.status === 'completed'
  ? new Set(selectedRun.value.report?.probes?.filter(probe => /^Q[1-8]$/.test(probe.id) && probe.status !== 'done').map(probe => probe.id)).size
  : 0)
const detailLoading = ref(false)
const detailError = ref('')
let detailID = ''
let disposed = false
let refreshTimer: ReturnType<typeof setInterval> | undefined
let listController: AbortController | null = null
let targetController: AbortController | null = null
let historyController: AbortController | null = null
let detailController: AbortController | null = null

const defaults = (): ChanshuiConfigInput => ({ account_id: 0, model: '', protocol: 'auto', sections: [...chanshuiSections], timeout_seconds: 360, enabled: true, schedule_enabled: false, interval_minutes: 60, disable_on_adulteration: false, auto_recover: false, minimum_score: null })
const form = reactive(defaults())
// 平台筛选只作用于管理员候选列表，提交仍只携带账号编号。
const selectedPlatform = ref('')
const apiKeyTargets = computed(() => targets.value.filter(target => target.type === 'apikey'))
const selectedTarget = computed(() => apiKeyTargets.value.find(target => target.id === form.account_id))
const platformOptions = computed(() => {
  const counts = new Map<string, number>()
  for (const target of apiKeyTargets.value) counts.set(target.platform, (counts.get(target.platform) || 0) + 1)
  return [...counts].map(([platform, count]) => ({ value: platform, label: `${platform === 'anthropic' ? 'Claude' : platform === 'openai' ? 'GPT / OpenAI' : platformLabel(platform)} (${count})` }))
})
function selectPlatform(value: string | number | boolean | null) {
  if (editingID.value || saving.value) return
  selectedPlatform.value = typeof value === 'string' ? value : ''
  form.account_id = 0
  form.model = ''
  form.protocol = 'auto'
  formError.value = ''
}
const accountOptions = computed(() => {
  const options = apiKeyTargets.value.filter(target => target.platform === selectedPlatform.value).map(target => ({ value: target.id, label: `${target.name} (#${target.id}) · ${target.platform}${target.supported ? '' : ` · ${target.reason || t('chanshui.unsupported')}`}` }))
  if (form.account_id && !options.some(option => option.value === form.account_id)) options.push({ value: form.account_id, label: configs.value.find(config => config.id === editingID.value)?.account_name || `#${form.account_id}` })
  return options
})
const modelOptions = computed(() => [...new Set([...(selectedTarget.value?.models || []), ...(form.model ? [form.model] : [])])].map(model => ({ value: model, label: model })))
const protocolOptions = computed(() => [{ value: 'auto', label: t('chanshui.protocolAuto') }, { value: 'chat', label: 'Chat Completions' }, { value: 'anthropic', label: 'Anthropic Messages' }, { value: 'openai', label: 'OpenAI Responses' }])
const recoveryValid = computed(() => !form.auto_recover || (form.enabled && form.schedule_enabled && form.sections.includes('fingerprint') && (form.disable_on_adulteration || minimumScore.value !== '')))
const valid = computed(() => {
  const score = minimumScore.value === '' ? null : Number(minimumScore.value)
  // 允许停用已失效账号的旧配置，启用或提交新配置必须由目标接口确认可检测。
  const supported = selectedTarget.value?.supported || (!!editingID.value && !form.enabled && !form.schedule_enabled)
  const samePlatform = !!editingID.value || (!!selectedPlatform.value && selectedTarget.value?.platform === selectedPlatform.value)
  return supported && samePlatform && form.account_id > 0 && !!form.model.trim() && form.model.trim().length <= 255 && !/[\r\n\0]/.test(form.model) && form.sections.length > 0
    && Number.isInteger(form.timeout_seconds) && form.timeout_seconds >= 10 && form.timeout_seconds <= 600
    && Number.isInteger(form.interval_minutes) && form.interval_minutes >= 5 && form.interval_minutes <= 10080
    && (score === null || (Number.isFinite(score) && score >= 0 && score <= 100))
    && recoveryValid.value
})
const busy = (config: ChanshuiConfig) => active(config.latest_run)
function unsupported(config: ChanshuiConfig) {
  if (targetsLoading.value) return t('common.loading')
  const target = targets.value.find(item => item.id === config.account_id)
  return targetsError.value || !target || !target.supported ? target?.reason || t('chanshui.unsupported') : ''
}
function runLabel(run: ChanshuiRun) { return t(runLabelKey(run)) }
function scoreLabel(run: ChanshuiRun) { return run.score === null || !Number.isFinite(run.score) ? t('chanshui.noScore') : t(run.score_complete ? 'chanshui.scoreValue' : 'chanshui.partialScore', { score: run.score }) }
function sectionLabel(section: string) { return (chanshuiSections as readonly string[]).includes(section) ? t(`chanshui.sectionNames.${section}`) : section }
function probeExplanation(probe: NonNullable<NonNullable<ChanshuiRun['report']>['probes']>[number], run: ChanshuiRun) {
  if (run.status === 'completed' && probe.status === 'queued') return t('chanshui.probeNotExecuted')
  if (run.status === 'completed' && probe.status === 'running') return t('chanshui.probeNotFinished')
  return probe.summary || ''
}
async function load(silent = false) {
  listController?.abort(); const controller = new AbortController(); listController = controller
  if (!silent) loading.value = true
  try {
    const data = await chanshuiAPI.configs(controller.signal)
    if (!controller.signal.aborted && !disposed) {
      moduleEnabled.value = data.enabled === true
      adminSettingsStore.chanshuiEnabled = moduleEnabled.value
      // 其它页面关闭总开关后，立即收起当前检测页并回到开关所在位置。
      if (!moduleEnabled.value) {
        configs.value = []
        showForm.value = false
        showDetail.value = false
        historyConfig.value = null
        void router.replace('/admin/settings?tab=features')
        return
      }
      configs.value = data.configs
      error.value = ''
    }
  }
  catch (err) { if (!controller.signal.aborted && !disposed) error.value = extractApiErrorMessage(err, t('chanshui.loadFailed')) }
  finally { if (!controller.signal.aborted && !disposed) loading.value = false }
}
async function loadTargets() {
  targetController?.abort(); const controller = new AbortController(); targetController = controller; targetsLoading.value = true; targetsError.value = ''
  try { const data = await chanshuiAPI.targets(controller.signal); if (!controller.signal.aborted && !disposed) targets.value = data }
  catch (err) { if (!controller.signal.aborted && !disposed) targetsError.value = extractApiErrorMessage(err, t('chanshui.loadFailed')) }
  finally { if (!controller.signal.aborted && !disposed) targetsLoading.value = false }
}
function refresh() { void load(); void loadTargets() }
function openForm(config?: ChanshuiConfig) {
  editingID.value = config?.id
  selectedPlatform.value = config ? apiKeyTargets.value.find(target => target.id === config.account_id)?.platform || '' : ''
  const initial = config ? { account_id: config.account_id, model: config.model, protocol: config.protocol, sections: [...config.sections], timeout_seconds: config.timeout_seconds, enabled: config.enabled, schedule_enabled: config.enabled && config.schedule_enabled, interval_minutes: config.interval_minutes, disable_on_adulteration: config.disable_on_adulteration, auto_recover: config.auto_recover === true, minimum_score: config.minimum_score } : defaults()
  Object.assign(form, initial); minimumScore.value = initial.minimum_score ?? ''; formError.value = ''; showForm.value = true
  if (!targets.value.length && !targetsLoading.value) void loadTargets()
}
function closeForm() { if (!saving.value) showForm.value = false }
async function save() {
  if (saving.value || !valid.value) return
  saving.value = true; formError.value = ''
  try { await chanshuiAPI.save({ ...form, model: form.model.trim(), sections: [...form.sections], minimum_score: minimumScore.value === '' ? null : Number(minimumScore.value) }, editingID.value); if (!disposed) { showForm.value = false; appStore.showSuccess(t('chanshui.saved')); await load() } }
  catch (err) { if (!disposed) formError.value = extractApiErrorMessage(err, t('chanshui.saveFailed')) }
  finally { if (!disposed) saving.value = false }
}
async function runTest(config: ChanshuiConfig) {
  if (!moduleEnabled.value || !config.enabled || busy(config) || runningIDs.has(config.id) || unsupported(config)) return
  runningIDs.add(config.id)
  try { const run = await chanshuiAPI.run(config.id); if (!disposed) { const current = configs.value.find(item => item.id === config.id); if (current) current.latest_run = run; appStore.showSuccess(t('chanshui.queued')) } }
  catch (err) { if (!disposed) appStore.showError(extractApiErrorMessage(err, t('chanshui.runFailed'))) }
  finally { runningIDs.delete(config.id) }
}
async function remove() {
  if (!deleting.value || removing.value) return
  removing.value = true; deleteError.value = ''
  try { await chanshuiAPI.remove(deleting.value.id); if (!disposed) { deleting.value = null; await load() } }
  catch (err) { if (!disposed) deleteError.value = extractApiErrorMessage(err, t('chanshui.removeFailed')) }
  finally { if (!disposed) removing.value = false }
}
async function openHistory(config: ChanshuiConfig) { historyConfig.value = config; history.value = []; await loadHistory() }
async function loadHistory(silent = false) {
  if (!historyConfig.value) return
  historyController?.abort(); const controller = new AbortController(); historyController = controller
  if (!silent) historyLoading.value = true
  try { const data = await chanshuiAPI.history(historyConfig.value.id, controller.signal); if (!controller.signal.aborted && !disposed) { history.value = data.slice(0, 60); historyError.value = '' } }
  catch (err) { if (!controller.signal.aborted && !disposed) historyError.value = extractApiErrorMessage(err, t('chanshui.loadFailed')) }
  finally { if (!controller.signal.aborted && !disposed) historyLoading.value = false }
}
function closeHistory() { historyController?.abort(); historyConfig.value = null; history.value = []; historyError.value = '' }
async function openDetail(id: string, silent = false) {
  detailController?.abort(); const controller = new AbortController(); detailController = controller; detailID = id; showDetail.value = true
  if (!silent) { detailLoading.value = true; selectedRun.value = null }
  try { const data = await chanshuiAPI.detail(id, controller.signal); if (!controller.signal.aborted && !disposed) { selectedRun.value = data; detailError.value = '' } }
  catch (err) { if (!controller.signal.aborted && !disposed) detailError.value = extractApiErrorMessage(err, t('chanshui.loadFailed')) }
  finally { if (!controller.signal.aborted && !disposed) detailLoading.value = false }
}
function closeDetail() { detailController?.abort(); showDetail.value = false; selectedRun.value = null; detailID = ''; detailError.value = '' }
watch(() => form.account_id, () => { if (!editingID.value) form.model = '' })
// 编辑时账号身份不可改变；候选异步加载完成后补齐其所属平台。
watch(selectedTarget, target => { if (editingID.value && target) selectedPlatform.value = target.platform })
watch(() => form.enabled, enabled => { if (!enabled) form.schedule_enabled = false })
watch(deleting, () => { deleteError.value = '' })
onMounted(() => {
  refresh()
  // 只刷新已有状态，不因打开页面或网络异常自动创建远端检测。
  refreshTimer = setInterval(() => {
    if (document.visibilityState === 'hidden') return
    if (!loading.value) void load(true)
    if (historyConfig.value && !historyLoading.value) void loadHistory(true)
    if (showDetail.value && !detailLoading.value && active(selectedRun.value)) void openDetail(detailID, true)
  }, 5000)
})
onBeforeUnmount(() => { disposed = true; if (refreshTimer) clearInterval(refreshTimer); listController?.abort(); targetController?.abort(); historyController?.abort(); detailController?.abort() })
</script>
