<template>
  <!-- 单条请求缓存的端点信息；未缓存或缓存已过期时显示提示 -->
  <div class="text-sm" data-testid="request-data-cache-view">
    <div v-if="loading" class="py-4 text-center text-gray-500" role="status">{{ t('common.loading') }}</div>
    <p v-else-if="missing" class="py-4 text-center text-gray-500 dark:text-dark-400">{{ t('usage.requestData.notCached') }}</p>
    <p v-else-if="loadError" class="py-4 text-center text-red-500">{{ t('usage.requestData.loadFailed') }}</p>
    <div v-else-if="entry" class="grid grid-cols-1 gap-x-6 gap-y-3 sm:grid-cols-2">
      <div v-for="item in items" :key="item.label" class="min-w-0">
        <span class="font-medium text-gray-500 dark:text-dark-400">{{ item.label }}</span>
        <p class="mt-0.5 break-all text-gray-900 dark:text-dark-100" :class="item.mono && 'font-mono text-xs'">{{ item.value || '-' }}</p>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { getUsageRequestData, getErrorRequestData, type RequestDataCacheEntry } from '@/api/usage'
import { formatDateTime } from '@/utils/format'

const props = defineProps<{
  /** usage：使用记录 ID；error：错误请求 ID。 */
  source: 'usage' | 'error'
  recordId: number | null
}>()

const { t } = useI18n()
const loading = ref(false)
const loadError = ref(false)
const missing = ref(false)
const entry = ref<RequestDataCacheEntry | null>(null)
// 只采纳当前记录的响应，避免快速切换时旧结果覆盖新结果。
let requestVersion = 0

const items = computed(() => {
  const e = entry.value
  if (!e) return []
  return [
    { label: t('usage.requestData.requestId'), value: e.request_id, mono: true },
    { label: t('usage.requestData.method'), value: e.method },
    { label: t('usage.inboundEndpoint'), value: e.inbound_endpoint, mono: true },
    { label: t('usage.requestData.upstreamEndpoint'), value: e.upstream_endpoint, mono: true },
    { label: t('usage.path'), value: e.path, mono: true },
    { label: t('usage.requestData.platform'), value: e.platform },
    { label: t('usage.model'), value: e.model },
    { label: t('usage.requestData.statusCode'), value: e.status_code ? String(e.status_code) : '' },
    { label: t('usage.type'), value: e.stream ? t('usage.stream') : t('usage.sync') },
    { label: t('usage.requestData.completedAt'), value: e.completed_at ? formatDateTime(e.completed_at) : '' },
    { label: t('usage.requestData.expiresAt'), value: e.expires_at ? formatDateTime(e.expires_at) : '' }
  ]
})

async function load(id: number) {
  const version = ++requestVersion
  loading.value = true
  loadError.value = false
  missing.value = false
  entry.value = null
  try {
    const result = props.source === 'usage' ? await getUsageRequestData(id) : await getErrorRequestData(id)
    if (version === requestVersion) entry.value = result
  } catch (err) {
    if (version !== requestVersion) return
    // 404 表示未缓存、已过期或已清除，属于正常情况。
    const status = (err as { status?: number; response?: { status?: number } })?.status ?? (err as { response?: { status?: number } })?.response?.status
    if (status === 404) missing.value = true
    else loadError.value = true
  } finally {
    if (version === requestVersion) loading.value = false
  }
}

watch(() => [props.source, props.recordId] as const, ([, id]) => {
  if (id != null) load(id)
  else {
    requestVersion++
    entry.value = null
  }
}, { immediate: true })
</script>
