import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

function readSource(path: string): string {
  return readFileSync(resolve(path), 'utf8')
}

describe('admin platform filters', () => {
  it('uses the shared group catalog on groups and subscriptions pages', () => {
    for (const path of ['src/views/admin/GroupsView.vue', 'src/views/admin/SubscriptionsView.vue']) {
      const source = readSource(path)
      expect(source).toContain('GROUP_PLATFORM_OPTIONS')
    }
  })

  it('uses the concrete catalog for account and error filters', () => {
    for (const path of [
      'src/components/admin/account/AccountTableFilters.vue',
      'src/components/admin/ErrorPassthroughRulesModal.vue'
    ]) {
      const source = readSource(path)
      expect(source).toContain('CONCRETE_PLATFORM_OPTIONS')
    }
  })

  it('keeps the operations helper on the shared catalog while retaining dynamic platforms', () => {
    const source = readSource('src/views/admin/ops/platformOptions.ts')
    expect(source).toContain('CONCRETE_PLATFORM_OPTIONS')
    expect(source).toContain('knownPlatforms')
  })
})
