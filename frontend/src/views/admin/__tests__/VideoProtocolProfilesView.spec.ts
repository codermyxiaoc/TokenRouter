import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import VideoProtocolProfilesView from '../VideoProtocolProfilesView.vue'
import { VIDEO_PROFILE_STARTER_SPEC, groupVideoProtocolProfiles, parseJsonObject, parseOptionalJson, videoProfileSampleRequest } from '../videoProtocolProfiles'

const { listProfiles, saveProfile, setStatus, previewProfile, generateWithAI, removeVersion, removeProfile, showError, showSuccess } = vi.hoisted(() => ({
  listProfiles: vi.fn(), saveProfile: vi.fn(), setStatus: vi.fn(), previewProfile: vi.fn(), generateWithAI: vi.fn(), removeVersion: vi.fn(), removeProfile: vi.fn(), showError: vi.fn(), showSuccess: vi.fn(),
}))
vi.mock('@/api/admin', () => ({
  adminAPI: { videoProtocolProfiles: { list: listProfiles, save: saveProfile, setStatus, preview: previewProfile, generateWithAI, removeVersion, removeProfile } },
}))
vi.mock('@/stores', () => ({ useAppStore: () => ({ showError, showSuccess }) }))
vi.mock('vue-i18n', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({ t: (key: string) => key }),
}))

const profile = (key: string, version: number, status: 'draft' | 'active' | 'disabled') => ({
  id: version * 10 + key.length, profile_key: key, version, name: `${key} v${version}`, status,
  spec: { auth: { scheme: 'bearer' }, version }, spec_hash: 'abcdef0123456789', created_at: '2026-10-01T00:00:00Z',
})

