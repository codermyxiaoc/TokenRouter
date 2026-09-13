import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

import type { AdminUser } from '@/types'

const { updateUser, updateUserAttributes, showError, showSuccess, runStepUp } = vi.hoisted(() => ({
  updateUser: vi.fn(),
  updateUserAttributes: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
  runStepUp: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    users: { update: updateUser },
    userAttributes: { updateUserAttributeValues: updateUserAttributes }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showSuccess })
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({ copyToClipboard: vi.fn() })
}))

vi.mock('@/composables/useStepUp', () => ({
  useStepUp: () => ({ run: runStepUp }),
  isStepUpBlocked: () => false,
  isStepUpCancelled: () => false,
  stepUpBlockReason: () => ''
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

import UserEditModal from '../UserEditModal.vue'

const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: { show: Boolean },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

const user: AdminUser = {
  id: 9,
  email: 'edit-user@example.com',
  username: 'editor',
  notes: '',
  role: 'user',
  balance: 0,
  concurrency: 3,
  rpm_limit: 0,
  api_key_limit: 7,
  status: 'active',
  allowed_groups: [],
  disabled_public_groups: [],
  balance_notify_enabled: true,
  balance_notify_threshold: null,
  balance_notify_extra_emails: [],
  created_at: '2026-07-23T00:00:00Z',
  updated_at: '2026-07-23T00:00:00Z'
}

function mountModal(concurrency = user.concurrency) {
  return mount(UserEditModal, {
    props: { show: true, user: { ...user, concurrency } },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        Select: true,
        UserAttributeForm: true,
        Icon: true,
        TotpStepUpDialog: true
      }
    }
  })
}

describe('UserEditModal API Key 上限与并发', () => {
  beforeEach(() => {
    updateUser.mockReset()
    updateUserAttributes.mockReset()
    showError.mockReset()
    showSuccess.mockReset()
    runStepUp.mockReset()
    runStepUp.mockImplementation(async (operation: () => Promise<unknown>) => operation())
    updateUser.mockResolvedValue({})
  })

  it('显示实际值并允许保存显式零值', async () => {
    const wrapper = mountModal()
    const input = wrapper.get('[data-test="api-key-limit-input"]')
    expect((input.element as HTMLInputElement).value).toBe('7')

    await input.setValue('0')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(updateUser).toHaveBeenCalledWith(9, expect.objectContaining({ api_key_limit: 0 }))
  })

  it('拒绝负数 API Key 上限', async () => {
    const wrapper = mountModal()
    await wrapper.get('[data-test="api-key-limit-input"]').setValue('-1')

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(updateUser).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalledWith('admin.users.form.apiKeyLimitInvalid')
  })

  it('拒绝超过数据库范围的 API Key 上限', async () => {
    const wrapper = mountModal()
    await wrapper.get('[data-test="api-key-limit-input"]').setValue('2147483648')

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(updateUser).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalledWith('admin.users.form.apiKeyLimitInvalid')
  })

  it('允许用零保存不限制并发，而不会阻止其他字段保存', async () => {
    const wrapper = mountModal(0)

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(showError).not.toHaveBeenCalled()
    expect(updateUser).toHaveBeenCalledWith(9, expect.objectContaining({ concurrency: 0 }))
    expect(wrapper.emitted('success')).toBeTruthy()
  })

  it('仍然拒绝负数并发', async () => {
    const wrapper = mountModal()

    await wrapper.get('[data-test="concurrency-input"]').setValue('-1')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('admin.users.concurrencyNonNegative')
    expect(updateUser).not.toHaveBeenCalled()
  })
})
