import { describe, expect, it } from 'vitest'
import { buildOpsPlatformOptions } from '../platformOptions'

describe('buildOpsPlatformOptions', () => {
  it('包含 Qoder 并从分组数据补充未知平台', () => {
    const options = buildOpsPlatformOptions(
      [
        { platform: 'qoder' },
        { platform: ' future-ai ' },
        { platform: 'FUTURE-AI' },
      ],
      '全部'
    )

    expect(options).toContainEqual({ value: 'qoder', label: 'Qoder' })
    expect(options).toContainEqual({ value: 'future-ai', label: 'FUTURE-AI' })
    expect(options.filter(option => option.value === 'future-ai')).toHaveLength(1)
  })
})
