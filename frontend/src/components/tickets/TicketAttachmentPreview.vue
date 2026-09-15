<template>
  <div class="space-y-4">
    <header class="card flex flex-wrap items-center justify-between gap-3 p-4">
      <div class="min-w-0 flex-1">
        <h1 v-if="showTitle" class="truncate text-lg font-semibold text-gray-900 dark:text-white" :title="attachment?.filename">{{ attachment?.filename || t('tickets.preview.title') }}</h1>
        <p v-if="attachment" class="mt-1 text-xs text-gray-500">{{ formatAttachmentSize(attachment.size) }}</p>
      </div>
      <button v-if="attachment" type="button" class="btn btn-secondary btn-sm" :disabled="downloading || disabled" @click="download">{{ t(downloading ? 'common.processing' : 'tickets.preview.download') }}</button>
    </header>
    <div v-if="loading" role="status" class="card p-12 text-center text-gray-500">{{ t('tickets.preview.loading') }}</div>
    <div v-if="error" role="alert" class="rounded-xl bg-red-50 p-4 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300">
      <p>{{ error }}</p>
      <button type="button" class="btn btn-secondary btn-sm mt-3" :disabled="loading" @click="load">{{ t('tickets.retry') }}</button>
    </div>
    <p v-if="downloadError" role="alert" class="text-sm text-red-600">{{ downloadError }}</p>
    <template v-if="!loading && !error">
      <div v-if="kind === 'image'" class="card overflow-auto p-3">
        <img :src="imageUrl" :alt="attachment?.filename" class="mx-auto max-h-[65dvh] max-w-full object-contain" @error="imageFailed" />
      </div>
      <section v-else-if="kind === 'text'" class="card space-y-4 p-4 sm:p-6">
        <p class="rounded-lg bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-900/20 dark:text-amber-300">{{ t('tickets.preview.wordHint') }}</p>
        <!-- Word 只显示经服务端提取的纯文本，不能通过 HTML、链接或嵌入对象执行内容。 -->
        <pre v-if="textContent" class="whitespace-pre-wrap break-words font-sans text-sm leading-7 text-gray-800 dark:text-gray-200">{{ textContent }}</pre>
        <p v-else class="text-sm text-gray-500">{{ t('tickets.preview.wordEmpty') }}</p>
      </section>
      <section v-else-if="kind === 'pdf'" class="card p-3 sm:p-5" :aria-label="t('tickets.preview.pdf')">
        <div class="mb-4 flex flex-wrap items-center justify-center gap-3">
          <button type="button" class="btn btn-secondary btn-sm" :disabled="rendering || pageNumber <= 1" @click="changePage(-1)">{{ t('tickets.preview.previous') }}</button>
          <span class="text-sm text-gray-600 dark:text-gray-300" aria-live="polite">{{ t('tickets.preview.page', { page: pageNumber, total: pageCount }) }}</span>
          <button type="button" class="btn btn-secondary btn-sm" :disabled="rendering || pageNumber >= pageCount" @click="changePage(1)">{{ t('tickets.preview.next') }}</button>
        </div>
        <p v-if="rendering" role="status" class="mb-3 text-center text-sm text-gray-500">{{ t('tickets.preview.rendering') }}</p>
        <canvas ref="canvasElement" class="mx-auto h-auto max-w-full bg-white" role="img" :aria-label="t('tickets.preview.page', { page: pageNumber, total: pageCount })" />
      </section>
    </template>
  </div>
</template>

<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { saveAs } from 'file-saver'
import type { PDFDocumentLoadingTask, PDFDocumentProxy, PDFPageProxy, RenderTask } from 'pdfjs-dist'
import { formatAttachmentSize } from '@/components/tickets/attachments'
import { ticketAPI, type TicketAttachment } from '@/api/tickets'
import { useAppStore } from '@/stores/app'
import { extractApiErrorCode, extractI18nErrorMessage } from '@/utils/apiError'

