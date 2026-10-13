import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import type { DailyActivitiesStatus } from '@/api/dailyActivities'
import { pendingClaimStorageKey } from '@/utils/dailyActivityClaims'

const { status, history, claim, refreshUser, auth } = vi.hoisted(() => ({
  status: vi.fn(), history: vi.fn(), claim: vi.fn(), refreshUser: vi.fn(), auth: { user: { id: 12 } },
}))
vi.mock('@/api/dailyActivities', async importOriginal => ({ ...(await importOriginal<typeof import('@/api/dailyActivities')>()), dailyActivitiesAPI: { status, history, claim } }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ ...auth, refreshUser }) }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ cachedPublicSettings: { payment_enabled: true } }) }))
vi.mock('@/composables/useBalanceDisplay', () => ({ useBalanceDisplay: () => ({ formatBalanceAmount: (amount: number) => `$${amount.toFixed(2)}` }) }))
vi.mock('vue-i18n', async importOriginal => ({ ...(await importOriginal<typeof import('vue-i18n')>()), useI18n: () => ({ t: (key: string, params: unknown) => params ? `${key} ${JSON.stringify(params)}` : key, locale: { value: 'zh' } }) }))
import DailyActivitiesView from '../DailyActivitiesView.vue'

const fixture = (): DailyActivitiesStatus => ({ enabled: true, started_at: '2026-10-13T00:00:00Z', day: '2026-10-13', activities: [
  { type: 'daily_gift', threshold: 10, reward_max: 0.5, eligible_amount: 10, available_count: 1, claimed_count: 0, progress: 1 },
  { type: 'recharge', threshold: 50, reward_max: 0.8, eligible_amount: 5500, available_count: 105, claimed_count: 5, progress: 110 },
  { type: 'consumption', threshold: 50, reward_max: 0.8, eligible_amount: 120, available_count: 1, claimed_count: 1, progress: 2.4 },
] })
const mountPage = () => mount(DailyActivitiesView, { global: { stubs: {
  AppLayout: { template: '<div><slot /></div>' }, RouterLink: { template: '<a><slot /></a>' },
  Select: { props: ['modelValue', 'options'], emits: ['update:modelValue'], template: '<button data-filter @click="$emit(\'update:modelValue\', \'recharge\')">Filter</button>' },
  Pagination: { props: ['page', 'total'], emits: ['update:page'], template: '<button data-page @click="$emit(\'update:page\', 2)">{{ page }} / {{ total }}</button>' },
} } })
const reward = { items: [{ id: 1, activity_type: 'recharge', amount: 0.25, created_at: '2026-10-13T00:00:00Z' }], amount: 0.25, balance: 5.25, remaining_count: 1, replayed: false }

