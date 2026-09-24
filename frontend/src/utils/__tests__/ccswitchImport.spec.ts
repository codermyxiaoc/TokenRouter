import { describe, expect, it } from 'vitest'
import {
  GROK_CC_SWITCH_MODEL,
  OPENAI_CC_SWITCH_CODEX_MODEL,
  buildCcSwitchImportDeeplink,
  buildCcSwitchUsageScript
} from '@/utils/ccswitchImport'
import type { GroupPlatform } from '@/types'

function paramsFromDeeplink(deeplink: string): URLSearchParams {
  const query = deeplink.split('?')[1] || ''
  return new URLSearchParams(query)
}

function decodeBase64Utf8(value: string): string {
  return new TextDecoder().decode(Uint8Array.from(atob(value), char => char.charCodeAt(0)))
}

describe('ccswitchImport utils', () => {
  it('defaults OpenAI CC Switch imports to the current Codex model', () => {
    expect(OPENAI_CC_SWITCH_CODEX_MODEL).toBe('gpt-5.6-sol')
  })

  it('defaults Grok Build imports to the current Grok model', () => {
    expect(GROK_CC_SWITCH_MODEL).toBe('grok-4.5')
  })

  it.each([
    'https://api.example.com',
    'https://api.example.com/',
    'https://api.example.com/v1',
    'https://api.example.com/v1/',
    'https://api.example.com/api/v1'
  ])('pins the usage script to /v1/usage for base URL %s', (baseUrl) => {
    const usageScript = buildCcSwitchUsageScript(baseUrl, 'USD')

    expect(usageScript).toContain('url: "https://api.example.com/v1/usage"')
    expect(usageScript).not.toContain('{{baseUrl}}')
    expect(usageScript).toContain('"Authorization": "Bearer {{apiKey}}"')
  })

  const baseInput = {
    baseUrl: 'https://api.example.com',
    providerName: 'Sub2API',
    apiKey: 'sk-test',
    usageScript: 'return true'
  }

  it('adds the Codex model parameter for OpenAI imports', () => {
    const params = paramsFromDeeplink(
      buildCcSwitchImportDeeplink({
        ...baseInput,
        platform: 'openai',
        clientType: 'claude'
      })
    )

    expect(params.get('resource')).toBe('provider')
    expect(params.get('app')).toBe('codex')
    expect(params.get('endpoint')).toBe(`${baseInput.baseUrl}/v1`)
    expect(params.get('model')).toBe('gpt-5.6-sol')
    expect(decodeBase64Utf8(params.get('usageScript') || '')).toBe(baseInput.usageScript)
  })

  it.each([
    'https://api.example.com',
    'https://api.example.com/',
    'https://api.example.com/v1',
    'https://api.example.com/v1/'
  ])('旧 OpenAI 导入仍生成单个 /v1 后缀：%s', (baseUrl) => {
    const params = paramsFromDeeplink(buildCcSwitchImportDeeplink({
      ...baseInput, baseUrl, platform: 'openai'
    }))
    expect(params.get('endpoint')).toBe('https://api.example.com/v1')
  })

  it.each([
    'https://api.example.com',
    'https://api.example.com/',
    'https://api.example.com/v1',
    'https://api.example.com/v1/'
  ])('imports Grok Build with one /v1 suffix for base URL %s', (baseUrl) => {
    const params = paramsFromDeeplink(
      buildCcSwitchImportDeeplink({
        ...baseInput,
        baseUrl,
        platform: 'grok',
        clientType: 'claude'
      })
    )

    expect(params.get('app')).toBe('grokbuild')
    expect(params.get('endpoint')).toBe('https://api.example.com/v1')
    expect(params.get('model')).toBe(GROK_CC_SWITCH_MODEL)
  })

  it.each([
    { platform: 'anthropic' as GroupPlatform, clientType: 'claude' as const, app: 'claude' },
    { platform: 'gemini' as GroupPlatform, clientType: 'gemini' as const, app: 'gemini' }
  ])('does not add a model parameter for $platform imports', ({ platform, clientType, app }) => {
    const params = paramsFromDeeplink(
      buildCcSwitchImportDeeplink({
        ...baseInput,
        platform,
        clientType
      })
    )

    expect(params.get('app')).toBe(app)
    expect(params.get('endpoint')).toBe(baseInput.baseUrl)
    expect(params.has('model')).toBe(false)
  })

  it('keeps Antigravity imports on the selected client endpoint without a model parameter', () => {
    const params = paramsFromDeeplink(
      buildCcSwitchImportDeeplink({
        ...baseInput,
        platform: 'antigravity',
        clientType: 'gemini'
      })
    )

    expect(params.get('app')).toBe('gemini')
    expect(params.get('endpoint')).toBe(`${baseInput.baseUrl}/antigravity`)
    expect(params.has('model')).toBe(false)
  })

  it('uses the Anthropic root endpoint and preserves UTF-8 usage scripts', () => {
    const usageScript = 'return "余额"'
    const params = paramsFromDeeplink(
      buildCcSwitchImportDeeplink({
        ...baseInput,
        baseUrl: 'https://api.example.com/v1',
        platform: 'anthropic',
        clientType: 'claude',
        usageScript
      })
    )

    expect(params.get('endpoint')).toBe('https://api.example.com')
    expect(decodeBase64Utf8(params.get('usageScript') || '')).toBe(usageScript)
  })

  it.each([
    { platform: 'openai' as const, app: 'claude' as const, endpoint: 'https://api.example.com' },
    { platform: 'gemini' as const, app: 'codex' as const, endpoint: 'https://api.example.com/v1' },
    { platform: 'anthropic' as const, app: 'gemini' as const, endpoint: 'https://api.example.com' },
    { platform: 'grok' as const, app: 'claude' as const, endpoint: 'https://api.example.com' }
  ])('显式选择 $app 优先于 $platform 平台推导', ({ platform, app, endpoint }) => {
    const params = paramsFromDeeplink(buildCcSwitchImportDeeplink({
      ...baseInput,
      platform,
      clientType: 'gemini',
      app,
      model: '自定义模型-1'
    }))

    expect(params.get('app')).toBe(app)
    expect(params.get('endpoint')).toBe(endpoint)
    expect(params.get('model')).toBe('自定义模型-1')
  })

  it.each([
    'https://api.example.com',
    'https://api.example.com/',
    'https://api.example.com/v1',
    'https://api.example.com/v1/'
  ])('显式应用为地址 %s 选择正确的版本后缀', (baseUrl) => {
    for (const app of ['claude', 'codex', 'gemini'] as const) {
      const params = paramsFromDeeplink(buildCcSwitchImportDeeplink({
        ...baseInput,
        baseUrl,
        app,
        model: 'selected-model'
      }))

      expect(params.get('endpoint')).toBe(`https://api.example.com${app === 'codex' ? '/v1' : ''}`)
    }
  })

  it.each(['claude', 'gemini'] as const)('显式 %s 保留调用方提供的专用路由', (app) => {
    const params = paramsFromDeeplink(buildCcSwitchImportDeeplink({
      ...baseInput,
      baseUrl: 'https://api.example.com/proxy/antigravity/v1/',
      platform: 'antigravity',
      app,
      model: 'selected-model'
    }))

    expect(params.get('endpoint')).toBe('https://api.example.com/proxy/antigravity')
  })

  it.each(['claude', 'codex', 'gemini'] as const)('智能路由显式 %s 保留公共入口且不推导专用路由', (app) => {
    const params = paramsFromDeeplink(buildCcSwitchImportDeeplink({
      ...baseInput,
      baseUrl: 'https://api.example.com/proxy/v1/',
      platform: 'antigravity',
      app,
      model: 'selected-model'
    }))

    expect(params.get('endpoint')).toBe(`https://api.example.com/proxy${app === 'codex' ? '/v1' : ''}`)
    expect(params.get('endpoint')).not.toContain('/antigravity')
  })

  it('使用官方 Claude 模型参数并完整编码自定义值', () => {
    const params = paramsFromDeeplink(buildCcSwitchImportDeeplink({
      ...baseInput,
      app: 'claude',
      providerName: '我的供应商 & 测试',
      apiKey: 'sk-test+/?=&',
      model: '  主模型/别名+1  ',
      haikuModel: '  quick-model  ',
      sonnetModel: 'balanced-model',
      opusModel: 'heavy-model'
    }))

    expect(params.get('name')).toBe('我的供应商 & 测试')
    expect(params.get('apiKey')).toBe('sk-test+/?=&')
    expect(params.get('model')).toBe('主模型/别名+1')
    expect(params.get('haikuModel')).toBe('quick-model')
    expect(params.get('sonnetModel')).toBe('balanced-model')
    expect(params.get('opusModel')).toBe('heavy-model')
    expect(params.has('haiku_model')).toBe(false)
  })

  it.each(['codex', 'gemini'] as const)('显式 %s 只写入主模型，不携带 Claude 专用模型', (app) => {
    const params = paramsFromDeeplink(buildCcSwitchImportDeeplink({
      ...baseInput,
      app,
      model: 'selected-model',
      haikuModel: 'quick-model',
      sonnetModel: 'balanced-model',
      opusModel: 'heavy-model'
    }))

    expect(params.get('model')).toBe('selected-model')
    expect(params.has('haikuModel')).toBe(false)
    expect(params.has('sonnetModel')).toBe(false)
    expect(params.has('opusModel')).toBe(false)
  })

  it('省略空白可选模型，不把空值写入 Claude 配置', () => {
    const params = paramsFromDeeplink(buildCcSwitchImportDeeplink({
      ...baseInput,
      app: 'claude',
      model: 'selected-model',
      haikuModel: '   ',
      sonnetModel: '',
      opusModel: undefined
    }))

    expect(params.get('model')).toBe('selected-model')
    expect(params.has('haikuModel')).toBe(false)
    expect(params.has('sonnetModel')).toBe(false)
    expect(params.has('opusModel')).toBe(false)
  })

  it('旧平台调用允许显式主模型覆盖原默认值', () => {
    const params = paramsFromDeeplink(buildCcSwitchImportDeeplink({
      ...baseInput,
      platform: 'openai',
      clientType: 'claude',
      model: 'custom-codex-model'
    }))

    expect(params.get('app')).toBe('codex')
    expect(params.get('model')).toBe('custom-codex-model')
  })
})
