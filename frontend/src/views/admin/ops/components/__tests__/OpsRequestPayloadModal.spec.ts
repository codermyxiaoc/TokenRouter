import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import OpsRequestPayloadModal from '../OpsRequestPayloadModal.vue'
import type { OpsRequestPayloadDetail } from '@/api/admin/ops'

const mocks = vi.hoisted(() => ({ getRequestPayloadDetail: vi.fn(), showError: vi.fn() }))
vi.mock('@/api/admin/ops', () => ({ getRequestPayloadDetail: mocks.getRequestPayloadDetail }))
vi.mock('@/stores', () => ({ useAppStore: () => ({ showError: mocks.showError }) }))
vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key }),
}))

function detail(requestId: string, body = '{"model":"test"}'): OpsRequestPayloadDetail {
  return { request_id: requestId, method: 'POST', path: '/v1/responses', status_code: 503, request_body: body, created_at: '', completed_at: '' }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}

function mountModal(show = true, requestId: string | null = 'first') {
  return mount(OpsRequestPayloadModal, {
    props: { show, requestId },
    global: { stubs: { BaseDialog: { template: '<div><slot /></div>' } } },
  })
}

describe('OpsRequestPayloadModal', () => {
  beforeEach(() => vi.clearAllMocks())

  it('首次打开加载并完整格式化大JSON，文本不能作为HTML执行', async () => {
    const raw = JSON.stringify({ text: 'x'.repeat(70000), literal: '<script>window.injected = true</script>', end: 'full-body-end' })
    mocks.getRequestPayloadDetail.mockResolvedValue(detail('first', raw))
    const wrapper = mountModal()
    await flushPromises()
    expect(mocks.getRequestPayloadDetail).toHaveBeenCalledWith('first')
    expect(wrapper.findAll('code')[1].text()).toBe(JSON.stringify(JSON.parse(raw), null, 2))
    expect(wrapper.find('script').exists()).toBe(false)
    wrapper.unmount()
  })

  it('非JSON响应保持原文并独立展示服务端截断标记', async () => {
    mocks.getRequestPayloadDetail.mockResolvedValue({ ...detail('first'), response_body: 'event: error\ndata: busy\n', response_truncated: true })
    const wrapper = mountModal()
    await flushPromises()
    expect(wrapper.findAll('code')[3].text()).toBe('event: error\ndata: busy')
    expect(wrapper.text()).toContain('admin.ops.requestDetails.payload.truncated')
    wrapper.unmount()
  })

  it('快速切换请求后丢弃旧响应', async () => {
    const old = deferred<OpsRequestPayloadDetail>()
    mocks.getRequestPayloadDetail.mockReturnValueOnce(old.promise).mockResolvedValueOnce(detail('second', '{"current":true}'))
    const wrapper = mountModal()
    await wrapper.setProps({ requestId: 'second' })
    await flushPromises()
    old.resolve(detail('first', '{"stale":true}'))
    await flushPromises()
    expect(wrapper.text()).toContain('second')
    expect(wrapper.text()).toContain('"current": true')
    expect(wrapper.text()).not.toContain('"stale"')
    wrapper.unmount()
  })

  it('关闭和卸载后不回填旧请求或弹出错误', async () => {
    const old = deferred<OpsRequestPayloadDetail>()
    mocks.getRequestPayloadDetail.mockReturnValueOnce(old.promise)
    const wrapper = mountModal()
    await wrapper.setProps({ show: false })
    old.reject(new Error('stale failure'))
    await flushPromises()
    expect(mocks.showError).not.toHaveBeenCalled()
    expect(wrapper.find('code').exists()).toBe(false)
    const unmounted = deferred<OpsRequestPayloadDetail>()
    mocks.getRequestPayloadDetail.mockReturnValueOnce(unmounted.promise)
    await wrapper.setProps({ show: true })
    wrapper.unmount()
    unmounted.reject(new Error('unmounted failure'))
    await flushPromises()
    expect(mocks.showError).not.toHaveBeenCalled()
  })

  it('未选请求不查询，当前加载失败显示错误', async () => {
    const wrapper = mountModal(false)
    await flushPromises()
    expect(mocks.getRequestPayloadDetail).not.toHaveBeenCalled()
    await wrapper.setProps({ show: true, requestId: null })
    expect(mocks.getRequestPayloadDetail).not.toHaveBeenCalled()
    mocks.getRequestPayloadDetail.mockRejectedValueOnce(new Error('detail missing'))
    await wrapper.setProps({ requestId: 'missing' })
    await flushPromises()
    expect(mocks.showError).toHaveBeenCalledWith('detail missing')
    expect(wrapper.text()).toContain('admin.ops.requestDetails.payload.empty')
    wrapper.unmount()
  })
})
