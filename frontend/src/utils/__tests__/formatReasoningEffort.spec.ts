import { describe, expect, it } from 'vitest'

import { formatReasoningEffortMapping, reasoningEffortValuesEqual } from '@/utils/format'

describe('reasoning effort formatting', () => {
  it('treats compatible separators as the same value', () => {
    expect(reasoningEffortValuesEqual('x-high', 'X_HIGH')).toBe(true)
  })

  it('renders a requested-to-forwarded mapping', () => {
    expect(formatReasoningEffortMapping('max', 'xhigh')).toBe('Max → XHigh')
  })

  it('falls back to the effective value for legacy rows', () => {
    expect(formatReasoningEffortMapping(undefined, 'high')).toBe('High')
  })
})
