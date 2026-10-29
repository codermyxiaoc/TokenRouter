import { computed, ref, watch, type Ref } from 'vue'
import type { Account, ClaudeModel } from '@/types'
import type { AccountTestEndpoint, AccountTestType } from '@/types/accountTest'
import { parseVideoProtocol, videoAdapterTextKey, videoPresetBehaviour, type VideoAdapterBehaviour, type VideoEndpoint } from '@/components/account/videoAccountConfig'

type Translate = (key: string, params?: Record<string, unknown>) => string

// 两个管理入口共用模式和端点矩阵，避免旧入口与账号页的测试能力发生漂移。
export function useAccountTestOptions(
  account: Ref<Account | null>,
  availableModels: Ref<ClaudeModel[]>,
  selectedModelId: Ref<string>,
  testType: Ref<AccountTestType>,
  t: Translate
) {
  const testEndpoint = ref<AccountTestEndpoint>('auto')
  const videoDuration = ref<number | string>(5)
  const videoResolution = ref('')
  const videoRatio = ref('16:9')
  const isVideoAccount = computed(() => account.value?.platform === 'video')
  // 每个模型只绑定一个协议，测试与正式网关一致，由服务端返回的 video_protocol 决定，管理员不再选择端点。
  const videoProtocol = computed(() => {
    if (!isVideoAccount.value) return ''
    const value = availableModels.value.find(model => model.id === selectedModelId.value)?.video_protocol
    return typeof value === 'string' ? value.trim() : ''
  })
  const parsedVideoProtocol = computed(() => parseVideoProtocol(videoProtocol.value))
  // 预置模板（旧厂商方案的模板化版本）沿用原方案的测试提示。
  const presetBehaviour = computed(() => videoPresetBehaviour(parsedVideoProtocol.value))
  // 内置协议所属端点；预置模板按键名推断端点，自定义模板没有内置端点。
  const videoEndpoint = computed<VideoEndpoint | ''>(() => parsedVideoProtocol.value?.kind === 'builtin'
    ? parsedVideoProtocol.value.endpoint : presetBehaviour.value?.endpoint ?? '')
  const isProfileVideoTest = computed(() => isVideoAccount.value && parsedVideoProtocol.value?.kind === 'profile' && !presetBehaviour.value)
  // 自定义模板和未知协议按通用参数处理，不套用任何内置供应商的专用限制。
  const videoProtocolAdapter = computed<VideoAdapterBehaviour>(() =>
    parsedVideoProtocol.value?.kind === 'builtin' ? parsedVideoProtocol.value.adapter : presetBehaviour.value?.adapter ?? 'legacy_v1')
  // 只读展示的协议名称：内置为“端点 · 方案”，模板为“模板协议 · 键名[@版本]”。
  const videoProtocolLabel = computed(() => {
    const parsed = parsedVideoProtocol.value
    if (!parsed) return videoProtocol.value
    if (parsed.kind === 'profile') {
      return t('admin.accounts.video.profileOption', { key: parsed.version ? `${parsed.key}@${parsed.version}` : parsed.key })
    }
    return `${t(`admin.accounts.video.endpoints.${parsed.endpoint}`)} · ${t(`admin.accounts.video.adapters.${videoAdapterTextKey(parsed.endpoint, parsed.adapter)}`)}`
  })
  const isMetasoH3VideoTest = computed(() => isVideoAccount.value && videoProtocolAdapter.value === 'metaso_h3_v1')
  const isYezonghuiVideoTest = computed(() => isVideoAccount.value && videoProtocolAdapter.value === 'yezonghui_v1')
  const videoResolutionHint = computed(() => `admin.accounts.video.${isProfileVideoTest.value ? 'testProfileResolutionHint'
    : videoProtocolAdapter.value === 'openai_v1' ? 'testOpenAIResolutionHint'
    : isYezonghuiVideoTest.value ? 'testYezonghuiResolutionHint'
    : videoProtocolAdapter.value === 'minimax_v2' ? 'testMiniMaxResolutionHint'
    : videoProtocolAdapter.value === 'seedance_v1' ? 'testSeedanceResolutionHint'
    : videoProtocolAdapter.value === 'vidu_v2' ? 'testViduResolutionHint'
    : videoProtocolAdapter.value === 'kemei_v1' ? 'testKemeiResolutionHint'
    : isMetasoH3VideoTest.value ? 'testH3ResolutionHint' : 'testResolutionHint'}`)
  const videoResolutionPlaceholder = computed(() => videoProtocolAdapter.value === 'openai_v1' ? '1280x720'
    : videoProtocolAdapter.value === 'minimax_v2' ? '768P'
    : ['seedance_v1', 'yezonghui_v1'].includes(videoProtocolAdapter.value) ? '720p'
    : videoProtocolAdapter.value === 'vidu_v2' ? t('admin.accounts.video.testViduResolutionPlaceholder')
    : videoProtocolAdapter.value === 'kemei_v1' ? kemeiResolution() || '720p'
    : isProfileVideoTest.value ? '720p' : '768p')
  // 官方 Seedance 各模型与输入模式的默认比例不同，不用 16:9 占位诱导覆盖上游默认值。
  const videoRatioPlaceholder = computed(() => ['seedance_v1', 'yezonghui_v1'].includes(videoProtocolAdapter.value)
    ? t('admin.accounts.video.testSeedanceRatioPlaceholder') : '16:9')
  // 可美按绑定协议所属端点确定测试档位，不根据模型名称或上游域名猜测协议。
  function kemeiResolution() {
    return ({ minimax: '768P', wan: '720P', kling: '720p', seedance: '720p', vidu: '540p' } as Partial<Record<VideoEndpoint, string>>)[videoEndpoint.value as VideoEndpoint] || ''
  }
  // 只调整尚未手工填写的默认值，切换模型不覆盖管理员的输入。
  let applyingVideoDefaults = false
  const videoEdited = { duration: false, resolution: false, ratio: false }
  // 输入相同值也算手工确认，不能只依赖值变化侦测后覆盖用户选择。
  const markVideoOptionEdited = (field: keyof typeof videoEdited) => { videoEdited[field] = true }
  watch(videoDuration, () => { if (!applyingVideoDefaults) videoEdited.duration = true }, { flush: 'sync' })
  watch(videoResolution, () => { if (!applyingVideoDefaults) videoEdited.resolution = true }, { flush: 'sync' })
  watch(videoRatio, () => { if (!applyingVideoDefaults) videoEdited.ratio = true }, { flush: 'sync' })
  function applyVideoDefaults(adapter: VideoAdapterBehaviour) {
    applyingVideoDefaults = true
    if (!videoEdited.duration) videoDuration.value = adapter === 'openai_v1' ? 4 : 5
    if (!videoEdited.resolution) videoResolution.value = adapter === 'minimax_v2' ? '768P' : adapter === 'kemei_v1' ? kemeiResolution() : ''
    // Seedance/Vidu、Yezonghui 与模板协议的比例由模型决定，留空继承上游默认，避免强行固定横屏。
    if (!videoEdited.ratio) videoRatio.value = isProfileVideoTest.value || ['openai_v1', 'vidu_v2', 'seedance_v1', 'yezonghui_v1'].includes(adapter) ? '' : '16:9'
    applyingVideoDefaults = false
  }
  function resetVideoOptions() {
    applyingVideoDefaults = true
    videoDuration.value = 5
    videoResolution.value = ''
    videoRatio.value = '16:9'
    applyingVideoDefaults = false
    videoEdited.duration = videoEdited.resolution = videoEdited.ratio = false
    // 重置后立即按当前绑定协议补默认值，重新打开同一模型时协议不变也不会遗漏。
    applyVideoDefaults(videoProtocolAdapter.value)
  }
  watch([videoProtocolAdapter, videoEndpoint, isProfileVideoTest], ([adapter]) => applyVideoDefaults(adapter), { immediate: true })
  function videoTestRatio(hasImage: boolean) {
    // 可美 MiniMax 图生默认继承图片比例；手工填写的比例仍保留。
    if (videoProtocolAdapter.value === 'kemei_v1' && videoEndpoint.value === 'minimax' && hasImage && !videoEdited.ratio) return ''
    return videoRatio.value.trim()
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

  const videoTestValidationMessage = computed(() => {
    if (!isVideoAccount.value) return ''
    if (!selectedModelId.value || !availableModels.value.some(model => model.id === selectedModelId.value)) {
      return t('admin.accounts.video.testModelsRequired')
    }
    if (!parsedVideoProtocol.value) return t('admin.accounts.video.testProtocolRequired')
    const duration = Number(videoDuration.value)
    if (!Number.isInteger(duration) || duration < 1 || duration > 60) return t('admin.accounts.video.testDurationInvalid')
    // Yezonghui 的分辨率是独立档位，必须明确选择；具体模型档位由服务端再次校验。
    if (isYezonghuiVideoTest.value && !/^[0-9]+[pk]$/.test(videoResolution.value.trim().toLowerCase())) {
      return t('admin.accounts.video.testYezonghuiResolutionRequired')
    }
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

  watch(endpointOptions, (options) => {
    if (!options.some((option) => option.value === testEndpoint.value)) testEndpoint.value = 'auto'
  })
  watch(modelOptionsForMode, (models) => {
    if ((isGrokAccount.value || isVideoAccount.value) && !models.some((model) => model.id === selectedModelId.value)) {
      selectedModelId.value = models[0]?.id || ''
    }
  })
  return {
    isVideoAccount, isMetasoH3VideoTest, isYezonghuiVideoTest, isProfileVideoTest, videoProtocol, videoProtocolLabel, videoProtocolAdapter, videoResolutionHint, videoResolutionPlaceholder, videoRatioPlaceholder, resetVideoOptions, markVideoOptionEdited,
    videoEndpoint, videoDuration, videoResolution, videoRatio, videoTestValidationMessage, videoTestRatio,
    testEndpoint, isGrokAccount, testTypeOptions, endpointOptions, showEndpointSelect,
    showModelSelect, modelOptionsForMode, supportsImageUpload, supportsAudioUpload,
    supportsPromptInput, promptInputLabel, promptInputPlaceholder, promptInputHint,
    defaultPrompt, testTypeSummary
  }
}
