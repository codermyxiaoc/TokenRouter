import { effectScope, nextTick, ref } from 'vue'
import { afterEach, describe, expect, it } from 'vitest'
import type { Account, ClaudeModel } from '@/types'
import type { AccountTestType } from '@/types/accountTest'
import { useAccountTestOptions } from '../useAccountTestOptions'
import type { VideoEndpoint } from '@/components/account/videoAccountConfig'

// 两个管理入口共用此矩阵，原生 Antigravity 图片测试不能被前端禁用。
describe('Antigravity 图片测试入口', () => {
  it.each(['oauth', 'apikey', 'upstream'])('%s 账号允许显式图片测试', (type) => {
    const options = useAccountTestOptions(ref({ platform: 'antigravity', type } as Account), ref<ClaudeModel[]>([]), ref('gemini-3.1-flash-image'), ref<AccountTestType>('image'), (key) => key)
    expect(options.testTypeOptions.value.find((item) => item.value === 'image')).toMatchObject({ disabled: false })
  })
  it('其它不支持图片的平台保持禁用', () => {
    const options = useAccountTestOptions(ref({ platform: 'anthropic', type: 'oauth' } as Account), ref<ClaudeModel[]>([]), ref('claude-sonnet-4-6'), ref<AccountTestType>('text'), (key) => key)
    expect(options.testTypeOptions.value.find((item) => item.value === 'image')).toMatchObject({ disabled: true })
  })
})

// 仅按服务端返回的生效协议限制参数，不能让官方校验误伤其它方案或覆盖手填值。
describe('视频测试分辨率协议边界', () => {
  const scopes: ReturnType<typeof effectScope>[] = []
  afterEach(() => {
    scopes.splice(0).forEach(scope => scope.stop())
  })

  async function setup(endpoint: VideoEndpoint = 'seedance', adapter?: string) {
    const account = ref({ platform: 'video', type: 'apikey', credentials: {} } as Account)
    const models = ref<ClaudeModel[]>([{
      id: 'public-alias', display_name: 'Public alias', video_endpoints: [endpoint],
      ...(adapter ? { video_protocol_adapters: { [endpoint]: adapter } } : {})
    }])
    const selected = ref('public-alias')
    const scope = effectScope()
    scopes.push(scope)
    const options = scope.run(() => useAccountTestOptions(account, models, selected, ref<AccountTestType>('video'), key => key))!
    options.videoEndpoint.value = endpoint
    await nextTick()
    return { options, models }
  }

  it.each(['', '   ', '480P', ' 720p ', '1080P', '4K'])('官方通用档位 %j 留待最终模型校验', async resolution => {
    const { options } = await setup('seedance', 'seedance_v1')
    options.videoResolution.value = resolution
    expect(options.videoTestValidationMessage.value).toBe('')
    expect(options.videoResolution.value).toBe(resolution)
  })

  it.each(['768p', '768P', '2K', '1280x720', '720', '720 p', '720p\u0000', '720p,1080p', 'NaN', '<script>'])('官方非法档位 %j 不允许提交', async resolution => {
    const { options } = await setup('seedance', 'seedance_v1')
    options.videoResolution.value = resolution
    expect(options.videoTestValidationMessage.value).toBe('admin.accounts.video.testSeedanceResolutionInvalid')
  })

  it.each([
    ['seedance', 'legacy_v1'], ['seedance', 'kemei_v1'],
    ['minimax', 'minimax_v2'], ['minimax', 'kemei_v1'],
    ['compat', 'legacy_v1'], ['compat', 'kemei_v1'],
    ['openai_videos', 'metaso_h3_v1'], ['wan', 'wan_v1'], ['kling', 'kling_v1']
  ] as const)('%s 的 %s 不套用官方 Seedance 限制', async (endpoint, adapter) => {
    const { options } = await setup(endpoint, adapter)
    options.videoResolution.value = '768p'
    expect(options.videoTestValidationMessage.value).toBe('')
  })

  it.each([undefined, 'unknown_adapter', 'minimax_v2'])('缺失或无效目录协议 %j 沿用旧行为', async adapter => {
    const { options } = await setup('seedance', adapter)
    options.videoResolution.value = '768p'
    expect(options.videoProtocolAdapter.value).toBe('legacy_v1')
    expect(options.videoTestValidationMessage.value).toBe('')
  })

  it('目录刷新后立即应用有效协议，但不悄悄改写手填分辨率', async () => {
    const { options, models } = await setup('seedance', 'legacy_v1')
    options.videoResolution.value = '768p'
    models.value = [{ ...models.value[0]!, video_protocol_adapters: { seedance: 'seedance_v1' } }]
    await nextTick()
    expect(options.videoResolution.value).toBe('768p')
    expect(options.videoResolutionPlaceholder.value).toBe('720p')
    expect(options.videoTestValidationMessage.value).toBe('admin.accounts.video.testSeedanceResolutionInvalid')
    models.value = [{ ...models.value[0]!, video_protocol_adapters: { seedance: 'kemei_v1' } }]
    await nextTick()
    expect(options.videoResolution.value).toBe('768p')
    expect(options.videoTestValidationMessage.value).toBe('')
  })

  it('未手动填写时切入官方协议清除供应商预填，但不发送占位值', async () => {
    const { options, models } = await setup('minimax', 'minimax_v2')
    expect(options.videoResolution.value).toBe('768P')
    models.value = [{ ...models.value[0]!, video_endpoints: ['seedance'], video_protocol_adapters: { seedance: 'seedance_v1' } }]
    await nextTick()
    expect(options.videoEndpoint.value).toBe('seedance')
    expect(options.videoResolution.value).toBe('')
    expect(options.videoResolutionPlaceholder.value).toBe('720p')
    expect(options.videoTestValidationMessage.value).toBe('')
  })
})
