<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { useAppStore } from '@/stores'
import { getRequestPayloadDetail, type OpsRequestPayloadDetail } from '@/api/admin/ops'
import { formatDateTime } from '../utils/opsFormatters'

interface Props {
  show: boolean
  requestId: string | null
}

const props = defineProps<Props>()
const emit = defineEmits<{ (e: 'update:show', value: boolean): void }>()
const { t } = useI18n()
const appStore = useAppStore()
const loading = ref(false)
const detail = ref<OpsRequestPayloadDetail | null>(null)
let loadGeneration = 0

const close = () => emit('update:show', false)
const pretty = (value?: string): string => {
  if (!value) return '-'
  try {
    return JSON.stringify(JSON.parse(value), null, 2)
  } catch {
    return value
  }
}
const endpoint = computed(() => {
  if (!detail.value) return '-'
  const inbound = detail.value.inbound_endpoint || detail.value.path || '-'
  const upstream = detail.value.upstream_endpoint
  return upstream ? `${inbound} → ${upstream}` : inbound
})

async function load(requestId: string, generation: number) {
  loading.value = true
  try {
    const result = await getRequestPayloadDetail(requestId)
    if (generation === loadGeneration) detail.value = result
  } catch (error: any) {
    if (generation === loadGeneration) {
      appStore.showError(error?.message || t('admin.ops.requestDetails.payload.failed'))
    }
  } finally {
    if (generation === loadGeneration) loading.value = false
  }
}

// 首次直接打开也加载；关闭、换请求或卸载后，旧响应不得覆盖新请求详情。
watch(() => [props.show, props.requestId] as const, ([show, requestId]) => {
  const generation = ++loadGeneration
  detail.value = null
  loading.value = false
  if (show && requestId) void load(requestId, generation)
}, { immediate: true })
onBeforeUnmount(() => { loadGeneration++ })
</script>

<template>
  <BaseDialog :show="show" :title="t('admin.ops.requestDetails.payload.title')" width="full" @close="close">
    <div v-if="loading" class="flex items-center justify-center py-16">
      <div class="h-8 w-8 animate-spin rounded-full border-b-2 border-primary-600"></div>
    </div>
    <div v-else-if="!detail" class="py-12 text-center text-sm text-gray-500 dark:text-gray-400">
      {{ t('admin.ops.requestDetails.payload.empty') }}
    </div>
    <div v-else class="space-y-5 p-4 sm:p-6">
      <div class="grid grid-cols-1 gap-3 md:grid-cols-4">
        <div class="rounded-lg bg-gray-50 p-3 dark:bg-dark-900"><div class="text-[11px] text-gray-500">{{ t('admin.ops.requestDetails.table.requestId') }}</div><div class="mt-1 break-all font-mono text-xs">{{ detail.request_id }}</div></div>
        <div class="rounded-lg bg-gray-50 p-3 dark:bg-dark-900"><div class="text-[11px] text-gray-500">{{ t('admin.ops.requestDetails.payload.endpoint') }}</div><div class="mt-1 break-all font-mono text-xs">{{ endpoint }}</div></div>
        <div class="rounded-lg bg-gray-50 p-3 dark:bg-dark-900"><div class="text-[11px] text-gray-500">{{ detail.method }} / {{ detail.status_code }}</div><div class="mt-1 text-xs">{{ detail.model || '-' }}</div></div>
        <div class="rounded-lg bg-gray-50 p-3 dark:bg-dark-900"><div class="text-[11px] text-gray-500">{{ t('admin.ops.requestDetails.table.time') }}</div><div class="mt-1 text-xs">{{ formatDateTime(detail.completed_at || detail.created_at) }}</div></div>
      </div>
      <div class="grid grid-cols-1 gap-5 lg:grid-cols-2">
        <section v-for="item in [
          { label: t('admin.ops.requestDetails.payload.requestHeaders'), value: detail.request_headers, truncated: false },
          { label: t('admin.ops.requestDetails.payload.requestBody'), value: detail.request_body, truncated: detail.request_truncated },
          { label: t('admin.ops.requestDetails.payload.responseHeaders'), value: detail.response_headers, truncated: false },
          { label: t('admin.ops.requestDetails.payload.responseBody'), value: detail.response_body, truncated: detail.response_truncated }
        ]" :key="item.label" class="min-w-0">
          <div class="mb-2 flex items-center gap-2 text-xs font-bold uppercase tracking-wider text-gray-500 dark:text-gray-400">
            <span>{{ item.label }}</span><span v-if="item.truncated" class="rounded bg-amber-100 px-1.5 py-0.5 text-[10px] text-amber-700 dark:bg-amber-900/30 dark:text-amber-300">{{ t('admin.ops.requestDetails.payload.truncated') }}</span>
          </div>
          <pre class="max-h-[360px] min-h-[96px] overflow-auto rounded-xl border border-gray-200 bg-white p-4 text-xs text-gray-800 dark:border-dark-700 dark:bg-dark-800 dark:text-gray-100"><code>{{ pretty(item.value) }}</code></pre>
        </section>
      </div>
    </div>
  </BaseDialog>
</template>
