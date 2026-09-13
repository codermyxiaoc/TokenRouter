import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import ScopeDropdown from '../ScopeDropdown.vue'

const mocks = vi.hoisted(() => ({ replace: vi.fn() }))

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-router', () => ({
  useRoute: () => ({ query: { retained: 'yes', scope: 'personal' } }),
  useRouter: () => ({ replace: mocks.replace }),
}))

describe('ScopeDropdown', () => {
  it('以下拉菜单切换作用域并保留其他 URL 参数', async () => {
    mocks.replace.mockResolvedValue(undefined)
    const wrapper = mount(ScopeDropdown, { props: { modelValue: 'personal' } })

    expect(wrapper.get('[data-test="scope-dropdown-trigger"]').text()).toContain('team.personalKeys')
    await wrapper.get('[data-test="scope-dropdown-trigger"]').trigger('click')
    await wrapper.get('[data-test="scope-option-team"]').trigger('click')

    expect(wrapper.emitted('update:modelValue')).toEqual([['team']])
    expect(mocks.replace).toHaveBeenCalledWith({ query: { retained: 'yes', scope: 'team' } })
    expect(wrapper.emitted('change')).toEqual([['team']])
  })
})