function mountView() {
  return mount(VideoProtocolProfilesView, {
    global: { stubs: {
      AppLayout: { template: '<div><slot /></div>' },
      // 列表沿用 TablePageLayout + DataTable；测试直接渲染每行的单元格插槽，避免虚拟滚动依赖布局尺寸。
      TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /></div>' },
      DataTable: {
        props: ['columns', 'data', 'loading', 'rowKey'],
        template: '<div><template v-if="data.length"><div v-for="row in data" :key="row.id"><template v-for="column in columns" :key="column.key"><slot :name="`cell-${column.key}`" :row="row" :value="row[column.key]" /></template></div></template><slot v-else name="empty" /></div>',
      },
      EmptyState: { props: ['description'], template: '<div>{{ description }}</div>' },
      BaseDialog: { props: ['show', 'title'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' },
      ConfirmDialog: { props: ['show', 'message'], emits: ['confirm', 'cancel'], template: '<div v-if="show" data-testid="confirm-dialog">{{ message }}<button data-testid="confirm-ok" @click="$emit(\'confirm\')" /></div>' },
      TextArea: { props: ['modelValue', 'label', 'hint', 'placeholder'], emits: ['update:modelValue'], template: '<textarea :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" />' },
    } },
  })
}

describe('video protocol profile helpers', () => {
  it('groups versions by key with newest first and marks the active version', () => {
    const groups = groupVideoProtocolProfiles([profile('b', 1, 'active'), profile('a', 1, 'disabled'), profile('a', 3, 'draft'), profile('a', 2, 'active')])
    expect(groups.map(group => group.key)).toEqual(['a', 'b'])
    expect(groups[0]!.versions.map(item => item.version)).toEqual([3, 2, 1])
    expect(groups[0]!.active?.version).toBe(2)
  })

  it('parses JSON objects strictly and optional JSON leniently', () => {
    expect(parseJsonObject('{"a":1}')).toEqual({ a: 1 })
    expect(parseJsonObject('[1]')).toBeNull()
    expect(parseJsonObject('{')).toBeNull()
    expect(parseOptionalJson('  ')).toBeUndefined()
    expect(parseOptionalJson('[1]')).toEqual([1])
    expect(parseOptionalJson('{')).toBeNull()
  })

  it('ships a starter template for POST /v1/video/generations and GET /v1/tasks/{task_id}', () => {
    expect(VIDEO_PROFILE_STARTER_SPEC.create.path).toBe('/v1/video/generations')
    expect(VIDEO_PROFILE_STARTER_SPEC.poll.path).toBe('/v1/tasks/{task_id}')
    expect(Object.values(VIDEO_PROFILE_STARTER_SPEC.poll.response.status_map)).toEqual(expect.arrayContaining(['completed', 'failed']))
  })

  // 调试样例按模板能力生成：只写模板读取的字段，角色、数量、时长与档位都落在允许范围内。
  it('builds a debugger sample that fits the template capabilities', () => {
    const kling = videoProfileSampleRequest({
      capabilities: { min_images: 1, max_images: 2, image_roles: ['first_frame', 'last_frame'] },
      create: { defaults: { duration: 5, resolution: '720p' }, body: [
        { from: 'prompt', to: 'contents', each: { type: 'prompt', text: '$value' }, append: true },
        { from: 'images', to: 'contents', each: { type: '$role', url: '$url' }, append: true },
        { from: 'duration', to: 'settings.duration' }, { from: 'resolution', to: 'settings.resolution' },
      ] },
    })
    expect(kling.images).toEqual([{ url: 'https://example.com/reference-1.png', role: 'first_frame' }])
    expect(kling).not.toHaveProperty('aspect_ratio')
    expect(kling.duration).toBe(5)
    const referenceOnly = videoProfileSampleRequest({
      capabilities: { image_roles: ['reference_image'], durations: [8], min_duration: 6 },
      create: { body: [{ from: 'images', to: 'refs', item: '$url' }, { from: 'duration', to: 'seconds' }] },
    })
    expect(referenceOnly.images).toEqual([{ url: 'https://example.com/reference-1.png', role: 'reference_image' }])
    expect(referenceOnly.duration).toBe(8)
    expect(referenceOnly).not.toHaveProperty('prompt')
    const lookup = videoProfileSampleRequest({
      capabilities: { resolutions: ['768p', '2k'], aspect_ratios: ['16:9', '9:16'] },
      create: { body: [{ from_all: ['resolution', 'aspect_ratio'], to: 'size', map: { '2k|9:16': '1024x1792' } }] },
    })
    expect(lookup).toMatchObject({ resolution: '2k', aspect_ratio: '9:16' })
    const textOnly = videoProfileSampleRequest({ create: { body: [{ from: 'prompt', to: 'prompt' }] } })
    expect(textOnly).not.toHaveProperty('images')
  })
})

describe('VideoProtocolProfilesView', () => {
  beforeEach(() => {
    for (const fn of [listProfiles, saveProfile, setStatus, previewProfile, showError, showSuccess]) fn.mockReset()
    listProfiles.mockResolvedValue([profile('vendor_x', 1, 'disabled'), profile('vendor_x', 2, 'active'), profile('draft_only', 1, 'draft')])
    setStatus.mockResolvedValue(undefined)
  })

  it('renders grouped versions with status actions', async () => {
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.find('[data-testid="video-profile-group-vendor_x"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="video-profile-group-draft_only"]').text()).toContain('admin.videoProtocolProfiles.noActiveVersion')
    expect(wrapper.find('[data-testid="video-profile-disable-vendor_x-2"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="video-profile-activate-vendor_x-2"]').exists()).toBe(false)
    await wrapper.get('[data-testid="video-profile-activate-vendor_x-1"]').trigger('click')
    await flushPromises()
    expect(setStatus).toHaveBeenCalledWith('vendor_x', 1, 'active')
    expect(listProfiles).toHaveBeenCalledTimes(2)
    await wrapper.get('[data-testid="video-profile-disable-vendor_x-2"]').trigger('click')
    await flushPromises()
    expect(setStatus).toHaveBeenLastCalledWith('vendor_x', 2, 'disabled')
    wrapper.unmount()
  })

  it('opens the starter editor, validates input and saves a new active version', async () => {
    saveProfile.mockResolvedValue(profile('acme', 1, 'active'))
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.find('[data-testid="video-profile-editor"]').exists()).toBe(false)
    await wrapper.get('[data-testid="video-profiles-create"]').trigger('click')
    const spec = wrapper.get('[data-testid="video-profile-spec"]').element as HTMLTextAreaElement
    expect(JSON.parse(spec.value)).toEqual(VIDEO_PROFILE_STARTER_SPEC)
    await wrapper.get('[data-testid="video-profile-save-active"]').trigger('click')
    expect(showError).toHaveBeenCalledWith('admin.videoProtocolProfiles.profileKeyInvalid')
    await wrapper.get('[data-testid="video-profile-key"]').setValue('acme')
    await wrapper.get('[data-testid="video-profile-name"]').setValue('Acme Video')
    await wrapper.get('[data-testid="video-profile-save-active"]').trigger('click')
    await flushPromises()
    expect(saveProfile).toHaveBeenCalledWith({ profile_key: 'acme', name: 'Acme Video', spec: VIDEO_PROFILE_STARTER_SPEC, activate: true })
    expect(showSuccess).toHaveBeenCalledWith('admin.videoProtocolProfiles.saved')
    wrapper.unmount()
  })

  it('clones an existing version as a draft base', async () => {
    saveProfile.mockResolvedValue(profile('vendor_x', 3, 'draft'))
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="video-profile-clone-vendor_x-2"]').trigger('click')
    expect((wrapper.get('[data-testid="video-profile-key"]').element as HTMLInputElement).value).toBe('vendor_x')
    await wrapper.get('[data-testid="video-profile-save-draft"]').trigger('click')
    await flushPromises()
    expect(saveProfile).toHaveBeenCalledWith({ profile_key: 'vendor_x', name: 'vendor_x v2', spec: { auth: { scheme: 'bearer' }, version: 2 }, activate: false })
    wrapper.unmount()
  })

  it('runs the debugger and renders paths, body, billing and parsed responses', async () => {
    previewProfile.mockResolvedValue({
      create_path: '/v1/video/generations', create_body: { model: 'm', prompt: 'p' },
      billing: { model: 'm', resolution: '720p', duration_seconds: 5, has_reference_video: false, reference_image_count: 1 },
      create: { task_id: 'task_123', status: 'queued', matches_task: true },
      poll_path: '/v1/tasks/task_123',
      poll: { task_id: 'task_123', status: 'completed', upstream_status: 'succeeded', video_url: 'https://cdn.example.com/a.mp4', seconds: 5, matches_task: true },
      content_path: '', request_error: 'images[0] is required',
    })
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="video-profiles-create"]').trigger('click')
    await wrapper.get('[data-testid="video-profile-debug-model"]').setValue('vendor-model')
    await wrapper.get('[data-testid="video-profile-debug-poll"]').setValue('')
    await wrapper.get('[data-testid="video-profile-run-preview"]').trigger('click')
    await flushPromises()
    const payload = previewProfile.mock.calls[0]![0]
    expect(payload.spec).toEqual(VIDEO_PROFILE_STARTER_SPEC)
    expect(payload.model).toBe('vendor-model')
    expect(payload.create_response).toEqual({ task_id: 'task_123', status: 'queued' })
    expect(payload).not.toHaveProperty('poll_response')
    expect(wrapper.get('[data-testid="video-profile-preview-create-path"]').text()).toBe('/v1/video/generations')
    expect(wrapper.get('[data-testid="video-profile-preview-poll-path"]').text()).toBe('/v1/tasks/task_123')
    expect(wrapper.get('[data-testid="video-profile-preview-content-path"]').text()).toBe('admin.videoProtocolProfiles.none')
    expect(JSON.parse(wrapper.get('[data-testid="video-profile-preview-create-body"]').text())).toEqual({ model: 'm', prompt: 'p' })
    expect(wrapper.get('[data-testid="video-profile-preview-billing"]').text()).toContain('720p')
    expect(wrapper.get('[data-testid="video-profile-preview-poll"]').text()).toContain('https://cdn.example.com/a.mp4')
    expect(wrapper.get('[data-testid="video-profile-preview-request-error"]').text()).toContain('images[0] is required')
    wrapper.unmount()
  })

  it('reports invalid debugger JSON without calling the API', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="video-profiles-create"]').trigger('click')
    await wrapper.get('[data-testid="video-profile-debug-request"]').setValue('{')
    await wrapper.get('[data-testid="video-profile-run-preview"]').trigger('click')
    await flushPromises()
    expect(previewProfile).not.toHaveBeenCalled()
    expect(wrapper.get('[data-testid="video-profile-preview-error"]').text()).toBe('admin.videoProtocolProfiles.invalidJson')
    wrapper.unmount()
  })
})

