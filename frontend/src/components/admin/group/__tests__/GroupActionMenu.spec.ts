import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import GroupActionMenu from '../GroupActionMenu.vue'
import type { AdminGroup } from '@/types'

vi.mock('vue-i18n', async () => ({
  ...await vi.importActual('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key }),
}))

// 菜单只透传分组对象，业务字段由页面和对应弹窗负责。
const group = { id: 42, name: 'Primary' } as AdminGroup
const mountMenu = (duplicating = false) => mount(GroupActionMenu, {
  props: { show: true, group, position: { top: 80, left: 8 }, duplicating },
  global: { stubs: { Teleport: true } },
})

describe('GroupActionMenu', () => {
  it.each([
    ['admin.groups.duplicate', 'duplicate'],
    ['admin.groups.rateMultipliers', 'rate-multipliers'],
    ['admin.groups.rpmOverrides', 'rpm-overrides'],
    ['common.delete', 'delete'],
  ])('%s 传递所选分组并关闭菜单', async (label, event) => {
    const wrapper = mountMenu()
    expect(wrapper.findAll('[role="menuitem"]')).toHaveLength(4)
    const button = wrapper.findAll('button').find(item => item.text() === label)!
    await button.trigger('click')
    expect(wrapper.emitted(event)?.[0]).toEqual([group])
    expect(wrapper.emitted('close')).toHaveLength(1)
    wrapper.unmount()
  })

  it('复制进行中禁用入口，其他菜单项仍然可用', async () => {
    const wrapper = mountMenu(true)
    const button = wrapper.get('[data-testid="group-duplicate"]')
    expect(button.attributes('disabled')).toBeDefined()
    await button.trigger('click')
    expect(wrapper.emitted('duplicate')).toBeUndefined()
    expect(wrapper.findAll('button:enabled')).toHaveLength(3)
    wrapper.unmount()
  })

  it('按 Escape 或点击遮罩关闭', async () => {
    const wrapper = mountMenu()
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await wrapper.get('[aria-hidden="true"]').trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(2)
    wrapper.unmount()
  })
})
