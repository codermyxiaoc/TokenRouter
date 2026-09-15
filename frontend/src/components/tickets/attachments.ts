import type { TicketSettings } from '@/api/tickets'

export const attachmentAccept = '.jpg,.jpeg,.png,.gif,.webp,.pdf,.doc,.docx'

// 浏览器校验用于及时反馈，实际文件格式、权限和数量仍由服务端最终校验。
export function validateTicketFiles(files: File[], settings: TicketSettings): 'count' | 'type' | 'size' | 'total' | null {
  if (files.length > settings.max_attachments) return 'count'
  if (files.some(file => !/\.(jpe?g|png|gif|webp|pdf|docx?)$/i.test(file.name))) return 'type'
  if (files.some(file => file.size === 0 || file.size > settings.max_attachment_size_mb * 1024 * 1024)) return 'size'
  if (files.reduce((sum, file) => sum + file.size, 0) > 50 * 1024 * 1024) return 'total'
  return null
}

export function formatAttachmentSize(bytes: number): string {
  return bytes >= 1024 * 1024 ? `${(bytes / (1024 * 1024)).toFixed(1)} MB` : `${Math.ceil(bytes / 1024)} KB`
}
