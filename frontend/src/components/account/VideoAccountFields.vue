<template>
  <section class="space-y-4 rounded-xl border border-violet-200 bg-violet-50/40 p-4 dark:border-violet-900/50 dark:bg-violet-950/20" data-testid="video-account-fields">
    <div>
      <h3 class="text-sm font-semibold text-gray-900 dark:text-gray-100">{{ t('admin.accounts.video.title') }}</h3>
      <p class="input-hint">{{ t('admin.accounts.video.description') }}</p>
    </div>
    <div class="space-y-3">
      <div class="flex items-center justify-between gap-2">
        <label class="input-label mb-0">{{ t('admin.accounts.video.protocolBindings') }}</label>
        <button type="button" class="btn btn-secondary btn-sm" data-testid="video-add-binding" @click="addRow">{{ t('common.add') }}</button>
      </div>
      <p class="input-hint">{{ t('admin.accounts.video.protocolBindingsHint') }}</p>
      <div v-for="(row, index) in modelValue.rows" :key="index" class="space-y-2 rounded-lg border border-gray-200 p-3 dark:border-dark-600" :data-testid="`video-binding-row-${index}`">
        <div class="flex flex-wrap gap-2">
          <input :value="row.model" class="input min-w-0 flex-1" :data-testid="`video-binding-model-${index}`" :placeholder="t('admin.accounts.video.modelPlaceholder')" @input="updateRow(index, { model: ($event.target as HTMLInputElement).value })" />
          <Select :model-value="row.protocol" :options="protocolOptions(row.protocol)" :placeholder="t('admin.accounts.video.protocolPlaceholder')" searchable :data-testid="`video-binding-protocol-${index}`" class="min-w-48 flex-[2]" @update:model-value="updateProtocol(index, String($event ?? ''))" />
          <button type="button" class="btn btn-secondary text-red-600" :disabled="modelValue.rows.length <= 1" :data-testid="`video-remove-binding-${index}`" :aria-label="t('common.delete')" @click="removeRow(index)">×</button>
        </div>
        <p v-if="protocolHint(row.protocol)" class="input-hint">{{ protocolHint(row.protocol) }}</p>
        <div v-if="videoProtocolRequiresPath(row.protocol)">
          <label class="input-label">{{ t('admin.accounts.video.operationPath') }}</label>
          <Select :model-value="row.path" :options="pathOptions(row.protocol)" :placeholder="t('admin.accounts.video.operationPathPlaceholder')" :data-testid="`video-binding-path-${index}`" @update:model-value="updateRow(index, { path: String($event ?? '') })" />
        </div>
        <div>
          <label class="input-label">{{ t('admin.accounts.video.rowBaseUrl') }}</label>
          <input class="input" type="url" :value="row.baseUrl" :data-testid="`video-binding-base-url-${index}`" :placeholder="t('admin.accounts.video.rowBaseUrlPlaceholder')" @input="updateRow(index, { baseUrl: ($event.target as HTMLInputElement).value })" />
        </div>
        <p v-if="rowError(row)" class="text-xs text-amber-600 dark:text-amber-400" :data-testid="`video-binding-error-${index}`">{{ t(`admin.accounts.video.${rowError(row)}`) }}</p>
      </div>
      <p v-if="profilesLoadFailed" class="text-xs text-amber-600 dark:text-amber-400" data-testid="video-profiles-load-failed">{{ t('admin.accounts.video.profilesLoadFailed') }}</p>
    </div>
    <div class="grid gap-3 sm:grid-cols-2">
      <div><label class="input-label">{{ t('admin.accounts.video.maxPendingTasks') }}</label><input class="input" type="number" min="1" max="1000" step="1" :value="modelValue.maxPendingTasks" data-testid="video-max-pending" @input="patch({ maxPendingTasks: ($event.target as HTMLInputElement).value })" /></div>
      <div><label class="input-label">{{ t('admin.accounts.video.maxDurationSeconds') }}</label><input class="input" type="number" min="0" step="any" :value="modelValue.maxDurationSeconds ?? ''" data-testid="video-max-duration" @input="patch({ maxDurationSeconds: ($event.target as HTMLInputElement).value })" /></div>
    </div>
    <p class="input-hint">{{ t('admin.accounts.video.budgetHint') }}</p>
  </section>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import { adminAPI } from '@/api/admin'
