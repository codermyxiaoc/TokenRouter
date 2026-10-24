import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import AccountGroupsCell from '../AccountGroupsCell.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

// 摘要字段应足以显示分组徽章与展开列表，不要求完整分组配置。
describe('AccountGroupsCell compact group summaries', () => {
  it('renders badges and remaining groups from lite account summaries', async () => {
    const wrapper = mount(AccountGroupsCell, {
      props: {
        groups: [
          { id: 1, name: 'Vertex', platform: 'gemini', display_brand: 'vertex', rate_multiplier: 1.2 },
          { id: 2, name: 'Claude', platform: 'anthropic', display_brand: '', rate_multiplier: 2 },
          { id: 3, name: 'GPT', platform: 'openai', display_brand: '', rate_multiplier: 1 }
        ],
        maxDisplay: 2
      },
      global: {
        stubs: {
          Teleport: true,
          GroupBadge: {
            props: ['name', 'platform', 'displayBrand', 'rateMultiplier'],
            template: '<span data-test="badge">{{ name }}:{{ platform }}:{{ displayBrand }}:{{ rateMultiplier }}</span>'
          }
        }
      }
    })
    expect(wrapper.findAll('[data-test="badge"]')).toHaveLength(1)
    expect(wrapper.get('[data-test="badge"]').text()).toBe('Vertex:gemini:vertex:1.2')
    await wrapper.get('button').trigger('click')
    expect(wrapper.text()).toContain('Claude:anthropic::2')
    expect(wrapper.text()).toContain('GPT:openai::1')
    wrapper.unmount()
  })
})
