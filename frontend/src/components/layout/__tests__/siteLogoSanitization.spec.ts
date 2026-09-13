import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

const dir = dirname(fileURLToPath(import.meta.url))
const headerSource = readFileSync(resolve(dir, '../AppHeader.vue'), 'utf8')
const homeViewSource = readFileSync(resolve(dir, '../../../views/HomeView.vue'), 'utf8')
const keyUsageViewSource = readFileSync(resolve(dir, '../../../views/KeyUsageView.vue'), 'utf8')
const marketplaceViewSource = readFileSync(resolve(dir, '../../../views/ModelMarketplaceView.vue'), 'utf8')
const appSource = readFileSync(resolve(dir, '../../../App.vue'), 'utf8')

describe('site_logo sanitization', () => {
  it('AppHeader imports sanitizeUrl and applies it to siteLogo', () => {
    expect(headerSource).toContain("import { sanitizeUrl } from '@/utils/url'")
    expect(headerSource).toContain('sanitizeUrl(appStore.siteLogo')
  })

  it('HomeView applies sanitizeUrl to siteLogo', () => {
    expect(homeViewSource).toContain('sanitizeUrl(appStore.cachedPublicSettings?.site_logo || appStore.siteLogo')
  })

  it('KeyUsageView applies sanitizeUrl to siteLogo', () => {
    expect(keyUsageViewSource).toContain('sanitizeUrl(appStore.cachedPublicSettings?.site_logo || appStore.siteLogo')
  })

  it('ModelMarketplaceView applies sanitizeUrl to siteLogo', () => {
    expect(marketplaceViewSource).toContain('sanitizeUrl(appStore.cachedPublicSettings?.site_logo || appStore.siteLogo')
  })

  it('App sanitizes the favicon URL', () => {
    expect(appSource).toContain('sanitizeUrl(appStore.siteLogo')
  })

  it('all entries pass allowRelative and allowDataUrl options', () => {
    for (const src of [headerSource, homeViewSource, keyUsageViewSource, marketplaceViewSource, appSource]) {
      expect(src).toContain('allowRelative: true')
      expect(src).toContain('allowDataUrl: true')
    }
  })
})
