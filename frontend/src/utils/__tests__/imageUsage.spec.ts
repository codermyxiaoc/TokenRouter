import { describe, expect, it } from 'vitest'

import { hasImageInputCost, hasImageInputTokens, textInputTokens } from '@/utils/imageUsage'

describe('imageUsage input helpers', () => {
  it('detects image input tokens and costs', () => {
    expect(hasImageInputTokens({ input_tokens: 371, image_input_tokens: 352 })).toBe(true)
    expect(hasImageInputTokens({ input_tokens: 19, image_input_tokens: 0 })).toBe(false)
    expect(hasImageInputCost({ image_input_cost: 0.002816 })).toBe(true)
    expect(hasImageInputCost({ image_input_cost: 0 })).toBe(false)
  })

  it('returns the text-only portion of total input tokens', () => {
    expect(textInputTokens({ input_tokens: 371, image_input_tokens: 352 })).toBe(19)
  })

  it('clamps malformed image input totals to zero text tokens', () => {
    expect(textInputTokens({ input_tokens: 10, image_input_tokens: 50 })).toBe(0)
  })
})
