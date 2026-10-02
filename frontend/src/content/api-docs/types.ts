// 文档只描述已实现的网关协议，不携带账号、密钥或运行时任务数据。
export type ApiDocCategory = 'text' | 'models' | 'images' | 'video' | 'audio' | 'tools'
export type ApiDocVideoProtocol = 'openai' | 'seedance' | 'kling' | 'wan' | 'minimax' | 'grok'
export interface ApiDocParameter {
  name: string
  type: string
  required: boolean | 'conditional'
  description: string
}
export interface ApiDocError {
  status: number | string
  code: string
  description: string
}
export interface ApiDocEndpoint {
  id: string
  category: ApiDocCategory
  // 视频目录按端点协议分组，独立于账号所属平台和调度资格。
  videoProtocol?: ApiDocVideoProtocol
  title: string
  summary: string
  platforms: string[]
  method: 'GET' | 'POST' | 'DELETE' | 'PATCH' | 'PUT' | 'WS'
  path: string
  aliases?: string[]
  auth?: 'bearer' | 'anthropic' | 'google' | 'websocket'
  contentType?: string
  requestPath?: string
  requestHeaders?: Record<string, string>
  parameters: ApiDocParameter[]
  requestExample?: string
  requestLanguage?: string
  responseExample: string
  responseLanguage?: string
  responseStatus?: number
  responseDescription?: string
  notes?: string[]
  errors?: ApiDocError[]
}
