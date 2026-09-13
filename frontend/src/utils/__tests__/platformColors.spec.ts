import { describe, expect, it } from 'vitest'

import { platformLabel, usagePlatformLabel } from '../platformColors'

describe('platformLabels', () => {
  it('使用品牌规定的 Qoder 大小写', () => {
    expect(platformLabel('qoder')).toBe('Qoder')
    expect(usagePlatformLabel('qoder')).toBe('Qoder')
  })

  it('用量页面继续使用 Claude 产品名', () => {
    expect(platformLabel('anthropic')).toBe('Anthropic')
    expect(usagePlatformLabel('anthropic')).toBe('Claude')
  })
})
