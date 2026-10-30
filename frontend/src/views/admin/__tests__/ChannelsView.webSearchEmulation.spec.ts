import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

/**
 * 渠道编辑器原先只渲染、提交 anthropic 维度，界面保存会抹掉通过 API 写入的其它平台维度。
 * 渲染与提交都改为按平台白名单，白名单与后端账号级白名单（anthropic、openai）一致。
 */
describe('ChannelsView web_search_emulation 平台白名单', () => {
  const source = readFileSync(resolve('src/views/admin/ChannelsView.vue'), 'utf8')
  const zh = readFileSync(resolve('src/i18n/locales/zh/admin/channels.ts'), 'utf8')
  const en = readFileSync(resolve('src/i18n/locales/en/admin/channels.ts'), 'utf8')

  it('声明与后端一致的白名单', () => {
    expect(source).toContain("const WEB_SEARCH_EMULATION_PLATFORMS: ReadonlySet<string> = new Set(['anthropic', 'openai'])")
  })

  it('白名单内的平台都显示开关', () => {
    expect(source).toContain('WEB_SEARCH_EMULATION_PLATFORMS.has(section.platform) && webSearchGlobalEnabled')
    expect(source).not.toContain("section.platform === 'anthropic' && webSearchGlobalEnabled")
  })

  it('保存时逐个提交白名单平台维度', () => {
    const block = source.match(/const wsEmulation[\s\S]*?delete featuresConfig\.web_search_emulation/)?.[0] ?? ''
    expect(block).toContain('WEB_SEARCH_EMULATION_PLATFORMS.has(section.platform)')
    expect(block).toContain('wsEmulation[section.platform] = section.enabled ? !!section.web_search_emulation : false')
    expect(block).not.toContain("'anthropic'")
  })

  it('提示文案不再只写 Anthropic', () => {
    expect(zh).not.toContain('所有 Anthropic 分组的账号将自动拦截')
    expect(en).not.toContain('Anthropic groups will intercept')
  })
})
