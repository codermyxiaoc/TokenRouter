<template>
  <!-- 缩略图不接收指针，鼠标滚轮和拖动交给画廊；触摸仍由浏览器原生处理。 -->
  <div
    ref="gallery" role="region" :aria-label="t('intelligence.previewTitle')" tabindex="0"
    class="scrollbar-hide flex min-w-0 touch-auto select-none gap-4 overflow-x-auto overscroll-x-contain pb-3 focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500"
    :class="dragging ? 'cursor-grabbing' : 'cursor-grab'"
    data-testid="drawing-gallery"
    @wheel="onWheel" @pointerdown="startDrag" @pointermove="moveDrag"
    @pointerup="endDrag" @pointercancel="endDrag" @lostpointercapture="endDrag"
    @keydown="onKeydown" @dragstart.prevent
  >
    <article v-for="artifact in recentArtifacts(artifacts)" :key="artifact.id" class="relative w-[min(78vw,360px)] shrink-0 overflow-hidden rounded-xl border border-gray-200 bg-white dark:border-dark-600">
      <div class="pointer-events-none h-56 overflow-hidden"><IntelligenceDrawing :run-id="artifact.id" :title="`${model} · ${formatDateTime(artifact.created_at)}`" thumbnail /></div>
      <span class="pointer-events-none absolute right-2 top-2 rounded-md px-2 py-1 text-xs font-semibold text-white shadow-sm" :class="resultColor(artifact)">{{ t(`intelligence.statuses.${resultStatus(artifact)}`) }}</span>
      <button type="button" class="flex w-full cursor-pointer items-center justify-between gap-3 border-t border-gray-100 px-3 py-3 text-left text-xs hover:bg-gray-50 dark:border-dark-700 dark:bg-dark-800 dark:hover:bg-dark-700" @click="emit('select', artifact)"><span class="text-gray-500">{{ formatDateTime(artifact.created_at) }}</span><span class="font-medium text-primary-600 dark:text-primary-400">{{ t('intelligence.preview') }}</span></button>
    </article>
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { IntelligenceRun } from '@/api/intelligence'
import { formatDateTime } from '@/utils/format'
import IntelligenceDrawing from './IntelligenceDrawing.vue'
import { recentArtifacts, resultColor, resultStatus } from './results'

defineProps<{ artifacts: IntelligenceRun[]; model: string }>()
const emit = defineEmits<{ select: [run: IntelligenceRun] }>()
const { t } = useI18n()
const gallery = ref<HTMLElement>()
const dragging = ref(false)
let drag: { pointerId: number; startX: number; scrollLeft: number } | null = null

function setScrollLeft(left: number) {
  const element = gallery.value
  if (!element) return false
  const before = element.scrollLeft
  element.scrollLeft = Math.max(0, Math.min(element.scrollWidth - element.clientWidth, left))
  return element.scrollLeft !== before
}

function onWheel(event: WheelEvent) {
  const element = gallery.value
  if (!element || event.ctrlKey) return
  const delta = Math.abs(event.deltaX) > Math.abs(event.deltaY) ? event.deltaX : event.deltaY
  const unit = event.deltaMode === 1 ? 16 : event.deltaMode === 2 ? element.clientWidth : 1
  // 只有画廊确实移动时才消费滚轮，边缘和无溢出时继续滚动页面。
  if (setScrollLeft(element.scrollLeft + delta * unit)) event.preventDefault()
}

function startDrag(event: PointerEvent) {
  const element = gallery.value
  if (!element || event.pointerType !== 'mouse' || event.button !== 0 || element.scrollWidth <= element.clientWidth) return
  // “查看作品”等控件保留点击和键盘行为，不把按钮点击变成拖动。
  if (event.target instanceof Element && event.target.closest('button, a, input, textarea, select, [contenteditable="true"]')) return
  drag = { pointerId: event.pointerId, startX: event.clientX, scrollLeft: element.scrollLeft }
  element.setPointerCapture(event.pointerId)
}

function moveDrag(event: PointerEvent) {
  if (!drag || event.pointerId !== drag.pointerId) return
  if (!(event.buttons & 1)) { endDrag(); return }
  const distance = event.clientX - drag.startX
  if (!dragging.value && Math.abs(distance) <= 5) return
  dragging.value = true
  setScrollLeft(drag.scrollLeft - distance)
  event.preventDefault()
}

function endDrag(event?: PointerEvent | FocusEvent) {
  if (event && 'pointerId' in event && drag && event.pointerId !== drag.pointerId) return
  const pointerId = drag?.pointerId
  drag = null
  dragging.value = false
  const element = gallery.value
  if (element && pointerId !== undefined && element.hasPointerCapture(pointerId)) element.releasePointerCapture(pointerId)
}

function onKeydown(event: KeyboardEvent) {
  const element = gallery.value
  if (!element || event.target !== element || event.ctrlKey || event.metaKey || event.altKey) return
  const offsets: Record<string, number> = {
    ArrowLeft: element.scrollLeft - 320, ArrowRight: element.scrollLeft + 320,
    PageUp: element.scrollLeft - element.clientWidth, PageDown: element.scrollLeft + element.clientWidth,
    Home: 0, End: element.scrollWidth,
  }
  const offset = offsets[event.key]
  if (typeof offset === 'number' && setScrollLeft(offset)) event.preventDefault()
}

// 指针捕获负责越界释放，窗口失焦与卸载也要清理拖动状态。
onMounted(() => window.addEventListener('blur', endDrag))
onBeforeUnmount(() => { window.removeEventListener('blur', endDrag); endDrag() })
</script>
