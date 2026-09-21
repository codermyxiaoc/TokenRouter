import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import AssignSubscriptionDialog from '../AssignSubscriptionDialog.vue'
import type { SubscriptionPlan } from '@/types/payment'

const { listUsers, assign, bulkAssign, showSuccess, showError } = vi.hoisted(() => ({
  listUsers: vi.fn(), assign: vi.fn(), bulkAssign: vi.fn(), showSuccess: vi.fn(), showError: vi.fn()
}))
vi.mock('@/api/admin', () => ({ adminAPI: { users: { list: listUsers }, subscriptions: { assign, bulkAssign } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess, showError }) }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => params ? `${key}:${JSON.stringify(params)}` : key })
}))

const user = (id: number) => ({ id, email: `user${id}@example.com` })
const result = (successIDs: number[], failedCount = 0) => ({
  success_count: successIDs.length, created_count: successIDs.length, reused_count: 0,
  failed_count: failedCount, subscriptions: successIDs.map(id => ({ id: id + 100, user_id: id })),
  errors: failedCount > 0 ? ['user 2: unavailable'] : []
})
let wrapper: VueWrapper
const mountDialog = () => {
  wrapper = mount(AssignSubscriptionDialog, {
    props: { show: true, plans: [{ id: 7, name: 'Test plan', description: null } as SubscriptionPlan] },
    global: { stubs: {
      BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' },
      Select: { name: 'Select', props: ['modelValue', 'options', 'disabled'], template: '<div data-test="plan-select" />' },
      Icon: true
    } }
  })
  wrapper.getComponent({ name: 'Select' }).vm.$emit('update:modelValue', 7)
  return wrapper
}
const pickUser = async (id: number) => {
  listUsers.mockResolvedValueOnce({ items: [user(id)] })
  const search = wrapper.get('#subscription-assign-user')
  await search.setValue(`user${id}`)
  await vi.advanceTimersByTimeAsync(300)
  await flushPromises()
  await wrapper.get(`[data-user-id="${id}"]`).trigger('click')
}
const enableBatch = async () => wrapper.get('input[type="checkbox"]').setValue(true)
const submit = async () => {
  await wrapper.get('#assign-subscription-form').trigger('submit')
  await flushPromises()
}

beforeEach(() => {
  vi.clearAllMocks()
  localStorage.clear()
  sessionStorage.clear()
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
  listUsers.mockResolvedValue({ items: [] })
  assign.mockResolvedValue({})
})
afterEach(() => {
  wrapper?.unmount()
  vi.useRealTimers()
})