describe('VideoProtocolProfilesView editing and AI', () => {
  beforeEach(() => {
    for (const fn of [listProfiles, saveProfile, setStatus, previewProfile, generateWithAI, removeVersion, removeProfile, showError, showSuccess]) fn.mockReset()
    listProfiles.mockResolvedValue([profile('vendor_x', 2, 'active')])
  })

  // 页面标题由 AppLayout 展示，视图内不再重复渲染一级标题。
  it('does not render a duplicated page heading', async () => {
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.find('h2').exists()).toBe(false)
    wrapper.unmount()
  })

  // 启用版本不提供删除；非启用版本与整个模板删除前需确认，成功后刷新列表。
  it('deletes a version or a whole template after confirmation', async () => {
    listProfiles.mockResolvedValue([profile('vendor_x', 2, 'active'), profile('vendor_x', 1, 'draft')])
    removeVersion.mockResolvedValue(undefined)
    removeProfile.mockResolvedValue(undefined)
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.find('[data-testid="video-profile-delete-vendor_x-2"]').exists()).toBe(false)
    await wrapper.get('[data-testid="video-profile-delete-vendor_x-1"]').trigger('click')
    expect(wrapper.get('[data-testid="confirm-dialog"]').text()).toContain('admin.videoProtocolProfiles.deleteVersionConfirm')
    await wrapper.get('[data-testid="confirm-ok"]').trigger('click')
    await flushPromises()
    expect(removeVersion).toHaveBeenCalledWith('vendor_x', 1)
    await wrapper.get('[data-testid="video-profile-delete-group-vendor_x"]').trigger('click')
    await wrapper.get('[data-testid="confirm-ok"]').trigger('click')
    await flushPromises()
    expect(removeProfile).toHaveBeenCalledWith('vendor_x')
    expect(listProfiles).toHaveBeenCalledTimes(3)
    wrapper.unmount()
  })

  // 编辑锁定键名，保存为新版本；源版本启用时以“保存修改并启用”提交。
  it('edits an existing version with a locked key', async () => {
    saveProfile.mockResolvedValue({ ...profile('vendor_x', 3, 'active') })
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="video-profile-edit-vendor_x-2"]').trigger('click')
    const key = wrapper.get<HTMLInputElement>('[data-testid="video-profile-key"]')
    expect(key.element.value).toBe('vendor_x')
    expect(key.element.disabled).toBe(true)
    expect(wrapper.get('[data-testid="video-profile-save-active"]').text()).toBe('admin.videoProtocolProfiles.editSaveActive')
    await wrapper.get('[data-testid="video-profile-save-active"]').trigger('click')
    await flushPromises()
    expect(saveProfile).toHaveBeenCalledWith(expect.objectContaining({ profile_key: 'vendor_x', activate: true }))
    wrapper.unmount()
  })

  // AI 生成结果只回填编辑器，不保存；校验未通过时展示错误供管理员修正。
  it('fills the editor from an AI draft without saving', async () => {
    generateWithAI.mockResolvedValue({ profile_key: 'ai_vendor', name: 'AI 厂商', spec: { auth: { scheme: 'bearer' } }, notes: '任务 ID 推测为 data.id', valid: false, validation_error: 'create.body requires at least one rule', attempts: 3 })
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="video-profiles-ai-open"]').trigger('click')
    await wrapper.get('[data-testid="video-profile-ai-generate"]').trigger('click')
    expect(generateWithAI).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="video-profile-ai-document"]').setValue('POST /v1/video ...')
    await wrapper.get('[data-testid="video-profile-ai-generate"]').trigger('click')
    await flushPromises()
    expect(generateWithAI).toHaveBeenCalledWith(expect.objectContaining({ document: 'POST /v1/video ...' }))
    expect(wrapper.get<HTMLInputElement>('[data-testid="video-profile-key"]').element.value).toBe('ai_vendor')
    expect(wrapper.get<HTMLTextAreaElement>('[data-testid="video-profile-spec"]').element.value).toContain('"scheme": "bearer"')
    expect(wrapper.get('[data-testid="video-profile-ai-notice"]').text()).toContain('任务 ID 推测为 data.id')
    expect(saveProfile).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
