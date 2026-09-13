import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import type { AdminUser, ApiKey } from '@/types'

const apiMocks = vi.hoisted(() => ({
  getUserApiKeys: vi.fn(),
  getAllGroups: vi.fn(),
  updateApiKeyGroup: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    users: {
      getUserApiKeys: apiMocks.getUserApiKeys
    },
    groups: {
      getAll: apiMocks.getAllGroups
    },
    apiKeys: {
      updateApiKeyGroup: apiMocks.updateApiKeyGroup
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn()
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

import UserApiKeysModal from '../UserApiKeysModal.vue'

// 构造弹窗测试所需的最小 API Key 数据，避免各用例重复无关字段。
const createApiKey = (overrides: Partial<ApiKey> = {}): ApiKey => ({
  id: 1,
  user_id: 99,
  key: 'sk-test-key',
  name: '测试 Key',
  group_id: null,
  status: 'active',
  fast_mode_policy: 'follow_request',
  ip_whitelist: [],
  ip_blacklist: [],
  last_used_at: null,
  last_used_ip: null,
  quota: 0,
  quota_used: 0,
  expires_at: null,
  created_at: '2026-08-05T00:00:00Z',
  updated_at: '2026-08-05T00:00:00Z',
  current_concurrency: 0,
  rate_limit_5h: 0,
  rate_limit_1d: 0,
  rate_limit_7d: 0,
  usage_5h: 0,
  usage_1d: 0,
  usage_7d: 0,
  window_5h_start: null,
  window_1d_start: null,
  window_7d_start: null,
  reset_5h_at: null,
  reset_1d_at: null,
  reset_7d_at: null,
  ...overrides
})

const user = {
  id: 99,
  email: 'admin@example.com',
  username: 'admin'
} as AdminUser

// 先以关闭状态挂载再打开，确保覆盖组件监听弹窗开启的加载流程。
const mountAndOpen = async () => {
  const wrapper = mount(UserApiKeysModal, {
    props: {
      show: false,
      user
    },
    global: {
      stubs: {
        BaseDialog: {
          props: ['show'],
          template: '<div v-if="show"><slot /></div>'
        },
        GroupBadge: {
          props: ['name'],
          template: '<span data-testid="group-badge">{{ name }}</span>'
        },
        GroupOptionItem: {
          props: ['name'],
          template: '<span>{{ name }}</span>'
        },
        Teleport: true
      }
    }
  })

  await wrapper.setProps({ show: true })
  await flushPromises()
  return wrapper
}

beforeEach(() => {
  vi.clearAllMocks()
  apiMocks.getAllGroups.mockResolvedValue([])
  apiMocks.updateApiKeyGroup.mockResolvedValue({
    api_key: createApiKey(),
    auto_granted_group_access: false
  })
})

describe('UserApiKeysModal', () => {
  it('展示复合 Key 的全部前缀分组映射且不提供普通换组入口', async () => {
    apiMocks.getUserApiKeys.mockResolvedValue({
      items: [
        createApiKey({
          is_composite: true,
          composite_groups: [
            { group_id: 10, prefix: 'GPT', group: { id: 10, name: 'OpenAI 主组', platform: 'openai' } as any },
            { group_id: 20, prefix: 'Claude', group: { id: 20, name: 'Anthropic 主组', platform: 'anthropic' } as any }
          ]
        })
      ],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1
    })

    const wrapper = await mountAndOpen()
    const mappings = wrapper.get('[data-testid="composite-group-mappings"]')

    expect(mappings.text()).toContain('GPT')
    expect(mappings.text()).toContain('OpenAI 主组')
    expect(mappings.text()).toContain('Claude')
    expect(mappings.text()).toContain('Anthropic 主组')
    expect(mappings.text()).not.toContain('admin.users.none')
    expect(wrapper.find('[data-testid="api-key-group-selector"]').exists()).toBe(false)
    expect(apiMocks.updateApiKeyGroup).not.toHaveBeenCalled()
  })

  it('普通 Key 继续显示并使用原有换组入口', async () => {
    const currentGroup = { id: 10, name: 'OpenAI 主组', platform: 'openai' } as any
    apiMocks.getUserApiKeys.mockResolvedValue({
      items: [createApiKey({ group_id: 10, group: currentGroup })],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1
    })
    apiMocks.getAllGroups.mockResolvedValue([currentGroup])

    const wrapper = await mountAndOpen()

    expect(wrapper.get('[data-testid="group-badge"]').text()).toBe('OpenAI 主组')
    expect(wrapper.get('[data-testid="api-key-group-selector"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="composite-group-mappings"]').exists()).toBe(false)
  })
})
