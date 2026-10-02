<template>
  <BaseDialog :show="show" :title="t('mediaTasks.preview.title')" width="full" @close="emit('close')">
    <div v-if="!task" class="flex min-h-64 flex-col items-center justify-center gap-4 px-4 text-center">
      <p :role="loading ? 'status' : 'alert'" class="text-sm text-gray-500 dark:text-gray-400">{{ loading ? t('mediaTasks.preview.loading') : error || t('mediaTasks.preview.loadFailed') }}</p>
      <button v-if="!loading" type="button" class="btn btn-secondary" @click="emit('retry')">{{ t('mediaTasks.preview.retry') }}</button>
    </div>
    <div v-else class="-mx-4 -my-3 grid min-w-0 sm:-mx-6 sm:-my-4" :class="expanded ? 'grid-cols-1' : 'lg:grid-cols-[minmax(0,1fr)_23rem]'" data-testid="media-preview-layout">
      <!-- 桌面媒体与属性左右展示；移动端保持媒体在上，所有信息可随弹窗滚动。 -->
      <section class="min-w-0 bg-gray-50 p-3 dark:bg-dark-950 sm:p-5">
        <div
          class="relative flex min-h-60 min-w-0 items-center justify-center overflow-hidden rounded-xl border border-gray-200 bg-gray-100 dark:border-dark-700 dark:bg-black"
          :class="expanded ? 'h-[65dvh] sm:h-[70dvh]' : 'h-[36dvh] sm:h-[45dvh] lg:h-[min(58dvh,36rem)]'"
          data-testid="media-preview-stage"
          :aria-busy="loading"
        >
          <div v-if="loading" role="status" class="flex flex-col items-center gap-3 p-6 text-center text-sm text-gray-500 dark:text-gray-400">
            <span class="h-7 w-7 animate-spin rounded-full border-2 border-gray-300 border-t-primary-500 dark:border-dark-600 dark:border-t-primary-400" aria-hidden="true" />
            {{ t('mediaTasks.preview.loading') }}
          </div>
          <div v-else-if="error || mediaFailed || !selected" class="flex max-w-md flex-col items-center gap-3 p-6 text-center">
            <span class="rounded-2xl bg-white p-4 text-gray-400 dark:bg-dark-800 dark:text-dark-400"><Icon :name="task.media_type === 'video' ? 'modalityVideo' : 'modalityImage'" size="xl" /></span>
            <p class="text-sm font-medium text-gray-800 dark:text-gray-200">{{ error || mediaFailed ? t('mediaTasks.preview.loadFailed') : t('mediaTasks.preview.unavailable') }}</p>
            <p :role="error || mediaFailed ? 'alert' : undefined" class="text-sm leading-6 text-gray-500 dark:text-gray-400">{{ error || (mediaFailed ? t('mediaTasks.preview.mediaFailed') : unavailableHint) }}</p>
            <button v-if="error || mediaFailed || expired" type="button" class="btn btn-secondary btn-sm mt-1" @click="emit('retry')">{{ t('mediaTasks.preview.retry') }}</button>
          </div>
          <img
            v-else-if="selected.media_type === 'image'"
            :key="mediaKey"
            ref="imageElement"
            :src="selected.url"
            :alt="t('mediaTasks.preview.imageAlt', { task: task.task_id })"
            referrerpolicy="no-referrer"
            class="h-full w-full object-contain"
            data-testid="preview-image"
            @load="imageLoaded"
            @error="mediaError"
          />
          <!-- 仅预加载媒体元数据，播放由用户操作；切换任务后 key 强制销毁旧播放器。 -->
          <video
            v-else
            :key="mediaKey"
            ref="videoElement"
            :src="selected.url"
            :aria-label="t('mediaTasks.preview.videoLabel', { task: task.task_id })"
            controls
            playsinline
            preload="metadata"
            class="h-full w-full object-contain"
            data-testid="preview-video"
            @loadedmetadata="videoLoaded"
            @error="mediaError"
          />
        </div>

        <div v-if="items.length > 1 && !loading" class="mt-3 flex flex-wrap items-center gap-2" :aria-label="t('mediaTasks.preview.title')" data-testid="preview-items">
          <button
            v-for="(item, index) in items"
            :key="`${index}-${item.url}`"
            type="button"
            class="flex h-10 w-10 items-center justify-center rounded-lg border text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500"
            :class="index === selectedIndex ? 'border-primary-500 bg-primary-50 text-primary-700 dark:bg-primary-950/40 dark:text-primary-300' : 'border-gray-200 bg-white text-gray-600 hover:bg-gray-100 dark:border-dark-700 dark:bg-dark-900 dark:text-gray-300 dark:hover:bg-dark-800'"
            :aria-label="t('mediaTasks.preview.item', { index: index + 1 })"
            :aria-pressed="index === selectedIndex"
            @click="selectedIndex = index"
          >{{ index + 1 }}</button>
          <span class="ml-auto text-xs tabular-nums text-gray-500 dark:text-gray-400">{{ t('mediaTasks.preview.itemCount', { index: selectedIndex + 1, total: items.length }) }}</span>
        </div>

        <div v-if="selected && !loading" class="mt-4 flex flex-wrap gap-2" data-testid="preview-actions">
          <button type="button" class="btn btn-secondary flex-1 whitespace-nowrap sm:flex-none" data-testid="preview-copy" @click="copyLink"><Icon name="link" size="sm" />{{ t('mediaTasks.preview.copyLink') }}</button>
          <!-- 跨域文件由浏览器处理，不向结果地址转发面板 JWT，也不把完整视频读入内存。 -->
          <a :href="selected.url" :download="downloadName" target="_blank" rel="noopener noreferrer" referrerpolicy="no-referrer" class="btn btn-primary flex-1 whitespace-nowrap sm:flex-none" data-testid="preview-download"><Icon name="download" size="sm" />{{ t('mediaTasks.preview.download') }}</a>
          <button type="button" class="btn btn-secondary flex-1 whitespace-nowrap sm:ml-auto sm:flex-none" :aria-pressed="expanded" data-testid="preview-enlarge" @click="expanded = !expanded"><Icon name="search" size="sm" />{{ t(expanded ? 'mediaTasks.preview.collapse' : 'mediaTasks.preview.enlarge') }}</button>
        </div>
        <p v-if="selected && !loading" class="mt-3 text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t('mediaTasks.preview.downloadHint') }}</p>
      </section>

      <aside v-show="!expanded" class="min-w-0 border-t border-gray-200 p-5 dark:border-dark-700 lg:border-l lg:border-t-0 sm:p-6" data-testid="preview-information">
        <div class="mb-5 flex flex-wrap items-center gap-2">
          <span class="inline-flex items-center gap-1.5 rounded-full bg-gray-100 px-2.5 py-1 text-xs font-medium text-gray-600 dark:bg-dark-800 dark:text-gray-300"><Icon :name="task.media_type === 'video' ? 'modalityVideo' : 'modalityImage'" size="sm" />{{ label('types', task.media_type) }}</span>
          <span class="rounded-full px-2.5 py-1 text-xs font-medium" :class="statusClass">{{ label('statuses', getMediaTaskDisplayStatus(task)) }}</span>
          <span v-if="isVideoTaskReconciliation(task) && getMediaTaskDisplayStatus(task) !== 'reconciliation'" class="rounded-full bg-amber-100 px-2.5 py-1 text-xs font-medium text-amber-800 dark:bg-amber-900/30 dark:text-amber-300">{{ t('mediaTasks.videoBilling.reconciliation') }}</span>
        </div>
        <h4 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('mediaTasks.preview.mediaInfo') }}</h4>
        <dl class="mt-3 grid grid-cols-2 gap-2" data-testid="preview-properties">
          <div v-for="field in mediaFields" :key="field.key" class="min-w-0 rounded-lg bg-gray-50 px-3 py-2.5 dark:bg-dark-800/70">
            <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t(`mediaTasks.preview.${field.key}`) }}</dt>
            <dd class="mt-1 break-words text-sm font-medium tabular-nums text-gray-900 dark:text-gray-100">{{ field.value || t('mediaTasks.preview.unknown') }}</dd>
          </div>
        </dl>
        <p class="mt-2 text-xs leading-5 text-gray-400 dark:text-gray-500">{{ t('mediaTasks.preview.metadataHint') }}</p>

        <h4 class="mb-3 mt-6 text-sm font-semibold text-gray-900 dark:text-white">{{ t('mediaTasks.preview.taskInfo') }}</h4>
        <dl class="space-y-3 text-sm">
          <div v-for="field in taskFields" :key="field.key" class="min-w-0">
            <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t(`mediaTasks.${field.key}`) }}</dt>
            <dd class="mt-1 break-all text-gray-900 dark:text-gray-100" :class="field.key === 'task' ? 'font-mono text-xs leading-5' : ''">{{ field.value }}</dd>
          </div>
          <div v-if="admin" class="min-w-0" data-testid="preview-user">
            <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('mediaTasks.user') }}</dt>
            <dd class="mt-1 break-all text-gray-900 dark:text-gray-100">{{ userTitle }} <span class="text-gray-500 dark:text-gray-400">#{{ task.user_id }}</span><span v-if="task.user?.deleted_at" class="ml-1.5 rounded bg-rose-100 px-1 py-px text-xs text-rose-600 dark:bg-rose-500/20 dark:text-rose-400">{{ t('admin.usage.userDeletedBadge') }}</span></dd>
          </div>
        </dl>

        <details class="mt-5 border-t border-gray-100 pt-4 dark:border-dark-700">
          <summary class="cursor-pointer text-xs font-medium text-gray-600 dark:text-gray-300">{{ t('mediaTasks.preview.moreDetails') }}</summary>
          <dl class="mt-4 space-y-3 text-sm"><div v-for="field in moreFields" :key="field.key" class="min-w-0"><dt class="text-xs text-gray-500 dark:text-gray-400">{{ t(`mediaTasks.${field.key}`) }}</dt><dd class="mt-1 break-all text-gray-900 dark:text-gray-100">{{ field.value }}</dd></div></dl>
        </details>
        <VideoTaskBillingDetails v-if="task.video_billing" :billing="task.video_billing" />
        <div v-if="task.error_message" class="mt-4 rounded-lg bg-red-50 p-3 text-sm dark:bg-red-950/30"><p class="text-xs text-red-600 dark:text-red-400">{{ t('mediaTasks.error') }}</p><p class="mt-1 whitespace-pre-wrap break-words text-red-700 [overflow-wrap:anywhere] dark:text-red-300">{{ task.error_message }}</p></div>
        <p class="mt-5 border-t border-gray-100 pt-4 text-xs leading-5 text-gray-500 dark:border-dark-700 dark:text-gray-400">{{ t('mediaTasks.billingHint') }}</p>
      </aside>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { getBillingModeLabel } from '@/utils/billingMode'
