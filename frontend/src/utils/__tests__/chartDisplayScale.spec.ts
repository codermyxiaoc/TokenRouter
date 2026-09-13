import { describe, expect, it } from 'vitest'

import { toLogarithmicDisplayValues } from '../chartDisplayScale'

describe('toLogarithmicDisplayValues', () => {
  it('uses logarithmic proportions even when values are relatively balanced', () => {
    const result = toLogarithmicDisplayValues([1200, 600])
    expect(result[0]).toBeCloseTo(1 + Math.log10(2))
    expect(result[1]).toBe(1)
  })

  it('normalizes a single positive value and preserves empty data', () => {
    expect(toLogarithmicDisplayValues([42])).toEqual([1])
    expect(toLogarithmicDisplayValues([0, 0])).toEqual([0, 0])
    expect(toLogarithmicDisplayValues([10, 0])).toEqual([1, 0])
  })

  it('keeps the smallest positive value visible under skewed values', () => {
    const result = toLogarithmicDisplayValues([1_000_000, 10_000, 100])
    // log10 压缩后最小值固定为 1，最大值 = log10(比值) + 1
    expect(result[2]).toBe(1)
    expect(result[0]).toBe(5)
    expect(result[1]).toBe(3)
  })

  it('keeps zero values at zero while compressing positives', () => {
    const result = toLogarithmicDisplayValues([1_000_000, 0, 100])
    expect(result[0]).toBe(5)
    expect(result[1]).toBe(0)
    expect(result[2]).toBe(1)
  })
})
