import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import SubscriptionsView from '../SubscriptionsView.vue'

const mocks = vi.hoisted(() => ({ list: vi.fn(), bulkExtend: vi.fn(), bulkResetQuota: vi.fn(), showError: vi.fn(), showSuccess: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: {
  subscriptions: { list: mocks.list, bulkExtend: mocks.bulkExtend, bulkResetQuota: mocks.bulkResetQuota },
  payment: { getPlans: vi.fn().mockResolvedValue({ data: [] }) }
} }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: mocks.showError, showSuccess: mocks.showSuccess }) }))
vi.mock('@/composables/useBalanceDisplay', () => ({ useBalanceDisplay: () => ({ formatBalanceAmount: String }) }))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({ t: (key: string) => key }) }))

// 复用 DataTable 的受控选择契约，测试页面发出的批量参数及失败重试边界。
const mountView = () => mount(SubscriptionsView, { global: { stubs: {
  AppLayout: { template: '<div><slot /></div>' },
  TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
  DataTable: { name: 'DataTable', props: ['selectedKeys'], emits: ['update:selectedKeys', 'sort'], template: '<div />' },
  BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' },
  Select: true, ConfirmDialog: true, EmptyState: true, Pagination: true, Icon: true, Teleport: true, RouterLink: true
} } })

const rows = [
  { id: 1, user_id: 7, status: 'active', starts_at: '2026-09-01', expires_at: '2026-10-01' },
  { id: 2, user_id: 7, status: 'pending', starts_at: '2026-10-01', expires_at: '2026-11-01' }
]

describe('subscription bulk management', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.clear()
    mocks.list.mockResolvedValue({ items: rows, total: 2, pages: 1 })
    mocks.bulkExtend.mockResolvedValue({ subscription_ids: [1, 2], updated_count: 2 })
    mocks.bulkResetQuota.mockResolvedValue({ subscription_ids: [1, 2], updated_count: 2 })
  })

  async function selectAndOpen(wrapper: ReturnType<typeof mountView>, action = 'bulkExtend') {
    await flushPromises()
    wrapper.getComponent({ name: 'DataTable' }).vm.$emit('update:selectedKeys', [1, 2])
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === `admin.subscriptions.${action}`)!.trigger('click')
  }

  it('adds the entered days to the selected IDs and clears selection after success', async () => {
    const wrapper = mountView()
    try {
      await selectAndOpen(wrapper)
      await wrapper.get('#bulk-subscription-days').setValue(5)
      await wrapper.get('#bulk-subscription-form').trigger('submit')
      await flushPromises()
      expect(mocks.bulkExtend).toHaveBeenCalledWith([1, 2], 5, expect.stringContaining('subscription-bulk-'))
      expect(wrapper.getComponent({ name: 'DataTable' }).props('selectedKeys')).toEqual([])
      expect(wrapper.find('#bulk-subscription-form').exists()).toBe(false)
    } finally { wrapper.unmount() }
  })

  it('keeps the confirmed payload and idempotency key when retrying a lost response', async () => {
    mocks.bulkExtend.mockRejectedValueOnce(new Error('network'))
    const wrapper = mountView()
    try {
      await selectAndOpen(wrapper)
      await wrapper.get('#bulk-subscription-days').setValue(3)
      await wrapper.get('#bulk-subscription-form').trigger('submit')
      await flushPromises()
      expect(wrapper.get('[role="alert"]').text()).toBe('admin.subscriptions.bulkFailed')
      expect((wrapper.get('#bulk-subscription-days').element as HTMLInputElement).disabled).toBe(true)
      await wrapper.get('#bulk-subscription-form').trigger('submit')
      await flushPromises()
      expect(mocks.bulkExtend).toHaveBeenCalledTimes(2)
      expect(mocks.bulkExtend.mock.calls[1]).toEqual(mocks.bulkExtend.mock.calls[0])
    } finally { wrapper.unmount() }
  })

  it('resets only selected quota windows and rejects an empty choice', async () => {
    const wrapper = mountView()
    try {
      await selectAndOpen(wrapper, 'bulkReset')
      for (const checkbox of wrapper.findAll('[data-bulk-window]')) await checkbox.setValue(false)
      await wrapper.get('#bulk-subscription-form').trigger('submit')
      expect(mocks.bulkResetQuota).not.toHaveBeenCalled()
      expect(wrapper.get('[role="alert"]').text()).toBe('admin.subscriptions.bulkSelectWindow')
      await wrapper.get('[data-bulk-window="daily"]').setValue(true)
      await wrapper.get('#bulk-subscription-form').trigger('submit')
      await flushPromises()
      expect(mocks.bulkResetQuota).toHaveBeenCalledWith([1, 2], { daily: true, weekly: false, monthly: false }, expect.any(String))
    } finally { wrapper.unmount() }
  })

  it('ignores hidden IDs and clears the selection when sorting changes the page', async () => {
    const wrapper = mountView()
    try {
      await flushPromises()
      const table = wrapper.getComponent({ name: 'DataTable' })
      table.vm.$emit('update:selectedKeys', [1, 1, 999])
      await flushPromises()
      expect(table.props('selectedKeys')).toEqual([1])
      table.vm.$emit('sort', 'expires_at', 'asc')
      await flushPromises()
      expect(table.props('selectedKeys')).toEqual([])
    } finally { wrapper.unmount() }
  })

  it('does not open a batch that includes revoked subscriptions', async () => {
    mocks.list.mockResolvedValue({ items: [rows[0], { ...rows[1], status: 'revoked' }], total: 2, pages: 1 })
    const wrapper = mountView()
    try {
      await selectAndOpen(wrapper)
      expect(mocks.showError).toHaveBeenCalledWith('admin.subscriptions.bulkInvalidStatus')
      expect(wrapper.find('#bulk-subscription-form').exists()).toBe(false)
      expect(mocks.bulkExtend).not.toHaveBeenCalled()
    } finally { wrapper.unmount() }
  })
})