import VideoTaskBillingDetails from './VideoTaskBillingDetails.vue'
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { useClipboard } from '@/composables/useClipboard'
import type { MediaTask, MediaTaskPreview } from '@/api/mediaTasks'
import { formatBytes } from '@/utils/format'
import { mediaAspectRatio, mediaDownloadName, mediaDuration, mediaFormat, mediaPreviewUrl } from './mediaTaskPreview'
import { formatMediaTaskCost, getMediaTaskDisplayStatus, isVideoTaskReconciliation } from './mediaTaskCost'

const props = withDefaults(defineProps<{
  show: boolean
  task: MediaTask | null
  preview: MediaTaskPreview | null
  loading?: boolean
  error?: string
  admin?: boolean
}>(), { loading: false, error: '', admin: false })
const emit = defineEmits<{ close: []; retry: [] }>()
const { t, te } = useI18n()
const { copyToClipboard } = useClipboard()
const selectedIndex = ref(0)
const expanded = ref(false)
const mediaFailed = ref(false)
const imageElement = ref<HTMLImageElement>()
const videoElement = ref<HTMLVideoElement>()
const measured = ref<{ width: number; height: number; duration?: number }>({ width: 0, height: 0 })
const previewOwner = ref('')
const identity = computed(() => `${props.admin ? 'admin' : 'user'}:${props.task?.user_id}:${props.task?.id}`)

