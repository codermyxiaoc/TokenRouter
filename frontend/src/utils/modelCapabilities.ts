import type { MarketplaceModelPricing, ModelModality } from '@/types'

// 模型能力（输入/输出模态）解析：模型广场卡片右上角能力标签的数据源。
// 优先使用后端从定价元数据下发的 input_modalities/output_modalities；
// 接口缺失或非空校验失败时，回退到按模型 ID 模式识别的本地规则（与 ModelIcon 同一思路），
// 其中定价数据（图片计费、图片输入价）比 ID 模式更可靠，ID 模式仅作为兜底。

export type { ModelModality } from '@/types'

export interface ModelCapabilities {
  input: ModelModality[]
  output: ModelModality[]
}

// 后端下发的能力入参：两侧都非空才视为可信数据。
export interface ApiModelCapabilities {
  input?: ModelModality[]
  output?: ModelModality[]
}

// 标签固定按该顺序渲染，保证不同卡片之间可扫读对比。
export const MODEL_MODALITY_ORDER: ModelModality[] = ['text', 'image', 'audio', 'video']

// 已知生图模型的 ID 特征（定价模式缺失时兜底，命中后只输出图片）。
const IMAGE_OUTPUT_PATTERNS = [/dall-e/, /flux/, /stable-diffusion/, /imagen/, /cogview/, /midjourney/, /mj-/, /gpt-image/]

// 已知视频生成模型的 ID 特征（命中后只输出视频）。
const VIDEO_OUTPUT_PATTERNS = [/veo/, /sora/, /cogvideo/]

// 音频输入输出双通的对话模型（如 gpt-4o-audio、realtime 系列）。
const AUDIO_IO_PATTERNS = [/gpt-4o-audio/, /gpt-audio/, /realtime/]

// 仅音频输入的模型（语音识别类，输入只有音频）。
const AUDIO_INPUT_PATTERNS = [/whisper/, /voxtral/, /qwen-audio/]

// 仅音频输出的模型（语音合成类）。
const AUDIO_OUTPUT_PATTERNS = [/tts/]

// 已知支持图片理解（视觉输入）的模型 ID 特征。
const IMAGE_INPUT_PATTERNS = [
  /claude/,
  /gemini/,
  /gpt-4o/,
  /gpt-4\.1/,
  /gpt-5/,
  /o3/,
  /o4/,
  /qwen-vl/,
  /glm-4v/,
  /pixtral/,
  /llama-4/,
]

// 支持图片编辑（图生图）的生图模型：输入除文字外还有图片。
const IMAGE_EDIT_PATTERNS = [/gpt-image/]

function hasPositivePrice(value?: number): boolean {
  return typeof value === 'number' && Number.isFinite(value) && value > 0
}

function sortedModalities(modalities: Set<ModelModality>): ModelModality[] {
  return MODEL_MODALITY_ORDER.filter((modality) => modalities.has(modality))
}

// 过滤接口下发值中的非法模态，并按固定顺序输出；空数组/缺失返回空。
function sanitizeApiModalities(modalities?: ModelModality[]): ModelModality[] {
  if (!Array.isArray(modalities)) {
    return []
  }
  return MODEL_MODALITY_ORDER.filter((modality) => modalities.includes(modality))
}

export function resolveModelCapabilities(
  modelId: string,
  pricing?: MarketplaceModelPricing,
  apiCapabilities?: ApiModelCapabilities,
): ModelCapabilities {
  // 后端下发的能力元数据优先：过滤非法取值后，两侧都非空就直接采用。
  const apiInput = sanitizeApiModalities(apiCapabilities?.input)
  const apiOutput = sanitizeApiModalities(apiCapabilities?.output)
  if (apiInput.length > 0 && apiOutput.length > 0) {
    return { input: apiInput, output: apiOutput }
  }

  const id = modelId.toLowerCase()

  // 大部分模型至少支持文字输入输出，作为默认值；命中更具体的能力时再调整。
  const input = new Set<ModelModality>(['text'])
  const output = new Set<ModelModality>(['text'])

  const imageOutputByPricing = pricing?.pricing_mode === 'image'
  const imageOutputById = IMAGE_OUTPUT_PATTERNS.some((pattern) => pattern.test(id))
  const audioIO = AUDIO_IO_PATTERNS.some((pattern) => pattern.test(id))
  const audioInput = AUDIO_INPUT_PATTERNS.some((pattern) => pattern.test(id))
  const audioOutput = AUDIO_OUTPUT_PATTERNS.some((pattern) => pattern.test(id))
  const videoOutput = VIDEO_OUTPUT_PATTERNS.some((pattern) => pattern.test(id))

  // 生图模型只输出图片，纯生成类模型没有对话输出。
  if (imageOutputByPricing || imageOutputById) {
    output.delete('text')
    output.add('image')
  }

  // 视频生成模型只输出视频。
  if (videoOutput) {
    output.delete('text')
    output.add('video')
  }

  if (audioIO) {
    input.add('audio')
    output.add('audio')
  }
  if (audioInput) {
    // 语音识别类模型输入只有音频。
    input.delete('text')
    input.add('audio')
  }
  if (audioOutput) {
    // 语音合成类模型输出只有音频。
    output.delete('text')
    output.add('audio')
  }

  // 图片输入：优先看定价数据里的图片输入价，其次按已知视觉模型兜底；
  // 纯生成类模型（已判定为图片/视频/音频输出）默认只有文字输入；
  // 但 gpt-image 系列支持图片编辑（图生图），输入需要叠加图片；
  // 音频对话/识别模型（如 gpt-4o-audio）不按 gpt-4o 视觉模式叠加图片输入。
  const generativeOnly = imageOutputByPricing || imageOutputById || videoOutput || audioOutput
  if (IMAGE_EDIT_PATTERNS.some((pattern) => pattern.test(id))) {
    input.add('image')
  } else if (!generativeOnly && !audioIO && !audioInput) {
    const imageInputByPricing = hasPositivePrice(pricing?.image_input_price_per_token)
    const imageInputById = IMAGE_INPUT_PATTERNS.some((pattern) => pattern.test(id))
    if (imageInputByPricing || imageInputById) {
      input.add('image')
    }
  }

  return {
    input: sortedModalities(input),
    output: sortedModalities(output),
  }
}