describe('订阅分配弹窗', () => {
  it('单条分配沿用套餐 ID 和初始 30 天，成功关闭并通知列表刷新', async () => {
    mountDialog()
    await pickUser(1)
    await submit()
    expect(assign).toHaveBeenCalledWith({ user_id: 1, plan_id: 7, validity_days: 30 })
    expect(bulkAssign).not.toHaveBeenCalled()
    expect(wrapper.emitted('assigned')).toHaveLength(1)
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('单条输入变化立即取消原用户，不能在防抖期间误发放', async () => {
    mountDialog()
    await pickUser(1)
    await wrapper.get('#subscription-assign-user').setValue('another')
    await submit()
    expect(assign).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalledWith('admin.subscriptions.pleaseSelectUser')
  })

  it('搜索忽略迟到的旧结果，关闭弹窗后也不恢复旧候选', async () => {
    mountDialog()
    let resolveOld!: (value: unknown) => void
    listUsers.mockReturnValueOnce(new Promise(resolve => { resolveOld = resolve }))
    await wrapper.get('#subscription-assign-user').setValue('old')
    await vi.advanceTimersByTimeAsync(300)
    listUsers.mockResolvedValueOnce({ items: [user(2)] })
    await wrapper.get('#subscription-assign-user').setValue('new')
    await vi.advanceTimersByTimeAsync(300)
    resolveOld({ items: [user(1)] })
    await flushPromises()
    expect(wrapper.find('[data-user-id="1"]').exists()).toBe(false)
    expect(wrapper.find('[data-user-id="2"]').exists()).toBe(true)
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    expect(wrapper.find('[data-user-id="2"]').exists()).toBe(false)
  })

  it('批量部分成功后只保留失败用户，下一次提交使用新幂等键', async () => {
    mountDialog()
    await enableBatch()
    await pickUser(1)
    await pickUser(2)
    bulkAssign.mockResolvedValueOnce(result([1], 1))
    await submit()
    const [firstPayload, firstKey] = bulkAssign.mock.calls[0]
    expect(firstPayload).toEqual({ user_ids: [1, 2], plan_id: 7, validity_days: 30 })
    expect(firstKey).toEqual(expect.any(String))
    expect(wrapper.get('[data-test="assign-users"]').text()).not.toContain(user(1).email)
    expect(wrapper.get('[data-test="assign-users"]').text()).toContain(user(2).email)
    expect(wrapper.find('[data-test="batch-assign-result"]').exists()).toBe(true)
    expect(wrapper.emitted('close')).toBeUndefined()
    bulkAssign.mockResolvedValueOnce(result([2]))
    await submit()
    expect(bulkAssign.mock.calls[1][0].user_ids).toEqual([2])
    expect(bulkAssign.mock.calls[1][1]).not.toBe(firstKey)
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeDefined()
  })

  it('网络失败锁住参数，关闭重开后仍以相同参数和幂等键确认', async () => {
    mountDialog()
    await enableBatch()
    await pickUser(1)
    bulkAssign.mockRejectedValueOnce(new Error('Network interrupted'))
    await submit()
    expect(wrapper.get('#subscription-assign-days').attributes('disabled')).toBeDefined()
    expect(wrapper.get('input[type="checkbox"]').attributes('disabled')).toBeDefined()
    expect(wrapper.find('[data-test="assign-pending"]').exists()).toBe(true)
    const closeButton = wrapper.findAll('button').find(button => button.text() === 'common.close')!
    await closeButton.trigger('click')
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    bulkAssign.mockResolvedValueOnce(result([1]))
    await submit()
    expect(bulkAssign.mock.calls[1]).toEqual(bulkAssign.mock.calls[0])
    expect(wrapper.find('[data-test="assign-pending"]').exists()).toBe(false)
  })

  it('明确首次参数拒绝允许纠正，后续提交产生新的请求键', async () => {
    mountDialog()
    await enableBatch()
    await pickUser(1)
    bulkAssign.mockRejectedValueOnce({ status: 400, message: 'Invalid request: rejected before execution' })
    await submit()
    expect(wrapper.find('[data-test="assign-pending"]').exists()).toBe(false)
    expect(wrapper.get('#subscription-assign-days').attributes('disabled')).toBeUndefined()
    expect(showError).toHaveBeenCalledWith('Invalid request: rejected before execution')
    bulkAssign.mockResolvedValueOnce(result([1]))
    await submit()
    expect(bulkAssign.mock.calls[1][1]).not.toEqual(bulkAssign.mock.calls[0][1])
  })

  it('已有未知结果后出现参数拒绝也不能解除锁定或丢失原请求', async () => {
    mountDialog()
    await enableBatch()
    await pickUser(1)
    bulkAssign.mockRejectedValueOnce({ status: 0, message: 'Network error' })
    await submit()
    bulkAssign.mockRejectedValueOnce({ status: 400, reason: 'IDEMPOTENCY_PAYLOAD_INVALID', message: 'Invalid payload' })
    await submit()
    expect(wrapper.find('[data-test="assign-pending"]').exists()).toBe(true)
    expect(wrapper.get('#subscription-assign-days').attributes('disabled')).toBeDefined()
    expect(bulkAssign.mock.calls[1]).toEqual(bulkAssign.mock.calls[0])
  })

  it('后端拒绝重新执行时显示核对提示，仍只确认原请求', async () => {
    mountDialog()
    await enableBatch()
    await pickUser(1)
    bulkAssign.mockRejectedValueOnce({ status: 409, reason: 'IDEMPOTENCY_RESULT_UNCONFIRMED', message: 'Verify existing subscriptions' })
    await submit()
    expect(wrapper.get('[data-test="assign-pending"]').text()).toBe('admin.subscriptions.batchAssign.blockedHint')
    expect(wrapper.get('button[type="submit"]').text()).toBe('admin.subscriptions.batchAssign.confirmResult')
    expect(showError).toHaveBeenCalledWith('Verify existing subscriptions')
    bulkAssign.mockResolvedValueOnce(result([1]))
    await submit()
    expect(bulkAssign.mock.calls[1]).toEqual(bulkAssign.mock.calls[0])
  })

  it('组件卸载重建后按管理员身份恢复待确认请求，成功后清除保存状态', async () => {
    localStorage.setItem('auth_user', JSON.stringify({ id: 7 }))
    mountDialog()
    await enableBatch()
    await pickUser(1)
    bulkAssign.mockRejectedValueOnce({ status: 409, reason: 'IDEMPOTENCY_RESULT_UNCONFIRMED', message: 'Verify existing subscriptions' })
    await submit()
    wrapper.unmount()
    mountDialog()
    expect(wrapper.get('[data-test="assign-pending"]').text()).toBe('admin.subscriptions.batchAssign.blockedHint')
    bulkAssign.mockResolvedValueOnce(result([1]))
    await submit()
    expect(bulkAssign.mock.calls[1]).toEqual(bulkAssign.mock.calls[0])
    expect(sessionStorage.getItem('subscription-operation:7:assign')).toBeNull()
  })

  it('结果结构缺少成功用户时不释放幂等键，也不重新发放已完成用户', async () => {
    mountDialog()
    await enableBatch()
    await pickUser(1)
    bulkAssign.mockResolvedValueOnce({ ...result([1]), subscriptions: [] })
    await submit()
    expect(wrapper.find('[data-test="assign-pending"]').exists()).toBe(true)
    expect(wrapper.emitted('assigned')).toBeUndefined()
    bulkAssign.mockResolvedValueOnce(result([1]))
    await submit()
    expect(bulkAssign.mock.calls[1]).toEqual(bulkAssign.mock.calls[0])
  })

  it('按紧凑响应中的 active 和 queued 状态移除成功用户，保留 failed 用户', async () => {
    mountDialog()
    await enableBatch()
    await pickUser(1)
    await pickUser(2)
    await pickUser(3)
    bulkAssign.mockResolvedValueOnce({ ...result([1, 3], 1), subscriptions: [], statuses: { 1: 'active', 2: 'failed', 3: 'queued' } })
    await submit()
    expect(wrapper.find('[data-test="assign-pending"]').exists()).toBe(false)
    expect(wrapper.findAll('[data-test="assign-users"] li')).toHaveLength(1)
    expect(wrapper.get('[data-test="assign-users"]').text()).toContain(user(2).email)
  })

  it('批量选择按 ID 去重，允许移除，最多 100 位用户', async () => {
    mountDialog()
    await enableBatch()
    await pickUser(1)
    listUsers.mockResolvedValueOnce({ items: [user(1)] })
    await wrapper.get('#subscription-assign-user').setValue('user1')
    await vi.advanceTimersByTimeAsync(300)
    await flushPromises()
    expect(wrapper.get('[data-user-id="1"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="assign-users"] button').trigger('click')
    expect(wrapper.find('[data-test="assign-users"]').exists()).toBe(false)
    for (let id = 1; id <= 100; id++) await pickUser(id)
    expect(wrapper.get('#subscription-assign-user').attributes('disabled')).toBeDefined()
    expect(wrapper.findAll('[data-test="assign-users"] li')).toHaveLength(100)
    bulkAssign.mockResolvedValueOnce(result(Array.from({ length: 100 }, (_, index) => index + 1)))
    await submit()
    expect(bulkAssign.mock.calls[0][0].user_ids).toHaveLength(100)
  })

  it.each([0, 1.5, 36501])('无效天数 %s 不发送分配请求', async days => {
    mountDialog()
    await pickUser(1)
    await wrapper.get('#subscription-assign-days').setValue(days)
    await submit()
    expect(assign).not.toHaveBeenCalled()
    expect(bulkAssign).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalledWith('admin.subscriptions.validityDaysRequired')
  })
})
