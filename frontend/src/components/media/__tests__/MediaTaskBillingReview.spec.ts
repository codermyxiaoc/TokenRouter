import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import MediaTaskBillingReview from '../MediaTaskBillingReview.vue'
import type { MediaTask, MediaTaskBillingReview as Review } from '@/api/mediaTasks'

const { settle } = vi.hoisted(() => ({ settle: vi.fn() }))
vi.mock('@/api/mediaTasks', () => ({ adminMediaTasksAPI: { settle } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string, values?: Record<string, unknown>) => `${key}${values ? JSON.stringify(values) : ''}` }) }))
vi.mock('@/utils/apiError', () => ({ extractApiErrorMessage: (_: unknown, fallback: string) => fallback }))

const review = (values: Partial<Review> = {}): Review => ({
  status: 'pending', held_balance_amount: 0.3, held_subscription_amount: 0.2,
  original_reserved_amount: 0.5, can_charge: true, can_release: true,
  expires_at: '2026-10-22T01:00:00Z', ...values,
})
const task = (values: Partial<Review> = {}, id = 1) => ({ id, task_id: `image-${id}`, status: 'failed', billing_review: review(values) }) as MediaTask

describe('媒体任务人工账务处理', () => {
  beforeEach(() => { settle.mockReset() })

  it.each([false, true])('显示服务端冻结及账务截止，个人入口 admin=%s 不提供资金操作', admin => {
    const wrapper = mount(MediaTaskBillingReview, { props: { task: task(), admin } })
    expect(wrapper.text()).toContain('$0.30000')
    expect(wrapper.text()).toContain('$0.20000')
    expect(wrapper.text()).toContain('mediaTasks.billingReview.deadline')
    expect(wrapper.find('[data-testid="billing-charge"]').exists()).toBe(admin)
    expect(wrapper.find('[data-testid="billing-release"]').exists()).toBe(admin)
    wrapper.unmount()
  })

  it.each(['charged', 'released', 'expired', 'unreserved', 'unavailable'] as const)('%s 即便携带旧能力字段也不提供操作', status => {
    const wrapper = mount(MediaTaskBillingReview, { props: { task: task({ status }), admin: true } })
    expect(wrapper.find('[data-testid="billing-charge"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="billing-release"]').exists()).toBe(false)
    expect(wrapper.text()).toContain(`mediaTasks.billingReview.hints.${status}`)
    wrapper.unmount()
  })

  it('缺失或禁止操作的账本不根据失败状态推断可退款', () => {
    const wrapper = mount(MediaTaskBillingReview, { props: { task: task({ can_charge: false, can_release: false }), admin: true } })
    expect(wrapper.findAll('button')).toHaveLength(0)
    wrapper.unmount()
    const missing = mount(MediaTaskBillingReview, { props: { task: { id: 1, status: 'failed' } as MediaTask, admin: true } })
    expect(missing.find('[data-testid="billing-review"]').exists()).toBe(false)
    missing.unmount()
  })

  it('账本不可用时不把未知冻结金额显示成零', () => {
    const wrapper = mount(MediaTaskBillingReview, { props: { task: task({ status: 'unavailable' }), admin: true } })
    expect(wrapper.text()).not.toContain('$0.')
    expect(wrapper.text()).toContain('—')
    wrapper.unmount()
  })

  it.each(['charge', 'release'] as const)('%s 展示原金额，理由验证且等待时只提交一次', async action => {
    let resolve!: (task: MediaTask) => void
    settle.mockReturnValue(new Promise<MediaTask>(done => { resolve = done }))
    const wrapper = mount(MediaTaskBillingReview, { props: { task: task(), admin: true } })
    await wrapper.get(`[data-testid="billing-${action}"]`).trigger('click')
    expect(wrapper.get('[data-testid="billing-confirmation"]').text()).toContain('$0.50000')
    await wrapper.get('[data-testid="billing-confirmation"]').trigger('submit')
    expect(settle).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('mediaTasks.billingReview.reasonInvalid')
    await wrapper.get('[data-testid="billing-reason"]').setValue('字'.repeat(501))
    await wrapper.get('[data-testid="billing-confirmation"]').trigger('submit')
    expect(settle).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="billing-reason"]').setValue('  已核对上游结果  ')
    await wrapper.get('[data-testid="billing-confirmation"]').trigger('submit')
    await wrapper.get('[data-testid="billing-confirmation"]').trigger('submit')
    expect(settle).toHaveBeenCalledTimes(1)
    expect(settle).toHaveBeenCalledWith(1, { action, reason: '已核对上游结果' })
    expect(wrapper.get('[data-testid="billing-confirm"]').attributes('disabled')).toBeDefined()
    const updated = task({ status: action === 'charge' ? 'charged' : 'released', can_charge: false, can_release: false })
    resolve(updated)
    await flushPromises()
    expect(wrapper.emitted('settled')).toEqual([[updated]])
    expect(wrapper.find('[data-testid="billing-confirmation"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('失败保留处理理由并恢复按钮，普通用户不展示管理员审计理由', async () => {
    settle.mockRejectedValue(new Error('conflict'))
    const wrapper = mount(MediaTaskBillingReview, { props: { task: task(), admin: true } })
    await wrapper.get('[data-testid="billing-release"]').trigger('click')
    await wrapper.get('[data-testid="billing-reason"]').setValue('需要核对')
    await wrapper.get('[data-testid="billing-confirmation"]').trigger('submit')
    await flushPromises()
    expect(wrapper.get<HTMLTextAreaElement>('[data-testid="billing-reason"]').element.value).toBe('需要核对')
    expect(wrapper.text()).toContain('mediaTasks.billingReview.failed')
    expect(wrapper.get('[data-testid="billing-confirm"]').attributes('disabled')).toBeUndefined()
    await wrapper.setProps({ admin: false, task: task({ reason: '管理员内部备注', actor_id: 8 }) })
    expect(wrapper.text()).not.toContain('管理员内部备注')
    expect(wrapper.find('[data-testid="billing-confirmation"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it.each(['role', 'task', 'unmount'])('%s 改变后忽略旧结算响应', async change => {
    let resolve!: (task: MediaTask) => void
    settle.mockReturnValue(new Promise<MediaTask>(done => { resolve = done }))
    const wrapper = mount(MediaTaskBillingReview, { props: { task: task(), admin: true } })
    await wrapper.get('[data-testid="billing-charge"]').trigger('click')
    await wrapper.get('[data-testid="billing-reason"]').setValue('确认产出')
    await wrapper.get('[data-testid="billing-confirmation"]').trigger('submit')
    if (change === 'role') await wrapper.setProps({ admin: false })
    else if (change === 'task') await wrapper.setProps({ task: task({}, 2) })
    else wrapper.unmount()
    resolve(task({ status: 'charged' }))
    await flushPromises()
    expect(wrapper.emitted('settled')).toBeUndefined()
    if (change !== 'unmount') wrapper.unmount()
  })
})
