import { CONCRETE_PLATFORM_OPTIONS } from '@/constants/platforms'
import type { ApiDocEndpoint, ApiDocVideoProtocol } from './types'
import { textEndpoints } from './text'
import { mediaEndpoints } from './media'
import { videoEndpoints } from './video'

// 分类与正文分离，新增网关能力时无需修改页面渲染逻辑。
export const apiDocCategories = [
  { id: 'text', label: '对话与生成', description: 'Messages、Responses、Chat 与 Gemini 原生协议' },
  { id: 'models', label: '模型与用量', description: '可见模型目录与 Key 用量查询' },
  { id: 'images', label: '图片与批量任务', description: '生成、编辑、异步图片和批量图片' },
  { id: 'video', label: '视频与异步任务', description: '统一视频入口与厂商原生视频协议' },
  { id: 'audio', label: '语音与实时会话', description: '语音合成、识别和实时交互' },
  { id: 'tools', label: '搜索与扩展', description: '搜索工具及平台专属扩展能力' },
] as const

// 统一入口与原生端点分别列出，不以账号平台推断厂商协议。
export const apiDocVideoProtocols = [
  { id: 'openai', label: 'OpenAI Videos' },
  { id: 'seedance', label: 'Seedance / 火山方舟' },
  { id: 'kling', label: 'Kling / 可灵' },
  { id: 'wan', label: 'Wan / 万相' },
  { id: 'minimax', label: 'MiniMax' },
  { id: 'grok', label: 'Grok / xAI' },
] as const satisfies ReadonlyArray<{ id: ApiDocVideoProtocol; label: string }>

export function videoProtocolLabel(id?: ApiDocVideoProtocol): string {
  return apiDocVideoProtocols.find(protocol => protocol.id === id)?.label ?? ''
}

export const apiDocEndpoints: ApiDocEndpoint[] = [...textEndpoints, ...mediaEndpoints, ...videoEndpoints]
export const apiDocPlatforms = CONCRETE_PLATFORM_OPTIONS.map(item => ({
  ...item,
  label: item.value === 'opencode_go' ? 'OpenCode Zen / GO' : item.label,
}))

export function platformLabel(id: string): string {
  return apiDocPlatforms.find(item => item.value === id)?.label ?? id
}

export function filterApiDocEndpoints(search: string, platform: string | null): ApiDocEndpoint[] {
  const terms = search.trim().toLocaleLowerCase().split(/\s+/).filter(Boolean)
  return apiDocEndpoints.filter(endpoint => {
    if (platform && !endpoint.platforms.includes(platform)) return false
    const searchable = [endpoint.title, endpoint.summary, endpoint.method, endpoint.path, videoProtocolLabel(endpoint.videoProtocol),
      ...(endpoint.aliases ?? []), ...endpoint.platforms.map(platformLabel),
      ...endpoint.parameters.map(parameter => parameter.name)].join(' ').toLocaleLowerCase()
    return terms.every(term => searchable.includes(term))
  })
}

export const commonApiErrors = [
  { status: '400', code: 'invalid_request_error / invalid_request', description: '检查 JSON、必填字段、模型 ID、参数类型与当前协议；厂商原生请求需要对应的原生结构。' },
  { status: '401', code: 'authentication_error', description: '检查站内 API Key 是否有效，是否正确携带认证头；站内 Key 与上游密钥不能互换。' },
  { status: '402 / 403', code: '余额、配额或权限不足', description: '检查余额、订阅、Key 配额、分组平台、模型权限和已开启的客户端协议；具体状态码依接口而定。' },
  { status: '404', code: 'not_found', description: '核对路由、模型或任务 ID；任务接口还会检查原用户与原 Key 的归属。' },
  { status: '409', code: 'conflict', description: '幂等键对应的请求不一致、任务状态冲突或内容尚未满足读取条件；按返回的错误信息处理。' },
  { status: '413', code: 'request_too_large', description: '请求体或上传素材超过服务限制，缩小输入后重试。' },
  { status: '429', code: 'rate_limit_error', description: '并发、速率或平台限额达到上限，降低并发并按响应提示退避。' },
  { status: '500 / 502 / 503 / 504', code: 'server_error / upstream_error', description: '服务或上游暂时不可用。异步创建结果不明确时先查已有任务，避免直接重复提交产生多次任务。' },
  { status: 'SSE', code: 'error / response.failed', description: '流式连接建立后仍可能收到错误事件；不要仅凭 HTTP 200 判定成功，应读到协议终止事件。' },
]
