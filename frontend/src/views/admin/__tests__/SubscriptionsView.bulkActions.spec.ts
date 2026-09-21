import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import SubscriptionsView from '../SubscriptionsView.vue'

const mocks = vi.hoisted(() => ({ list: vi.fn(), bulkExtend: vi.fn(), bulkResetQuota: vi.fn(), bulkRevoke: vi.fn(), bulkRestore: vi.fn(), showError: vi.fn(), showSuccess: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: {
  subscriptions: { list: mocks.list, bulkExtend: mocks.bulkExtend, bulkResetQuota: mocks.bulkResetQuota, bulkRevoke: mocks.bulkRevoke, bulkRestore: mocks.bulkRestore },
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
    sessionStorage.clear()
    mocks.list.mockResolvedValue({ items: rows, total: 2, pages: 1 })
    mocks.bulkExtend.mockResolvedValue({ subscription_ids: [1, 2], updated_count: 2 })
    mocks.bulkResetQuota.mockResolvedValue({ subscription_ids: [1, 2], updated_count: 2 })
  })

  async function selectAndOpen(wrapper: ReturnType<typeof mountView>, action = 'bulkExtend') {
    await flushPromises()
    wrapper.getComponent({ name: 'DataTable' }).vm.$emit('update:selectedKeys', [1, 2])
    await flushPromises()
    await wrapper.get(`[data-test="bulk-${action === 'bulkReset' ? 'reset' : 'extend'}"]`).trigger('click')
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
      expect(wrapper.get('[role="alert"]').text()).toBe('network')
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
  it('confirms all non-revoked statuses for revocation, including queued and suspended subscriptions', async () => {
    const targets = [...rows, { ...rows[0], id: 3, status: 'suspended' }, { ...rows[0], id: 4, status: 'revoked' }]
    mocks.list.mockResolvedValue({ items: targets, total: 4, pages: 1 })
    mocks.bulkRevoke.mockResolvedValue({ subscription_ids: [1, 2, 3], updated_count: 3 })
    const wrapper = mountView()
    try {
      await flushPromises()
      wrapper.getComponent({ name: 'DataTable' }).vm.$emit('update:selectedKeys', [1, 2, 3, 4])
      await flushPromises()
      await wrapper.get('[data-test="bulk-revoke"]').trigger('click')
      expect(wrapper.findAll('[data-test="bulk-targets"] li')).toHaveLength(3)
      expect(wrapper.find('[data-bulk-window]').exists()).toBe(false)
      await wrapper.get('#bulk-subscription-form').trigger('submit')
      await flushPromises()
      expect(mocks.bulkRevoke).toHaveBeenCalledWith([1, 2, 3], expect.stringContaining('subscription-bulk-'))
      expect(mocks.bulkResetQuota).not.toHaveBeenCalled()
    } finally { wrapper.unmount() }
  })

  it('restores only the revoked records and preserves the server overlap error', async () => {
    mocks.list.mockResolvedValue({ items: [rows[0], { ...rows[1], status: 'revoked' }], total: 2, pages: 1 })
    mocks.bulkRestore.mockRejectedValueOnce({ response: { status: 409, data: { message: 'Subscription time overlaps' } } })
    const wrapper = mountView()
    try {
      await flushPromises()
      wrapper.getComponent({ name: 'DataTable' }).vm.$emit('update:selectedKeys', [1, 2])
      await flushPromises()
      await wrapper.get('[data-test="bulk-restore"]').trigger('click')
      expect(wrapper.findAll('[data-test="bulk-targets"] li')).toHaveLength(1)
      await wrapper.get('#bulk-subscription-form').trigger('submit')
      await flushPromises()
      expect(mocks.bulkRestore).toHaveBeenCalledWith([2], expect.any(String))
      expect(wrapper.get('[role="alert"]').text()).toBe('Subscription time overlaps')
      expect(wrapper.getComponent({ name: 'DataTable' }).props('selectedKeys')).toEqual([1, 2])
    } finally { wrapper.unmount() }
  })

  it('keeps an unconfirmed batch when closed and reopened through another action', async () => {
    mocks.bulkExtend.mockRejectedValueOnce(new Error('network'))
    const wrapper = mountView()
    try {
      await selectAndOpen(wrapper)
      await wrapper.get('#bulk-subscription-days').setValue(8)
      await wrapper.get('#bulk-subscription-form').trigger('submit')
      await flushPromises()
      await wrapper.findAll('button').find(button => button.text() === 'common.cancel')!.trigger('click')
      await wrapper.get('[data-test="bulk-reset"]').trigger('click')
      expect((wrapper.get('#bulk-subscription-days').element as HTMLInputElement).value).toBe('8')
      await wrapper.get('#bulk-subscription-form').trigger('submit')
      await flushPromises()
      expect(mocks.bulkExtend.mock.calls[1]).toEqual(mocks.bulkExtend.mock.calls[0])
      expect(mocks.bulkResetQuota).not.toHaveBeenCalled()
    } finally { wrapper.unmount() }
  })

  it('unlocks a definitively rolled-back restore conflict from the normalized API error', async () => {
    mocks.list.mockResolvedValue({ items: [{ ...rows[0], status: 'revoked' }], total: 1, pages: 1 })
    mocks.bulkRestore.mockRejectedValueOnce({ status: 409, reason: 'SUBSCRIPTION_RESTORE_CONFLICT', message: 'Overlapping subscription' })
    const wrapper = mountView()
    try {
      await flushPromises()
      wrapper.getComponent({ name: 'DataTable' }).vm.$emit('update:selectedKeys', [1])
      await flushPromises()
      await wrapper.get('[data-test="bulk-restore"]').trigger('click')
      await wrapper.get('#bulk-subscription-form').trigger('submit')
      await flushPromises()
      expect(wrapper.get('[role="alert"]').text()).toBe('Overlapping subscription')
      await wrapper.findAll('button').find(button => button.text() === 'common.cancel')!.trigger('click')
      expect(wrapper.find('[data-test="resume-bulk-operation"]').exists()).toBe(false)
    } finally { wrapper.unmount() }
  })

  it('shows the specific expired-chain error and allows correcting the selection', async () => {
    mocks.bulkExtend.mockRejectedValueOnce({ status: 400, reason: 'SUBSCRIPTION_BULK_EXPIRED_HAS_SUCCESSOR', metadata: { subscription_id: '1' }, message: 'Cannot extend past entry' })
    const wrapper = mountView()
    try {
      await selectAndOpen(wrapper)
      await wrapper.get('#bulk-subscription-form').trigger('submit')
      await flushPromises()
      expect(wrapper.get('[role="alert"]').text()).toBe('admin.subscriptions.bulkExpiredHasSuccessor')
      expect((wrapper.get('#bulk-subscription-days').element as HTMLInputElement).disabled).toBe(false)
    } finally { wrapper.unmount() }
  })

  it('restores an unconfirmed request after remount even when its rows have left the current page', async () => {
    localStorage.setItem('auth_user', JSON.stringify({ id: 100, role: 'admin' }))
    mocks.bulkExtend.mockRejectedValueOnce({ status: 0, message: 'Network error' })
    const first = mountView()
    await selectAndOpen(first)
    await first.get('#bulk-subscription-days').setValue(9)
    await first.get('#bulk-subscription-form').trigger('submit')
    await flushPromises()
    const original = mocks.bulkExtend.mock.calls[0]
    first.unmount()
    mocks.list.mockResolvedValue({ items: [], total: 0, pages: 0 })
    const second = mountView()
    try {
      await flushPromises()
      await second.get('[data-test="resume-bulk-operation"]').trigger('click')
      expect(second.findAll('[data-test="bulk-targets"] li')).toHaveLength(2)
      await second.get('#bulk-subscription-form').trigger('submit')
      await flushPromises()
      expect(mocks.bulkExtend.mock.calls[1]).toEqual(original)
      expect(second.find('[data-test="resume-bulk-operation"]').exists()).toBe(false)
    } finally { second.unmount() }
  })

})