// 即使父组件分批更新 props，旧预览也不能在另一用户或另一任务的详情中短暂重现。
watch(() => [props.show, identity.value, props.preview] as const, (current, previous) => {
  stopMedia()
  selectedIndex.value = 0
  expanded.value = false
  resetMetadata()
  if (!current[0] || !props.task || !current[2]) previewOwner.value = ''
  else if (!previous || current[2] !== previous[2]) previewOwner.value = current[1]
  else if (current[1] !== previous[1] || current[0] !== previous[0]) previewOwner.value = ''
}, { immediate: true, flush: 'sync' })

const activePreview = computed(() => props.show && previewOwner.value === identity.value ? props.preview : null)
const expired = ref(false)
let expiryTimer: ReturnType<typeof setTimeout> | undefined

// 票据有效期独立于任务状态；弹窗久留后及时收起失效媒体，只由用户重试获取新票据。
function updateExpiry() {
  if (expiryTimer !== undefined) clearTimeout(expiryTimer)
  expiryTimer = undefined
  const preview = activePreview.value
  const deadline = preview?.expires_at ? new Date(preview.expires_at).getTime() : Number.NaN
  const remaining = deadline - Date.now()
  expired.value = preview?.unavailable_reason === 'expired' || (Number.isFinite(deadline) && remaining <= 0)
  if (expired.value) {
    stopMedia()
    expanded.value = false
  } else if (Number.isFinite(deadline)) {
    // 浏览器的单次计时器上限约为 24.8 天，超长有效期分段检查，避免溢出后立即执行。
    expiryTimer = setTimeout(updateExpiry, Math.min(remaining, 2_147_483_647))
  }
}
watch(activePreview, updateExpiry, { immediate: true, flush: 'sync' })
const items = computed(() => {
  if (!props.show || props.loading || props.error || expired.value || !activePreview.value) return []
  return activePreview.value.items
    .map(item => ({ ...item, url: mediaPreviewUrl(item.url) }))
    .filter(item => item.url && (item.media_type === 'image' || item.media_type === 'video'))
})
const selected = computed(() => items.value[selectedIndex.value])
const mediaKey = computed(() => `${identity.value}:${selectedIndex.value}:${selected.value?.url || ''}`)
const downloadName = computed(() => mediaDownloadName(props.task?.task_id || '', selectedIndex.value, selected.value?.mime_type))
const unavailableHint = computed(() => {
  if (expired.value || props.task?.status === 'expired') return t('mediaTasks.preview.expiredHint')
  if (activePreview.value?.unavailable_reason === 'pending' || ['queued', 'processing'].includes(props.task?.status || '')) return t('mediaTasks.preview.pendingHint')
  return t('mediaTasks.preview.unavailableHint')
})

