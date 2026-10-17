/**
 * 使用真实创作台状态机验证模型目录、参数面板和提交请求的衔接。
 * 仅替换网络与本地存储，不向上游发起生成请求。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import CreativeComposer from '@/components/creative/CreativeComposer.vue'
import { creativeOptionKey, useCreativeStudio } from '@/composables/useCreativeStudio'
import * as creativeApi from '@/api/creative'
import type { CreativeModelOption } from '@/api/creative'
import * as localStore from '@/utils/creativeLocalStore'
import zhCreative from '@/i18n/locales/zh/creative'
import enCreative from '@/i18n/locales/en/creative'

let language: 'zh' | 'en' = 'zh'
const messages = { zh: zhCreative, en: enCreative }

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, values?: Record<string, unknown> | string) => {
        if (key === 'creative.panel.midjourneyResolutionHint') {
          return messages[language].creative.panel.midjourneyResolutionHint
        }
        return typeof values === 'object' ? `${key}:${Object.values(values).join(',')}` : key
      },
    }),
  }
})

vi.mock('@/composables/useBalanceDisplay', () => ({
  useBalanceDisplay: () => ({
    formatBalanceAmount: (value: number, options: { fractionDigits?: number }) =>
      `$${value.toFixed(options.fractionDigits ?? 2)}`,
  }),
}))

vi.mock('@/api/creative', () => ({
  CREATIVE_RUN_TERMINAL_STATUSES: ['succeeded', 'failed', 'cancelled', 'result_lost'],
  getCreativeModels: vi.fn(),
  getCreativeCapabilities: vi.fn(),
  getCreativeActiveRuns: vi.fn(),
  createCreativeRun: vi.fn(),
  getCreativeRuns: vi.fn(),
  getCreativeRunOutputContent: vi.fn(),
  ackCreativeRunOutput: vi.fn(),
}))

vi.mock('@/utils/creativeLocalStore', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/utils/creativeLocalStore')>()
  return {
    ...actual,
    saveAsset: vi.fn(),
    listAssets: vi.fn(),
    loadSetting: vi.fn(),
    saveSetting: vi.fn(),
    clearAll: vi.fn(),
  }
})

const mockedApi = vi.mocked(creativeApi)
const mockedStore = vi.mocked(localStore)
const sizes = ['1K', '2K', '4K']
const ratios = ['1:1', '4:3', '3:4', '16:9', '9:16']
// 使用不同的档位单价，防止界面错误复用 1K 价格或漏乘四张。
const midjourney: CreativeModelOption = {
  group_id: 'mj-group',
  group_name: 'MJ 图片分组',
  model: 'MJ-v8.2',
  operations: ['generate'],
  image_sizes: sizes,
  aspect_ratios: ratios,
  qualities: [],
  output_formats: [],
  output_compression: null,
  background_options: [],
  thinking_levels: [],
  output_count: 4,
  max_output_count: 4,
  max_reference_images: 0,
  price_1k: 0.12,
  price_2k: 0.24,
  price_4k: 0.48,
}
const limitedModel: CreativeModelOption = {
  ...midjourney,
  model: 'MJ-only-1K',
  image_sizes: ['1K'],
  aspect_ratios: ['1:1'],
}

let mounted: VueWrapper | null = null

async function mountComposer(initialModel = midjourney) {
  let studio!: ReturnType<typeof useCreativeStudio>
  let pendingSubmission: Promise<boolean> | undefined
  const wrapper = mount(defineComponent({
    setup() {
      studio = useCreativeStudio()
      return () => h(CreativeComposer, {
        studio,
        onGenerate: () => {
          pendingSubmission = studio.createRun({ sourceBlobs: [], maskBlob: null })
        },
      })
    },
  }), {
    global: { stubs: { Icon: true, ProviderIcon: true } },
  })
  mounted = wrapper
  await studio.loadModels()
  studio.selectOption(creativeOptionKey(initialModel))
  await nextTick()
  return { wrapper, studio, submission: () => pendingSubmission }
}

function parameterButton(wrapper: VueWrapper, label: string) {
  const button = wrapper.findAll('button.param-chip').find((item) => item.text() === label)
  expect(button, `参数面板应包含 ${label}`).toBeDefined()
  return button!
}

describe('MJ 创作台尺寸和比例', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.useFakeTimers()
    localStorage.clear()
    language = 'zh'
    mockedApi.getCreativeModels.mockResolvedValue([midjourney, limitedModel])
    mockedApi.getCreativeCapabilities.mockResolvedValue({
      max_prompt_chars: 8000,
      max_asset_bytes: 33554432,
      max_total_input_bytes: 67108864,
      max_mask_bytes: 4194304,
      allowed_mime_types: ['image/png', 'image/jpeg', 'image/webp'],
    })
    mockedApi.createCreativeRun.mockResolvedValue({
      id: 'crun_mj', status: 'queued', operation: 'generate',
      model: midjourney.model, group_id: midjourney.group_id, requested_output_count: 4,
    })
    mockedApi.getCreativeActiveRuns.mockResolvedValue({ items: [], next_cursor: '', has_more: false })
    mockedApi.getCreativeRuns.mockResolvedValue({ items: [], total: 0 })
    mockedStore.loadSetting.mockResolvedValue(null)
    mockedStore.saveSetting.mockResolvedValue(undefined)
    mockedStore.listAssets.mockResolvedValue([])
  })

  afterEach(() => {
    mounted?.unmount()
    mounted = null
    vi.useRealTimers()
    vi.clearAllMocks()
  })

  it('仅按服务端目录显示三档尺寸和五种比例，连续切换参数高亮同步', async () => {
    const { wrapper, studio } = await mountComposer()
    await wrapper.get('button[title="creative.composer.params"]').trigger('click')

    expect(wrapper.findAll('.param-chip').map((item) => item.text())).toEqual([...sizes, ...ratios])
    expect(wrapper.find('select').exists()).toBe(false)
    expect(wrapper.text()).toContain('creative.panel.outputCount:4')
    for (const size of sizes) {
      await parameterButton(wrapper, size).trigger('click')
      expect(studio.imageSize.value).toBe(size)
      expect(parameterButton(wrapper, size).classes()).toContain('param-chip-active')
    }
    for (const ratio of ratios) {
      await parameterButton(wrapper, ratio).trigger('click')
      expect(studio.aspectRatio.value).toBe(ratio)
      expect(parameterButton(wrapper, ratio).classes()).toContain('param-chip-active')
    }
  })

  // 覆盖全部十五种 UI 组合到真实 FormData 的传递，不通过直接修改 ref 绕过控件。
  it.each(sizes.flatMap((size) => ratios.map((ratio) => [size, ratio])))('%s / %s 提交保留选择并显示四张总价', async (size, ratio) => {
    const { wrapper, studio, submission } = await mountComposer()
    await wrapper.get('textarea').setValue('红色小球在白色背景上')
    await wrapper.get('button[title="creative.composer.params"]').trigger('click')
    await parameterButton(wrapper, size).trigger('click')
    await parameterButton(wrapper, ratio).trigger('click')

    const expectedPrice = { '1K': 0.48, '2K': 0.96, '4K': 1.92 }[size]!
    expect(studio.estimatedCost.value).toBeCloseTo(expectedPrice)
    expect(wrapper.text()).toContain(`creative.panel.estimatedCost:$${expectedPrice.toFixed(3)}`)
    await wrapper.get('button[title="creative.composer.send"]').trigger('click')
    expect(await submission()).toBe(true)
    expect(mockedApi.createCreativeRun).toHaveBeenCalledTimes(1)
    const [form] = mockedApi.createCreativeRun.mock.calls[0]
    expect(form.get('model')).toBe(midjourney.model)
    expect(form.get('group_id')).toBe(midjourney.group_id)
    expect(form.get('operation')).toBe('generate')
    expect(form.get('image_size')).toBe(size)
    expect(form.get('aspect_ratio')).toBe(ratio)
    // 固定张数由服务端决定，客户端不能借参数压低预扣或夹带 GPT 专用选项。
    for (const field of ['n', 'output_count', 'quality', 'background', 'thinking_level', 'mask', 'source_images[]']) {
      expect(form.has(field)).toBe(false)
    }
  })

  it('从 4K 竖图切换至仅 1K 模型后重置参数、估价和持久草稿', async () => {
    const { wrapper, studio, submission } = await mountComposer()
    await wrapper.get('textarea').setValue('切换模型后生成')
    await wrapper.get('button[title="creative.composer.params"]').trigger('click')
    await parameterButton(wrapper, '4K').trigger('click')
    await parameterButton(wrapper, '9:16').trigger('click')
    await wrapper.get('button[title="creative.composer.model"]').trigger('click')
    const limitedOption = wrapper.findAll('button.composer-option').find((item) => item.text().includes(limitedModel.model))
    expect(limitedOption).toBeDefined()
    await limitedOption!.trigger('click')
    await wrapper.get('button[title="creative.composer.params"]').trigger('click')

    expect(wrapper.findAll('.param-chip').map((item) => item.text())).toEqual(['1K', '1:1'])
    expect(studio.imageSize.value).toBe('1K')
    expect(studio.aspectRatio.value).toBe('1:1')
    expect(wrapper.text()).toContain('creative.panel.estimatedCost:$0.480')
    await vi.advanceTimersByTimeAsync(300)
    expect(mockedStore.saveSetting).toHaveBeenLastCalledWith('creative:selection', expect.objectContaining({
      optionKey: creativeOptionKey(limitedModel), imageSize: '1K', aspectRatio: '1:1',
    }))
    await wrapper.get('button[title="creative.composer.send"]').trigger('click')
    expect(await submission()).toBe(true)
    const [form] = mockedApi.createCreativeRun.mock.calls[0]
    expect(form.get('model')).toBe(limitedModel.model)
    expect(form.get('image_size')).toBe('1K')
    expect(form.get('aspect_ratio')).toBe('1:1')
  })

  it('目录只开放已定价的 2K 时不凭 MJ 名称补出 1K 或 4K', async () => {
    mockedApi.getCreativeModels.mockResolvedValue([{ ...midjourney, image_sizes: ['2K'] }])
    const { wrapper, studio } = await mountComposer()
    await wrapper.get('button[title="creative.composer.params"]').trigger('click')
    expect(wrapper.findAll('.param-chip').map((item) => item.text())).toEqual(['2K', ...ratios])
    expect(studio.imageSize.value).toBe('2K')
    expect(wrapper.text()).toContain('creative.panel.estimatedCost:$0.960')
  })

  it.each([
    { name: '正常 MJ', model: 'MJ-v8.2', outputCount: 4, visible: true },
    { name: '服务端解析的模型别名', model: 'studio-image', outputCount: 4, visible: true },
    { name: '普通单张模型', model: 'gpt-image-2', outputCount: 1, visible: false },
    { name: 'MJ 名称实际映射至单张模型', model: 'MJ-alias', outputCount: 1, visible: false },
    { name: '旧服务 MJ 字段缺失', model: 'mj_custom', outputCount: undefined, visible: true },
    { name: '旧服务非 MJ 字段缺失', model: 'gpt-image-2', outputCount: undefined, visible: false },
  ])('$name 按服务端能力决定是否显示分辨率计费提示', async ({ model, outputCount, visible }) => {
    const option = { ...midjourney, model, output_count: outputCount }
    mockedApi.getCreativeModels.mockResolvedValue([option])
    const { wrapper } = await mountComposer(option)
    await wrapper.get('button[title="creative.composer.params"]').trigger('click')

    const notice = zhCreative.creative.panel.midjourneyResolutionHint
    expect(wrapper.findAll('p').some((item) => item.text() === notice)).toBe(visible)
    // 提示与三个尺寸按钮位于同一区域，不增加确认弹窗或改变选择能力。
    if (visible) {
      const noticeElement = wrapper.findAll('p').find((item) => item.text() === notice)!
      expect(Array.from(noticeElement.element.parentElement!.querySelectorAll('button')).map((button) => button.textContent?.trim())).toEqual(sizes)
    }
  })

  it.each(['zh', 'en'] as const)('%s 提示明确实际分辨率来源和所选档位计费', async (locale) => {
    language = locale
    const { wrapper } = await mountComposer()
    await wrapper.get('button[title="creative.composer.params"]').trigger('click')
    const expected = locale === 'zh'
      ? '实际分辨率由上游决定，按所选档位计费。'
      : 'Actual resolution is determined by the provider. Billing uses the selected size tier.'
    expect(wrapper.text()).toContain(expected)
  })
})
