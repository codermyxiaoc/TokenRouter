import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent, reactive, ref } from 'vue'

const mocks = vi.hoisted(() => ({
  get: vi.fn(), preview: vi.fn(), download: vi.fn(), config: vi.fn(), ticketAPI: vi.fn(), saveAs: vi.fn(),
  syncTicketModuleEnabled: vi.fn(), loadTicketPDF: vi.fn(), destroy: vi.fn(), cleanup: vi.fn(), render: vi.fn(), cancel: vi.fn(), getPage: vi.fn(),
}))
vi.mock('@/api/tickets', () => ({ ticketAPI: mocks.ticketAPI }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ syncTicketModuleEnabled: mocks.syncTicketModuleEnabled }) }))
vi.mock('file-saver', () => ({ saveAs: mocks.saveAs }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-router', () => ({ useRoute: () => route, RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' } }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
vi.mock('@/components/tickets/pdfPreview', () => ({ loadTicketPDF: mocks.loadTicketPDF, pdfAnnotationMode: 0, pdfMaxCanvasPixels: 8_000_000 }))
import TicketAttachmentPreviewView from '../TicketAttachmentPreviewView.vue'
import TicketAttachmentPreview from '@/components/tickets/TicketAttachmentPreview.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'

const route = reactive({ params: { id: '42', attachmentId: '9' } })
const attachment = { id: 9, filename: 'example.docx', size: 8, content_type: 'application/octet-stream' }
let wrappers: VueWrapper[] = []
function render(admin = false) { const wrapper = mount(TicketAttachmentPreviewView, { props: { admin } }); wrappers.push(wrapper); return wrapper }
function blob(type: string, text = '<script>alert(1)</script>\n正文\t单元格') { return { type, text: async () => text, arrayBuffer: async () => new ArrayBuffer(8) } }
function button(wrapper: VueWrapper, key: string) { return wrapper.findAll('button').find(element => element.text() === key)! }

// 使用真实弹窗和预览组件验证关闭生命周期，保留独立页面兼容测试。
function renderDialog() {
  const wrapper = mount(defineComponent({
    components: { BaseDialog, TicketAttachmentPreview },
    setup() { return { show: ref(true) } },
    template: '<BaseDialog :show="show" title="example.docx" @close="show = false"><TicketAttachmentPreview v-if="show" :ticket-id="42" :attachment-id="9" :show-title="false" /></BaseDialog>',
  }), { global: { stubs: { teleport: true, Icon: true } } })
  wrappers.push(wrapper)
  return wrapper
}