function positive(value: number | undefined): number {
  return value !== undefined && Number.isFinite(value) && value > 0 ? value : 0
}
const mediaFields = computed(() => {
  const item = selected.value
  const width = positive(measured.value.width) || positive(item?.width)
  const height = positive(measured.value.height) || positive(item?.height)
  return [
    { key: 'resolution', value: width && height ? `${width} × ${height}` : '' },
    { key: 'aspectRatio', value: mediaAspectRatio(width, height) },
    { key: 'format', value: mediaFormat(item?.mime_type) },
    ...(props.task?.media_type === 'video' ? [{ key: 'duration', value: mediaDuration(measured.value.duration ?? item?.duration_seconds) }] : []),
    ...(positive(item?.size_bytes) ? [{ key: 'size', value: formatBytes(item!.size_bytes!) }] : []),
  ]
})
const formatTime = (value: string | null | undefined) => value && Number.isFinite(new Date(value).getTime()) ? new Date(value).toLocaleString() : '—'
function label(kind: string, value: string) {
  const key = `mediaTasks.${kind}.${value}`
  return te(key) ? t(key) : value || '—'
}
const formatCost = (task: MediaTask) => formatMediaTaskCost(task, t('mediaTasks.pendingCost'), {
  released: t('mediaTasks.videoBilling.notCharged'), reconciliation: t('mediaTasks.videoBilling.reconciliation'),
})
const statusClass = computed(() => {
  if (props.task && getMediaTaskDisplayStatus(props.task) === 'reconciliation') return 'bg-amber-100 text-amber-800 dark:bg-amber-900/30 dark:text-amber-300'
  if (props.task?.status === 'completed') return 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300'
  if (props.task?.status === 'failed') return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
  if (['queued', 'processing'].includes(props.task?.status || '')) return 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300'
  return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
})
const userTitle = computed(() => {
  const username = props.task?.user?.username?.trim() || ''
  const email = props.task?.user?.email?.trim() || ''
  return username && email && username !== email ? `${username} (${email})` : username || email || '—'
})
const taskFields = computed(() => props.task ? [
  { key: 'task', value: props.task.task_id },
  { key: 'model', value: props.task.model || '—' },
  { key: 'cost', value: formatCost(props.task) },
  { key: 'createdAt', value: formatTime(props.task.created_at) },
  { key: 'completedAt', value: formatTime(props.task.completed_at) },
] : [])
const moreFields = computed(() => {
  const task = props.task
  if (!task) return []
  return [
    { key: 'source', value: label('sources', task.source) }, { key: 'platform', value: task.platform || '—' },
    { key: 'upstreamStatus', value: task.upstream_status || '—' }, { key: 'billingMode', value: task.video_billing ? getBillingModeLabel(task.video_billing.mode, t) : task.billing_mode || '—' },
    { key: 'group', value: task.group_name || task.group_id || '—' }, { key: 'apiKeyId', value: task.api_key_id },
    { key: 'requestId', value: task.request_id || '—' }, { key: 'httpStatus', value: task.http_status || '—' },
    ...(props.admin ? [{ key: 'accountId', value: task.account_id ?? '—' }] : []),
    { key: 'updatedAt', value: formatTime(task.updated_at) }, { key: 'expiresAt', value: formatTime(task.expires_at) },
    ...(activePreview.value?.expires_at ? [{ key: 'preview.expiresAt', value: formatTime(activePreview.value.expires_at) }] : []),
  ]
})

