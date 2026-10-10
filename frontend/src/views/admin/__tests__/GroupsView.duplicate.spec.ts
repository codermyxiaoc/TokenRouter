import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { AdminGroup } from '@/types'
import GroupsView from '@/views/admin/GroupsView.vue'

const {
  listGroups,
  duplicateGroup,
  updateGroup,
  getAll,
  getModelsListCandidates,
  getUsageSummary,
  getCapacitySummary,
  getLiveCapability,
  showSuccess,
  showError
} = vi.hoisted(() => ({
  listGroups: vi.fn(),
  duplicateGroup: vi.fn(),
  updateGroup: vi.fn(),
  getAll: vi.fn(),
  getModelsListCandidates: vi.fn(),
  getUsageSummary: vi.fn(),
  getCapacitySummary: vi.fn(),
  getLiveCapability: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    groups: {
      list: listGroups,
      duplicate: duplicateGroup,
      getModelsListCandidates,
      getUsageSummary,
      getCapacitySummary,
      getLiveCapability,
      getAll,
      create: vi.fn(),
      update: updateGroup,
      delete: vi.fn(),
      updateSortOrder: vi.fn()
    },
    accounts: {
      list: vi.fn(),
      getById: vi.fn()
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess, showError })
}))

vi.mock('@/stores/onboarding', () => ({
  useOnboardingStore: () => ({
    isCurrentStep: vi.fn(() => false),
    nextStep: vi.fn()
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

const sourceGroup: AdminGroup = {
  id: 42,
  name: 'Primary',
  description: null,
  platform: 'openai',
  scheduler_type: 'advanced',
  rate_multiplier: 1,
  rpm_limit: 0,
  is_exclusive: false,
  status: 'active',
  subscription_type: 'standard',
  daily_limit_usd: null,
  weekly_limit_usd: null,
  monthly_limit_usd: null,
  allow_image_generation: false,
  allow_batch_image_generation: false,
  image_rate_independent: false,
  image_rate_multiplier: 1,
  batch_image_discount_multiplier: 0.5,
  batch_image_hold_multiplier: 0.6,
  image_price_1k: null,
  image_price_2k: null,
  image_price_4k: null,
  video_rate_independent: false,
  video_rate_multiplier: 1,
  video_price_480p: null,
  video_price_720p: null,
  video_price_1080p: null,
  web_search_price_per_call: null,
  peak_rate_enabled: false,
  peak_start: '',
  peak_end: '',
  peak_rate_multiplier: 1,
  claude_code_only: false,
  fallback_group_id: null,
  fallback_group_id_on_invalid_request: null,
  allow_messages_dispatch: false,
  allow_live: false,
  default_mapped_model: '',
  messages_dispatch_model_config: undefined,
  require_oauth_only: false,
  require_privacy_set: false,
  created_at: '2026-07-16T00:00:00Z',
  updated_at: '2026-07-16T00:00:00Z',
  model_routing: null,
  model_routing_enabled: false,
  mcp_xml_inject: true,
  supported_model_scopes: [],
  account_count: 1,
  active_account_count: 1,
  rate_limited_account_count: 0,
  models_list_config: undefined,
  sort_order: 10
}

const AppLayoutStub = defineComponent({
  template: '<main><slot /></main>'
})

const TablePageLayoutStub = defineComponent({
  template: '<section><slot name="filters" /><slot name="table" /><slot name="pagination" /></section>'
})

const DataTableStub = defineComponent({
  props: {
    data: { type: Array, default: () => [] },
    columns: { type: Array, default: () => [] },
    loading: { type: Boolean, default: false }
  },
  template: '<div><div v-for="row in data" :key="row.id"><slot name="cell-actions" :row="row" /></div></div>'
})

const BaseDialogStub = defineComponent({
  props: {
    show: { type: Boolean, default: false }
  },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

function mountView() {
  return mount(GroupsView, {
    global: {
      stubs: {
        Teleport: true,
        AppLayout: AppLayoutStub,
        TablePageLayout: TablePageLayoutStub,
        DataTable: DataTableStub,
        Pagination: true,
        BaseDialog: BaseDialogStub,
        ConfirmDialog: true,
        EmptyState: true,
        Select: true,
        PlatformIcon: true,
        Icon: true,
        GroupCapacityBadge: true,
        GroupRateMultipliersModal: true,
        GroupRPMOverridesModal: true,
        VueDraggable: true
      }
    }
  })
}

describe('GroupsView duplicate action', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.spyOn(console, 'error').mockImplementation(() => {})
    for (const fn of [
      listGroups,
      duplicateGroup,
      updateGroup,
      getAll,
      getModelsListCandidates,
      getUsageSummary,
      getCapacitySummary,
      getLiveCapability,
      showSuccess,
      showError
    ]) {
      fn.mockReset()
    }
    getAll.mockResolvedValue([])

    listGroups.mockResolvedValue({
      items: [sourceGroup],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1
    })
    duplicateGroup.mockResolvedValue({
      ...sourceGroup,
      id: 43,
      name: 'Primary (Copy)',
      status: 'inactive'
    })
    getModelsListCandidates.mockResolvedValue([])
    getUsageSummary.mockResolvedValue([])
    getCapacitySummary.mockResolvedValue([])
    getLiveCapability.mockResolvedValue({ supported: false, reason: 'test server unsupported' })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  // Video 编辑的价卡保持百万 Token 单位，文本高级调度和协议不出现。
  it.each([['video', false], ['video', true], ['video_token', false], ['video_token', true], ['video_per_request', false], ['video_per_request', true]] as const)('round-trips Video %s pricing, fallback-only=%s', async (billingMode, fallbackOnly) => {
    const videoGroup = { ...sourceGroup, platform: 'video', scheduler_type: 'basic', allowed_client_protocols: [],
      video_rate_independent: true, video_rate_multiplier: 1.25,
      model_pricing: [{ platform: 'video', models: ['seedance-v2'], model_details: { 'seedance-v2': { enabled: false, description: '5–30 秒，草稿' }, removed: { enabled: true, description: '失效模型' } }, billing_mode: billingMode,
        video_image_input_pricing: { free_images: 0, price: 0.05 }, video_fallback_price: 0, video_token_prepay: billingMode === 'video_token' ? { price_per_second: 0.3 } : null,
        video_prices: fallbackOnly ? [] : [{ resolution: '480p', has_reference_video: false, price: 0 }, { resolution: '480P', has_reference_video: true, price: 0 }, { resolution: '720p', has_reference_video: true, price: 15 }] }] }
    listGroups.mockResolvedValue({ items: [videoGroup], total: 1, page: 1, page_size: 20, pages: 1 })
    updateGroup.mockResolvedValue(videoGroup)
    const wrapper = mountView()
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === 'common.edit')!.trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-group-tab-button="protocol"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('admin.groups.form.schedulerType')
    await wrapper.get('[data-group-tab-button="pricing"]').trigger('click')
    const card = wrapper.getComponent({ name: 'PricingEntryCard' })
    expect(card.props('platform')).toBe('video')
    expect(card.props('modelDetailsScope')).toBe('group')
    expect(card.props('entry').model_details).toEqual({ 'seedance-v2': { enabled: false, description: '5–30 秒，草稿' } })
    const expectedPrices = fallbackOnly ? [] : [{ resolution: '480p', price: 0 }, { resolution: '720p', price: 15 }]
    expect(card.props('entry').video_prices).toEqual(expectedPrices)
    expect(card.props('entry').video_image_input_pricing).toEqual({ free_images: 0, price: 0.05 })
    expect(card.props('entry')).toMatchObject({ video_fallback_price: 0, video_token_prepay: billingMode === 'video_token' ? { price_per_second: 0.3 } : null })
    await wrapper.get('#edit-group-form').trigger('submit')
    await flushPromises()
    expect(showError).not.toHaveBeenCalled()
    expect(updateGroup).toHaveBeenCalledOnce()
    const request = updateGroup.mock.calls[0][1]
    expect(request).toMatchObject({ scheduler_type: 'basic', video_rate_independent: true, video_rate_multiplier: 1.25, allowed_client_protocols: [] })
    expect(request.model_pricing[0].video_prices).toEqual(expectedPrices)
    expect(request.model_pricing[0].model_details).toEqual({ 'seedance-v2': { enabled: false, description: '5–30 秒，草稿' } })
    expect(request.model_pricing[0].video_image_input_pricing).toEqual({ free_images: 0, price: 0.05 })
    expect(request.model_pricing[0]).toMatchObject({ billing_mode: billingMode, video_fallback_price: 0, video_token_prepay: billingMode === 'video_token' ? { price_per_second: 0.3 } : null })
    wrapper.unmount()
  })

  // 纯说明条目保存后恢复继承会删除空卡，不能拦截已有渠道定价。
  it.each(['video', 'video_token', 'video_per_request'] as const)('分组 %s 可只设置说明而不增加独立价格', async billingMode => {
    const details = { 'seedance-v2': { enabled: false, description: '临时关闭的草稿' } }
    const videoGroup = { ...sourceGroup, platform: 'video', scheduler_type: 'basic', allowed_client_protocols: [],
      model_pricing: [{ platform: 'video', models: ['seedance-v2'], billing_mode: billingMode, model_details: details }] }
    listGroups.mockImplementation(async () => ({ items: [videoGroup], total: 1, page: 1, page_size: 20, pages: 1 }))
    updateGroup.mockImplementation(async (_id, request) => {
      videoGroup.model_pricing = JSON.parse(JSON.stringify(request.model_pricing))
      return videoGroup
    })
    const wrapper = mountView()
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === 'common.edit')!.trigger('click')
    await flushPromises()
    await wrapper.get('[data-group-tab-button="pricing"]').trigger('click')
    const card = wrapper.getComponent({ name: 'PricingEntryCard' })
    const original = { ...card.props('entry') }
    card.vm.$emit('update', { ...original, video_fallback_price: -1 })
    await wrapper.get('#edit-group-form').trigger('submit')
    await flushPromises()
    expect(updateGroup).not.toHaveBeenCalled()
    card.vm.$emit('update', original)
    await wrapper.vm.$nextTick()
    await wrapper.get('#edit-group-form').trigger('submit')
    await flushPromises()
    expect(updateGroup).toHaveBeenCalledOnce()
    expect(updateGroup.mock.calls[0][1].model_pricing[0]).toMatchObject({
      model_details: details, billing_mode: billingMode, video_prices: [], video_fallback_price: null,
      video_token_prepay: null, video_image_input_pricing: null, price_multiplier: null,
      input_price: null, output_price: null, per_request_price: null, intervals: [], time_pricing: null,
    })
    await wrapper.findAll('button').find(button => button.text() === 'common.edit')!.trigger('click')
    await flushPromises()
    await wrapper.get('[data-group-tab-button="pricing"]').trigger('click')
    await wrapper.get('[data-testid="inherit-model-detail"]').trigger('click')
    expect(wrapper.findComponent({ name: 'PricingEntryCard' }).exists()).toBe(false)
    await wrapper.get('#edit-group-form').trigger('submit')
    await flushPromises()
    expect(updateGroup.mock.calls[1][1].model_pricing).toEqual([])
    wrapper.unmount()
  })

  // 实际表单保存再重开，确保“显式关闭”和“恢复继承”不被同一空值覆盖。
  it('保存逐模型详情并可重新打开、关闭及恢复渠道继承', async () => {
    const details = { 'seedance-v2': { enabled: true, description: '480p，5–30 秒' }, kling: { enabled: false, description: '1080p 草稿' } }
    const videoGroup = { ...sourceGroup, platform: 'video', scheduler_type: 'basic', allowed_client_protocols: [],
      model_pricing: [{ platform: 'video', models: ['seedance-v2', 'kling'], billing_mode: 'video_token', video_fallback_price: 12,
        model_details: details as typeof details | undefined }] }
    listGroups.mockImplementation(async () => ({ items: [videoGroup], total: 1, page: 1, page_size: 20, pages: 1 }))
    updateGroup.mockImplementation(async (_id, request) => {
      videoGroup.model_pricing = JSON.parse(JSON.stringify(request.model_pricing))
      return videoGroup
    })
    const wrapper = mountView()
    await flushPromises()
    const open = async () => {
      await wrapper.findAll('button').find(button => button.text() === 'common.edit')!.trigger('click')
      await flushPromises()
      await wrapper.get('[data-group-tab-button="pricing"]').trigger('click')
      return wrapper.getComponent({ name: 'PricingEntryCard' })
    }
    let card = await open()
    expect(card.props('entry').model_details).toEqual(details)
    const updated = { 'seedance-v2': { enabled: false, description: '480p，5–30 秒' }, kling: { enabled: true, description: '1080p 新说明' } }
    card.vm.$emit('update', { ...card.props('entry'), model_details: updated })
    await wrapper.get('#edit-group-form').trigger('submit')
    await flushPromises()
    expect(updateGroup.mock.calls[0][1].model_pricing[0]).toMatchObject({ video_fallback_price: 12, model_details: updated })
    card = await open()
    expect(card.props('entry').model_details).toEqual(updated)
    card.vm.$emit('update', { ...card.props('entry'), model_details: undefined })
    await wrapper.get('#edit-group-form').trigger('submit')
    await flushPromises()
    expect(updateGroup.mock.calls[1][1].model_pricing[0].model_details).toBeUndefined()
    card = await open()
    expect(card.props('entry').model_details).toBeUndefined()
    expect(card.props('entry').video_fallback_price).toBe(12)
    wrapper.unmount()
  })

  // 分组价卡也必须拒绝非法数值，并在重新读取接口后区分零预扣与关闭状态。
  it('validates, saves and reloads zero or cleared Video user pricing', async () => {
    const videoGroup = { ...sourceGroup, platform: 'video', scheduler_type: 'basic', allowed_client_protocols: [],
      video_rate_independent: true, video_rate_multiplier: 4,
      model_pricing: [{ platform: 'video', models: ['seedance-v2'], billing_mode: 'video_token', price_multiplier: 2,
        video_fallback_price: 15 as number | null,
        video_token_prepay: { price_per_second: 0.3 } as { price_per_second: number } | null,
        video_prices: [{ resolution: '720p', has_reference_video: false, price: 15 }] }] }
    listGroups.mockImplementation(async () => ({ items: [videoGroup], total: 1, page: 1, page_size: 20, pages: 1 }))
    updateGroup.mockImplementation(async (_id, request) => {
      videoGroup.model_pricing = JSON.parse(JSON.stringify(request.model_pricing))
      return videoGroup
    })
    const wrapper = mountView()
    await flushPromises()
    const open = async () => {
      await wrapper.findAll('button').find(button => button.text() === 'common.edit')!.trigger('click')
      await flushPromises()
      await wrapper.get('[data-group-tab-button="pricing"]').trigger('click')
      return wrapper.getComponent({ name: 'PricingEntryCard' })
    }
    let card = await open()
    const original = { ...card.props('entry') }
    for (const invalid of [
      { video_fallback_price: NaN },
      { video_token_prepay: { price_per_second: -1 } },
      { video_token_prepay: { price_per_second: '' } },
    ]) {
      card.vm.$emit('update', { ...original, ...invalid })
      await wrapper.get('#edit-group-form').trigger('submit')
      await flushPromises()
      expect(updateGroup).not.toHaveBeenCalled()
    }
    // 负数由数字输入框原生有效性拦截，其余非法值由业务校验提示。
    expect(showError).toHaveBeenCalledWith('admin.channels.videoPricing.fallbackInvalid')
    expect(showError).toHaveBeenCalledWith('admin.channels.videoTokenPrepay.invalid')
    card.vm.$emit('update', { ...original, video_fallback_price: '0', video_token_prepay: { price_per_second: '0' } })
    await wrapper.get('#edit-group-form').trigger('submit')
    await flushPromises()
    expect(updateGroup.mock.calls[0][1].model_pricing[0]).toMatchObject({ price_multiplier: 2,
      video_fallback_price: 0, video_token_prepay: { price_per_second: 0 } })
    card = await open()
    expect(card.props('entry')).toMatchObject({ video_fallback_price: 0, video_token_prepay: { price_per_second: 0 } })
    card.vm.$emit('update', { ...card.props('entry'), video_fallback_price: '', video_token_prepay: null })
    await wrapper.get('#edit-group-form').trigger('submit')
    await flushPromises()
    expect(updateGroup.mock.calls[1][1].model_pricing[0]).toMatchObject({ video_fallback_price: null, video_token_prepay: null })
    card = await open()
    expect(card.props('entry')).toMatchObject({ video_fallback_price: null, video_token_prepay: null })
    wrapper.unmount()
  })

  it('duplicates the selected group, reports success, and refreshes the list', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.find('[data-testid="group-duplicate"]').exists()).toBe(false)
    await wrapper.get('[data-testid="group-more"]').trigger('click')
    await wrapper.get('[data-testid="group-duplicate"]').trigger('click')
    await flushPromises()

    expect(duplicateGroup).toHaveBeenCalledTimes(1)
    expect(duplicateGroup).toHaveBeenCalledWith(42)
    expect(showSuccess).toHaveBeenCalledWith('admin.groups.duplicateSuccess')
    expect(listGroups).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[role="menu"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('ignores repeated clicks while the duplicate request is in flight', async () => {
    let resolveDuplicate!: (value: AdminGroup) => void
    duplicateGroup.mockImplementationOnce(
      () => new Promise<AdminGroup>((resolve) => { resolveDuplicate = resolve })
    )
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-testid="group-more"]').trigger('click')
    const button = wrapper.get('[data-testid="group-duplicate"]')
    void button.trigger('click')
    void button.trigger('click')
    await wrapper.vm.$nextTick()

    expect(duplicateGroup).toHaveBeenCalledTimes(1)
    // 请求期间重新打开菜单仍禁用复制，避免重复提交。
    await wrapper.get('[data-testid="group-more"]').trigger('click')
    const pendingButton = wrapper.get('[data-testid="group-duplicate"]')
    expect(pendingButton.attributes('disabled')).toBeDefined()
    expect(pendingButton.attributes('title')).toBe('admin.groups.duplicating')

    resolveDuplicate({ ...sourceGroup, id: 43, name: 'Primary (Copy)', status: 'inactive' })
    await flushPromises()
    expect(wrapper.get('[data-testid="group-duplicate"]').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('shows the API error and restores the action when duplication fails', async () => {
    duplicateGroup.mockRejectedValueOnce(new Error('duplicate failed'))
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-testid="group-more"]').trigger('click')
    await wrapper.get('[data-testid="group-duplicate"]').trigger('click')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('duplicate failed')
    await wrapper.get('[data-testid="group-more"]').trigger('click')
    expect(wrapper.get('[data-testid="group-duplicate"]').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('does not report a successful duplicate as failed when the refresh fails', async () => {
    listGroups
      .mockResolvedValueOnce({
        items: [sourceGroup],
        total: 1,
        page: 1,
        page_size: 20,
        pages: 1
      })
      .mockRejectedValueOnce(new Error('refresh failed'))
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-testid="group-more"]').trigger('click')
    await wrapper.get('[data-testid="group-duplicate"]').trigger('click')
    await flushPromises()

    expect(showSuccess).toHaveBeenCalledWith('admin.groups.duplicateSuccess')
    expect(showError).toHaveBeenCalledWith('admin.groups.failedToLoad')
    expect(showError).not.toHaveBeenCalledWith('admin.groups.duplicateFailed')
    wrapper.unmount()
  })

  it('shows the standardized API message when updating a group fails', async () => {
    updateGroup.mockRejectedValueOnce({
      status: 409,
      code: 409,
      message: 'group name already exists',
      reason: 'GROUP_EXISTS'
    })
    const wrapper = mountView()
    await flushPromises()

    const editButton = wrapper.findAll('button').find((button) => button.text() === 'common.edit')
    expect(editButton).toBeTruthy()
    await editButton!.trigger('click')
    await flushPromises()
    await wrapper.get('#edit-group-form').trigger('submit')
    await flushPromises()

    expect(updateGroup).toHaveBeenCalledTimes(1)
    expect(showError).toHaveBeenCalledWith('group name already exists')
    wrapper.unmount()
  })
})