import {
  VIDEO_BUILTIN_PROTOCOLS, VIDEO_PROFILE_PREFIX, VIDEO_PROTOCOL_WILDCARD, emptyVideoProtocolRow, parseVideoProtocol,
  videoAdapterTextKey, videoProtocolPaths, videoProtocolRequiresPath, videoProtocolRowError,
  type VideoAccountForm, type VideoProtocolRow
} from './videoAccountConfig'

const props = defineProps<{ modelValue: VideoAccountForm }>()
const emit = defineEmits<{ 'update:modelValue': [value: VideoAccountForm] }>()
const { t } = useI18n()
// 当前启用的模板键名，选择框只提供 profile:<key>（跟随启用版本）。
const activeProfileKeys = ref<string[]>([])
const profilesLoadFailed = ref(false)

onMounted(async () => {
  try {
    const profiles = await adminAPI.videoProtocolProfiles.list()
    activeProfileKeys.value = [...new Set(profiles.filter(item => item.status === 'active').map(item => item.profile_key))].sort()
  } catch {
    // 模板列表读取失败不影响内置协议配置，只提示管理员。
    profilesLoadFailed.value = true
  }
})

// 协议展示名：内置协议为“端点 · 方案”，模板协议为“模板协议 · 键名[@版本]”。
function protocolLabel(protocol: string): string {
  const parsed = parseVideoProtocol(protocol)
  if (!parsed) return protocol
  if (parsed.kind === 'profile') {
    return t('admin.accounts.video.profileOption', { key: parsed.version ? `${parsed.key}@${parsed.version}` : parsed.key })
  }
  return `${t(`admin.accounts.video.endpoints.${parsed.endpoint}`)} · ${t(`admin.accounts.video.adapters.${videoAdapterTextKey(parsed.endpoint, parsed.adapter)}`)}`
}
// 选项包含全部内置协议与启用模板；已保存但不在列表中的值也保留，避免编辑时显示为空。
function protocolOptions(current: string) {
  const values = [...VIDEO_BUILTIN_PROTOCOLS, ...activeProfileKeys.value.map(key => `${VIDEO_PROFILE_PREFIX}${key}`)]
  if (current && !values.includes(current)) values.push(current)
  return values.map(value => ({ value, label: protocolLabel(value) }))
}
function protocolHint(protocol: string): string {
  const parsed = parseVideoProtocol(protocol)
  if (!parsed) return ''
  if (parsed.kind === 'profile') return t('admin.accounts.video.profileHint')
  return t(`admin.accounts.video.adapterHints.${videoAdapterTextKey(parsed.endpoint, parsed.adapter)}`)
}
const pathOptions = (protocol: string) => videoProtocolPaths(protocol).map(value => ({ value, label: value }))
// 行内只提示协议与路径问题，模型名与地址问题在提交时统一校验。
const rowError = (row: VideoProtocolRow) => row.protocol.trim() ? videoProtocolRowError(row) : null

const patch = (value: Partial<VideoAccountForm>) => emit('update:modelValue', { ...props.modelValue, ...value })
function updateRow(index: number, value: Partial<VideoProtocolRow>) {
  patch({ rows: props.modelValue.rows.map((row, current) => current === index ? { ...row, ...value } : row) })
}
// 切换协议后路径必须重新选择，不能把另一协议的路径带入新协议。
function updateProtocol(index: number, protocol: string) {
  const row = props.modelValue.rows[index]
  if (!row) return
  const path = videoProtocolPaths(protocol).includes(row.path) ? row.path : ''
  updateRow(index, { protocol, path })
}
// 没有 * 默认行时新行预填 *，否则留空等待填写精确模型名。
function addRow() {
  const hasDefault = props.modelValue.rows.some(row => row.model.trim() === VIDEO_PROTOCOL_WILDCARD)
  patch({ rows: [...props.modelValue.rows, emptyVideoProtocolRow(hasDefault ? '' : VIDEO_PROTOCOL_WILDCARD)] })
}
function removeRow(index: number) {
  if (props.modelValue.rows.length <= 1) return
  patch({ rows: props.modelValue.rows.filter((_, current) => current !== index) })
}
</script>
