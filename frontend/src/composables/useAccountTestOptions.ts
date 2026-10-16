import { computed, ref, watch, type Ref } from 'vue'
import type { Account, ClaudeModel } from '@/types'
import type { AccountTestEndpoint, AccountTestType } from '@/types/accountTest'
import { VIDEO_ENDPOINTS, VIDEO_VIDU_PATHS, isVideoEndpointAdapter, videoAdapterTextKey, type VideoEndpoint, type VideoProtocolAdapter } from '@/components/account/videoAccountConfig'

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
  const videoOperation = ref('')
  const isVideoAccount = computed(() => account.value?.platform === 'video')
  // 模型列表中的适配器已应用映射和模型覆盖；旧服务只回退旧配置，不在浏览器猜测模型映射。
  const videoProtocolAdapter = computed<VideoProtocolAdapter>(() => {
    if (!isVideoAccount.value || !videoEndpoint.value) return 'legacy_v1'
    const adapter = availableModels.value.find(model => model.id === selectedModelId.value)?.video_protocol_adapters?.[videoEndpoint.value]
    if (isVideoEndpointAdapter(videoEndpoint.value, adapter)) return adapter
    return videoEndpoint.value === 'openai_videos' && account.value?.credentials?.video_openai_profile === 'metaso_h3' ? 'metaso_h3_v1' : 'legacy_v1'
  })
  const isMetasoH3VideoTest = computed(() => isVideoAccount.value && videoProtocolAdapter.value === 'metaso_h3_v1')
  const videoResolutionHint = computed(() => `admin.accounts.video.${videoProtocolAdapter.value === 'openai_v1' ? 'testOpenAIResolutionHint'
    : videoProtocolAdapter.value === 'minimax_v2' ? 'testMiniMaxResolutionHint'
    : videoProtocolAdapter.value === 'seedance_v1' ? 'testSeedanceResolutionHint'
    : videoProtocolAdapter.value === 'vidu_v2' ? 'testViduResolutionHint'
    : videoProtocolAdapter.value === 'kemei_v1' ? 'testKemeiResolutionHint'
    : isMetasoH3VideoTest.value ? 'testH3ResolutionHint' : 'testResolutionHint'}`)
  const videoResolutionPlaceholder = computed(() => videoProtocolAdapter.value === 'openai_v1' ? '1280x720'
    : videoProtocolAdapter.value === 'minimax_v2' ? '768P'
    : videoProtocolAdapter.value === 'seedance_v1' ? '720p'
    : videoProtocolAdapter.value === 'vidu_v2' ? t('admin.accounts.video.testViduResolutionPlaceholder')
    : videoProtocolAdapter.value === 'kemei_v1' ? kemeiResolution() || '720p' : '768p')
  // 官方 Seedance 各模型与输入模式的默认比例不同，不用 16:9 占位诱导覆盖上游默认值。
  const videoRatioPlaceholder = computed(() => videoProtocolAdapter.value === 'seedance_v1'
    ? t('admin.accounts.video.testSeedanceRatioPlaceholder') : '16:9')
  // 可美按已选端点确定测试档位，不根据模型名称或上游域名猜测协议。
  function kemeiResolution() {
    return ({ minimax: '768P', wan: '720P', kling: '720p', seedance: '720p', vidu: '540p' } as Partial<Record<VideoEndpoint, string>>)[videoEndpoint.value as VideoEndpoint] || ''
  }
  const videoOperationOptions = computed(() => VIDEO_VIDU_PATHS.map(value => ({ value, label: value })))
  // 只调整尚未手工填写的默认值，切换模型或端点不覆盖管理员的输入。
  let applyingVideoDefaults = false
  const videoEdited = { duration: false, resolution: false, ratio: false }
  // 输入相同值也算手工确认，不能只依赖值变化侦测后覆盖用户选择。
  const markVideoOptionEdited = (field: keyof typeof videoEdited) => { videoEdited[field] = true }
  watch(videoDuration, () => { if (!applyingVideoDefaults) videoEdited.duration = true }, { flush: 'sync' })
  watch(videoResolution, () => { if (!applyingVideoDefaults) videoEdited.resolution = true }, { flush: 'sync' })
  watch(videoRatio, () => { if (!applyingVideoDefaults) videoEdited.ratio = true }, { flush: 'sync' })
  function applyVideoDefaults(adapter: VideoProtocolAdapter) {
    applyingVideoDefaults = true
    if (!videoEdited.duration) videoDuration.value = adapter === 'openai_v1' ? 4 : 5
    if (!videoEdited.resolution) videoResolution.value = adapter === 'minimax_v2' ? '768P' : adapter === 'kemei_v1' ? kemeiResolution() : ''
    // Seedance/Vidu 官方比例由模型和输入模式决定，留空继承上游默认，避免首帧图被固定比例拒绝。
    if (!videoEdited.ratio) videoRatio.value = ['openai_v1', 'vidu_v2', 'seedance_v1'].includes(adapter) ? '' : '16:9'
    applyingVideoDefaults = false
  }
  function resetVideoOptions() {
    applyingVideoDefaults = true
    videoEndpoint.value = ''
    videoDuration.value = 5
    videoResolution.value = ''
    videoRatio.value = '16:9'
    videoOperation.value = ''
    applyingVideoDefaults = false
    videoEdited.duration = videoEdited.resolution = videoEdited.ratio = false
  }
  watch([videoProtocolAdapter, videoEndpoint], ([adapter]) => applyVideoDefaults(adapter))
  // 同一供应商的多个端点共享适配 ID，切换模型和端点仍必须重新读取各自的操作配置。
  watch([videoEndpoint, selectedModelId, availableModels], () => {
    const saved = availableModels.value.find(model => model.id === selectedModelId.value)?.video_vidu_operation
    videoOperation.value = videoEndpoint.value === 'vidu' && (VIDEO_VIDU_PATHS as readonly unknown[]).includes(saved) ? saved! : ''
  })
  function videoTestRatio(hasImage: boolean) {
    // 可美 MiniMax 图生默认继承图片比例；手工填写的比例仍保留。
    if (videoProtocolAdapter.value === 'kemei_v1' && videoEndpoint.value === 'minimax' && hasImage && !videoEdited.ratio) return ''
    return videoRatio.value.trim()
  }
  function videoTestMediaValidation(hasImage: boolean) {
    if (!isVideoAccount.value || videoEndpoint.value !== 'vidu') return ''
    if (videoOperation.value === '/ent/v2/start-end2video') return t('admin.accounts.video.testViduTwoImagesRequired')
    if (videoOperation.value === '/ent/v2/text2video') return hasImage ? t('admin.accounts.video.testViduTextOnly') : ''
    return hasImage ? '' : t('admin.accounts.video.testViduImageRequired')
  }
  const isGrokAccount = computed(() => account.value?.platform === 'grok')
  const imageTestAvailable = computed(() => {
    const platform = account.value?.platform
    return platform === 'openai' || platform === 'gemini' || platform === 'grok' ||
      platform === 'antigravity'
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
    if (videoEndpoint.value === 'vidu' && !(VIDEO_VIDU_PATHS as readonly string[]).includes(videoOperation.value)) return t('admin.accounts.video.testViduOperationRequired')
    const duration = Number(videoDuration.value)
    if (!Number.isInteger(duration) || duration < 1 || duration > 60) return t('admin.accounts.video.testDurationInvalid')
    // 官方 Seedance 不接受 768p；这里只验证协议档位，最终映射模型的限制由服务端检查。
    if (videoProtocolAdapter.value === 'seedance_v1' && videoResolution.value.trim() &&
      !['480p', '720p', '1080p', '4k'].includes(videoResolution.value.trim().toLowerCase())) {
      return t('admin.accounts.video.testSeedanceResolutionInvalid')
    }
    if (isMetasoH3VideoTest.value) {
      if (!['768p', '2k'].includes(videoResolution.value.trim().toLowerCase())) return t('admin.accounts.video.testH3ResolutionInvalid')
      if (!['16:9', '9:16'].includes(videoRatio.value.trim())) return t('admin.accounts.video.testH3RatioInvalid')
    }
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
    isVideoAccount, isMetasoH3VideoTest, videoProtocolAdapter, videoAdapterTextKey, videoResolutionHint, videoResolutionPlaceholder, videoRatioPlaceholder, resetVideoOptions, markVideoOptionEdited,
    videoEndpoint, videoDuration, videoResolution, videoRatio, videoEndpointOptions, videoTestValidationMessage,
    videoOperation, videoOperationOptions, videoTestRatio, videoTestMediaValidation,
    testEndpoint, isGrokAccount, testTypeOptions, endpointOptions, showEndpointSelect,
    showModelSelect, modelOptionsForMode, supportsImageUpload, supportsAudioUpload,
    supportsPromptInput, promptInputLabel, promptInputPlaceholder, promptInputHint,
    defaultPrompt, testTypeSummary
  }
}
