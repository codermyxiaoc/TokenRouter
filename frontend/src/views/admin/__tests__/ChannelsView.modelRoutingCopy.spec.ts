import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

import ChannelsView from '../ChannelsView.vue'

const { listChannels, createChannel, updateChannel, getGroups, getWebSearchEmulationConfig, syncPricingModels } = vi.hoisted(() => ({
  listChannels: vi.fn(),
  createChannel: vi.fn(),
  updateChannel: vi.fn(),
  getGroups: vi.fn(),
  getWebSearchEmulationConfig: vi.fn(),
  syncPricingModels: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    channels: {
      list: listChannels,
      create: createChannel,
      update: updateChannel,
      remove: vi.fn(),
      syncPricingModels,
      getModelDefaultPricing: vi.fn()
    },
    groups: {
      getAll: getGroups
    },
    settings: {
      getWebSearchEmulationConfig
    },
    accounts: {
      list: vi.fn().mockResolvedValue({ items: [], total: 0 }),
      getById: vi.fn()
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showInfo: vi.fn()
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

const BaseDialogStub = defineComponent({
  props: {
    show: {
      type: Boolean,
      default: false
    }
  },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

const SelectStub = defineComponent({
  props: {
    modelValue: {
      type: [String, Number],
      default: ''
    },
    options: {
      type: Array,
      default: () => []
    }
  },
  emits: ['update:modelValue'],
  template: `
    <div>
      <button
        v-for="option in options"
        :key="option.value"
        type="button"
        :data-option="option.value"
        @click="$emit('update:modelValue', option.value)"
      >
        {{ option.label }}
      </button>
    </div>
  `
})

function mountView() {
  return mount(ChannelsView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
        DataTable: { props: ['data'], template: '<div><div v-for="row in data" :key="row.id"><slot name="cell-actions" :row="row" /></div></div>' },
        Pagination: true,
        BaseDialog: BaseDialogStub,
        ConfirmDialog: true,
        EmptyState: true,
        Select: SelectStub,
        Icon: true,
        PlatformIcon: true,
        Toggle: true,
        PricingEntryCard: true
      }
    }
  })
}

describe('ChannelsView model routing copy', () => {
  it('模型同步结果绑定原平台对象，删除平台后不覆盖新索引', async () => {
    let resolve!: (value: { models: string[] }) => void
    syncPricingModels.mockReturnValueOnce(new Promise(done => { resolve = done }))
    const wrapper = mountView()
    await flushPromises()
    const vm = wrapper.vm as any
    const oldSection = { platform: 'openai', model_pricing: [] }
    const keptSection = { platform: 'cline', model_pricing: [] }
    vm.form.platforms = [oldSection, keptSection]
    const syncing = vm.syncLatestModels(0)
    vm.form.platforms.splice(0, 1)
    resolve({ models: ['gpt-example'] })
    await syncing
    expect(keptSection.model_pricing).toEqual([])
    expect(oldSection.model_pricing).toEqual([])
    wrapper.unmount()
  })

  it('TypeSafe 和视频无通用模型同步入口，Cline 和 Command Code 保留同步', async () => {
    const wrapper = mountView()
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.supportsChannelModelSync('typesafe')).toBe(false)
    expect(vm.supportsChannelModelSync('video')).toBe(false)
    expect(vm.supportsChannelModelSync('cline')).toBe(true)
    expect(vm.supportsChannelModelSync('command_code')).toBe(true)
    wrapper.unmount()
  })

  beforeEach(() => {
    listChannels.mockReset()
    createChannel.mockReset().mockResolvedValue({})
    updateChannel.mockReset().mockResolvedValue({})
    getGroups.mockReset()
    getWebSearchEmulationConfig.mockReset()
    listChannels.mockResolvedValue({ items: [], total: 0 })
    getGroups.mockResolvedValue([])
    getWebSearchEmulationConfig.mockResolvedValue({ enabled: false, providers: [] })
  })

  it.each([['video', false], ['video', true], ['video_token', false], ['video_token', true], ['video_per_request', false], ['video_per_request', true]] as const)('submits Video %s pricing, fallback-only=%s', async (billingMode, fallbackOnly) => {
    getGroups.mockResolvedValue([{ id: 77, name: 'VideoGroup', platform: 'video', rate_multiplier: 1 }])
    const wrapper = mountView()
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text().includes('admin.channels.createChannel'))!.trigger('click')
    await flushPromises()
    await wrapper.get('#channel-form input[type="text"]').setValue('Video channel')
    const toggle = wrapper.findAll('label').find(label => label.text().includes('admin.groups.platforms.video'))!
    await toggle.get('input[type="checkbox"]').setValue(true)
    await wrapper.findAll('button').find(button => button.text().includes('admin.groups.platforms.video'))!.trigger('click')
    const group = wrapper.findAll('label').find(label => label.text().includes('VideoGroup'))!
    await group.get('input[type="checkbox"]').setValue(true)
    expect(wrapper.text()).not.toContain('admin.channels.form.syncLatestModels')
    await wrapper.findAll('button').filter(button => button.text() === '+ common.add')[1].trigger('click')
    const card = wrapper.getComponent({ name: 'PricingEntryCard' })
    expect(card.props('entry').billing_mode).toBe('video_token')
    const videoPrices = fallbackOnly ? [] : [{ resolution: '480P', price: 0 }, { resolution: '2K', price: 15 }]
    card.vm.$emit('update', { ...card.props('entry'), models: ['video-model'], video_prices: videoPrices, video_image_input_pricing: { free_images: '1.5', price: '0.05' } })
    await wrapper.get('#channel-form').trigger('submit')
    await flushPromises()
    expect(createChannel).not.toHaveBeenCalled()
    // 非法兜底及已启用却空白的预扣都必须阻止提交。
    for (const invalid of [{ video_fallback_price: -1 }, { video_token_prepay: { price_per_second: '' } }]) {
      card.vm.$emit('update', { ...card.props('entry'), models: ['video-model'], video_image_input_pricing: null, video_fallback_price: null, video_token_prepay: null, ...invalid })
      await wrapper.get('#channel-form').trigger('submit')
      await flushPromises()
      expect(createChannel).not.toHaveBeenCalled()
    }
    card.vm.$emit('update', { ...card.props('entry'), billing_mode: billingMode, models: ['video-model'], video_prices: videoPrices, video_fallback_price: '0', video_token_prepay: { price_per_second: '0.3' }, video_image_input_pricing: { free_images: '2', price: '0.05' } })
    await wrapper.get('#channel-form').trigger('submit')
    await flushPromises()
    expect(createChannel).toHaveBeenCalledOnce()
    expect(createChannel.mock.calls[0][0].model_pricing[0]).toMatchObject({ platform: 'video', billing_mode: billingMode, video_prices: fallbackOnly ? [] : [
      { resolution: '480p', price: 0 }, { resolution: '2k', price: 15 },
    ], video_image_input_pricing: { free_images: 2, price: 0.05 }, video_fallback_price: 0, video_token_prepay: billingMode === 'video_token' ? { price_per_second: 0.3 } : null })
    wrapper.unmount()
  })

  // 使用接口回写后的响应重新打开表单，区分显式零价与关闭，不能只验证输入控件。
  it('saves, reloads and clears zero fallback and zero fixed prepayment', async () => {
    const channel = { id: 9, name: 'Video channel', status: 'active', group_ids: [77],
      model_pricing: [{ platform: 'video', models: ['video-model'], billing_mode: 'video_token',
        video_prices: [{ resolution: '720p', has_reference_video: false, price: 15 }, { resolution: '720P', has_reference_video: true, price: 15 }],
        price_multiplier: 4, video_fallback_price: 12 as number | null,
        video_token_prepay: { price_per_second: 0.3 } as { price_per_second: number } | null }] }
    listChannels.mockImplementation(async () => ({ items: [channel], total: 1 }))
    updateChannel.mockImplementation(async (_id, request) => {
      channel.model_pricing = JSON.parse(JSON.stringify(request.model_pricing))
      return channel
    })
    getGroups.mockResolvedValue([{ id: 77, name: 'VideoGroup', platform: 'video', rate_multiplier: 3 }])
    const wrapper = mountView()
    await flushPromises()
    const open = async () => {
      await wrapper.findAll('button').find(button => button.text() === 'common.edit')!.trigger('click')
      await flushPromises()
      return wrapper.getComponent({ name: 'PricingEntryCard' })
    }
    let card = await open()
    expect(card.props('entry').video_prices).toEqual([{ resolution: '720p', price: 15 }])
    card.vm.$emit('update', { ...card.props('entry'), video_fallback_price: '0', video_token_prepay: { price_per_second: '0' } })
    await wrapper.get('#channel-form').trigger('submit')
    await flushPromises()
    expect(updateChannel.mock.calls[0][1].model_pricing[0]).toMatchObject({
      price_multiplier: 4, video_fallback_price: 0, video_token_prepay: { price_per_second: 0 },
    })
    expect(updateChannel.mock.calls[0][1].model_pricing[0].video_prices).toEqual([{ resolution: '720p', price: 15 }])
    card = await open()
    expect(card.props('entry').video_prices).toEqual([{ resolution: '720p', price: 15 }])
    expect(card.props('entry')).toMatchObject({ video_fallback_price: 0, video_token_prepay: { price_per_second: 0 } })
    card.vm.$emit('update', { ...card.props('entry'), video_fallback_price: '', video_token_prepay: null })
    await wrapper.get('#channel-form').trigger('submit')
    await flushPromises()
    expect(updateChannel.mock.calls[1][1].model_pricing[0]).toMatchObject({ video_fallback_price: null, video_token_prepay: null })
    card = await open()
    expect(card.props('entry')).toMatchObject({ video_fallback_price: null, video_token_prepay: null })
    wrapper.unmount()
  })

  // 用户价卡与旧账号统计规则一起保存时，固定附加费、兜底及预扣不进入账号统计合同。
  it('keeps account statistics pricing independent from Video user-only rules', async () => {
    const matrix = [{ resolution: '720p', has_reference_video: false, price: 8 }]
    listChannels.mockResolvedValue({ items: [{ id: 9, name: 'Video channel', status: 'active', group_ids: [77],
      model_pricing: [{ platform: 'video', models: ['user-video'], billing_mode: 'video_token', video_prices: [],
        price_multiplier: 7, video_fallback_price: 15, video_token_prepay: { price_per_second: 0.3 },
        video_image_input_pricing: { free_images: 5, price: 0.15 } }],
      account_stats_pricing_rules: [{ name: 'Provider cost', group_ids: [77], account_ids: [], pricing: [{
        platform: 'video', models: ['provider-video'], billing_mode: 'video_token', video_prices: matrix, price_multiplier: 2,
      }] }],
    }], total: 1 })
    getGroups.mockResolvedValue([{ id: 77, name: 'VideoGroup', platform: 'video', rate_multiplier: 3 }])
    const wrapper = mountView()
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === 'common.edit')!.trigger('click')
    await flushPromises()
    const cards = wrapper.findAllComponents({ name: 'PricingEntryCard' })
    expect(cards).toHaveLength(2)
    expect(cards.find(card => card.props('entry').models[0] === 'provider-video')!.props('hideVideoUserPricing')).toBe(true)
    await wrapper.get('#channel-form').trigger('submit')
    await flushPromises()
    expect(updateChannel).toHaveBeenCalledOnce()
    const request = updateChannel.mock.calls[0][1]
    expect(request.model_pricing[0]).toMatchObject({ price_multiplier: 7, video_fallback_price: 15,
      video_token_prepay: { price_per_second: 0.3 }, video_image_input_pricing: { free_images: 5, price: 0.15 } })
    const statistics = request.account_stats_pricing_rules[0].pricing[0]
    expect(statistics).toMatchObject({ price_multiplier: 2, video_prices: [{ resolution: '720p', price: 8 }] })
    for (const field of ['video_fallback_price', 'video_token_prepay', 'video_image_input_pricing']) expect(statistics).not.toHaveProperty(field)
    wrapper.unmount()
  })

  // 编辑回读保持固定单价与免费张数，关闭时明确提交 null，不能留下旧规则。
  it('loads saved Video image pricing and explicitly clears it when disabled', async () => {
    const rule = { free_images: 3, price: 0.05 }
    listChannels.mockResolvedValue({ items: [{ id: 9, name: 'Video channel', status: 'active', group_ids: [77],
      model_pricing: [{ platform: 'video', models: ['video-model'], billing_mode: 'video_token',
        video_prices: [{ resolution: '720p', has_reference_video: false, price: 15 }], video_image_input_pricing: rule, video_fallback_price: 12, video_token_prepay: { price_per_second: 0.3 } }] }], total: 1 })
    getGroups.mockResolvedValue([{ id: 77, name: 'VideoGroup', platform: 'video', rate_multiplier: 2 }])
    const wrapper = mountView()
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === 'common.edit')!.trigger('click')
    await flushPromises()
    const card = wrapper.getComponent({ name: 'PricingEntryCard' })
    expect(card.props('entry').video_image_input_pricing).toEqual(rule)
    expect(card.props('entry').video_image_input_pricing).not.toBe(rule)
    expect(card.props('entry').video_fallback_price).toBe(12)
    expect(card.props('entry').video_token_prepay).toEqual({ price_per_second: 0.3 })
    card.vm.$emit('update', { ...card.props('entry'), video_image_input_pricing: null, video_fallback_price: null, video_token_prepay: null })
    await wrapper.get('#channel-form').trigger('submit')
    await flushPromises()
    expect(updateChannel).toHaveBeenCalledOnce()
    expect(updateChannel.mock.calls[0][1].model_pricing[0].video_image_input_pricing).toBeNull()
    expect(updateChannel.mock.calls[0][1].model_pricing[0]).toMatchObject({ video_fallback_price: null, video_token_prepay: null })
    expect(rule).toEqual({ free_images: 3, price: 0.05 })
    wrapper.unmount()
  })

  it('updates the restriction stage hint for all three pricing bases', async () => {
    const wrapper = mountView()
    await flushPromises()

    const createButton = wrapper.findAll('button').find(button => button.text().includes('admin.channels.createChannel'))
    expect(createButton).toBeTruthy()
    await createButton!.trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-testid="billing-model-source-hint"]').text()).toBe('admin.channels.form.billingModelSourceHintChannelMapped')

    await wrapper.get('[data-option="requested"]').trigger('click')
    expect(wrapper.get('[data-testid="billing-model-source-hint"]').text()).toBe('admin.channels.form.billingModelSourceHintRequested')

    await wrapper.get('[data-option="upstream"]').trigger('click')
    expect(wrapper.get('[data-testid="billing-model-source-hint"]').text()).toBe('admin.channels.form.billingModelSourceHintUpstream')
  })

  it('shows the complete channel and account mapping chain', async () => {
    const wrapper = mountView()
    await flushPromises()
    const createButton = wrapper.findAll('button').find(button => button.text().includes('admin.channels.createChannel'))
    await createButton!.trigger('click')
    await flushPromises()

    const anthropicToggle = wrapper
      .findAll('label')
      .find(label => label.text().includes('admin.groups.platforms.anthropic'))
    expect(anthropicToggle).toBeTruthy()
    await anthropicToggle!.get('input[type="checkbox"]').trigger('change')

    const anthropicTab = wrapper
      .findAll('button')
      .find(button => button.text().includes('admin.groups.platforms.anthropic'))
    expect(anthropicTab).toBeTruthy()
    await anthropicTab!.trigger('click')

    expect(wrapper.get('[data-testid="channel-model-mapping-hint"]').text()).toBe('admin.channels.form.modelMappingHint')
  })
})
