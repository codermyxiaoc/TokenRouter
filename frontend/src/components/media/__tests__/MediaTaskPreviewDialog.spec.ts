import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import MediaTaskPreviewDialog from '../MediaTaskPreviewDialog.vue'
import type { MediaTask, MediaTaskPreview } from '@/api/mediaTasks'

const { copyToClipboard } = vi.hoisted(() => ({ copyToClipboard: vi.fn() }))
vi.mock('@/composables/useClipboard', () => ({ useClipboard: () => ({ copyToClipboard }) }))
vi.mock('@/utils/format', () => ({ formatBytes: (value: number) => `${value} bytes` }))
vi.mock('vue-i18n', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({ t: (key: string) => key, te: () => true }),
}))

function task(overrides: Partial<MediaTask> = {}): MediaTask {
  return {
    id: 1, task_id: 'task-1', source: 'async_image', media_type: 'image', platform: 'openai', model: 'gpt-image-2',
    status: 'completed', upstream_status: 'done', user_id: 10, api_key_id: 20, group_id: 30, account_id: 40, group_name: 'Images',
    http_status: 200, error_message: '', request_id: 'request-1', created_at: '2026-09-22T00:00:00Z', updated_at: '2026-09-22T00:01:00Z',
    completed_at: null, expires_at: null, actual_cost: null, billing_mode: null, ...overrides,
  }
}
const imagePreview = (): MediaTaskPreview => ({ items: [{ media_type: 'image', url: 'https://cdn.example.com/image.png' }] })
const ticketPath = `/api/v1/media-tasks/preview-content/${'a'.repeat(64)}`
const wrappers: VueWrapper[] = []
function mountDialog(overrides: Record<string, unknown> = {}) {
  const wrapper = mount(MediaTaskPreviewDialog, {
    props: { show: true, task: task(), preview: imagePreview(), ...overrides },
    global: { stubs: {
      BaseDialog: { props: ['show', 'title'], template: '<div v-if="show"><slot /></div>' },
      Icon: true,
    } },
  })
  wrappers.push(wrapper)
  return wrapper
}

beforeEach(() => { copyToClipboard.mockReset().mockResolvedValue(true) })
afterEach(() => { wrappers.splice(0).forEach(wrapper => wrapper.unmount()); vi.useRealTimers() })

