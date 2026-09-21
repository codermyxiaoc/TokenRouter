import { shallowMount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import OpsErrorDetailsModal from '../OpsErrorDetailsModal.vue'
import OpsErrorLogTable from '../OpsErrorLogTable.vue'
import OpsRequestPayloadModal from '../OpsRequestPayloadModal.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'

vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key }),
}))
vi.mock('@/api/admin/ops', () => {
  const opsAPI = { listRequestErrors: vi.fn(), listUpstreamErrors: vi.fn() }
  return { opsAPI, default: opsAPI, getRequestPayloadDetail: vi.fn() }
})

describe('OpsErrorDetailsModal 请求载荷入口', () => {
  // 运维页也必须承接表格的端点事件，关闭载荷后保留原列表上下文。
  it('点击端点打开载荷，关闭返回列表，主弹窗关闭同时关闭载荷', async () => {
    const wrapper = shallowMount(OpsErrorDetailsModal, {
      props: { show: true, timeRange: '1h', errorType: 'request' },
      global: { stubs: { BaseDialog: { props: ['show'], template: '<div><slot /></div>' } } },
    })
    wrapper.findComponent(OpsErrorLogTable).vm.$emit('openRequestPayloadDetail', 'request-123')
    await wrapper.vm.$nextTick()
    expect(wrapper.findComponent(OpsRequestPayloadModal).props()).toMatchObject({ show: true, requestId: 'request-123' })
    expect(wrapper.findComponent(BaseDialog).props('show')).toBe(false)
    wrapper.findComponent(OpsRequestPayloadModal).vm.$emit('update:show', false)
    await wrapper.vm.$nextTick()
    expect(wrapper.findComponent(BaseDialog).props('show')).toBe(true)
    wrapper.findComponent(OpsErrorLogTable).vm.$emit('openRequestPayloadDetail', 'request-456')
    await wrapper.setProps({ show: false })
    expect(wrapper.findComponent(OpsRequestPayloadModal).props('show')).toBe(false)
    wrapper.unmount()
  })
})
