import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, post, patch } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), patch: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { get, post, patch } }))
import { ticketAPI } from '@/api/tickets'

describe('工单 HTTP 契约', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    get.mockResolvedValue({ data: {} })
    post.mockResolvedValue({ data: { id: 42 } })
    patch.mockResolvedValue({ data: { id: 42 } })
  })

  it('创建使用 JSON payload、重复 files 和稳定的重试请求头', async () => {
    const files = [new File(['pdf'], 'a.pdf'), new File(['doc'], 'b.docx')]
    const payload = { type: 'financial' as const, priority: 'high' as const, title: '付款问题', content: '详情', order_id: 12 }
    const result = await ticketAPI().create(payload, files, 'retry-create-1')
    const [path, data, options] = post.mock.calls[0]
    expect(path).toBe('/tickets')
    expect(JSON.parse(data.get('payload'))).toEqual(payload)
    expect(data.getAll('files')).toEqual(files)
    expect(options.headers['Idempotency-Key']).toBe('retry-create-1')
    expect(result.id).toBe(42)
  })

  it('管理员回复与优先级修改都通过管理员前缀，不发送客户端身份字段', async () => {
    await ticketAPI(true).reply(42, '正在处理', [], 'retry-reply-1')
    expect(post.mock.calls[0][0]).toBe('/admin/tickets/42/replies')
    expect(JSON.parse(post.mock.calls[0][1].get('payload'))).toEqual({ content: '正在处理' })
    await ticketAPI(true).update(42, { priority: 'normal' })
    expect(patch).toHaveBeenCalledWith('/admin/tickets/42', { priority: 'normal' })
  })

  it('附件必须使用现有鉴权客户端读取 blob，地址不含令牌或文件名', async () => {
    await ticketAPI().download(42, 9)
    expect(get).toHaveBeenLastCalledWith('/tickets/42/attachments/9', { responseType: 'blob' })
    await ticketAPI(true).download(42, 9)
    expect(get).toHaveBeenLastCalledWith('/admin/tickets/42/attachments/9', { responseType: 'blob' })
  })

  it('后台支持完成及撤销工单，用户操作使用自己的接口前缀', async () => {
    await ticketAPI(true).close(42, 'cancel')
    expect(post).toHaveBeenLastCalledWith('/admin/tickets/42/cancel')
    await ticketAPI(true).close(42, 'complete')
    expect(post).toHaveBeenLastCalledWith('/admin/tickets/42/complete')
    await ticketAPI().close(42, 'cancel')
    expect(post).toHaveBeenLastCalledWith('/tickets/42/cancel')
  })

  it.each([false, true])('预览沿用鉴权客户端及取消信号，401 保留统一刷新（admin=%s）', async admin => {
    const data = new Blob(['pdf'], { type: 'application/pdf' })
    get.mockResolvedValue({ status: 200, data })
    const signal = new AbortController().signal
    expect(await ticketAPI(admin).preview(42, 9, signal)).toBe(data)
    const [url, options] = get.mock.calls[0]
    expect(url).toBe(`${admin ? '/admin' : ''}/tickets/42/attachments/9/preview`)
    expect(options.responseType).toBe('blob')
    expect(options.signal).toBe(signal)
    expect(options.validateStatus(401)).toBe(false)
    expect(options.validateStatus(403)).toBe(true)
  })

  it('预览解析有界 JSON Blob 的业务原因，不把 HTML 错误页展示成附件', async () => {
    get.mockResolvedValue({ status: 403, data: { type: 'application/json', size: 80, text: async () => JSON.stringify({ reason: 'TICKET_DISABLED', code: 403 }) } })
    await expect(ticketAPI().preview(42, 9)).rejects.toMatchObject({ status: 403, reason: 'TICKET_DISABLED' })
    const text = vi.fn()
    get.mockResolvedValue({ status: 502, data: { type: 'text/html', size: 80, text } })
    await expect(ticketAPI().preview(42, 9)).rejects.toMatchObject({ status: 502 })
    expect(text).not.toHaveBeenCalled()
  })
})
