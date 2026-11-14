import { mount, flushPromises } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import RequestDataCacheView from '../RequestDataCacheView.vue'

const api = vi.hoisted(() => ({ getUsageRequestData: vi.fn(), getErrorRequestData: vi.fn() }))
vi.mock('@/api/usage', () => api)
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key }),
}))

const entry = {
  request_id: 'req-1', kind: 'error', method: 'POST', path: '/v1/responses', inbound_endpoint: '/v1/responses',
  upstream_endpoint: '/backend-api/codex/responses', platform: 'openai', model: 'gpt-5', status_code: 502,
  stream: true, created_at: '2026-11-13T00:00:00Z', completed_at: '2026-11-13T00:00:01Z',
}

describe('请求缓存数据查看', () => {
  beforeEach(() => vi.clearAllMocks())

  it('错误请求按错误 ID 读取并展示上游端点', async () => {
    api.getErrorRequestData.mockResolvedValue(entry)
    const wrapper = mount(RequestDataCacheView, { props: { source: 'error', recordId: 9 } })
    await flushPromises()
    expect(api.getErrorRequestData).toHaveBeenCalledWith(9)
    expect(api.getUsageRequestData).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('/backend-api/codex/responses')
    expect(wrapper.text()).toContain('502')
  })

  it('未缓存（404）显示提示而不是加载失败', async () => {
    api.getUsageRequestData.mockRejectedValue({ status: 404 })
    const wrapper = mount(RequestDataCacheView, { props: { source: 'usage', recordId: 3 } })
    await flushPromises()
    expect(wrapper.text()).toContain('usage.requestData.notCached')
    expect(wrapper.text()).not.toContain('usage.requestData.loadFailed')
  })

  it('快速切换记录时旧响应不覆盖新记录', async () => {
    let resolveOld!: (v: unknown) => void
    api.getUsageRequestData
      .mockImplementationOnce(() => new Promise((r) => { resolveOld = r }))
      .mockResolvedValueOnce({ ...entry, request_id: 'req-new' })
    const wrapper = mount(RequestDataCacheView, { props: { source: 'usage', recordId: 1 } })
    await wrapper.setProps({ recordId: 2 })
    await flushPromises()
    resolveOld({ ...entry, request_id: 'req-old' })
    await flushPromises()
    expect(wrapper.text()).toContain('req-new')
    expect(wrapper.text()).not.toContain('req-old')
  })
})
