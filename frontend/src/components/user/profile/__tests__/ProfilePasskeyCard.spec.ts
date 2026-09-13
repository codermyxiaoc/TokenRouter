import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ProfilePasskeyCard from '@/components/user/profile/ProfilePasskeyCard.vue'

const mocks = vi.hoisted(() => ({
  isSupported: vi.fn(),
  list: vi.fn(),
  register: vi.fn(),
  rename: vi.fn(),
  remove: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn()
}))

vi.mock('@/api', () => ({
  passkeyAPI: {
    isSupported: mocks.isSupported,
    list: mocks.list,
    register: mocks.register,
    rename: mocks.rename,
    remove: mocks.remove
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showSuccess: mocks.showSuccess,
    showError: mocks.showError
  })
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key
  })
}))

// 锁定禁用态与公开设置变更竞态下的静默行为，真实错误仍需提示用户。
describe('ProfilePasskeyCard', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.isSupported.mockReturnValue(true)
    mocks.list.mockResolvedValue([])
  })

  it('does not request credentials while passkeys are disabled', async () => {
    const wrapper = mount(ProfilePasskeyCard, {
      props: { enabled: false },
      global: { stubs: { Icon: true } }
    })

    await flushPromises()

    expect(mocks.list).not.toHaveBeenCalled()
    expect(mocks.showError).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('profile.passkey.featureDisabled')
  })

  it('silences PASSKEY_DISABLED returned during a settings race', async () => {
    mocks.list.mockRejectedValue({ code: 403, reason: 'PASSKEY_DISABLED' })

    mount(ProfilePasskeyCard, {
      props: { enabled: true },
      global: { stubs: { Icon: true } }
    })
    await flushPromises()

    expect(mocks.list).toHaveBeenCalledTimes(1)
    expect(mocks.showError).not.toHaveBeenCalled()
  })

  it('shows a toast for real credential loading failures', async () => {
    mocks.list.mockRejectedValue({ code: 500, reason: 'INTERNAL_ERROR' })

    mount(ProfilePasskeyCard, {
      props: { enabled: true },
      global: { stubs: { Icon: true } }
    })
    await flushPromises()

    expect(mocks.showError).toHaveBeenCalledWith('profile.passkey.loadFailed')
  })
})
