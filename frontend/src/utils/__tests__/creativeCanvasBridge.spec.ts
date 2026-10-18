import { describe, expect, it } from 'vitest'
import { isCanvasBridgeMessage } from '../creativeCanvasBridge'

describe('云端画布会话边界', () => {
  const child = {} as Window
  const valid = { origin: 'https://example.test', source: child, data: { namespace: 'tokenrouter-canvas', type: 'ready' } }
  it('只接受当前同源画布的已知消息', () => {
    expect(isCanvasBridgeMessage(valid, valid.origin, child)).toBe(true)
    expect(isCanvasBridgeMessage({ ...valid, origin: 'https://attacker.test' }, valid.origin, child)).toBe(false)
    expect(isCanvasBridgeMessage({ ...valid, source: {} as Window }, valid.origin, child)).toBe(false)
    expect(isCanvasBridgeMessage(valid, valid.origin, null)).toBe(false)
  })
  it.each([null, 'ready', {}, { namespace: 'tokenrouter-canvas', type: 'session' }, { namespace: 'other', type: 'ready' }])('忽略异常或伪造数据 %j', (data) => {
    expect(isCanvasBridgeMessage({ ...valid, data }, valid.origin, child)).toBe(false)
  })
})
