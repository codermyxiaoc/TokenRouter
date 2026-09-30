import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import MediaTaskUserFilter from '../MediaTaskUserFilter.vue'

const { searchUsers } = vi.hoisted(() => ({ searchUsers: vi.fn() }))
vi.mock('@/api/admin/usage', () => ({ adminUsageAPI: { searchUsers } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const alice = { id: 10, username: '小明', email: 'alice@example.com', deleted: false }

describe('MediaTaskUserFilter', () => {
  beforeEach(() => { vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] }); searchUsers.mockReset() })
  afterEach(() => vi.useRealTimers())

  it('按用户名搜索并用键盘选择，展示邮箱和 ID 以区分同名用户', async () => {
    searchUsers.mockResolvedValue([alice, { ...alice, id: 11, username: '小明二号' }])
    const wrapper = mount(MediaTaskUserFilter, { props: { id: 'user', modelValue: null } })
    await wrapper.get('input').setValue('小明')
    await vi.advanceTimersByTimeAsync(300)
    await flushPromises()
    expect(searchUsers).toHaveBeenCalledWith('小明')
    expect(wrapper.get('[role="option"]').text()).toContain('小明')
    expect(wrapper.get('[role="option"]').text()).toContain('alice@example.com')
    expect(wrapper.get('[role="option"]').text()).toContain('#10')
    await wrapper.get('input').trigger('keydown', { key: 'ArrowUp' })
    expect(wrapper.get('input').attributes('aria-activedescendant')).toBe('user-option-1')
    await wrapper.get('input').trigger('keydown', { key: 'ArrowDown' })
    await wrapper.get('input').trigger('keydown', { key: 'Enter' })
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([alice])
    expect(wrapper.get('input').attributes('aria-activedescendant')).toBeUndefined()
    wrapper.unmount()
  })

  it('编辑名称取消旧选择，晚到的旧搜索响应不会污染新候选', async () => {
    let oldResolve!: (users: typeof alice[]) => void
    searchUsers.mockReturnValueOnce(new Promise(resolve => { oldResolve = resolve }))
    const wrapper = mount(MediaTaskUserFilter, { props: { id: 'user', modelValue: alice } })
    await wrapper.get('input').setValue('旧')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([null])
    await wrapper.setProps({ modelValue: null })
    expect((wrapper.get('input').element as HTMLInputElement).value).toBe('旧')
    await vi.advanceTimersByTimeAsync(300)
    const bob = { ...alice, id: 11, username: '新用户' }
    searchUsers.mockResolvedValueOnce([bob])
    await wrapper.get('input').setValue('新')
    await vi.advanceTimersByTimeAsync(300)
    await flushPromises()
    oldResolve([alice])
    await flushPromises()
    expect(wrapper.findAll('[role="option"]')).toHaveLength(1)
    expect(wrapper.get('[role="option"]').text()).toContain('新用户')
    wrapper.unmount()
  })

  it('卸载清理防抖，搜索失败能显示提示', async () => {
    searchUsers.mockRejectedValue(new Error('offline'))
    const wrapper = mount(MediaTaskUserFilter, { props: { id: 'user', modelValue: null } })
    await wrapper.get('input').setValue('alice')
    await vi.advanceTimersByTimeAsync(300)
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe('mediaTasks.userSearchFailed')
    await wrapper.get('input').setValue('bob')
    wrapper.unmount()
    await vi.advanceTimersByTimeAsync(300)
    expect(searchUsers).toHaveBeenCalledTimes(1)
  })
})
