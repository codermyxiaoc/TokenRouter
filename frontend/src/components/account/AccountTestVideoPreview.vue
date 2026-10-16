<template>
  <div class="space-y-2" :aria-busy="loading" data-testid="account-test-video-preview">
    <video
      v-if="source"
      :key="generation"
      ref="player"
      :src="source"
      controls
      playsinline
      preload="metadata"
      class="max-h-[360px] w-full rounded-xl"
      @error="previewError"
      @loadedmetadata="previewLoaded"
    />
    <p v-if="loading" role="status" class="text-sm text-gray-500 dark:text-gray-400" data-testid="account-test-video-preview-loading">
      {{ t('admin.accounts.videoPreviewLoading') }}
    </p>
    <p v-if="failed || timedOut || !source" role="alert" class="text-sm text-amber-600 dark:text-amber-400" data-testid="account-test-video-preview-error">
      {{ t(timedOut ? 'admin.accounts.videoPreviewTimedOut' : 'admin.accounts.videoPreviewFailed') }}
    </p>
    <div v-if="failed || timedOut || externalLink" class="flex flex-wrap items-center gap-3">
      <button v-if="(failed || timedOut) && source" type="button" class="btn btn-secondary btn-sm" data-testid="account-test-video-reload" @click="reloadPreview">
        {{ t('admin.accounts.videoPreviewReload') }}
      </button>
      <a v-if="externalLink" :href="externalLink" target="_blank" rel="noopener noreferrer" referrerpolicy="no-referrer" class="text-sm text-primary-600 hover:text-primary-700 dark:text-primary-400" data-testid="account-test-video-open">
        {{ t('admin.accounts.videoPreviewOpen') }}
      </a>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'

const props = defineProps<{ url: string }>()
const { t } = useI18n()
const player = ref<HTMLVideoElement | null>(null)
const failed = ref(false)
const loading = ref(false)
const timedOut = ref(false)
const generation = ref(0)
let loadTimer: ReturnType<typeof setTimeout> | undefined

// 链接只接受 HTTP(S) 或本站路径；不得把媒体事件里的任意协议变成可点击导航。
const externalLink = computed(() => {
  const value = props.url.trim()
  const relative = value.startsWith('/') && !value.startsWith('//') && !value.includes('\\')
  if (!relative && !/^https?:\/\//i.test(value)) return ''
  try {
    const url = new URL(value, window.location.origin)
    if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password) return ''
    if (relative && url.origin !== window.location.origin) return ''
    return value
  } catch {
    return ''
  }
})
// 服务端可能返回已下载的视频 Data URL，仅交给播放器，不创建 Data URL 页面导航。
const source = computed(() => externalLink.value || (/^data:video\/[a-z0-9.+-]+;base64,/i.test(props.url.trim()) ? props.url.trim() : ''))

// 播放器失败独立于上游生成结果；重试只换播放器实例，绝不再次请求生成。
function clearLoadTimer() {
  if (loadTimer !== undefined) clearTimeout(loadTimer)
  loadTimer = undefined
}
function reloadPreview() {
  clearLoadTimer()
  failed.value = false
  timedOut.value = false
  loading.value = Boolean(source.value)
  generation.value++
  if (loading.value) {
    // 慢速媒体可能只触发 stalled 而不报错；超时仅提示，可继续等待元数据到达。
    loadTimer = setTimeout(() => {
      loadTimer = undefined
      loading.value = false
      timedOut.value = true
    }, 30_000)
  }
}
function previewError(event: Event) {
  if (event.currentTarget !== player.value) return
  clearLoadTimer()
  loading.value = false
  timedOut.value = false
  failed.value = true
}
function previewLoaded(event: Event) {
  if (event.currentTarget !== player.value) return
  clearLoadTimer()
  loading.value = false
  timedOut.value = false
  failed.value = false
}
watch(() => props.url, reloadPreview, { flush: 'sync', immediate: true })
onBeforeUnmount(clearLoadTimer)
</script>
