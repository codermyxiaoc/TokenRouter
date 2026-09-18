import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import UserErrorRequestsTable from '../UserErrorRequestsTable.vue'
import UserErrorDetailModal from '../UserErrorDetailModal.vue'
import type { UserErrorRequest, UserErrorRequestDetail } from '@/types'

const { getMyErrorDetail } = vi.hoisted(() => ({ getMyErrorDetail: vi.fn() }))

vi.mock('@/api/usage', () => ({ getMyErrorDetail }))
vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key }),
}))

function errorRow(overrides: Partial<UserErrorRequest> = {}): UserErrorRequest {
  return {
    id: 1,
    created_at: '2026-09-17T00:00:00Z',
    model: 'gpt-6-astra',
    inbound_endpoint: '/v1/responses',
    status_code: 503,
    category: 'upstream',
    platform: 'openai',
    message: '上游暂时不可用',
    key_name: '用户 Key',
    key_deleted: false,
    group_name: '失败分组',
    ...overrides,
  }
}

function mountTable(rows: UserErrorRequest[]) {
  return mount(UserErrorRequestsTable, {
    props: { rows, total: rows.length, loading: false, page: 1, pageSize: 20 },
    global: { stubs: { IpGeoBatchToolbar: true, Pagination: true, UserErrorDetailModal: true } },
  })
}

describe('用户错误请求恢复信息', () => {
  beforeEach(() => getMyErrorDetail.mockReset())

  // 同一列表同时包含恢复和最终失败，不以客户端 HTTP 200 判断恢复。
  it('区分恢复记录与最终失败并保留实际失败分组', () => {
    const wrapper = mountTable([
      errorRow({ recovered_upstream: true, client_status_code: 200, recovered_group_id: 3, recovered_group_name: '恢复分组' }),
      errorRow({ id: 2, recovered_upstream: false, client_status_code: 200, recovered_group_name: '禁止猜测的目标' }),
    ])

    expect(wrapper.text()).toContain('503')
    expect(wrapper.text()).toContain('失败分组')
    expect(wrapper.text()).toContain('usage.errors.recovered')
    expect(wrapper.text()).toContain('usage.errors.finalStatus 200')
    expect(wrapper.text()).toContain('usage.errors.recoveredTo')
    expect(wrapper.text()).toContain('恢复分组')
    expect(wrapper.text()).toContain('usage.errors.finalFailed')
    expect(wrapper.text()).not.toContain('禁止猜测的目标')
  })

  it('历史恢复记录缺少目标快照时仅展示恢复状态', () => {
    const wrapper = mountTable([errorRow({ recovered_upstream: true, client_status_code: 200 })])

    expect(wrapper.text()).toContain('usage.errors.recovered')
    expect(wrapper.text()).not.toContain('usage.errors.recoveredTo')
    expect(wrapper.text()).not.toContain('usage.errors.finalFailed')
  })

  it('只有目标 ID 时明确显示该 ID', () => {
    const wrapper = mountTable([errorRow({ recovered_upstream: true, recovered_group_id: 3 })])

    expect(wrapper.text()).toContain('usage.errors.recoveredTo')
    expect(wrapper.text()).toContain('#3')
  })

  it('详情展示恢复目标和原失败分组', async () => {
    const detail: UserErrorRequestDetail = {
      ...errorRow({ recovered_upstream: true, client_status_code: 200, recovered_group_name: '恢复分组' }),
      error_body: '',
      upstream_status_code: 503,
    }
    getMyErrorDetail.mockResolvedValue(detail)
    const wrapper = mount(UserErrorDetailModal, {
      props: { show: false, errorId: null },
      global: { stubs: { BaseDialog: { template: '<div><slot /></div>' } } },
    })
    await wrapper.setProps({ show: true, errorId: 1 })
    await flushPromises()

    expect(getMyErrorDetail).toHaveBeenCalledWith(1)
    expect(wrapper.text()).toContain('失败分组')
    expect(wrapper.text()).toContain('恢复分组')
    expect(wrapper.text()).toContain('usage.errors.recoveredTo')
    expect(wrapper.text()).toContain('usage.errors.finalStatus 200')
    expect(wrapper.text()).not.toContain('usage.errors.finalFailed')
    expect(wrapper.find('pre').exists()).toBe(false)
  })
})
