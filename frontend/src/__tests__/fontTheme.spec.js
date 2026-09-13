import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

import tailwindConfig from '../../tailwind.config.js'

const currentDir = dirname(fileURLToPath(import.meta.url))
const mainSource = readFileSync(resolve(currentDir, '../main.ts'), 'utf8')
const globalStyleSource = readFileSync(resolve(currentDir, '../style.css'), 'utf8')
const onboardingStyleSource = readFileSync(resolve(currentDir, '../styles/onboarding.css'), 'utf8')

// 固定全站英文字体与中文回退契约，避免局部样式重新使用旧系统字体。
describe('OpenRouter 字体主题', () => {
  const fontFamily = tailwindConfig.theme.extend.fontFamily

  it('使用 Plus Jakarta Sans 并保留中文系统字体回退', () => {
    expect(fontFamily.sans[0]).toBe('"Plus Jakarta Sans Variable"')
    expect(fontFamily.sans).toEqual(
      expect.arrayContaining(['PingFang SC', 'Hiragino Sans GB', 'Microsoft YaHei', 'sans-serif'])
    )
  })

  it('使用 Geist Mono 并保留系统等宽字体回退', () => {
    expect(fontFamily.mono[0]).toBe('"Geist Mono Variable"')
    expect(fontFamily.mono).toEqual(
      expect.arrayContaining(['ui-monospace', 'SFMono-Regular', 'Menlo', 'Monaco', 'Consolas', 'monospace'])
    )
  })

  it('在全局样式前加载正常体、斜体和等宽字体', () => {
    const sansImport = "import '@fontsource-variable/plus-jakarta-sans'"
    const sansItalicImport = "import '@fontsource-variable/plus-jakarta-sans/wght-italic.css'"
    const monoImport = "import '@fontsource-variable/geist-mono'"
    const styleImport = "import './style.css'"

    expect(mainSource).toContain(sansImport)
    expect(mainSource).toContain(sansItalicImport)
    expect(mainSource).toContain(monoImport)
    expect(mainSource.indexOf(sansImport)).toBeLessThan(mainSource.indexOf(styleImport))
    expect(mainSource.indexOf(sansItalicImport)).toBeLessThan(mainSource.indexOf(styleImport))
    expect(mainSource.indexOf(monoImport)).toBeLessThan(mainSource.indexOf(styleImport))
  })

  it('由根页面和引导浮层统一继承字体主题', () => {
    expect(globalStyleSource).toMatch(/html\s*\{[\s\S]*?@apply[^;]*font-sans[^;]*;/)
    expect(onboardingStyleSource).toContain('font-family: inherit !important;')
    expect(onboardingStyleSource).not.toContain('font-family: ui-sans-serif')
  })
})
