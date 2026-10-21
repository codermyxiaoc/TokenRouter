import { beforeEach, describe, expect, it, vi } from 'vitest'
import { adminMediaTasksAPI, mediaTasksAPI } from '../mediaTasks'

const { get, post } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('../client', () => ({ apiClient: { get, post } }))

// 用户请求不能通过共用参数对象携带管理端的用户筛选字段。
describe('mediaTasksAPI', () => {
  beforeEach(() => { get.mockReset(); post.mockReset(); get.mockResolvedValue({ data: { items: [] } }) })

  it('结算仅使用独立管理员入口，按原请求传理由而不传客户端金额', async () => {
    const updated = { id: 42, billing_review: { status: 'released' } }
    post.mockResolvedValue({ data: updated })
    const request = { action: 'release' as const, reason: '已核对上游失败' }
    expect(await adminMediaTasksAPI.settle(42, request)).toEqual(updated)
    expect(post).toHaveBeenCalledWith('/admin/media-tasks/42/settlement', request)
    expect(mediaTasksAPI()).not.toHaveProperty('settle')
    expect(mediaTasksAPI(true)).not.toHaveProperty('settle')
  })

  it('使用个人入口并移除 user_id', async () => {
    await mediaTasksAPI().list({ page: 2, page_size: 20, user_id: 99, media_type: 'video' })
    expect(get).toHaveBeenCalledWith('/media-tasks', { params: { page: 2, page_size: 20, media_type: 'video' } })
    await mediaTasksAPI().get(42)
    expect(get).toHaveBeenLastCalledWith('/media-tasks/42')
  })

  it('管理员入口保留完整筛选并读取管理详情', async () => {
    const filters = { page: 1, page_size: 50, user_id: 99, source: 'async_image', status: 'failed', model: 'gpt-image-2' }
    await mediaTasksAPI(true).list(filters)
    expect(get).toHaveBeenCalledWith('/admin/media-tasks', { params: filters })
    await mediaTasksAPI(true).get(42)
    expect(get).toHaveBeenLastCalledWith('/admin/media-tasks/42')
  })

  it('模型选项与预览沿用角色入口，个人模型筛选不接受其他用户 ID', async () => {
    await mediaTasksAPI().models({ user_id: 99, media_type: 'video' })
    expect(get).toHaveBeenLastCalledWith('/media-tasks/models', { params: { media_type: 'video' } })
    await mediaTasksAPI(true).models({ user_id: 99 })
    expect(get).toHaveBeenLastCalledWith('/admin/media-tasks/models', { params: { user_id: 99 } })
    await mediaTasksAPI().preview(5)
    expect(get).toHaveBeenLastCalledWith('/media-tasks/5/preview')
    await mediaTasksAPI(true).preview(5)
    expect(get).toHaveBeenLastCalledWith('/admin/media-tasks/5/preview')
  })
})