function resetMetadata() {
  measured.value = { width: 0, height: 0 }
  mediaFailed.value = false
}
function stopMedia() {
  if (videoElement.value && !videoElement.value.paused) videoElement.value.pause()
}
function imageLoaded(event: Event) {
  const image = event.currentTarget as HTMLImageElement
  if (image !== imageElement.value || !selected.value) return
  measured.value = { width: image.naturalWidth, height: image.naturalHeight }
}
function videoLoaded(event: Event) {
  const video = event.currentTarget as HTMLVideoElement
  if (video !== videoElement.value || !selected.value) return
  measured.value = { width: video.videoWidth, height: video.videoHeight, duration: Number.isFinite(video.duration) ? video.duration : undefined }
}
function mediaError(event: Event) {
  if (event.currentTarget !== imageElement.value && event.currentTarget !== videoElement.value) return
  stopMedia()
  mediaFailed.value = true
}
function copyLink() {
  if (!selected.value) return
  // 同源代理的相对路径需转成完整 URL，粘贴到外部环境后仍能打开。
  void copyToClipboard(new URL(selected.value.url, window.location.origin).href, t('mediaTasks.preview.linkCopied'))
}

watch(mediaKey, () => { stopMedia(); resetMetadata() }, { flush: 'sync' })
onBeforeUnmount(() => {
  if (expiryTimer !== undefined) clearTimeout(expiryTimer)
  stopMedia()
})
</script>
