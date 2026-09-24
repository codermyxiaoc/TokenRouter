import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import MediaTasksView from '../MediaTasksView.vue'
import type { MediaTask } from '@/api/mediaTasks'

const { list, get, api } = vi.hoisted(() => ({ list: vi.fn(), get: vi.fn(), api: vi.fn() }))
vi.mock('@/api/mediaTasks', async (importOriginal) => ({ ...(await importOriginal<typeof import('@/api/mediaTasks')>()), mediaTasksAPI: api }))
vi.mock('vue-i18n', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({ t: (key: string) => key, te: () => true }),
}))
vi.mock('@/utils/apiError', () => ({ extractApiErrorMessage: (_: unknown, fallback: string) => fallback }))

function task(overrides: Partial<MediaTask> = {}): MediaTask {
  return { id: 1, task_id: 'video-1', source: 'grok_video', media_type: 'video', platform: 'grok', model: 'grok-imagine-video',
    status: 'completed', upstream_status: 'done', user_id: 10, api_key_id: 20, group_id: 30, account_id: 40, group_name: 'Video group',
    http_status: 200, error_message: '', request_id: 'request-1', created_at: '2026-09-22T00:00:00Z', updated_at: '2026-09-22T00:01:00Z',
    completed_at: null, expires_at: null, actual_cost: null, billing_mode: null, ...overrides }
}
const result = (items: MediaTask[], page = 1) => ({ items, total: 50, page, page_size: 20, pages: 3 })
function mountView(admin = false) {
  return mount(MediaTasksView, {
    props: { admin },
    global: { stubs: {
      AppLayout: { template: '<div><slot name="page-heading-actions" /><slot /></div>' },
      Select: { props: ['modelValue', 'options'], template: '<div />' },
      Pagination: { props: ['page', 'pageSize', 'total'], template: '<button data-testid="next" @click="$emit(\'update:page\', 2)">next</button>' },
      BaseDialog: { props: ['show'], template: '<div v-if="show" data-testid="detail"><slot /></div>' },
    } },
  })
}

describe('MediaTasksView', () => {
  beforeEach(() => { list.mockReset(); get.mockReset(); api.mockReset(); api.mockReturnValue({ list, get }); list.mockResolvedValue(result([])) })

  it('完成任务仍可待确认，明确零费用显示为零且无写操作', async () => {
    list.mockResolvedValue(result([task(), task({ id: 2, task_id: 'zero-cost', actual_cost: '0' })]))
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.text()).toContain('mediaTasks.pendingCost')
    expect(wrapper.text()).toContain('$0.00')
    expect(wrapper.text()).toContain('mediaTasks.billingHint')
    expect(wrapper.find('#media-user').exists()).toBe(false)
    expect(wrapper.findAll('button').map(button => button.text())).not.toContain('common.delete')
    get.mockResolvedValue(task({ actual_cost: '0.125', error_message: '<script>alert(1)</script>' }))
    await wrapper.findAll('button').find(button => button.text() === 'mediaTasks.details')!.trigger('click')
    await flushPromises()
    expect(get).toHaveBeenCalledWith(1)
    expect(wrapper.get('[data-testid="detail"]').text()).toContain('$0.125')
    expect(wrapper.find('[data-testid="detail"] script').exists()).toBe(false)
    wrapper.unmount()
  })

  it('管理员筛选与翻页使用管理员入口，搜索回到第一页', async () => {
    list.mockResolvedValue(result([task()]))
    const wrapper = mountView(true)
    await flushPromises()
    await wrapper.get('#media-user').setValue('42')
    await wrapper.get('#media-model').setValue('  gpt-image-2  ')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api).toHaveBeenLastCalledWith(true)
    expect(list).toHaveBeenLastCalledWith(expect.objectContaining({ user_id: 42, model: 'gpt-image-2', page: 1 }))
    await wrapper.get('[data-testid="next"]').trigger('click')
    await flushPromises()
    expect(list).toHaveBeenLastCalledWith(expect.objectContaining({ page: 2 }))
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(list).toHaveBeenLastCalledWith(expect.objectContaining({ page: 1 }))
    wrapper.unmount()
  })

  it('管理员用户列展示名称、邮箱提示、删除标记，并兼容缺失身份', async () => {
    const named = task({ user: { id: 10, username: '任务测试用户', email: 'tester@example.com' } })
    list.mockResolvedValue(result([
      named,
      task({ id: 2, user_id: 11, user: { id: 11, username: '', email: 'alice@example.com', deleted_at: '2026-09-23T00:00:00Z' } }),
      task({ id: 3, user_id: 12 }),
    ]))
    const wrapper = mountView(true)
    await flushPromises()
    const cells = wrapper.findAll('[data-testid="task-user-cell"]')
    expect(cells).toHaveLength(3)
    expect(cells[0].text()).toContain('任务测试用户')
    expect(cells[0].text()).toContain('#10')
    expect(cells[0].get('[title]').attributes('title')).toBe('任务测试用户 (tester@example.com)')
    expect(cells[1].text()).toContain('a***e')
    expect(cells[1].text()).toContain('admin.usage.userDeletedBadge')
    expect(cells[1].get('[title]').attributes('title')).toBe('alice@example.com')
    expect(cells[2].text()).toContain('—')
    expect(cells[2].text()).toContain('#12')
    get.mockResolvedValue(named)
    await wrapper.findAll('button').find(button => button.text() === 'mediaTasks.details')!.trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="detail"]').text()).toContain('任务测试用户 (tester@example.com) #10')
    wrapper.unmount()
  })

  it('个人入口不展示管理员用户摘要或用户详情字段', async () => {
    // 即便拿到带用户摘要的对象，个人页面也不渲染管理员字段。
    const item = task({ user: { id: 10, username: '不应展示的名称', email: 'hidden@example.com' } })
    list.mockResolvedValue(result([item]))
    get.mockResolvedValue(item)
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.find('[data-testid="task-user-cell"]').exists()).toBe(false)
    await wrapper.findAll('button').find(button => button.text() === 'mediaTasks.details')!.trigger('click')
    await flushPromises()
    expect(wrapper.text()).not.toContain('不应展示的名称')
    expect(wrapper.text()).not.toContain('hidden@example.com')
    wrapper.unmount()
  })

  it('晚到的旧列表响应不会覆盖当前筛选', async () => {
    let resolveOld!: (value: ReturnType<typeof result>) => void
    list.mockReturnValueOnce(new Promise(resolve => { resolveOld = resolve }))
    const wrapper = mountView()
    list.mockResolvedValueOnce(result([task({ task_id: 'new-result' })]))
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    resolveOld(result([task({ task_id: 'old-result' })]))
    await flushPromises()
    expect(wrapper.text()).toContain('new-result')
    expect(wrapper.text()).not.toContain('old-result')
    wrapper.unmount()
  })

  it('加载失败显示错误并清空上次列表', async () => {
    list.mockResolvedValueOnce(result([task()]))
    const wrapper = mountView()
    await flushPromises()
    list.mockRejectedValueOnce(new Error('network'))
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe('mediaTasks.loadFailed')
    expect(wrapper.text()).not.toContain('video-1')
    wrapper.unmount()
  })
})
