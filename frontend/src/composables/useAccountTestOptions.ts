import { computed, ref, watch, type Ref } from 'vue'
import type { Account, ClaudeModel } from '@/types'
import type { AccountTestEndpoint, AccountTestType } from '@/types/accountTest'

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
  const isGrokAccount = computed(() => account.value?.platform === 'grok')
  const imageTestAvailable = computed(() => {
    const platform = account.value?.platform
    return platform === 'openai' || platform === 'gemini' || platform === 'grok' ||
      (platform === 'antigravity' && account.value?.type === 'apikey')
  })
  const testTypeOptions = computed(() => isGrokAccount.value
    ? (['text', 'image', 'video', 'search', 'tts', 'stt', 'realtime'] as const).map((value) => ({
      value,
      label: t(`admin.accounts.grok.testMode${({ text: 'Text', image: 'Image', video: 'Video', search: 'Search', tts: 'TTS', stt: 'STT', realtime: 'Realtime' })[value]}`)
    }))
    : [
      { value: 'text', label: t('admin.accounts.testTypeText') },
      { value: 'image', label: t('admin.accounts.testTypeImage'), disabled: !imageTestAvailable.value }
    ])

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
  const showEndpointSelect = computed(() => !isGrokAccount.value && testType.value === 'text')
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
  const supportsImageUpload = computed(() => isGrokAccount.value && ['image', 'video'].includes(testType.value))
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
  const promptInputHint = computed(() => t(`admin.accounts.${promptKeys.value[2]}`))
  const defaultPrompt = computed(() => t(`admin.accounts.${promptKeys.value[3]}`))
  const testTypeSummary = computed(() => testTypeOptions.value.find((option) => option.value === testType.value)?.label || '')

  watch(endpointOptions, (options) => {
    if (!options.some((option) => option.value === testEndpoint.value)) testEndpoint.value = 'auto'
  })
  watch(modelOptionsForMode, (models) => {
    if (isGrokAccount.value && !models.some((model) => model.id === selectedModelId.value)) {
      selectedModelId.value = models[0]?.id || ''
    }
  })
  return {
    testEndpoint, isGrokAccount, testTypeOptions, endpointOptions, showEndpointSelect,
    showModelSelect, modelOptionsForMode, supportsImageUpload, supportsAudioUpload,
    supportsPromptInput, promptInputLabel, promptInputPlaceholder, promptInputHint,
    defaultPrompt, testTypeSummary
  }
}
