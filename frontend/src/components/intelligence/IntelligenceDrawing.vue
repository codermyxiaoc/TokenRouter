<template>
  <div ref="container" class="flex h-full w-full items-center justify-center bg-gray-50 dark:bg-dark-900">
    <!-- 独立内容响应提供专用 CSP，避免 srcdoc 继承面板 CSP 导致绘图脚本失效。 -->
    <iframe
      v-if="visible && pageVisible && url" :src="url" sandbox="allow-scripts" referrerpolicy="no-referrer"
      :title="title" :scrolling="thumbnail ? 'no' : undefined" :tabindex="thumbnail ? -1 : undefined"
      class="h-full w-full border-0 bg-white" :class="{ 'pointer-events-none': thumbnail }"
      allow="camera 'none'; microphone 'none'; geolocation 'none'; clipboard-read 'none'; clipboard-write 'none'"
    />
    <p v-else-if="error" class="px-4 text-center text-xs text-gray-500">{{ error }}</p>
    <p v-else role="status" class="text-xs text-gray-400">{{ t('common.loading') }}</p>
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { intelligencePreviewURL } from './results'
import { useIntelligenceResults } from './useIntelligenceResults'
// 列表缩略图隐藏自身滚动条且不进入键盘焦点；详情预览仍可滚动和交互。
const props = defineProps<{ runId: string; title: string; thumbnail?: boolean }>()
const { t } = useI18n()
const loader = useIntelligenceResults()
const container = ref<HTMLElement>()
const visible = ref(false)
const pageVisible = ref(!document.hidden)
const url = ref('')
const error = ref('')
let observer: IntersectionObserver | undefined
let revision = 0
let disposed = false
async function load() {
  if (!visible.value || document.hidden) return
  const current = ++revision
  error.value = ''
  try {
    const result = await loader.preview(props.runId)
    if (current !== revision || disposed) return
    url.value = intelligencePreviewURL(result.url)
    if (!url.value) error.value = t('intelligence.noArtwork')
  } catch { if (current === revision && !disposed) error.value = t('intelligence.noArtwork') }
}
function visibilityChanged() {
  pageVisible.value = !document.hidden
  if (!pageVisible.value) url.value = ''
  else if (visible.value) void load()
}
watch(() => props.runId, () => { url.value = ''; void load() })
onMounted(() => {
  document.addEventListener('visibilitychange', visibilityChanged)
  if (typeof IntersectionObserver === 'undefined') { visible.value = true; void load(); return }
  observer = new IntersectionObserver(entries => {
    visible.value = entries.some(entry => entry.isIntersecting)
    if (visible.value) void load()
    else url.value = ''
  }, { rootMargin: '80px' })
  if (container.value) observer.observe(container.value)
})
onBeforeUnmount(() => { disposed = true; ++revision; observer?.disconnect(); document.removeEventListener('visibilitychange', visibilityChanged) })
</script>