const props = withDefaults(defineProps<{ ticketId: number; attachmentId: number; admin?: boolean; showTitle?: boolean }>(), { admin: false, showTitle: true })
const emit = defineEmits<{ disabled: [] }>()
const { t } = useI18n()
const appStore = useAppStore()
const attachment = ref<TicketAttachment>()
const loading = ref(false)
const downloading = ref(false)
const disabled = ref(false)
const error = ref('')
const downloadError = ref('')
const kind = ref<'image' | 'pdf' | 'text' | ''>('')
const imageUrl = ref('')
const textContent = ref('')
const canvasElement = ref<HTMLCanvasElement>()
const rendering = ref(false)
const pageNumber = ref(1)
const pageCount = ref(0)
let generation = 0
let controller: AbortController | undefined
let pdfTask: PDFDocumentLoadingTask | undefined
let pdfDocument: PDFDocumentProxy | undefined
let pdfPage: PDFPageProxy | undefined
let renderTask: RenderTask | undefined
let pdfModule: typeof import('@/components/tickets/pdfPreview') | undefined

// @project-doc docs/domains/support_tickets.md#ticket_attachments
// 关闭弹窗、离开页面、切换附件和重试都取消旧请求/渲染，回收对象 URL 和 worker。
function clearPreview() {
  controller?.abort()
  renderTask?.cancel()
  renderTask = undefined
  pdfPage?.cleanup()
  pdfPage = undefined
  if (pdfTask) void pdfTask.destroy().catch(() => {})
  pdfTask = undefined
  pdfDocument = undefined
  if (imageUrl.value) URL.revokeObjectURL(imageUrl.value)
  imageUrl.value = ''
  textContent.value = ''
  kind.value = ''
  rendering.value = false
  if (canvasElement.value) { canvasElement.value.width = 0; canvasElement.value.height = 0 }
}

function handleError(err: unknown, fallback: string) {
  if (extractApiErrorCode(err) === 'TICKET_DISABLED') {
    disabled.value = true
    attachment.value = undefined
    clearPreview()
    void appStore.syncTicketModuleEnabled(false)
    emit('disabled')
  }
  error.value = extractI18nErrorMessage(err, t, 'tickets.errors', t(fallback)) || t(fallback)
}

async function load() {
  const current = ++generation
  clearPreview()
  controller = new AbortController()
  const signal = controller.signal
  attachment.value = undefined
  loading.value = true
  downloading.value = false
  disabled.value = false
  error.value = ''; downloadError.value = ''; pageNumber.value = 1; pageCount.value = 0
  try {
    const id = props.ticketId
    const attachmentId = props.attachmentId
    if (!Number.isSafeInteger(id) || id <= 0 || !Number.isSafeInteger(attachmentId) || attachmentId <= 0) throw { reason: 'TICKET_ATTACHMENT_NOT_FOUND' }
    const api = ticketAPI(props.admin)
    const ticket = await api.get(id, signal)
    if (current !== generation) return
    const metadata = ticket.messages?.flatMap(message => message.attachments || []).find(item => item.id === attachmentId)
    if (!metadata) throw { reason: 'TICKET_ATTACHMENT_NOT_FOUND' }
    attachment.value = metadata
    const blob = await api.preview(id, attachmentId, signal)
    if (current !== generation) return
    const mime = blob.type.split(';')[0].toLowerCase()
    // 展示方式由经过服务端校验的响应 MIME 决定，不信任文件名或详情中的旧元数据。
    if (['image/jpeg', 'image/png', 'image/gif', 'image/webp'].includes(mime)) {
      kind.value = 'image'; imageUrl.value = URL.createObjectURL(blob)
    } else if (mime === 'text/plain') {
      const text = await blob.text()
      if (current !== generation) return
      kind.value = 'text'; textContent.value = text
    } else if (mime === 'application/pdf') {
      const module = await import('@/components/tickets/pdfPreview')
      const data = new Uint8Array(await blob.arrayBuffer())
      if (current !== generation) return
      pdfModule = module
      pdfTask = module.loadTicketPDF(data, signal)
      const document = await pdfTask.promise
      if (current !== generation) return
      pdfDocument = document; pageCount.value = document.numPages; kind.value = 'pdf'
      loading.value = false
      await nextTick()
      await renderPage(current)
    } else throw { reason: 'TICKET_ATTACHMENT_PREVIEW_UNSUPPORTED' }
  } catch (err) {
    if (current === generation && !signal.aborted) {
      clearPreview()
      handleError(err, 'tickets.preview.failed')
    }
  } finally { if (current === generation) loading.value = false }
}