describe('每日活动页面', () => {
  beforeEach(() => {
    vi.clearAllMocks(); sessionStorage.clear(); auth.user.id = 12
    status.mockResolvedValue(fixture())
    history.mockResolvedValue({ items: [], total: 45, page: 1, page_size: 20 })
    claim.mockResolvedValue(reward); refreshUser.mockResolvedValue({})
  })
  it('三活动顺序、跨日累计与每日重置说明、100次分批说明和进度上限', async () => {
    const wrapper = mountPage(); await flushPromises()
    expect(wrapper.findAll('[data-activity]').map(item => item.attributes('data-activity'))).toEqual(['daily_gift', 'recharge', 'consumption'])
    expect(wrapper.get('[data-activity="daily_gift"]').classes()).toContain('md:col-span-2')
    expect(wrapper.text()).toContain('dailyActivities.dailyReset')
    expect(wrapper.text()).toContain('dailyActivities.persistent')
    expect(wrapper.text()).toContain('dailyActivities.batchLimit')
    expect(wrapper.findAll('[role="progressbar"]').every(item => Number(item.attributes('aria-valuenow')) <= 100)).toBe(true)
    expect(claim).not.toHaveBeenCalled(); wrapper.unmount()
  })
  it('关闭时禁领，仍保留本人记录', async () => {
    status.mockResolvedValue({ ...fixture(), enabled: false })
    history.mockResolvedValue({ items: reward.items, total: 1, page: 1, page_size: 20 })
    const wrapper = mountPage(); await flushPromises()
    expect(wrapper.get('[data-claim="recharge"]').attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('+\u00240.25')
    expect(wrapper.text()).toContain('dailyActivities.disabled'); wrapper.unmount()
  })
  it('不展示奖励区间和充值口径长提示，保留奖励记录金额', async () => {
    history.mockResolvedValue({ items: reward.items, total: 1, page: 1, page_size: 20 })
    const wrapper = mountPage(); await flushPromises()
    expect(wrapper.text()).not.toContain('dailyActivities.rewardRange')
    expect(wrapper.text()).not.toContain('dailyActivities.eligibilityHint')
    expect(wrapper.find('[data-recharge-sources]').exists()).toBe(false)
    expect(wrapper.text()).toContain('$0.25')
    wrapper.unmount()
  })
  it.each([
    [true, true, true], [true, true, false], [true, false, true], [true, false, false],
    [false, true, true], [false, true, false], [false, false, true], [false, false, false],
  ])('独立开关控制卡片与布局：礼包%s、充值%s、消费%s', async (giftEnabled, rechargeEnabled, consumptionEnabled) => {
    const enabled = [giftEnabled, rechargeEnabled, consumptionEnabled]
    const result = fixture()
    result.activities.forEach((activity, index) => { activity.enabled = enabled[index] })
    status.mockResolvedValue(result)
    history.mockResolvedValue({ items: reward.items, total: 1, page: 1, page_size: 20 })
    const wrapper = mountPage(); await flushPromises()
    expect(wrapper.findAll('[data-activity]').map(item => item.attributes('data-activity'))).toEqual(result.activities.filter(item => item.enabled).map(item => item.type))
    const cumulativeCount = Number(rechargeEnabled) + Number(consumptionEnabled)
    for (const activity of result.activities.filter(item => item.enabled)) {
      expect(wrapper.get(`[data-activity="${activity.type}"]`).classes().includes('md:col-span-2')).toBe(activity.type === 'daily_gift' || cumulativeCount === 1)
    }
    expect(wrapper.text()).toContain('$0.25')
    if (enabled.every(item => !item)) expect(wrapper.text()).toContain('dailyActivities.noActiveActivities')
    expect(claim).not.toHaveBeenCalled()
    wrapper.unmount()
  })
  it('单项关闭后隐藏卡片，仍可用原请求号确认未完成的领取', async () => {
    const original = { activity_type: 'recharge', all: true, idempotency_key: '00000000-0000-4000-8000-000000000000' }
    sessionStorage.setItem(pendingClaimStorageKey(12, 'recharge'), JSON.stringify(original))
    const result = fixture()
    result.activities[1].enabled = false
    status.mockResolvedValue(result)
    const wrapper = mountPage(); await flushPromises()
    expect(wrapper.find('[data-activity="recharge"]').exists()).toBe(false)
    expect(wrapper.find('[data-claim="recharge"]').exists()).toBe(false)
    await wrapper.get('[data-confirm-pending="recharge"]').trigger('click'); await flushPromises()
    expect(claim).toHaveBeenCalledWith(original)
    expect(sessionStorage.getItem(pendingClaimStorageKey(12, 'recharge'))).toBeNull()
    expect(wrapper.find('[data-pending-hidden="recharge"]').exists()).toBe(false)
    wrapper.unmount()
  })
  it('关闭后允许确认原领取请求，不开启新的奖励请求', async () => {
    const original = { activity_type: 'recharge', all: true, idempotency_key: '00000000-0000-4000-8000-000000000000' }
    sessionStorage.setItem(pendingClaimStorageKey(12, 'recharge'), JSON.stringify(original))
    status.mockResolvedValue({ ...fixture(), enabled: false })
    const wrapper = mountPage(); await flushPromises()
    expect(wrapper.get('[data-claim="daily_gift"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-claim="recharge"]').attributes('disabled')).toBeUndefined()
    await wrapper.get('[data-claim="recharge"]').trigger('click'); await flushPromises()
    expect(claim).toHaveBeenCalledWith(original)
    expect(wrapper.get('[data-claim="recharge"]').attributes('disabled')).toBeDefined(); wrapper.unmount()
  })
  it('筛选重置页码并将类型交给接口，分页保持类型', async () => {
    const wrapper = mountPage(); await flushPromises()
    await wrapper.get('[data-page]').trigger('click'); await flushPromises()
    expect(history).toHaveBeenLastCalledWith(2, '', expect.any(AbortSignal))
    await wrapper.get('[data-filter]').trigger('click'); await flushPromises()
    expect(history).toHaveBeenLastCalledWith(1, 'recharge', expect.any(AbortSignal))
    await wrapper.get('[data-page]').trigger('click'); await flushPromises()
    expect(history).toHaveBeenLastCalledWith(2, 'recharge', expect.any(AbortSignal)); wrapper.unmount()
  })
  it('重复点击只提交一次，服务响应后刷新余额和列表', async () => {
    let resolve!: (value: typeof reward) => void
    claim.mockReturnValue(new Promise(r => { resolve = r }))
    const wrapper = mountPage(); await flushPromises()
    await wrapper.get('[data-claim="recharge"]').trigger('click')
    await wrapper.get('[data-claim="consumption"]').trigger('click')
    expect(claim).toHaveBeenCalledTimes(1)
    resolve(reward); await flushPromises()
    expect(refreshUser).toHaveBeenCalledTimes(1)
    expect(sessionStorage.getItem(pendingClaimStorageKey(12, 'recharge'))).toBeNull()
    expect(wrapper.get('[role="status"]').text()).toContain('$0.25'); wrapper.unmount()
  })
  it('批量超时后跨页面刷新重试复用原请求及all语义', async () => {
    claim.mockRejectedValueOnce({ status: 0, message: 'timeout' }).mockResolvedValueOnce({ ...reward, replayed: true })
    let wrapper = mountPage(); await flushPromises()
    await wrapper.get('[data-claim-all="recharge"]').trigger('click'); await flushPromises()
    const original = claim.mock.calls[0][0]
    expect(original.all).toBe(true)
    expect(wrapper.get('[data-pending="recharge"]').exists()).toBe(true)
    wrapper.unmount()
    wrapper = mountPage(); await flushPromises()
    expect(wrapper.get('[data-claim-all="recharge"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-claim="recharge"]').trigger('click'); await flushPromises()
    expect(claim.mock.calls[1][0]).toEqual(original); wrapper.unmount()
  })
  it('拒绝后仍保留请求，未确认请求不会泄露到另一个用户', async () => {
    sessionStorage.setItem(pendingClaimStorageKey(99, 'recharge'), JSON.stringify({ activity_type: 'recharge', all: true, idempotency_key: '00000000-0000-4000-8000-000000000000' }))
    claim.mockRejectedValueOnce({ status: 400, message: 'no opportunities' })
    const wrapper = mountPage(); await flushPromises()
    expect(wrapper.find('[data-pending="recharge"]').exists()).toBe(false)
    await wrapper.get('[data-claim="recharge"]').trigger('click'); await flushPromises()
    expect(sessionStorage.getItem(pendingClaimStorageKey(12, 'recharge'))).not.toBeNull()
    expect(sessionStorage.getItem(pendingClaimStorageKey(99, 'recharge'))).not.toBeNull(); wrapper.unmount()
  })
  it.each([401, 403])('首次超时后重试%s不能丢失原领取请求', async httpStatus => {
    claim.mockRejectedValueOnce({ status: 0 }).mockRejectedValueOnce({ status: httpStatus }).mockResolvedValueOnce({ ...reward, replayed: true })
    let wrapper = mountPage(); await flushPromises()
    await wrapper.get('[data-claim="recharge"]').trigger('click'); await flushPromises()
    const original = claim.mock.calls[0][0]
    await wrapper.get('[data-claim="recharge"]').trigger('click'); await flushPromises()
    expect(claim.mock.calls[1][0]).toEqual(original)
    wrapper.unmount()
    wrapper = mountPage(); await flushPromises()
    await wrapper.get('[data-claim="recharge"]').trigger('click'); await flushPromises()
    expect(claim.mock.calls[2][0]).toEqual(original)
    expect(sessionStorage.getItem(pendingClaimStorageKey(12, 'recharge'))).toBeNull(); wrapper.unmount()
  })
  it('不能保存请求号时禁止发送领奖请求，恢复存储后重试', async () => {
    const wrapper = mountPage(); await flushPromises()
    const save = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('denied') })
    await wrapper.get('[data-claim="recharge"]').trigger('click'); await flushPromises()
    expect(claim).not.toHaveBeenCalled()
    expect(wrapper.get('[role="alert"]').text()).toContain('dailyActivities.storageUnavailable')
    save.mockRestore()
    await wrapper.get('[data-claim="recharge"]').trigger('click'); await flushPromises()
    expect(claim).toHaveBeenCalledTimes(1); wrapper.unmount()
  })
  it('状态加载失败不会继续展示可领按钮', async () => {
    status.mockRejectedValueOnce({ status: 503, message: 'unavailable' })
    const wrapper = mountPage(); await flushPromises()
    expect(wrapper.find('[data-claim]').exists()).toBe(false)
    expect(wrapper.get('[role="alert"]').text()).toContain('unavailable'); wrapper.unmount()
  })
  it('服务端拒绝包含具体原因且渲染为文本，重试保留原请求', async () => {
    const message = '<img src=x onerror=alert(1)> 此活动已停用'
    claim.mockRejectedValueOnce({ status: 403, reason: 'DAILY_ACTIVITY_RULE_DISABLED', message })
    const wrapper = mountPage(); await flushPromises()
    await wrapper.get('[data-claim="recharge"]').trigger('click'); await flushPromises()
    const original = claim.mock.calls[0][0]
    expect(wrapper.get('[role="alert"]').text()).toBe(message)
    expect(wrapper.get('[role="alert"]').find('img').exists()).toBe(false)
    expect(JSON.parse(sessionStorage.getItem(pendingClaimStorageKey(12, 'recharge'))!)).toEqual(original)
    await wrapper.get('[data-claim="recharge"]').trigger('click'); await flushPromises()
    expect(claim.mock.calls[1][0]).toEqual(original)
    wrapper.unmount()
  })
  it('分页旧响应晚到不会覆盖新筛选结果', async () => {
    const wrapper = mountPage(); await flushPromises()
    let resolve!: (value: unknown) => void
    history.mockReturnValueOnce(new Promise(r => { resolve = r }))
    await wrapper.get('[data-page]').trigger('click')
    const oldSignal = history.mock.calls.at(-1)![2] as AbortSignal
    history.mockResolvedValueOnce({ items: reward.items, total: 1, page: 1, page_size: 20 })
    await wrapper.get('[data-filter]').trigger('click'); await flushPromises()
    expect(oldSignal.aborted).toBe(true)
    resolve({ items: [{ ...reward.items[0], amount: 9.99 }], total: 100, page: 2, page_size: 20 }); await flushPromises()
    expect(wrapper.text()).toContain('$0.25')
    expect(wrapper.text()).not.toContain('$9.99')
    expect(wrapper.get('[data-page]').text()).toBe('1 / 1')
    wrapper.unmount()
  })
  it('领取成功后刷新失败不能把原领取当成失败再次提交', async () => {
    const wrapper = mountPage(); await flushPromises()
    status.mockRejectedValueOnce({ status: 503, message: 'refresh unavailable' })
    history.mockRejectedValueOnce({ status: 503, message: 'history unavailable' })
    refreshUser.mockRejectedValueOnce({ status: 503 })
    await wrapper.get('[data-claim="recharge"]').trigger('click'); await flushPromises()
    expect(claim).toHaveBeenCalledTimes(1)
    expect(wrapper.get('[role="status"]').text()).toContain('$0.25')
    expect(sessionStorage.getItem(pendingClaimStorageKey(12, 'recharge'))).toBeNull()
    expect(wrapper.find('[data-claim="recharge"]').exists()).toBe(false)
    wrapper.unmount()
  })
  it.each([
    ['缺少明细', { amount: 0.25 }],
    ['空响应', null],
    ['数组响应', []],
    ['空明细', { ...reward, items: [] }],
    ['错误明细类型', { ...reward, items: {} }],
    ['空明细项', { ...reward, items: [null] }],
    ['总金额为字符串', { ...reward, amount: '0.25' }],
    ['总金额非有限数', { ...reward, amount: Number.NaN }],
    ['总金额不匹配', { ...reward, amount: 0.5 }],
    ['缺少余额', { ...reward, balance: undefined }],
    ['余额非有限数', { ...reward, balance: Number.POSITIVE_INFINITY }],
    ['缺少剩余次数', { ...reward, remaining_count: undefined }],
    ['剩余次数为负', { ...reward, remaining_count: -1 }],
    ['剩余次数非整数', { ...reward, remaining_count: 0.5 }],
    ['缺少重放标记', { ...reward, replayed: undefined }],
    ['重放标记类型错误', { ...reward, replayed: 'false' }],
    ['明细缺少标识', { ...reward, items: [{ ...reward.items[0], id: undefined }] }],
    ['明细类型不匹配', { ...reward, items: [{ ...reward.items[0], activity_type: 'consumption' }] }],
    ['明细金额非有限数', { ...reward, items: [{ ...reward.items[0], amount: Number.POSITIVE_INFINITY }] }],
    ['明细金额为负', { ...reward, items: [{ ...reward.items[0], amount: -0.25 }] }],
    ['明细时间无效', { ...reward, items: [{ ...reward.items[0], created_at: 'invalid' }] }],
    ['单次返回多条', { ...reward, amount: 0.5, items: [reward.items[0], { ...reward.items[0], id: 2 }] }],
  ])('损坏的领取成功响应不能清除原请求号或重复新领：%s', async (_case, malformed) => {
    claim.mockResolvedValueOnce(malformed).mockResolvedValueOnce({ ...reward, replayed: true })
    let wrapper = mountPage(); await flushPromises()
    await wrapper.get('[data-claim="recharge"]').trigger('click'); await flushPromises()
    const original = claim.mock.calls[0][0]
    expect(JSON.parse(sessionStorage.getItem(pendingClaimStorageKey(12, 'recharge'))!)).toEqual(original)
    expect(wrapper.find('[role="status"]').exists()).toBe(false)
    expect(wrapper.get('[role="alert"]').text()).toContain('dailyActivities.claimFailed')
    expect(refreshUser).not.toHaveBeenCalled()
    // 页面重建后仍从会话存储恢复同一个请求，不能用新请求重复消耗领取资格。
    wrapper.unmount()
    wrapper = mountPage(); await flushPromises()
    await wrapper.get('[data-claim="recharge"]').trigger('click'); await flushPromises()
    expect(claim.mock.calls[1][0]).toEqual(original)
    expect(sessionStorage.getItem(pendingClaimStorageKey(12, 'recharge'))).toBeNull()
    expect(wrapper.get('[role="status"]').text()).toContain('$0.25')
    wrapper.unmount()
  })
  it('批量明细重复时保留原批量请求，完整重放可确认小数金额和既有负余额', async () => {
    const duplicate = { ...reward, amount: 0.5, items: [reward.items[0], reward.items[0]] }
    const complete = {
      ...reward, amount: 0.3, balance: -5.3, replayed: true,
      items: [{ ...reward.items[0], amount: 0.1 }, { ...reward.items[0], id: 2, amount: 0.2 }],
    }
    claim.mockResolvedValueOnce(duplicate).mockResolvedValueOnce(complete)
    const wrapper = mountPage(); await flushPromises()
    await wrapper.get('[data-claim-all="recharge"]').trigger('click'); await flushPromises()
    const original = claim.mock.calls[0][0]
    expect(original.all).toBe(true)
    expect(JSON.parse(sessionStorage.getItem(pendingClaimStorageKey(12, 'recharge'))!)).toEqual(original)
    await wrapper.get('[data-claim="recharge"]').trigger('click'); await flushPromises()
    expect(claim.mock.calls[1][0]).toEqual(original)
    expect(sessionStorage.getItem(pendingClaimStorageKey(12, 'recharge'))).toBeNull()
    expect(wrapper.get('[role="status"]').text()).toContain('$0.30')
    expect(wrapper.get('[role="status"]').text()).toContain('"count":2')
    wrapper.unmount()
  })
})
