import { mount, flushPromises } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import RequestDataCacheSettingsTab from '../RequestDataCacheSettingsTab.vue'

const api = vi.hoisted(() => ({ get: vi.fn(), update: vi.fn(), clear: vi.fn() }))
const groups = vi.hoisted(() => ({ getAllIncludingInactive: vi.fn() }))
const { showSuccess, showError } = vi.hoisted(() => ({ showSuccess: vi.fn(), showError: vi.fn() }))
vi.mock('@/api/admin/requestDataCache', () => ({ requestDataCacheAPI: api }))
vi.mock('@/api/admin/groups', () => ({ groupsAPI: groups }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess, showError }) }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key }),
}))

const scope = (extra: Record<string, unknown> = {}) => ({
  enabled: false, ttl_minutes: 1440, all_groups: true, group_ids: [], include_ungrouped: true, ...extra,
})
const view = (normal = scope(), error = scope({ enabled: true, ttl_minutes: 10080 })) => ({
  settings: { normal, error }, stats: { normal: 3, error: 5 },
})

// Toggle 用按钮替身，便于直接切换；确认框只在打开时渲染确认按钮。
const stubs = {
  Icon: true, Select: true,
  GroupSelector: { props: ['disabled'], template: '<div data-testid="group-selector" :data-disabled="String(disabled)" />' },
  Toggle: { props: ['modelValue'], emits: ['update:modelValue'], template: '<button class="toggle" @click="$emit(\'update:modelValue\', !modelValue)" />' },
  ConfirmDialog: { props: ['show'], emits: ['confirm', 'cancel'], template: '<div v-if="show"><button data-testid="confirm-ok" @click="$emit(\'confirm\')">ok</button></div>' },
}

async function render() {
  const wrapper = mount(RequestDataCacheSettingsTab, { global: { stubs } })
  await flushPromises()
  return wrapper
}

describe('数据缓存设置', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.get.mockResolvedValue(view())
    groups.getAllIncludingInactive.mockResolvedValue([])
  })

  it('普通请求与错误请求分开展示条数，开启普通请求只改普通配置', async () => {
    api.update.mockImplementation(async (s) => ({ settings: s, stats: { normal: 3, error: 5 } }))
    const wrapper = await render()
    expect(wrapper.get('[data-testid="data-cache-normal-count"]').text()).toContain('admin.requestDataCacheSettings.cachedCount')
    // 普通请求默认关闭，不显示缓存时间；错误请求已开启，显示缓存时间。
    expect(wrapper.find('[data-testid="data-cache-normal-ttl"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="data-cache-error-ttl"]').exists()).toBe(true)

    await wrapper.get('[data-testid="data-cache-normal"] .toggle').trigger('click')
    await wrapper.get('[data-testid="data-cache-save"]').trigger('click')
    await flushPromises()
    expect(api.update).toHaveBeenCalledWith({
      normal: scope({ enabled: true }),
      error: scope({ enabled: true, ttl_minutes: 10080 }),
    })
    expect(showSuccess).toHaveBeenCalled()
  })

  it('按分组缓存但未选择分组且不含未分组请求时禁止保存', async () => {
    api.get.mockResolvedValue(view(scope({ enabled: true, all_groups: false, include_ungrouped: false })))
    const wrapper = await render()
    expect(wrapper.text()).toContain('admin.requestDataCacheSettings.groupsRequired')
    expect(wrapper.get('[data-testid="data-cache-save"]').attributes('disabled')).toBeDefined()
  })

  it('保存请求进行中锁定分组勾选，避免回包覆盖保存期间的勾选', async () => {
    api.get.mockResolvedValue(view(scope({ enabled: true, all_groups: false, group_ids: [1] })))
    let resolveUpdate: (v: unknown) => void = () => {}
    api.update.mockImplementation(() => new Promise((r) => { resolveUpdate = r }))
    const wrapper = await render()
    const selector = () => wrapper.get('[data-testid="data-cache-normal"] [data-testid="group-selector"]')
    expect(selector().attributes('data-disabled')).toBe('false')
    await wrapper.get('[data-testid="data-cache-save"]').trigger('click')
    expect(selector().attributes('data-disabled')).toBe('true')
    resolveUpdate(view(scope({ enabled: true, all_groups: false, group_ids: [1] })))
    await flushPromises()
    expect(selector().attributes('data-disabled')).toBe('false')
  })

  it('手动清除错误请求缓存只清除错误类别并刷新条数', async () => {
    api.clear.mockResolvedValue({ ...view(), stats: { normal: 3, error: 0 }, deleted: 5 })
    const wrapper = await render()
    await wrapper.get('[data-testid="data-cache-error-clear"]').trigger('click')
    await wrapper.get('[data-testid="confirm-ok"]').trigger('click')
    await flushPromises()
    expect(api.clear).toHaveBeenCalledWith('error')
    expect(api.update).not.toHaveBeenCalled()
    expect(showSuccess).toHaveBeenCalled()
  })
})
