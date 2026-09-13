import { describe, it, expect } from 'vitest'
import { buildApiKeyGroupFilterOptions } from '../apiKeyGroupFilterOptions'
import type { AdminGroup } from '@/types'

const labels = {
  all: 'All',
  exclusive: 'Exclusive',
  public: 'Public',
  disabled: 'Disabled',
}

function g(partial: Partial<AdminGroup>): AdminGroup {
  return {
    id: 0,
    name: '',
    status: 'active',
    is_exclusive: false,
    ...partial,
  } as AdminGroup
}

describe('buildApiKeyGroupFilterOptions', () => {
  it('partitions active groups into exclusive/public with headers', () => {
    const groups = [
      g({ id: 1, name: 'Excl', is_exclusive: true }),
      g({ id: 2, name: 'Pub', is_exclusive: false }),
    ]
    expect(buildApiKeyGroupFilterOptions(groups, labels)).toEqual([
      { value: null, label: 'All' },
      { value: -1, label: 'Exclusive', kind: 'group', disabled: true },
      { value: 1, label: 'Excl' },
      { value: -2, label: 'Public', kind: 'group', disabled: true },
      { value: 2, label: 'Pub' },
    ])
  })

  it('does not depend on upstream-only subscription_type fields', () => {
    const groups = [g({ id: 9, name: 'X', is_exclusive: true })]
    const opts = buildApiKeyGroupFilterOptions(groups, labels)
    expect(opts).toContainEqual({ value: 9, label: 'X' })
    expect(opts.find((o) => o.label === 'Exclusive')).toBeDefined()
    expect(opts.find((o) => o.label === 'Subscription')).toBeUndefined()
  })

  it('skips empty section headers', () => {
    const groups = [g({ id: 2, name: 'Pub', is_exclusive: false })]
    const opts = buildApiKeyGroupFilterOptions(groups, labels)
    expect(opts.find((o) => o.label === 'Exclusive')).toBeUndefined()
    expect(opts.find((o) => o.label === 'Subscription')).toBeUndefined()
    expect(opts).toContainEqual({ value: -2, label: 'Public', kind: 'group', disabled: true })
  })

  it('places non-active groups in a separate disabled section (not omitted)', () => {
    const groups = [
      g({ id: 1, name: 'Active', is_exclusive: true }),
      g({ id: 2, name: 'Inactive', is_exclusive: true, status: 'inactive' }),
    ]
    const opts = buildApiKeyGroupFilterOptions(groups, labels)
    // 启用的专属分组出现在专属分区。
    expect(opts).toContainEqual({ value: 1, label: 'Active' })
    // 禁用分组出现在禁用分区。
    expect(opts).toContainEqual({ value: 2, label: 'Inactive' })
    // 禁用分区标题存在。
    expect(opts).toContainEqual({ value: -3, label: 'Disabled', kind: 'group', disabled: true })
    // 禁用分组不会落在专属分区内。
    const exclIdx = opts.findIndex((o) => o.value === -1)
    const disabledItemIdx = opts.findIndex((o) => o.value === 2)
    expect(exclIdx).toBeLessThan(disabledItemIdx)
  })

  it('section headers use distinct negative values (no duplicate Vue :key)', () => {
    const groups = [
      g({ id: 1, name: 'E', is_exclusive: true }),
      g({ id: 2, name: 'P', is_exclusive: false }),
      g({ id: 4, name: 'D', status: 'inactive' }),
    ]
    const opts = buildApiKeyGroupFilterOptions(groups, labels)
    const headerValues = opts.filter((o) => o.kind === 'group').map((o) => o.value)
    const unique = new Set(headerValues)
    expect(unique.size).toBe(headerValues.length) // 标题值均不重复
    headerValues.forEach((v) => expect(v).toBeLessThan(0)) // 标题值均为负数
  })

  it('returns only the all-option when there are no groups', () => {
    expect(buildApiKeyGroupFilterOptions([], labels)).toEqual([{ value: null, label: 'All' }])
  })
})
