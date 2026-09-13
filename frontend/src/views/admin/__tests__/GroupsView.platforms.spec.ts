import { describe, expect, it } from 'vitest'
import { GROUP_PLATFORM_OPTIONS } from '@/constants/platforms'

describe('GroupsView platform options', () => {
  it('keeps all fork-supported concrete platforms available', () => {
    expect(GROUP_PLATFORM_OPTIONS.map((option) => option.value)).toEqual(
      expect.arrayContaining(['qoder', 'kimi', 'zhipu', 'deepseek', 'minimax'])
    )
  })
})
