import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import type { AdminUser } from '@/types'
import UserAllowedGroupsModal from '../UserAllowedGroupsModal.vue'

const api = vi.hoisted(() => ({ list: vi.fn(), update: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { groups: { list: api.list }, users: { update: api.update } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: vi.fn() }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const user = {
  id: 1, email: 'first@example.com', allowed_groups: [10], disabled_public_groups: [20], group_rates: { 10: 0.5 }
} as unknown as AdminUser
const groups = [
  { id: 10, name: '专属', platform: 'openai', status: 'active', is_exclusive: true, rate_multiplier: 1 },
  { id: 20, name: '公开', platform: 'openai', status: 'active', is_exclusive: false, rate_multiplier: 1 }
]

const openModal = async () => {
  const wrapper = mount(UserAllowedGroupsModal, {
    props: { show: false, user },
    global: { stubs: {
      BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
      UserAvatar: true, PlatformIcon: true
    } }
  })
  await wrapper.setProps({ show: true })
  return wrapper
}

beforeEach(() => {
  vi.clearAllMocks()
  api.update.mockResolvedValue({})
})

describe('用户分组权限加载保护', () => {
  it('加载失败或尚未返回时不能提交空权限', async () => {
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    let reject!: (error: Error) => void
    api.list.mockReturnValueOnce(new Promise((_, rejectRequest) => { reject = rejectRequest }))
    const wrapper = await openModal()
    const save = wrapper.get('button.btn-primary')
    expect(save.attributes('disabled')).toBeDefined()
    await save.trigger('click')
    reject(new Error('temporary unavailable'))
    await flushPromises()
    expect(save.attributes('disabled')).toBeDefined()
    await save.trigger('click')
    expect(api.update).not.toHaveBeenCalled()
    wrapper.unmount()
    errorSpy.mockRestore()
  })

  it('切换用户后丢弃旧加载结果，保留当前用户的公开禁用与专属授权规则', async () => {
    let resolveOld!: (value: unknown) => void
    api.list.mockReturnValueOnce(new Promise(resolve => { resolveOld = resolve }))
    const wrapper = await openModal()
    api.list.mockResolvedValueOnce({ items: groups })
    await wrapper.setProps({ user: { ...user, id: 2, allowed_groups: [], disabled_public_groups: [], group_rates: {} } })
    await flushPromises()
    resolveOld({ items: [] })
    await flushPromises()
    await wrapper.get('button.btn-primary').trigger('click')
    expect(api.update).toHaveBeenCalledWith(2, {
      allowed_groups: [], disabled_public_groups: [], group_rates: undefined
    })
    wrapper.unmount()
  })

  it('正常保存继续保留公开禁用、专属授权和自定义倍率', async () => {
    api.list.mockResolvedValueOnce({ items: groups })
    const wrapper = await openModal()
    await flushPromises()
    await wrapper.get('button.btn-primary').trigger('click')
    expect(api.update).toHaveBeenCalledWith(1, {
      allowed_groups: [10], disabled_public_groups: [20], group_rates: { 10: 0.5 }
    })
    wrapper.unmount()
  })
})
