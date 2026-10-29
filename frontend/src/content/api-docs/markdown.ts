import { apiDocCategories, apiDocEndpoints, commonApiErrors } from './index'
import { apiDocAuthExample, apiDocEnvironment, apiDocMarkdown } from './format'
import type { ApiDocEndpoint } from './types'

// API 文档统一以 Markdown 作为正文：内置文档由下面的函数生成，管理员可在系统设置中逐篇改写。
// 改写正文中的 {{BASE_URL}} 会替换为当前站点的网关地址。
// @project-doc docs/interfaces/api_reference.md#api_docs_markdown

/** 文档目录项：指南与接口共用同一 ID 空间（即 /docs/:id）。 */
export interface ApiDocDocument {
  id: string
  title: string
  group: string
}

export const API_DOC_GUIDES = [
  { id: 'quickstart', title: '快速开始' },
  { id: 'auth', title: '认证' },
  { id: 'overview', title: '接口总览' },
  { id: 'errors', title: '错误处理' },
] as const

const modelListExample = 'curl "$BASE_URL/v1/models" \\\n  -H "Authorization: Bearer $TOKENROUTER_API_KEY"'

function quickstartMarkdown(baseUrl: string): string {
  return [
    '# 从一个端点开始接入',
    '在同一站点接入对话、图片、视频、语音及原生厂商协议。先选择 Key 对应的平台和模型，再从目录找到匹配的接口。所有示例仅用于展示请求结构。',
    '1. **创建 API Key**：选择可用分组和扣费方式，保存站内密钥。\n2. **核对模型与协议**：查询可见模型，确认分组已启用目标请求协议。\n3. **提交并读取结果**：文本解析普通响应或 SSE；异步任务保存 ID 后轮询。',
    '## 1. 配置站点地址与密钥',
    '```bash\n' + apiDocEnvironment(baseUrl) + '\n```',
    '## 2. 查询当前 Key 可见的模型',
    '```bash\n' + modelListExample + '\n```',
    '## 3. 选择协议',
    apiDocCategories.map(category => {
      const first = apiDocEndpoints.find(endpoint => endpoint.category === category.id)
      return `- [${category.label}](/docs/${first?.id ?? 'overview'})：${category.description}`
    }).join('\n'),
    '视频统一 URL 只统一请求入口，参数仍按账号绑定的上游协议解释；原生视频参数无需改成聊天格式。异步图片和视频请使用对应的任务查询接口，长时间生成不需要保持同一个 HTTP 连接。',
    '[查看模型广场](/models)',
  ].join('\n\n')
}

function authMarkdown(): string {
  return [
    '# 认证',
    '使用本站创建的 API Key，而不是登录令牌或上游厂商密钥。常规接口使用 Bearer；Anthropic 和 Gemini 原生入口也支持各自的认证头。',
    '**OpenAI / Video / 通用**\n\n```http\n' + apiDocAuthExample() + '\n```',
    '**Anthropic Messages**\n\n```http\nx-api-key: $TOKENROUTER_API_KEY\nanthropic-version: 2023-06-01\n```',
    '**Gemini 原生**\n\n```http\nx-goog-api-key: $TOKENROUTER_API_KEY\n```',
    '## 接入约定',
    [
      '示例中的 BASE_URL 是网关根地址，路径已包含 /v1 或厂商前缀，不要重复拼接 /v1。模型 ID 请以分组模型广场或当前 Key 的模型目录为准。',
      '普通 Key 按绑定分组调用；复合 Key 的模型名称可能包含分组前缀，请使用模型目录返回的名称。客户端协议是否开放与上游平台是两个不同条件。',
      'JSON 请求使用 Content-Type: application/json；文件上传使用 multipart/form-data 并由客户端自动生成 boundary。WebSocket、SDP、音频和文件响应见对应接口。',
      '密钥仅保存在服务端环境变量中，不要放进公开网页、示例仓库或浏览器 URL。文档页面不会读取你的密钥，也不会自动发送计费请求。',
      '通用网关拒绝 URL 中的非空 key / api_key 参数；仅 /v1beta 和 /antigravity/v1beta 为 Gemini SDK 兼容 ?key=，仍拒绝 ?api_key=。优先使用请求头，避免密钥出现在 URL 日志中。',
      '/v1、/v1beta 等网关接口允许浏览器跨域调用（不携带 Cookie），但请勿在公开网页中暴露密钥。',
      '网关响应遵循对应 AI 协议，不套用管理面板的统一 data envelope。SSE 必须完整解析事件；HTTP 200 并不代表流中没有错误。',
      '排查时保留响应头 X-Request-ID、X-Client-Request-ID 或 X-Sub2API-Request-ID。不要把密钥和完整隐私内容放到公开错误报告。',
    ].map(item => `- ${item}`).join('\n'),
  ].join('\n\n')
}

