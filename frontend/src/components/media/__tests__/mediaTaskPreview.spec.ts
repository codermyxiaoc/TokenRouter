import { describe, expect, it } from 'vitest'
import { mediaAspectRatio, mediaDownloadName, mediaDuration, mediaFormat, mediaPreviewUrl } from '../mediaTaskPreview'

describe('媒体预览地址与属性', () => {
  it('只允许绝对 HTTP(S) 与结构固定的签名内容地址', () => {
    const ticketPath = `/api/v1/media-tasks/preview-content/${'a'.repeat(64)}`
    expect(mediaPreviewUrl(ticketPath)).toBe(ticketPath)
    expect(mediaPreviewUrl(' https://cdn.example.com/result.png ')).toBe('https://cdn.example.com/result.png')
    for (const unsafe of ['javascript:alert(1)', 'data:image/png;base64,AA==', 'blob:https://site.test/id', '//example.com/file', '/api/v1/admin/users', '/api/v1/media-tasks/preview-content/short', `${ticketPath}?url=https://evil.test`, `${ticketPath}#fragment`, 'https://user:secret@example.com/video.mp4', '/\\example.com/file']) {
      expect(mediaPreviewUrl(unsafe)).toBe('')
    }
  })

  it('不从 URL 或缺失属性猜测媒体格式、比例和时长', () => {
    expect(mediaFormat(undefined)).toBe('')
    expect(mediaFormat('application/octet-stream')).toBe('')
    expect(mediaFormat('IMAGE/JPEG; charset=binary')).toBe('JPEG')
    expect(mediaAspectRatio(1920, 1080)).toBe('16:9')
    expect(mediaAspectRatio(1536, 1024)).toBe('3:2')
    expect(mediaAspectRatio(0, 1080)).toBe('')
    expect(mediaAspectRatio(1280, undefined)).toBe('')
    expect(mediaDuration(Number.NaN)).toBe('')
    expect(mediaDuration(Infinity)).toBe('')
    expect(mediaDuration(undefined)).toBe('')
    expect(mediaDuration(65.3)).toBe('01:05.3')
    expect(mediaDuration(59.98)).toBe('01:00')
  })

  it('下载文件名移除路径字符且不凭空添加格式后缀', () => {
    expect(mediaDownloadName('../task/name', 0, 'image/jpeg')).toBe('media----task-name-1.jpg')
    expect(mediaDownloadName('video-task', 1)).toBe('media-video-task-2')
  })
})
