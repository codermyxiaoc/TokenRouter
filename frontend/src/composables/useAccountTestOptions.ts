import { computed, ref, watch, type Ref } from 'vue'
import type { Account, ClaudeModel } from '@/types'
import type { AccountTestEndpoint, AccountTestType } from '@/types/accountTest'
import { VIDEO_ENDPOINTS, type VideoEndpoint } from '@/components/account/videoAccountConfig'

type Translate = (key: string) => string

// 两个管理入口共用模式和端点矩阵，避免旧入口与账号页的测试能力发生漂移。
export function useAccountTestOptions(
  account: Ref<Account | null>,
  availableModels: Ref<ClaudeModel[]>,
  selectedModelId: Ref<string>,
  testType: Ref<AccountTestType>,
  t: Translate
) {
  const testEndpoint = ref<AccountTestEndpoint>('auto')
  const videoEndpoint = ref<VideoEndpoint | ''>('')
  const videoDuration = ref<number | string>(5)
  const videoResolution = ref('')
  const videoRatio = ref('16:9')
  const isVideoAccount = computed(() => account.value?.platform === 'video')
  const isGrokAccount = computed(() => account.value?.platform === 'grok')
  const imageTestAvailable = computed(() => {
    const platform = account.value?.platform
    return platform === 'openai' || platform === 'gemini' || platform === 'grok' ||
      (platform === 'antigravity' && account.value?.type === 'apikey')
  })
  const testTypeOptions = computed(() => isVideoAccount.value
    ? [{ value: 'video', label: t('admin.accounts.video.testType') }]
    : isGrokAccount.value
    ? (['text', 'image', 'video', 'search', 'tts', 'stt', 'realtime'] as const).map((value) => ({
      value,
      label: t(`admin.accounts.grok.testMode${({ text: 'Text', image: 'Image', video: 'Video', search: 'Search', tts: 'TTS', stt: 'STT', realtime: 'Realtime' })[value]}`)
    }))
    : [
      { value: 'text', label: t('admin.accounts.testTypeText') },
      { value: 'image', label: t('admin.accounts.testTypeImage'), disabled: !imageTestAvailable.value }
    ])

  // 端点只来自服务端已应用映射和绑定的模型目录，不在前端猜测默认协议。
  const videoEndpointOptions = computed(() => {
    if (!isVideoAccount.value) return []
    const endpoints = availableModels.value.find(model => model.id === selectedModelId.value)?.video_endpoints
    return VIDEO_ENDPOINTS.filter(endpoint => Array.isArray(endpoints) && endpoints.includes(endpoint))
      .map(value => ({ value, label: t(`admin.accounts.video.endpoints.${value}`) }))
  })
  const videoTestValidationMessage = computed(() => {
    if (!isVideoAccount.value) return ''
    if (!selectedModelId.value || !availableModels.value.some(model => model.id === selectedModelId.value)) {
      return t('admin.accounts.video.testModelsRequired')
    }
    if (!videoEndpointOptions.value.some(option => option.value === videoEndpoint.value)) {
      return t('admin.accounts.video.testEndpointsRequired')
    }
    const duration = Number(videoDuration.value)
    if (!Number.isInteger(duration) || duration < 1 || duration > 60) return t('admin.accounts.video.testDurationInvalid')
    return ''
  })
  const endpointOptions = computed(() => {
    const current = account.value
    let endpoints: AccountTestEndpoint[] = ['auto']
    if (current) {
      switch (current.platform) {
        case 'openai':
          endpoints = current.type === 'apikey' ? ['auto', 'chat_completions', 'responses', 'anthropic'] : ['auto', 'responses']
          break
        case 'anthropic':
          endpoints = current.type === 'apikey' ? ['auto', 'chat_completions', 'responses', 'anthropic'] : ['auto', 'anthropic']
          break
        case 'kimi':
        case 'deepseek':
        case 'minimax':
          endpoints = ['auto', 'chat_completions', 'responses', 'anthropic']
          break
        case 'command_code':
          endpoints = ['auto', 'chat_completions', 'responses', 'anthropic']
          break
        case 'typesafe':
          endpoints = ['auto', 'systemone']
          break
        case 'cline':
          endpoints = ['auto', 'chat_completions']
          break
        case 'zhipu':
          endpoints = ['auto', 'chat_completions', 'anthropic']
          break
        case 'opencode_go':
          endpoints = selectedModelId.value.toLowerCase().startsWith('jev-')
            ? ['auto', 'systemone']
            : ['auto', 'chat_completions', 'responses', 'anthropic']
          break
        case 'gemini':
          endpoints = ['auto', 'gemini']
          break
      }
    }
    return endpoints.map((value) => ({ value, label: t(`admin.accounts.testEndpoints.${value}`) }))
  })
  const showEndpointSelect = computed(() => !isGrokAccount.value && !isVideoAccount.value && testType.value === 'text')
  const showModelSelect = computed(() => !isGrokAccount.value || ['text', 'image', 'video'].includes(testType.value))
  const isImageModel = (id: string) => /^(grok-imagine(?:-edit)?$|grok-imagine-image)/i.test(id)
  const isVideoModel = (id: string) => /^(grok-imagine-video|grok-video)/i.test(id)
  const modelOptionsForMode = computed(() => {
    if (!isGrokAccount.value) return availableModels.value.map((model) => ({ ...model }))
    return availableModels.value.filter((model) => {
      if (testType.value === 'image') return isImageModel(model.id)
      if (testType.value === 'video') return isVideoModel(model.id)
      return !isImageModel(model.id) && !isVideoModel(model.id)
    }).map((model) => ({ ...model }))
  })
  const supportsImageUpload = computed(() => isVideoAccount.value || (isGrokAccount.value && ['image', 'video'].includes(testType.value)))
  const supportsAudioUpload = computed(() => isGrokAccount.value && testType.value === 'stt')
  const supportsPromptInput = computed(() => !isGrokAccount.value || !['stt', 'realtime'].includes(testType.value))
  const promptKeys = computed(() => {
    switch (testType.value) {
      case 'image': return ['imagePromptLabel', 'imagePromptPlaceholder', 'imageTestHint', 'imagePromptDefault']
      case 'video': return ['videoPromptLabel', 'videoPromptPlaceholder', 'videoTestHint', 'videoPromptDefault']
      case 'search': return ['grok.searchQueryLabel', 'grok.searchQueryPlaceholder', 'grok.searchTestHint', 'grok.searchQueryDefault']
      case 'tts': return ['grok.ttsTextLabel', 'grok.ttsTextPlaceholder', 'grok.ttsTestHint', 'grok.ttsTextDefault']
      default: return ['textPromptLabel', 'textPromptPlaceholder', 'textTestHint', 'textPromptDefault']
    }
  })
  const promptInputLabel = computed(() => t(`admin.accounts.${promptKeys.value[0]}`))
  const promptInputPlaceholder = computed(() => t(`admin.accounts.${promptKeys.value[1]}`))
  const promptInputHint = computed(() => isVideoAccount.value
    ? t('admin.accounts.video.testHint')
    : t(`admin.accounts.${promptKeys.value[2]}`))
  const defaultPrompt = computed(() => t(`admin.accounts.${promptKeys.value[3]}`))
  const testTypeSummary = computed(() => testTypeOptions.value.find((option) => option.value === testType.value)?.label || '')

  watch(videoEndpointOptions, (options) => {
    if (!options.some(option => option.value === videoEndpoint.value)) videoEndpoint.value = options[0]?.value || ''
  })
  watch(endpointOptions, (options) => {
    if (!options.some((option) => option.value === testEndpoint.value)) testEndpoint.value = 'auto'
  })
  watch(modelOptionsForMode, (models) => {
    if ((isGrokAccount.value || isVideoAccount.value) && !models.some((model) => model.id === selectedModelId.value)) {
      selectedModelId.value = models[0]?.id || ''
    }
  })
  return {
    isVideoAccount, videoEndpoint, videoDuration, videoResolution, videoRatio, videoEndpointOptions, videoTestValidationMessage,
    testEndpoint, isGrokAccount, testTypeOptions, endpointOptions, showEndpointSelect,
    showModelSelect, modelOptionsForMode, supportsImageUpload, supportsAudioUpload,
    supportsPromptInput, promptInputLabel, promptInputPlaceholder, promptInputHint,
    defaultPrompt, testTypeSummary
  }
}