/** 接口总览表；文档页传入筛选后的接口，表格随目录搜索同步。 */
export function overviewMarkdown(endpoints: ApiDocEndpoint[] = apiDocEndpoints, emptyText = ''): string {
  const cell = (value: string) => value.replace(/\|/g, '\\|')
  return [
    '# 接口总览',
    '使用左侧搜索和平台筛选缩小范围；点击接口查看完整调用说明。不同平台支持的协议以分组配置为准。',
    endpoints.length
      ? '| 方法 | 路径 | 说明 |\n| --- | --- | --- |\n' + endpoints
        .map(endpoint => `| ${endpoint.method} | [\`${cell(endpoint.path)}\`](/docs/${endpoint.id}) | ${cell(endpoint.title)} |`).join('\n')
      : emptyText,
  ].join('\n\n')
}

function errorsMarkdown(): string {
  return [
    '# 错误处理',
    '不同协议、上游以及站点错误策略的状态码和错误字段可能不同，请同时检查 HTTP 状态、响应正文和流式终止事件。以下是排查方向，不是强制统一的错误格式。',
    ...commonApiErrors.map(error => `### ${error.status} · ${error.code}\n\n${error.description}`),
    '## 异步任务没有立即返回结果',
    '提交成功后保存任务 ID，使用原 Key 轮询对应平台的查询接口。queued / processing / in_progress 等中间态不代表失败。创建返回错误且没有任务 ID 的独立 Video 任务会按当前规则释放预留；网络中断等无法确认的情况可能进入待核对，不能根据一次超时直接判断退款结果。失败或取消后的额度以任务记录中的结算状态为准。',
  ].join('\n\n')
}

/** 返回内置的默认 Markdown；未知 ID 返回 null。 */
export function defaultApiDocMarkdown(id: string, baseUrl: string): string | null {
  switch (id) {
    case 'quickstart': return quickstartMarkdown(baseUrl)
    case 'auth': return authMarkdown()
    case 'overview': return overviewMarkdown()
    case 'errors': return errorsMarkdown()
  }
  const endpoint = apiDocEndpoints.find(item => item.id === id)
  return endpoint ? apiDocMarkdown(endpoint, baseUrl) : null
}

/** 实际展示的 Markdown：优先使用管理员改写，并替换 {{BASE_URL}} 占位符。 */
export function effectiveApiDocMarkdown(id: string, baseUrl: string, overrides: Record<string, string>): string | null {
  const custom = overrides[id]
  if (custom && custom.trim()) return custom.split('{{BASE_URL}}').join(baseUrl)
  return defaultApiDocMarkdown(id, baseUrl)
}

/** 全部可编辑文档，按“指南 / 分类”分组，供系统设置选择。 */
export function apiDocDocuments(): ApiDocDocument[] {
  const guides = API_DOC_GUIDES.map(guide => ({ id: guide.id, title: guide.title, group: '指南' }))
  const endpoints = apiDocEndpoints.map(endpoint => ({
    id: endpoint.id,
    title: `${endpoint.method} ${endpoint.path} · ${endpoint.title}`,
    group: apiDocCategories.find(category => category.id === endpoint.category)?.label ?? '',
  }))
  return [...guides, ...endpoints]
}

/** 提取二级标题作为页面大纲，忽略代码块中的 # 行。 */
export function apiDocOutline(markdown: string): string[] {
  const headings: string[] = []
  let fenced = false
  for (const line of markdown.split('\n')) {
    if (/^\s*(```|~~~)/.test(line)) fenced = !fenced
    else if (!fenced && /^##\s+/.test(line)) headings.push(line.replace(/^##\s+/, '').trim())
  }
  return headings
}
