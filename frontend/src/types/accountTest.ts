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
}
