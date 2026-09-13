import { describe, expect, it } from 'vitest'
import {
  isSupportedCreativeImageFile,
  parseCreativeOutputDrag,
  serializeCreativeOutputDrag,
} from '@/utils/creativeDrag'

describe('creativeDrag', () => {
  it('识别支持的图片 MIME 与无 MIME 文件扩展名', () => {
    expect(isSupportedCreativeImageFile(new File(['x'], 'a.png', { type: 'image/png' }))).toBe(true)
    expect(isSupportedCreativeImageFile(new File(['x'], 'a.jpg', { type: 'image/jpeg' }))).toBe(true)
    expect(isSupportedCreativeImageFile(new File(['x'], 'a.webp', { type: '' }))).toBe(true)
    expect(isSupportedCreativeImageFile(new File(['x'], 'a.gif', { type: 'image/gif' }))).toBe(false)
    expect(isSupportedCreativeImageFile(new File(['x'], 'a.png', { type: 'text/plain' }))).toBe(false)
  })

  it('往返序列化历史输出拖放载荷', () => {
    const payload = { runId: 'crun_0123456789abcdef', outputIndex: 2 }
    expect(parseCreativeOutputDrag(serializeCreativeOutputDrag(payload))).toEqual(payload)
  })

  it.each([
    '',
    '{bad json',
    JSON.stringify(null),
    JSON.stringify({ runId: '', outputIndex: 0 }),
    JSON.stringify({ runId: 'crun 1', outputIndex: 0 }),
    JSON.stringify({ runId: 'crun_1', outputIndex: -1 }),
    JSON.stringify({ runId: 'crun_1', outputIndex: 1.5 }),
    JSON.stringify({ runId: 'crun_1', outputIndex: '1' }),
  ])('拒绝非法拖放载荷 %s', (value) => {
    expect(parseCreativeOutputDrag(value)).toBeNull()
  })
})
