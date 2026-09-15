import { describe, expect, it } from 'vitest'
import { validateTicketFiles } from '../attachments'
import type { TicketSettings } from '@/api/tickets'

const settings: TicketSettings = { enabled: true, max_open_tickets: 3, max_attachments: 5, max_attachment_size_mb: 20, notify_on_staff_reply: false, auto_expire_hours: 72 }
// 大小边界使用 File 的只读元数据替身，避免测试分配数十 MB 内容。
function file(name: string, size = 1): File {
  const value = new File(['x'], name)
  Object.defineProperty(value, 'size', { value: size })
  return value
}

describe('工单附件输入限制', () => {
  it('禁止附件时拒绝文件但允许纯文本提交', () => {
    expect(validateTicketFiles([file('a.pdf')], { ...settings, max_attachments: 0 })).toBe('count')
    expect(validateTicketFiles([], { ...settings, max_attachments: 0 })).toBeNull()
  })
  it('拒绝空文件、伪装双后缀和超出单文件限制的文件', () => {
    expect(validateTicketFiles([file('a.docx', 0)], settings)).toBe('size')
    expect(validateTicketFiles([file('a.pdf.html')], settings)).toBe('type')
    expect(validateTicketFiles([file('a.pdf', 20 * 1024 * 1024 + 1)], settings)).toBe('size')
    expect(validateTicketFiles([file('a.DOCX')], settings)).toBeNull()
  })
  it('允许恰好50MiB，并拒绝单个都合法但总量越界的附件组合', () => {
    const mb = 1024 * 1024
    expect(validateTicketFiles([file('a.pdf', 20 * mb), file('b.doc', 20 * mb), file('c.png', 10 * mb)], settings)).toBeNull()
    expect(validateTicketFiles([file('a.pdf', 20 * mb), file('b.doc', 20 * mb), file('c.png', 10 * mb + 1)], settings)).toBe('total')
  })
})
