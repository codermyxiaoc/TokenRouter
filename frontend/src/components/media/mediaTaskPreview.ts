import { sanitizeUrl } from '@/utils/url'

// 相对地址只允许本系统签名预览代理，禁止任意面板接口被当作媒体触发请求。
const previewContentPath = /^\/api\/v1\/media-tasks\/preview-content\/[a-f0-9]{64}$/

// 禁止把内联数据、脚本或 URL 内凭据带入播放器和下载入口。
export function mediaPreviewUrl(value: unknown): string {
  if (typeof value !== 'string') return ''
  const trimmed = value.trim()
  if (trimmed.startsWith('/') && !trimmed.startsWith('//') && !trimmed.includes('\\')) {
    const relative = new URL(trimmed, 'https://preview.invalid')
    return previewContentPath.test(relative.pathname) && !relative.search && !relative.hash ? relative.pathname : ''
  }
  const sanitized = sanitizeUrl(value)
  if (!sanitized) return ''
  const parsed = new URL(sanitized)
  return parsed.username || parsed.password ? '' : sanitized
}

const mediaFormats: Record<string, string> = {
  'image/png': 'PNG',
  'image/jpeg': 'JPEG',
  'image/webp': 'WebP',
  'image/gif': 'GIF',
  'image/avif': 'AVIF',
  'video/mp4': 'MP4',
  'video/webm': 'WebM',
  'video/quicktime': 'MOV',
  'video/ogg': 'OGV',
}

// 文件格式以明确的 MIME 为准，不把 URL 后缀当作已验证的文件属性。
export function mediaFormat(mimeType: string | null | undefined): string {
  return mediaFormats[(mimeType || '').split(';', 1)[0].trim().toLowerCase()] || ''
}

export function mediaAspectRatio(width: number | null | undefined, height: number | null | undefined): string {
  if (!Number.isSafeInteger(width) || !Number.isSafeInteger(height) || !width || !height || width < 0 || height < 0) return ''
  let a = width
  let b = height
  while (b) [a, b] = [b, a % b]
  return `${width / a}:${height / a}`
}

// 使用时间码保留真实时长的小数位，未加载完成和无限流不显示为零秒。
export function mediaDuration(seconds: number | null | undefined): string {
  if (seconds === null || seconds === undefined || !Number.isFinite(seconds) || seconds < 0) return ''
  const tenths = Math.round(seconds * 10)
  const minutes = Math.floor(tenths / 600)
  const remainder = tenths % 600
  const wholeSeconds = Math.floor(remainder / 10).toString().padStart(2, '0')
  const decimal = remainder % 10
  return `${minutes.toString().padStart(2, '0')}:${wholeSeconds}${decimal ? `.${decimal}` : ''}`
}

export function mediaDownloadName(taskID: string, index: number, mimeType?: string | null): string {
  const safeID = taskID.replace(/[^a-zA-Z0-9_-]/g, '-').slice(0, 80) || 'result'
  const format = mediaFormat(mimeType)
  const extension = format === 'JPEG' ? 'jpg' : format.toLowerCase()
  return `media-${safeID}-${index + 1}${extension ? `.${extension}` : ''}`
}
