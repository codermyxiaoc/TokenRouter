import type { GroupPlatform } from '@/types'
import { OPENAI_CODEX_DEFAULT_MODEL } from '@/constants/openai'

export const OPENAI_CC_SWITCH_CODEX_MODEL = OPENAI_CODEX_DEFAULT_MODEL
export const GROK_CC_SWITCH_MODEL = 'grok-4.5'

export type CcSwitchApp = 'claude' | 'codex' | 'gemini'
export type CcSwitchClientType = 'claude' | 'gemini'

// 导入弹窗确认后提供完整选择；旧深链接调用仍可省略新增字段。
export interface CcSwitchImportSelection {
  app: CcSwitchApp
  providerName: string
  model: string
  haikuModel?: string
  sonnetModel?: string
  opusModel?: string
}

export interface CcSwitchImportConfig {
  app: string
  endpoint: string
  model?: string
}

export interface CcSwitchImportDeeplinkInput {
  baseUrl: string
  platform?: GroupPlatform | null
  // 显式选择优先于平台推导；调用方负责专用路由或智能路由的入口选择。
  app?: CcSwitchApp
  clientType?: CcSwitchClientType
  providerName: string
  apiKey: string
  usageScript: string
  model?: string
  haikuModel?: string
  sonnetModel?: string
  opusModel?: string
}

const encodeBase64Utf8 = (value: string) => {
  const bytes = new TextEncoder().encode(value)
  let binary = ''
  const chunkSize = 0x8000

  for (let i = 0; i < bytes.length; i += chunkSize) {
    binary += String.fromCharCode(...bytes.subarray(i, i + chunkSize))
  }

  return btoa(binary)
}

const stripTrailingV1 = (value: string) => value.replace(/\/+$/, '').replace(/\/v1$/, '')

// 生成可安全嵌入 CCS JavaScript 模板的字符串字面量。
const toJsStringLiteral = (value: string) =>
  JSON.stringify(value || 'USD')
    .replace(/\u2028/g, '\\u2028')
    .replace(/\u2029/g, '\\u2029')

// 用量接口固定在 API 域名根路径；无效旧配置回退到兼容的后缀规范化。
const resolveCcSwitchUsageUrl = (baseUrl: string): string => {
  try {
    return `${new URL(baseUrl).origin}/v1/usage`
  } catch {
    return `${stripTrailingV1(baseUrl)}/v1/usage`
  }
}

// 固化导入时的用量端点，避免 CCS 中的 provider endpoint 被修改后影响用量统计。
export function buildCcSwitchUsageScript(baseUrl: string, fallbackUnit: string): string {
  const usageUrlLiteral = toJsStringLiteral(resolveCcSwitchUsageUrl(baseUrl))
  const fallbackUnitLiteral = toJsStringLiteral(fallbackUnit)

  return `({
    request: {
      url: ${usageUrlLiteral},
      method: "GET",
      headers: { "Authorization": "Bearer {{apiKey}}" }
    },
    extractor: function(response) {
      const remaining = response?.remaining ?? response?.quota?.remaining ?? response?.balance;
      const unit = response?.unit ?? response?.quota?.unit ?? ${fallbackUnitLiteral};
      return {
        isValid: response?.is_active ?? response?.isValid ?? true,
        remaining,
        unit
      };
    }
  })`
}

// 将 Codex 或旧 Grok Build 端点规范为单个 /v1 后缀。
function withV1Endpoint(baseUrl: string): string {
  const normalizedBaseUrl = baseUrl.replace(/\/+$/, '')
  return normalizedBaseUrl.endsWith('/v1') ? normalizedBaseUrl : `${normalizedBaseUrl}/v1`
}

export function resolveCcSwitchImportConfig(
  platform: GroupPlatform | undefined | null,
  clientType: CcSwitchClientType,
  baseUrl: string,
  app?: CcSwitchApp
): CcSwitchImportConfig {
  if (app) {
    // 显式导入只按客户端规范化版本后缀，保留调用方选好的部署路径及专用入口。
    const rootEndpoint = stripTrailingV1(baseUrl.trim())
    return {
      app,
      endpoint: app === 'codex' ? withV1Endpoint(rootEndpoint) : rootEndpoint
    }
  }

  switch (platform || 'anthropic') {
    case 'antigravity':
      return {
        app: clientType === 'gemini' ? 'gemini' : 'claude',
        endpoint: `${baseUrl.trim().replace(/\/+$/, '')}/antigravity`
      }
    case 'openai':
      return {
        app: 'codex',
        endpoint: withV1Endpoint(baseUrl.trim()),
        model: OPENAI_CC_SWITCH_CODEX_MODEL
      }
    case 'gemini':
      return {
        app: 'gemini',
        endpoint: baseUrl
      }
    case 'grok':
      return {
        app: 'grokbuild',
        endpoint: withV1Endpoint(baseUrl),
        model: GROK_CC_SWITCH_MODEL
      }
    default:
      return {
        app: 'claude',
        endpoint: stripTrailingV1(baseUrl)
      }
  }
}

export function buildCcSwitchImportDeeplink(input: CcSwitchImportDeeplinkInput): string {
  const config = resolveCcSwitchImportConfig(input.platform, input.clientType ?? 'claude', input.baseUrl, input.app)
  const entries: [string, string][] = [
    ['resource', 'provider'],
    ['app', config.app],
    ['name', input.providerName],
    ['homepage', input.baseUrl],
    ['endpoint', config.endpoint],
    ['apiKey', input.apiKey],
    ['configFormat', 'json'],
    ['usageEnabled', 'true'],
    ['usageScript', encodeBase64Utf8(input.usageScript)],
    ['usageAutoInterval', '30']
  ]

  // 新表单显式选择模型，旧调用未提供时继续沿用原平台默认值。
  const model = input.model === undefined ? config.model : input.model.trim()
  if (model) {
    entries.splice(2, 0, ['model', model])
  }

  // 官方 v1/import 仅在 Claude 配置中消费这三个模型参数。
  if (config.app === 'claude') {
    for (const field of ['haikuModel', 'sonnetModel', 'opusModel'] as const) {
      const value = input[field]?.trim()
      if (value) entries.push([field, value])
    }
  }

  return `ccswitch://v1/import?${new URLSearchParams(entries).toString()}`
}
