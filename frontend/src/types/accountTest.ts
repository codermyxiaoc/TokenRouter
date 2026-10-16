import type { VideoEndpoint } from '@/components/account/videoAccountConfig'

/** 管理员账号连接测试协议；仅用于单次探测，不保存账号配置。 */
export type AccountTestType = 'text' | 'image' | 'video' | 'search' | 'tts' | 'stt' | 'realtime'
export type AccountTestEndpoint = 'auto' | 'chat_completions' | 'responses' | 'anthropic' | 'gemini' | 'systemone'
export type AccountTestMode = 'default' | 'compact' | 'legacy_compact'

export interface AccountTestRequest {
  model_id: string
  prompt: string
  test_type: AccountTestType
  test_endpoint?: AccountTestEndpoint
  mode?: AccountTestMode
  image_data_url?: string
  audio_data_url?: string
  // 视频账号仅测试所选模型已配置的协议，时长单位为秒。
  video_endpoint?: VideoEndpoint
  duration?: number
  resolution?: string
  ratio?: string
  // MiniMax 原生测试可选择公网首帧地址及显式水印，省略时保留供应商默认值。
  reference_image_url?: string
  reference_image_transport?: 'data_url' | 'https_url'
  aigc_watermark?: boolean
}
