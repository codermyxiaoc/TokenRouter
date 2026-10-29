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
  // 表格单元格内的竖线与换行会破坏 Markdown 表格结构，需转义或折叠。
  const cell = (value: string) => value.replace(/\|/g, '\\|').replace(/\r?\n/g, ' ')
  // 表头与所有数据行必须连续成一个块，中间不能出现空行，否则表格会被拆散。
  const parameterTable = endpoint.parameters.length
    ? ['| 参数 | 类型 | 必填 | 说明 |', '| --- | --- | --- | --- |',
      ...endpoint.parameters.map(p => `| \`${cell(p.name)}\` | ${cell(p.type)} | ${p.required === true ? '是' : p.required === 'conditional' ? '条件必填' : '否'} | ${cell(p.description)} |`)].join('\n')
    : '无请求参数。'
  const parts = [
    `# ${endpoint.title}`, endpoint.summary,
    `**${endpoint.method}** \`${endpoint.path}\``,
    `适用平台：${endpoint.platforms.map(platformLabel).join('、')}`,
    '## 鉴权', apiDocFence(apiDocAuthExample(endpoint), 'http'),
    '## 环境变量', apiDocFence(apiDocEnvironment(baseUrl), 'bash'),
    '## 请求参数',
    ...(endpoint.contentType ? [`Content-Type: \`${endpoint.contentType}\``] : []),
    parameterTable,
    '## 请求示例', apiDocFence(apiDocCurl(endpoint), endpoint.method === 'WS' ? 'text' : 'bash'),
    ...(endpoint.requestExample && endpoint.requestLanguage !== 'bash' && endpoint.requestLanguage !== 'shell' && endpoint.method !== 'WS'
      ? [apiDocFence(endpoint.requestExample, endpoint.requestLanguage ?? 'json')] : []),
    '## 响应示例', endpoint.responseDescription ?? '',
    apiDocFence(endpoint.responseExample, endpoint.responseLanguage ?? 'json'),
  ]
  // 列表项之间不留空行，保持为同一个列表；项内换行按 Markdown 续行缩进，不改写原文。
  const listItem = (text: string) => `- ${text.replace(/\r?\n/g, '\n  ')}`
  if (endpoint.aliases?.length) parts.push('## 兼容路径', endpoint.aliases.map(path => `- \`${path}\``).join('\n'))
  if (endpoint.notes?.length) parts.push('## 使用说明', endpoint.notes.map(listItem).join('\n'))
  if (endpoint.errors?.length) parts.push('## 常见错误', endpoint.errors.map(error => listItem(`**${error.status} ${error.code}**：${error.description}`)).join('\n'))
  return parts.filter(Boolean).join('\n\n')
}

// 代码块语言标识只能是单个词，“cURL · Bash”“HTTP headers”等展示名称会让围栏信息串失效。
function apiDocFenceLanguage(language: string): string {
  const normalized = language.trim().toLowerCase().split(/[\s·/]+/)[0] || 'text'
  return /^[a-z0-9_+-]+$/.test(normalized) ? normalized : 'text'
}

/** 生成 Markdown 代码块；正文含 ``` 时自动加长围栏，避免示例提前结束代码块。 */
export function apiDocFence(code: string, language = 'text'): string {
  const longest = Math.max(2, ...(code.match(/`+/g) ?? []).map(run => run.length))
  const fence = '`'.repeat(longest + 1)
  return `${fence}${apiDocFenceLanguage(language)}\n${code}\n${fence}`
}