describe('MediaTaskPreviewDialog', () => {
  // 预览共用任务费用规则，不能把待核对或已释放再次展示成普通处理中或待扣费。
  it('预览保留真实生成终态并区分待核对和已释放', async () => {
    const billing = { status: 'reconciliation' as const, mode: 'video_per_request' as const, resolution: '720p',
      has_reference_video: false, unit_price: 5, unit: 'request' as const, duration_seconds: 15, reserved_amount: 5, pricing_source: 'channel' }
    const videoTask = task({ source: 'video', media_type: 'video', status: 'completed', video_billing: billing })
    const wrapper = mountDialog({ task: videoTask, preview: { items: [] } })
    expect(wrapper.text()).toContain('mediaTasks.statuses.completed')
    expect(wrapper.text()).toContain('mediaTasks.videoBilling.reconciliation')
    expect(wrapper.text()).not.toContain('mediaTasks.pendingCost')
    await wrapper.setProps({ task: { ...videoTask, status: 'processing' } })
    expect(wrapper.text()).toContain('mediaTasks.statuses.reconciliation')
    expect(wrapper.text()).not.toContain('mediaTasks.statuses.processing')
    await wrapper.setProps({ task: { ...videoTask, status: 'failed', video_billing: { ...billing, status: 'released' } } })
    expect(wrapper.text()).toContain('mediaTasks.statuses.failed')
    expect(wrapper.text()).toContain('mediaTasks.videoBilling.notCharged')
    expect(wrapper.text()).not.toContain('mediaTasks.pendingCost')
  })

  it('通过真实图片尺寸补齐分辨率和宽高比，不从扩展名伪造文件格式', async () => {
    const wrapper = mountDialog()
    const image = wrapper.get('[data-testid="preview-image"]')
    expect(image.attributes('referrerpolicy')).toBe('no-referrer')
    expect(wrapper.get('[data-testid="preview-properties"]').text()).toContain('mediaTasks.preview.unknown')
    expect(wrapper.get('[data-testid="preview-properties"]').text()).not.toContain('PNG')
    Object.defineProperties(image.element, { naturalWidth: { value: 1536 }, naturalHeight: { value: 1024 } })
    await image.trigger('load')
    expect(wrapper.get('[data-testid="preview-properties"]').text()).toContain('1536 × 1024')
    expect(wrapper.get('[data-testid="preview-properties"]').text()).toContain('3:2')
    expect(wrapper.get('[data-testid="preview-properties"]').text()).not.toContain('mediaTasks.preview.size')
    expect(wrapper.text()).toContain('mediaTasks.pendingCost')
  })

  it('视频使用原生控件且不自动播放，并显示播放器返回的实际时长', async () => {
    const wrapper = mountDialog({ task: task({ media_type: 'video', actual_cost: 0 }), preview: { items: [{ media_type: 'video', url: ticketPath, mime_type: 'video/mp4', size_bytes: 123456 }] } })
    const video = wrapper.get('[data-testid="preview-video"]')
    expect(video.attributes('controls')).toBeDefined()
    expect(video.attributes('playsinline')).toBeDefined()
    expect(video.attributes('preload')).toBe('metadata')
    expect(video.attributes('autoplay')).toBeUndefined()
    expect(video.attributes('aria-label')).toBe('mediaTasks.preview.videoLabel')
    Object.defineProperties(video.element, { videoWidth: { value: 1920 }, videoHeight: { value: 1080 }, duration: { value: 65.3 } })
    await video.trigger('loadedmetadata')
    const properties = wrapper.get('[data-testid="preview-properties"]').text()
    expect(properties).toContain('1920 × 1080')
    expect(properties).toContain('16:9')
    expect(properties).toContain('01:05.3')
    expect(properties).toContain('123456 bytes')
    expect(wrapper.text()).toContain('$0.00')
    await wrapper.get('[data-testid="preview-copy"]').trigger('click')
    expect(copyToClipboard).toHaveBeenCalledWith(new URL(ticketPath, window.location.origin).href, 'mediaTasks.preview.linkCopied')
    const download = wrapper.get('[data-testid="preview-download"]')
    expect(download.attributes('href')).toBe(ticketPath)
    expect(download.attributes('download')).toBe('media-task-1-1.mp4')
    expect(download.attributes('rel')).toBe('noopener noreferrer')
    // 独立视频价格使用五位展示，底层任务费用保持接口原值。
    const billedTask = task({ source: 'video', media_type: 'video', actual_cost: 0.7059298 })
    await wrapper.setProps({ task: billedTask })
    expect(wrapper.text()).toContain('$0.70593')
    expect(wrapper.text()).not.toContain('$0.7059298')
    expect(billedTask.actual_cost).toBe(0.7059298)
  })

  it('切换图片、任务和用户角色时清除旧媒体和元数据', async () => {
    const originalPreview: MediaTaskPreview = { items: [...imagePreview().items, { media_type: 'image', url: 'https://cdn.example.com/second.png' }] }
    const wrapper = mountDialog({ preview: originalPreview, admin: true, task: task({ user: { id: 10, username: 'Alice', email: 'alice@example.com' } }) })
    const originalImage = wrapper.get('[data-testid="preview-image"]')
    Object.defineProperties(originalImage.element, { naturalWidth: { value: 800 }, naturalHeight: { value: 600 } })
    await originalImage.trigger('load')
    await wrapper.get('[data-testid="preview-items"]').findAll('button')[1].trigger('click')
    expect(wrapper.get('[data-testid="preview-image"]').attributes('src')).toBe('https://cdn.example.com/second.png')
    expect(wrapper.text()).not.toContain('800 × 600')
    expect(wrapper.get('[data-testid="preview-user"]').text()).toContain('Alice (alice@example.com)')
    await wrapper.setProps({ task: task({ id: 2, user_id: 11 }) })
    expect(wrapper.find('[data-testid="preview-image"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="preview-actions"]').exists()).toBe(false)
    await wrapper.setProps({ preview: imagePreview() })
    expect(wrapper.find('[data-testid="preview-image"]').exists()).toBe(true)
    await wrapper.setProps({ admin: false })
    expect(wrapper.find('[data-testid="preview-user"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="preview-image"]').exists()).toBe(false)
  })

  it('关闭后不复用旧预览，放大状态也随新请求重置', async () => {
    const wrapper = mountDialog()
    await wrapper.get('[data-testid="preview-enlarge"]').trigger('click')
    expect(wrapper.get('[data-testid="preview-enlarge"]').attributes('aria-pressed')).toBe('true')
    expect(wrapper.get('[data-testid="preview-information"]').attributes('style')).toContain('display: none')
    await wrapper.setProps({ show: false })
    expect(wrapper.find('[data-testid="preview-image"]').exists()).toBe(false)
    await wrapper.setProps({ show: true })
    expect(wrapper.find('[data-testid="preview-image"]').exists()).toBe(false)
    await wrapper.setProps({ preview: imagePreview() })
    expect(wrapper.get('[data-testid="preview-enlarge"]').attributes('aria-pressed')).toBe('false')
  })

  it('预览失败仍保留任务信息，安全地址的链接操作可用且支持重试', async () => {
    const wrapper = mountDialog()
    await wrapper.get('[data-testid="preview-image"]').trigger('error')
    expect(wrapper.text()).toContain('mediaTasks.preview.mediaFailed')
    expect(wrapper.text()).toContain('gpt-image-2')
    expect(wrapper.find('[data-testid="preview-download"]').exists()).toBe(true)
    await wrapper.findAll('button').find(button => button.text() === 'mediaTasks.preview.retry')!.trigger('click')
    expect(wrapper.emitted('retry')).toHaveLength(1)
  })

  it('过期、未完成、非法链接与加载状态不会嵌入旧媒体', async () => {
    const wrapper = mountDialog({ preview: { items: imagePreview().items, unavailable_reason: 'expired' } })
    expect(wrapper.text()).toContain('mediaTasks.preview.expiredHint')
    expect(wrapper.find('img').exists()).toBe(false)
    await wrapper.setProps({ preview: { items: [], unavailable_reason: 'pending' } })
    expect(wrapper.text()).toContain('mediaTasks.preview.pendingHint')
    await wrapper.setProps({ preview: { items: [{ media_type: 'image', url: 'javascript:alert(1)' }] } })
    expect(wrapper.find('[data-testid="preview-download"]').exists()).toBe(false)
    await wrapper.setProps({ preview: imagePreview(), loading: true })
    expect(wrapper.text()).toContain('mediaTasks.preview.loading')
    expect(wrapper.find('img').exists()).toBe(false)
    await wrapper.setProps({ loading: false })
    await flushPromises()
    expect(wrapper.find('img').exists()).toBe(true)
  })

  it('弹窗停留到票据过期时立即移除媒体，用户重试后可加载新票据', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-09-30T08:00:00Z'))
    const wrapper = mountDialog({ preview: { ...imagePreview(), expires_at: '2026-09-30T08:10:00Z' } })
    await wrapper.get('[data-testid="preview-enlarge"]').trigger('click')
    expect(wrapper.find('img').exists()).toBe(true)
    await vi.advanceTimersByTimeAsync(10 * 60 * 1000)
    expect(wrapper.find('img').exists()).toBe(false)
    expect(wrapper.find('[data-testid="preview-actions"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('mediaTasks.preview.expiredHint')
    expect(wrapper.get('[data-testid="preview-information"]').isVisible()).toBe(true)
    await wrapper.findAll('button').find(button => button.text() === 'mediaTasks.preview.retry')!.trigger('click')
    expect(wrapper.emitted('retry')).toHaveLength(1)
    await wrapper.setProps({ preview: { ...imagePreview(), expires_at: '2026-09-30T08:20:00Z' } })
    expect(wrapper.find('img').exists()).toBe(true)
    await wrapper.setProps({ show: false })
    expect(vi.getTimerCount()).toBe(0)
  })

  it('新任务的有效期不会被旧预览计时器提前失效', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-09-30T08:00:00Z'))
    const wrapper = mountDialog({ preview: { ...imagePreview(), expires_at: '2026-09-30T08:00:01Z' } })
    await wrapper.setProps({ task: task({ id: 2 }), preview: { ...imagePreview(), expires_at: '2026-09-30T08:10:00Z' } })
    await vi.advanceTimersByTimeAsync(2000)
    expect(wrapper.find('img').exists()).toBe(true)
    expect(vi.getTimerCount()).toBe(1)
    wrapper.unmount()
    expect(vi.getTimerCount()).toBe(0)
  })
})