async function renderPage(current = generation) {
  if (!pdfDocument || !pdfModule || !canvasElement.value) return
  rendering.value = true
  try {
    const page = await pdfDocument.getPage(pageNumber.value)
    if (current !== generation) { page.cleanup(); return }
    pdfPage?.cleanup(); pdfPage = page
    const canvas = canvasElement.value
    if (!canvas) return
    const viewport = page.getViewport({ scale: 1 })
    if (![viewport.width, viewport.height].every(value => Number.isFinite(value) && value > 0)) throw new Error()
    // 一次仅渲染一页，限制边长和总像素，异常超大页面不能分配无限 canvas。
    const scale = Math.min(1.5 * Math.min(window.devicePixelRatio || 1, 2), 4096 / viewport.width, 4096 / viewport.height, Math.sqrt(pdfModule.pdfMaxCanvasPixels / (viewport.width * viewport.height)))
    const bounded = page.getViewport({ scale })
    canvas.width = Math.max(1, Math.floor(bounded.width)); canvas.height = Math.max(1, Math.floor(bounded.height))
    canvas.style.width = `${Math.min(1100, bounded.width)}px`
    renderTask = page.render({ canvas, viewport: bounded, annotationMode: pdfModule.pdfAnnotationMode })
    await renderTask.promise
    if (current === generation) renderTask = undefined
  } catch {
    if (current === generation) { clearPreview(); error.value = t('tickets.preview.failed') }
  } finally { if (current === generation) rendering.value = false }
}

async function changePage(delta: number) {
  if (rendering.value || pageNumber.value + delta < 1 || pageNumber.value + delta > pageCount.value) return
  pageNumber.value += delta
  await renderPage()
}

function imageFailed() {
  error.value = t('tickets.preview.failed')
  if (imageUrl.value) URL.revokeObjectURL(imageUrl.value)
  imageUrl.value = ''
}

async function download() {
  if (!attachment.value || downloading.value || disabled.value) return
  downloading.value = true; downloadError.value = ''
  const current = generation
  const file = attachment.value
  const api = ticketAPI(props.admin)
  try {
    const blob = await api.download(props.ticketId, file.id)
    if (current === generation) saveAs(blob, file.filename)
  } catch (err) {
    if (current !== generation) return
    // 原件下载保留旧接口，403 时额外检查开关，避免旧 Blob 错误丢失原因。
    if (err && typeof err === 'object' && 'status' in err && err.status === 403) {
      try {
        const config = await api.config()
        if (current !== generation) return
        if (!config.enabled) { handleError({ reason: 'TICKET_DISABLED' }, 'tickets.disabled'); return }
      } catch { /* 开关读取失败时显示原始下载失败，不静默吞掉错误。 */ }
    }
    downloadError.value = extractI18nErrorMessage(err, t, 'tickets.errors', t('tickets.downloadFailed'))
  } finally { if (current === generation) downloading.value = false }
}

watch(() => [props.ticketId, props.attachmentId, props.admin], () => { void load() }, { immediate: true })
onBeforeUnmount(() => { ++generation; clearPreview() })
</script>