describe('工单附件在线预览', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    route.params.id = '42'; route.params.attachmentId = '9'
    mocks.ticketAPI.mockReturnValue({ get: mocks.get, preview: mocks.preview, download: mocks.download, config: mocks.config })
    mocks.get.mockResolvedValue({ id: 42, messages: [{ attachments: [attachment] }] })
    mocks.preview.mockResolvedValue(blob('text/plain;charset=utf-8'))
    mocks.destroy.mockResolvedValue(undefined)
    mocks.render.mockReturnValue({ promise: Promise.resolve(), cancel: mocks.cancel })
    mocks.getPage.mockImplementation(async () => ({
      cleanup: mocks.cleanup, render: mocks.render,
      getViewport: ({ scale }: { scale: number }) => ({ width: 20_000 * scale, height: 30_000 * scale }),
    }))
    mocks.loadTicketPDF.mockReturnValue({ promise: Promise.resolve({ numPages: 2, getPage: mocks.getPage }), destroy: mocks.destroy })
    vi.stubGlobal('URL', Object.assign(URL, { createObjectURL: vi.fn(() => 'blob:local-image'), revokeObjectURL: vi.fn() }))
  })
  afterEach(() => { wrappers.forEach(wrapper => wrapper.unmount()); wrappers = []; vi.unstubAllGlobals() })

  it.each([false, true])('Word 只显示正文，不执行 HTML；使用正确权限入口（admin=%s）', async admin => {
    const wrapper = render(admin)
    await flushPromises()
    expect(mocks.ticketAPI).toHaveBeenCalledWith(admin)
    expect(mocks.get).toHaveBeenCalledWith(42, expect.any(AbortSignal))
    expect(mocks.preview).toHaveBeenCalledWith(42, 9, expect.any(AbortSignal))
    expect(wrapper.get('pre').element.textContent).toBe('<script>alert(1)</script>\n正文\t单元格')
    expect(wrapper.find('script').exists()).toBe(false)
    expect(wrapper.text()).toContain('tickets.preview.wordHint')
    expect(mocks.download).not.toHaveBeenCalled()
    expect(wrapper.get('a').attributes('href')).toBe(`${admin ? '/admin' : ''}/tickets/42`)
  })

  it('图片使用已鉴权的 Blob URL，离开时回收且不自动下载', async () => {
    const image = blob('image/png')
    mocks.preview.mockResolvedValue(image)
    const wrapper = render()
    await flushPromises()
    expect(URL.createObjectURL).toHaveBeenCalledWith(image)
    expect(wrapper.get('img').attributes('src')).toBe('blob:local-image')
    expect(mocks.download).not.toHaveBeenCalled()
    wrapper.unmount(); wrappers = []
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:local-image')
  })

  it('弹窗只显示一个附件标题，Escape 关闭即回收图片并可继续原页面操作', async () => {
    mocks.preview.mockResolvedValue(blob('image/png'))
    const wrapper = renderDialog()
    await flushPromises()
    expect(wrapper.get('[role="dialog"]').text()).toContain('example.docx')
    expect(wrapper.find('h1').exists()).toBe(false)
    expect(wrapper.find('img').exists()).toBe(true)
    expect(document.body.classList.contains('modal-open')).toBe(true)

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await flushPromises()
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(wrapper.findComponent(TicketAttachmentPreview).exists()).toBe(false)
    expect(document.body.classList.contains('modal-open')).toBe(false)
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:local-image')
    expect(mocks.download).not.toHaveBeenCalled()
  })

  it('PDF 尚未加载完成时关闭弹窗会中止请求并销毁 worker，迟到结果不重新显示', async () => {
    let resolve!: (value: unknown) => void
    mocks.preview.mockResolvedValue(blob('application/pdf'))
    mocks.loadTicketPDF.mockReturnValueOnce({ promise: new Promise(done => { resolve = done }), destroy: mocks.destroy })
    const wrapper = renderDialog()
    await flushPromises()
    const signal = mocks.preview.mock.calls[0][2] as AbortSignal
    await wrapper.get('button[aria-label="Close modal"]').trigger('click')
    await flushPromises()
    expect(signal.aborted).toBe(true)
    expect(mocks.destroy).toHaveBeenCalledOnce()
    resolve({ numPages: 1, getPage: mocks.getPage })
    await flushPromises()
    expect(mocks.getPage).not.toHaveBeenCalled()
    expect(wrapper.find('canvas').exists()).toBe(false)
  })

  it('点击下载原件才触发下载接口', async () => {
    const file = new Blob(['word'])
    mocks.download.mockResolvedValue(file)
    const wrapper = render()
    await flushPromises()
    await button(wrapper, 'tickets.preview.download').trigger('click')
    await flushPromises()
    expect(mocks.download).toHaveBeenCalledWith(42, 9)
    expect(mocks.saveAs).toHaveBeenCalledWith(file, 'example.docx')
  })

  it('PDF 只绘制单页并限制像素，翻页释放旧页，离开销毁 worker', async () => {
    mocks.preview.mockResolvedValue(blob('application/pdf'))
    const wrapper = render()
    await flushPromises()
    expect(mocks.loadTicketPDF).toHaveBeenCalledWith(expect.any(Uint8Array), expect.any(AbortSignal))
    expect(mocks.getPage).toHaveBeenLastCalledWith(1)
    const canvas = wrapper.get('canvas').element as HTMLCanvasElement
    expect(canvas.width * canvas.height).toBeLessThanOrEqual(8_000_000)
    expect(canvas.width).toBeLessThanOrEqual(4096)
    expect(canvas.height).toBeLessThanOrEqual(4096)
    expect(mocks.render.mock.calls[0][0].annotationMode).toBe(0)
    await button(wrapper, 'tickets.preview.next').trigger('click')
    await flushPromises()
    expect(mocks.getPage).toHaveBeenLastCalledWith(2)
    expect(mocks.cleanup).toHaveBeenCalled()
    expect(button(wrapper, 'tickets.preview.next').attributes('disabled')).toBeDefined()
    wrapper.unmount(); wrappers = []
    expect(mocks.destroy).toHaveBeenCalledOnce()
  })

  it('PDF 解析失败也立即释放 worker 并允许重试', async () => {
    mocks.preview.mockResolvedValue(blob('application/pdf'))
    mocks.loadTicketPDF.mockImplementationOnce(() => ({ promise: Promise.reject(new Error()), destroy: mocks.destroy }))
    const wrapper = render()
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('tickets.preview.failed')
    expect(mocks.destroy).toHaveBeenCalledOnce()
    await button(wrapper, 'tickets.retry').trigger('click')
    await flushPromises()
    expect(wrapper.find('canvas').exists()).toBe(true)
  })

  it('异步加载遇到工单开关关闭时不保留附件或原件下载按钮', async () => {
    mocks.preview.mockRejectedValue({ reason: 'TICKET_DISABLED', status: 403 })
    const wrapper = render()
    await flushPromises()
    expect(mocks.syncTicketModuleEnabled).toHaveBeenCalledWith(false)
    expect(wrapper.find('pre').exists()).toBe(false)
    expect(button(wrapper, 'tickets.preview.download')).toBeUndefined()
    expect(wrapper.find('[role="alert"]').exists()).toBe(true)
  })

  it('路由切换取消旧请求，延迟响应不会覆盖新附件', async () => {
    let resolve!: (value: unknown) => void
    mocks.preview.mockImplementationOnce(() => new Promise(done => { resolve = done }))
    const wrapper = render()
    await flushPromises()
    const signal = mocks.preview.mock.calls[0][2] as AbortSignal
    mocks.get.mockResolvedValue({ messages: [{ attachments: [{ ...attachment, id: 10, filename: 'second.doc' }] }] })
    mocks.preview.mockResolvedValue(blob('text/plain', '新附件'))
    route.params.attachmentId = '10'
    await flushPromises()
    expect(signal.aborted).toBe(true)
    resolve(blob('image/png'))
    await flushPromises()
    expect(wrapper.get('pre').text()).toBe('新附件')
    expect(wrapper.find('img').exists()).toBe(false)
  })

  it('未匹配到当前工单附件时不请求文件；不信任响应中的 HTML MIME', async () => {
    mocks.get.mockResolvedValueOnce({ messages: [] })
    const missing = render()
    await flushPromises()
    expect(mocks.preview).not.toHaveBeenCalled()
    expect(missing.find('[role="alert"]').exists()).toBe(true)
    missing.unmount(); wrappers = []
    mocks.preview.mockResolvedValue(blob('text/html'))
    const html = render()
    await flushPromises()
    expect(html.find('pre').exists()).toBe(false)
    expect(html.find('iframe').exists()).toBe(false)
    expect(html.find('[role="alert"]').exists()).toBe(true)
  })
})
