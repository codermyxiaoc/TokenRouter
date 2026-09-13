import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

const viewPath = resolve(dirname(fileURLToPath(import.meta.url)), '../RedeemView.vue')
const viewSource = readFileSync(viewPath, 'utf8')

describe('RedeemView responsive layout', () => {
  it('places recent activity beside the primary content on wide screens', () => {
    // 两栏仅在宽屏启用，确保移动端仍保持自然的单列阅读顺序。
    expect(viewSource).toContain(
      'max-w-6xl gap-6 xl:grid-cols-[minmax(0,1fr)_minmax(360px,0.8fr)] xl:items-start'
    )
    expect(viewSource).toContain('data-testid="redeem-primary-column" class="min-w-0 space-y-6"')
    expect(viewSource).toContain('data-testid="redeem-activity-column" class="card min-w-0"')
  })

  it('omits supplementary redeem guidance', () => {
    // 页面只保留兑换操作与历史，避免再次加入重复说明或大小写提示。
    expect(viewSource).not.toContain("t('redeem.redeemCodeHint')")
    expect(viewSource).not.toContain("t('redeem.aboutCodes')")
    expect(viewSource).not.toContain('contactInfo')
    expect(viewSource).not.toContain('authAPI')
  })
})
