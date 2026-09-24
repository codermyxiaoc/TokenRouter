import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import ImageStorageSettings from '../ImageStorageSettings.vue'

const { get, update, testConnection, stepUpRun, showError, showSuccess } = vi.hoisted(() => ({
  get: vi.fn(), update: vi.fn(), testConnection: vi.fn(), stepUpRun: vi.fn(), showError: vi.fn(), showSuccess: vi.fn(),
}))
vi.mock('@/api', () => ({ adminAPI: { backup: { getImageStorageConfig: get, updateImageStorageConfig: update, testImageStorageConnection: testConnection } } }))
vi.mock('@/stores', () => ({ useAppStore: () => ({ showError, showSuccess }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/composables/useStepUp', () => ({
  useStepUp: () => ({ run: stepUpRun }), isStepUpCancelled: () => false, isStepUpBlocked: () => false, stepUpBlockReason: () => '',
}))
const config = { enabled: true, reuse_backup_s3: false, bucket: 'images', prefix: 'images/', public_base_url: '', presign_expiry_hours: 24,
  max_download_bytes: 33554432, endpoint: 'https://s3.example.com', region: 'auto', access_key_id: 'access', secret_access_key: '********', force_path_style: false }
function mountSettings() { return mount(ImageStorageSettings, { global: { stubs: { TotpStepUpDialog: true } } }) }

// 保证只读密钥掩码不会被重新写入，加载失败也不能用默认表单覆盖配置。
describe('ImageStorageSettings', () => {
  beforeEach(() => {
    get.mockReset(); get.mockResolvedValue({ config, secret_configured: true })
    update.mockReset(); update.mockResolvedValue(config)
    testConnection.mockReset(); testConnection.mockResolvedValue({ ok: true, message: '' })
    stepUpRun.mockReset(); stepUpRun.mockImplementation((action: () => unknown) => action())
    showError.mockReset(); showSuccess.mockReset()
  })

  it('显示密钥已配置但不回填，保存空密钥时省略字段并经二次验证', async () => {
    const wrapper = mountSettings()
    await flushPromises()
    expect((wrapper.get('#image-storage-secret').element as HTMLInputElement).value).toBe('')
    expect(wrapper.get('#image-storage-secret').attributes('placeholder')).toBe('admin.backup.s3.secretConfigured')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(stepUpRun).toHaveBeenCalledOnce()
    expect(update).toHaveBeenCalledWith(expect.objectContaining({ bucket: 'images', enabled: true }))
    expect(update.mock.calls[0][0]).not.toHaveProperty('secret_access_key')
    expect(showSuccess).toHaveBeenCalledWith('imageStorage.saved')
    wrapper.unmount()
  })

  it('测试使用当前独立密钥且不保存配置', async () => {
    const wrapper = mountSettings()
    await flushPromises()
    await wrapper.get('#image-storage-secret').setValue('new-secret')
    await wrapper.get('[data-testid="image-storage-test"]').trigger('click')
    await flushPromises()
    expect(stepUpRun).toHaveBeenCalledOnce()
    expect(testConnection).toHaveBeenCalledWith(expect.objectContaining({ secret_access_key: 'new-secret' }))
    expect(update).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('读取配置失败后阻止保存默认值', async () => {
    get.mockRejectedValueOnce(new Error('unavailable'))
    const wrapper = mountSettings()
    await flushPromises()
    expect(wrapper.get('fieldset').attributes()).toHaveProperty('disabled')
    expect(wrapper.get('[role="alert"]').text()).toContain('unavailable')
    await wrapper.get('form').trigger('submit')
    expect(update).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('从复用备份切换独立配置时不把备份密钥误认为独立密钥', async () => {
    get.mockResolvedValueOnce({ config: { ...config, reuse_backup_s3: true, access_key_id: '', secret_access_key: '' }, secret_configured: true })
    const wrapper = mountSettings()
    await flushPromises()
    await wrapper.get('[data-testid="image-storage-reuse"]').setValue(false)
    expect(wrapper.get('#image-storage-secret').attributes('placeholder')).toBe('')
    expect(wrapper.get('#image-storage-secret').attributes()).toHaveProperty('required')
    await wrapper.get('[data-testid="image-storage-enabled"]').setValue(false)
    expect(wrapper.get('#image-storage-secret').attributes()).not.toHaveProperty('required')
    wrapper.unmount()
  })
})
