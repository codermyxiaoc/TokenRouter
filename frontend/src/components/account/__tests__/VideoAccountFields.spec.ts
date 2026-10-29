import { describe, expect, it, vi, beforeEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import VideoAccountFields from '../VideoAccountFields.vue'
import { createVideoAccountForm, videoAccountCredentials, type VideoAccountForm } from '../videoAccountConfig'

const { listProfiles } = vi.hoisted(() => ({ listProfiles: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { videoProtocolProfiles: { list: listProfiles } } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => params ? `${key}:${JSON.stringify(params)}` : key }) }))

async function mountFields(modelValue: VideoAccountForm) {
  const wrapper = mount(VideoAccountFields, { props: { modelValue }, global: { stubs: { Select: true } } })
  await flushPromises()
  const accept = async () => wrapper.setProps({ modelValue: wrapper.emitted('update:modelValue')!.at(-1)![0] as VideoAccountForm })
  const select = (id: string) => wrapper.findAllComponents({ name: 'Select' }).find(item => item.attributes('data-testid') === id)!
  return { wrapper, accept, select }
}

describe('VideoAccountFields 模型协议绑定表', () => {
  beforeEach(() => {
    listProfiles.mockReset()
    listProfiles.mockResolvedValue([
      { profile_key: 'vendor_x', version: 2, status: 'active' },
      { profile_key: 'vendor_x', version: 1, status: 'disabled' },
      { profile_key: 'draft_only', version: 1, status: 'draft' },
    ])
  })

  it('offers builtin protocols plus active templates and uses the project Select', async () => {
    const { wrapper, select } = await mountFields(createVideoAccountForm())
    const values = select('video-binding-protocol-0').props('options').map((item: { value: string }) => item.value)
    // 内置协议只保留官方格式，厂商格式通过模板（含预置模板）提供。
    expect(values).toContain('kling.kling_v1')
    expect(values).not.toContain('openai_videos.yezonghui_v1')
    expect(values).not.toContain('kling.kemei_v1')
    expect(values).toContain('profile:vendor_x')
    expect(values).not.toContain('profile:draft_only')
    expect(values).not.toContain('compat.yezonghui_v1')
    const label = select('video-binding-protocol-0').props('options').find((item: { value: string }) => item.value === 'compat.openai_v1').label
    expect(label).toBe('admin.accounts.video.endpoints.compat · admin.accounts.video.adapters.compat_openai')
    expect(wrapper.find('select').exists()).toBe(false)
    wrapper.unmount()
  })

  it('shows a path select only for Kling/Vidu, filtered by adapter, and resets paths on protocol change', async () => {
    const { wrapper, accept, select } = await mountFields(createVideoAccountForm())
    expect(wrapper.find('[data-testid="video-binding-path-0"]').exists()).toBe(false)
    select('video-binding-protocol-0').vm.$emit('update:modelValue', 'kling.kling_v1')
    await accept()
    expect(select('video-binding-path-0').props('options').map((item: { value: string }) => item.value)).toEqual(['/text-to-video/{model}', '/image-to-video/{model}'])
    expect(wrapper.find('[data-testid="video-binding-error-0"]').text()).toBe('admin.accounts.video.protocolPathRequired')
    select('video-binding-path-0').vm.$emit('update:modelValue', '/image-to-video/{model}')
    await accept()
    expect(wrapper.find('[data-testid="video-binding-error-0"]').exists()).toBe(false)
    // 切到 Vidu 时路径必须重新选择。
    select('video-binding-protocol-0').vm.$emit('update:modelValue', 'vidu.vidu_v2')
    await accept()
    expect(wrapper.props('modelValue').rows[0]!.path).toBe('')
    expect(select('video-binding-path-0').props('options')).toHaveLength(4)
    select('video-binding-protocol-0').vm.$emit('update:modelValue', 'profile:vendor_x')
    await accept()
    expect(wrapper.find('[data-testid="video-binding-path-0"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('admin.accounts.video.profileHint')
    wrapper.unmount()
  })

  it('adds and removes rows, edits model/base URL and budgets, then serializes only new fields', async () => {
    const { wrapper, accept, select } = await mountFields(createVideoAccountForm())
    select('video-binding-protocol-0').vm.$emit('update:modelValue', 'openai_videos.openai_v1')
    await accept()
    expect(wrapper.get('[data-testid="video-remove-binding-0"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="video-add-binding"]').trigger('click')
    await accept()
    // 已有 * 默认行时新增行留空，等待填写精确模型名。
    expect(wrapper.props('modelValue').rows[1]!.model).toBe('')
    await wrapper.get('[data-testid="video-binding-model-1"]').setValue('seedance-1')
    await accept()
    select('video-binding-protocol-1').vm.$emit('update:modelValue', 'seedance.seedance_v1')
    await accept()
    await wrapper.get('[data-testid="video-binding-base-url-1"]').setValue('https://ark.example/api/v3')
    await accept()
    await wrapper.get('[data-testid="video-max-pending"]').setValue('4')
    await accept()
    await wrapper.get('[data-testid="video-max-duration"]').setValue('12')
    await accept()
    expect(videoAccountCredentials(wrapper.props('modelValue'))).toEqual({
      video_model_protocols: {
        '*': { protocol: 'openai_videos.openai_v1' },
        'seedance-1': { protocol: 'seedance.seedance_v1', base_url: 'https://ark.example/api/v3' },
      },
      video_max_pending_tasks: 4, video_max_duration_seconds: 12,
    })
    await wrapper.get('[data-testid="video-remove-binding-0"]').trigger('click')
    await accept()
    expect(wrapper.props('modelValue').rows.map(row => row.model)).toEqual(['seedance-1'])
    // 删除 * 后新增行重新预填 *。
    await wrapper.get('[data-testid="video-add-binding"]').trigger('click')
    await accept()
    expect(wrapper.props('modelValue').rows[1]!.model).toBe('*')
    wrapper.unmount()
  })

  it('keeps a saved but inactive template selectable and tolerates template load failure', async () => {
    listProfiles.mockRejectedValue(new Error('offline'))
    const { wrapper, select } = await mountFields(createVideoAccountForm({ video_model_protocols: { m: { protocol: 'profile:old_vendor@3' } } }))
    expect(wrapper.find('[data-testid="video-profiles-load-failed"]').exists()).toBe(true)
    const option = select('video-binding-protocol-0').props('options').find((item: { value: string }) => item.value === 'profile:old_vendor@3')
    expect(option.label).toBe('admin.accounts.video.profileOption:{"key":"old_vendor@3"}')
    wrapper.unmount()
  })
})
