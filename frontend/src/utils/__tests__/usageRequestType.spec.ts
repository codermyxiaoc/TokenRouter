import { describe, expect, it } from 'vitest'

import { numericRequestTypeKind, requestTypeLabelKey } from '@/utils/errorBadges'
import {
  isUsageRequestType,
  requestTypeToLegacyStream,
  resolveUsageRequestType
} from '@/utils/usageRequestType'

describe('usage request type utils', () => {
  it('识别并展示 Live 请求类型', () => {
    expect(isUsageRequestType('live')).toBe(true)
    expect(resolveUsageRequestType({ request_type: 'live', stream: false })).toBe('live')
    expect(numericRequestTypeKind(5, false)).toBe('live')
    expect(requestTypeLabelKey('live')).toBe('usage.live')
  })

  it('不把 Live 回填为旧版流式维度', () => {
    // Live 是独立请求类型，不能被旧版 stream 布尔值错误归类。
    expect(requestTypeToLegacyStream('live')).toBeNull()
  })
})
