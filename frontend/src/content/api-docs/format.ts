import type { ApiDocEndpoint } from './types'
import { platformLabel } from './index'

// 示例只引用服务端环境变量，复制文档不会读取或泄露当前用户的任何密钥。
export function apiDocAuthExample(endpoint?: ApiDocEndpoint): string {
  if (endpoint?.auth === 'google') return 'x-goog-api-key: $TOKENROUTER_API_KEY'
  if (endpoint?.auth === 'anthropic') return 'x-api-key: $TOKENROUTER_API_KEY\nanthropic-version: 2023-06-01'
  return 'Authorization: Bearer $TOKENROUTER_API_KEY'
}

function shellQuote(value: string): string {
  return "'" + value.replace(/'/g, "'\"'\"'") + "'"
}

export function apiDocBaseUrl(configured: string, origin: string): string {
  try {
    const parsed = new URL(configured || origin)
    if (!['http:', 'https:'].includes(parsed.protocol) || parsed.username || parsed.password) return origin
    return parsed.origin + parsed.pathname.replace(/\/+$/, '').replace(/\/v1$/, '')
  } catch {
    return origin
  }
}

export function apiDocEnvironment(baseUrl: string): string {
  return `export BASE_URL=${shellQuote(baseUrl)}\nexport TOKENROUTER_API_KEY='sk-your-api-key'`
}

export function apiDocCurl(endpoint: ApiDocEndpoint): string {
  if (endpoint.requestLanguage === 'bash' || endpoint.requestLanguage === 'shell') return endpoint.requestExample ?? ''
  if (endpoint.method === 'WS') return `wss://YOUR_GATEWAY_HOST${endpoint.requestPath ?? endpoint.path}\n${apiDocAuthExample(endpoint)}\n\n${endpoint.requestExample ?? ''}`
  const headers = apiDocAuthExample(endpoint).split('\n').map(header => `  -H "${header}"`)
  const hasContentType = Object.keys(endpoint.requestHeaders ?? {}).some(name => name.toLowerCase() === 'content-type')
  for (const [name, value] of Object.entries(endpoint.requestHeaders ?? {})) headers.push(`  -H ${shellQuote(`${name}: ${value}`)}`)
  const lines = [`curl --globoff -X ${endpoint.method} "$BASE_URL${endpoint.requestPath ?? endpoint.path}"`, ...headers]
  if (endpoint.requestExample && (!endpoint.contentType || endpoint.contentType.includes('json'))) {
    if (!hasContentType) lines.push('  -H "Content-Type: application/json"')
    lines.push(`  --data-raw ${shellQuote(endpoint.requestExample)}`)
  } else if (endpoint.requestExample) {
    // 非 JSON 请求保持原生字段示例，避免把 multipart 或 SDP 包装成 JSON。
    if (!hasContentType) lines.push(`  -H "Content-Type: ${endpoint.contentType}"`)
    lines.push('  --data-binary @request-body')
  }
  return lines.join(' \\\n')
}

export function apiDocMarkdown(endpoint: ApiDocEndpoint, baseUrl: string): string {
  const cell = (value: string) => value.replace(/\|/g, '\\|').replace(/\n/g, ' ')
  const parts = [
    `# ${endpoint.title}`, endpoint.summary,
    `**${endpoint.method}** \`${endpoint.path}\``,
    `适用平台：${endpoint.platforms.map(platformLabel).join('、')}`,
    '## 鉴权', '```text\n' + apiDocAuthExample(endpoint) + '\n```',
    '## 环境变量', '```bash\n' + apiDocEnvironment(baseUrl) + '\n```',
    '## 请求参数', '| 参数 | 类型 | 必填 | 说明 |\n| --- | --- | --- | --- |',
    ...endpoint.parameters.map(p => `| ${cell(p.name)} | ${cell(p.type)} | ${p.required === true ? '是' : p.required === 'conditional' ? '条件必填' : '否'} | ${cell(p.description)} |`),
    '## 请求示例', '```' + (endpoint.method === 'WS' ? 'text' : 'bash') + '\n' + apiDocCurl(endpoint) + '\n```',
    ...(endpoint.requestExample && endpoint.requestLanguage !== 'bash' && endpoint.requestLanguage !== 'shell' && endpoint.method !== 'WS'
      ? ['```' + (endpoint.requestLanguage ?? 'json') + '\n' + endpoint.requestExample + '\n```'] : []),
    '## 响应示例', endpoint.responseDescription ?? '',
    '```' + (endpoint.responseLanguage ?? 'json') + '\n' + endpoint.responseExample + '\n```',
  ]
  if (endpoint.aliases?.length) parts.push('## 兼容路径', ...endpoint.aliases.map(path => `- \`${path}\``))
  if (endpoint.notes?.length) parts.push('## 使用说明', ...endpoint.notes.map(note => `- ${note}`))
  if (endpoint.errors?.length) parts.push('## 常见错误', ...endpoint.errors.map(error => `- ${error.status} ${error.code}：${error.description}`))
  return parts.filter(Boolean).join('\n\n')
}
