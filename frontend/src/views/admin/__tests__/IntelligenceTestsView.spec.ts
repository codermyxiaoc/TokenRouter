import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import IntelligenceTestsView from '../IntelligenceTestsView.vue'
import { configFixture, runFixture } from '@/components/intelligence/__tests__/fixtures'
import type { ApiKey } from '@/types'

const { configs, save, run, remove, history, detail, preview, getAll, candidates, listKeys, appStore } = vi.hoisted(() => ({
  configs: vi.fn(), save: vi.fn(), run: vi.fn(), remove: vi.fn(), history: vi.fn(), detail: vi.fn(), preview: vi.fn(), getAll: vi.fn(), candidates: vi.fn(), listKeys: vi.fn(),
  appStore: { cachedPublicSettings: { intelligence_enabled: false }, showSuccess: vi.fn(), showError: vi.fn() },
}))
vi.mock('@/api/intelligence', () => ({ intelligenceAPI: { configs, save, run, remove, history, detail, preview } }))
vi.mock('@/api/admin/groups', () => ({ default: { getAll, getModelsListCandidates: candidates }, getAll, getModelsListCandidates: candidates }))
vi.mock('@/api/keys', () => ({ keysAPI: { list: listKeys } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => appStore }))
vi.mock('vue-i18n', async (importOriginal) => ({ ...(await importOriginal<typeof import('vue-i18n')>()), useI18n: () => ({ t: (key: string) => key }) }))

const mountPage = () => mount(IntelligenceTestsView, { global: { stubs: {
  AppLayout: { template: '<div><slot /></div>' },
  BaseDialog: { props: ['show', 'title'], template: '<div v-if="show" class="dialog"><h2>{{ title }}</h2><slot /><slot name="footer" /></div>' },
  Select: { props: ['id', 'modelValue', 'options', 'disabled'], emits: ['update:modelValue'], template: '<div :id="id" />' },
  Toggle: { props: ['id', 'modelValue', 'disabled'], emits: ['update:modelValue'], template: '<div :id="id" />' },
  IntelligenceRunDialog: true,
} } })
const clickText = async (wrapper: ReturnType<typeof mountPage>, key: string) => { await wrapper.findAll('button').find(button => button.text() === key)!.trigger('click'); await flushPromises() }
// 使用不具备调用能力的本地夹具，并显式模拟普通 Key 默认开启自动降级的真实接口字段。
const keyFixture = (overrides: Partial<ApiKey> = {}) => ({ id: 1, user_id: 1, name: 'Personal key', key: 'test-only-secret', group_id: 1, scope: 'personal', team_id: null, is_composite: false, smart_routing: false, composite_groups: [], smart_routing_group_ids: [], fallback_to_default_group_when_unavailable: true, status: 'active', quota: 0, quota_used: 0, expires_at: null, ...overrides }) as ApiKey
const keyPage = (items: ApiKey[], total = items.length, page = 1) => ({ items, total, page, page_size: 100, pages: Math.ceil(total / 100) })
const chooseGroup = async (wrapper: ReturnType<typeof mountPage>, id: number) => { wrapper.getComponent('#intelligence-group').vm.$emit('update:modelValue', id); await flushPromises() }
const chooseKey = async (wrapper: ReturnType<typeof mountPage>, id: number | null) => { wrapper.getComponent('#intelligence-key-select').vm.$emit('update:modelValue', id); await flushPromises() }

describe('管理员降智检测', () => {
  beforeEach(() => {
    configs.mockReset().mockResolvedValue([configFixture()]); save.mockReset().mockResolvedValue(configFixture()); run.mockReset().mockResolvedValue(runFixture({ status: 'queued' })); remove.mockReset(); history.mockReset(); detail.mockReset(); preview.mockReset()
    getAll.mockReset().mockResolvedValue([{ id: 1, name: 'Group A' }]); candidates.mockReset().mockResolvedValue(['model-a', 'model-b'])
    listKeys.mockReset().mockResolvedValue(keyPage([]))
    appStore.cachedPublicSettings.intelligence_enabled = false
    appStore.showSuccess.mockReset(); appStore.showError.mockReset()
  })
  it('开关关闭仍能配置但不能运行，打开页面不会自动提交测试', async () => {
    const wrapper = mountPage()
    await flushPromises()
    expect(wrapper.text()).toContain('intelligence.disabledHint')
    expect(wrapper.findAll('button').find(button => button.text() === 'intelligence.run')!.attributes('disabled')).toBeDefined()
    await clickText(wrapper, 'common.edit')
    expect(wrapper.find('#intelligence-config-form').exists()).toBe(true)
    expect(run).not.toHaveBeenCalled()
    wrapper.unmount()
  })
  it('编辑保留密钥发送空值，分组模型与类型只读，不自动替换已有密钥', async () => {
    listKeys.mockResolvedValue(keyPage([keyFixture()]))
    const wrapper = mountPage()
    await flushPromises()
    await clickText(wrapper, 'common.edit')
    expect((wrapper.get('#intelligence-api-key').element as HTMLInputElement).value).toBe('')
    for (const id of ['intelligence-group', 'intelligence-model', 'intelligence-benchmark']) expect(wrapper.getComponent(`#${id}`).props('disabled')).toBe(true)
    await wrapper.get('#intelligence-config-form').trigger('submit')
    await flushPromises()
    expect(save).toHaveBeenCalledWith(expect.objectContaining({ api_key: '', group_id: 1, model: 'model-a', benchmark: 'candy' }), 1)
    expect(wrapper.find('#intelligence-config-form').exists()).toBe(false)
    wrapper.unmount()
  })
  it('点击使用当前网站补全 /v1，不会提交表单或发起测试', async () => {
    const wrapper = mountPage()
    await flushPromises()
    await clickText(wrapper, 'intelligence.add')
    await clickText(wrapper, 'intelligence.useCurrentSite')
    expect((wrapper.get('#intelligence-base-url').element as HTMLInputElement).value).toBe(`${window.location.origin}/v1`)
    expect(save).not.toHaveBeenCalled()
    expect(run).not.toHaveBeenCalled()
    wrapper.unmount()
  })
  it('按个人分组分页列出开启或关闭自动降级的普通 Key，排除复合、智能路由和失效 Key', async () => {
    listKeys.mockResolvedValueOnce(keyPage([
      keyFixture(), keyFixture({ id: 2, is_composite: true }), keyFixture({ id: 3, smart_routing: true }),
      keyFixture({ id: 4, composite_groups: [{ group_id: 1, prefix: 'a' }] }),
      keyFixture({ id: 5, fallback_to_default_group_when_unavailable: false }),
      keyFixture({ id: 6, group_id: 2 }), keyFixture({ id: 7, status: 'inactive' }),
      keyFixture({ id: 8, expires_at: '2000-01-01T00:00:00Z' }), keyFixture({ id: 9, quota: 1, quota_used: 1 }),
      keyFixture({ id: 10, team_id: 1 }), keyFixture({ id: 11, smart_routing_group_ids: [1] }),
    ], 101)).mockResolvedValueOnce(keyPage([keyFixture({ id: 101, name: 'Last key', key: 'last-test-secret' })], 101, 2))
    const wrapper = mountPage()
    await flushPromises()
    await clickText(wrapper, 'intelligence.add')
    expect(listKeys).not.toHaveBeenCalled()
    await chooseGroup(wrapper, 1)
    expect(listKeys).toHaveBeenCalledTimes(2)
    expect(listKeys).toHaveBeenLastCalledWith(2, 100, expect.objectContaining({ scope: 'personal', group_id: 1, status: 'active' }), expect.objectContaining({ signal: expect.any(AbortSignal) }))
    expect(wrapper.getComponent('#intelligence-key-select').props('options')).toEqual([{ value: 1, label: 'Personal key (#1)' }, { value: 5, label: 'Personal key (#5)' }, { value: 101, label: 'Last key (#101)' }])
    await chooseKey(wrapper, 101)
    expect((wrapper.get('#intelligence-api-key').element as HTMLInputElement).value).toBe('last-test-secret')
    expect(wrapper.text()).not.toContain('last-test-secret')
    wrapper.getComponent('#intelligence-model').vm.$emit('update:modelValue', 'model-b')
    await wrapper.get('#intelligence-base-url').setValue('https://gateway.example/v1')
    await wrapper.get('#intelligence-config-form').trigger('submit')
    await flushPromises()
    expect(save).toHaveBeenCalledWith(expect.objectContaining({ api_key: 'last-test-secret', group_id: 1 }), undefined)
    wrapper.unmount()
  })
  it('切换分组立即清除旧 Key，并忽略迟到的旧分组响应', async () => {
    let resolveOld!: (value: ReturnType<typeof keyPage>) => void
    listKeys.mockResolvedValueOnce(keyPage([keyFixture()]))
      .mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve }))
      .mockResolvedValueOnce(keyPage([keyFixture({ id: 30, group_id: 3, name: 'Group 3' })]))
    const wrapper = mountPage()
    await flushPromises()
    await clickText(wrapper, 'intelligence.add')
    await chooseGroup(wrapper, 1)
    await chooseKey(wrapper, 1)
    await chooseGroup(wrapper, 2)
    expect((wrapper.get('#intelligence-api-key').element as HTMLInputElement).value).toBe('')
    expect(wrapper.getComponent('#intelligence-key-select').props('modelValue')).toBeNull()
    const oldSignal = listKeys.mock.calls[1][3].signal as AbortSignal
    await chooseGroup(wrapper, 3)
    expect(oldSignal.aborted).toBe(true)
    resolveOld(keyPage([keyFixture({ id: 20, group_id: 2 })]))
    await flushPromises()
    expect(wrapper.getComponent('#intelligence-key-select').props('options')).toEqual([{ value: 30, label: 'Group 3 (#30)' }])
    wrapper.unmount()
  })
  it('关闭并重新打开弹窗时忽略旧查询且不保留手填或选择的密钥', async () => {
    let resolveOld!: (value: ReturnType<typeof keyPage>) => void
    listKeys.mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve }))
      .mockResolvedValueOnce(keyPage([keyFixture({ id: 2, name: 'Fresh key' })]))
    const wrapper = mountPage()
    await flushPromises()
    await clickText(wrapper, 'common.edit')
    await wrapper.get('#intelligence-api-key').setValue('typed-test-secret')
    const oldSignal = listKeys.mock.calls[0][3].signal as AbortSignal
    await clickText(wrapper, 'common.cancel')
    expect(oldSignal.aborted).toBe(true)
    await clickText(wrapper, 'common.edit')
    resolveOld(keyPage([keyFixture()]))
    await flushPromises()
    expect((wrapper.get('#intelligence-api-key').element as HTMLInputElement).value).toBe('')
    expect(wrapper.getComponent('#intelligence-key-select').props('options')).toEqual([{ value: 2, label: 'Fresh key (#2)' }])
    wrapper.unmount()
  })
  it('查询失败可重试，手动改写后解除选择关联且刷新不会覆盖手填 Key', async () => {
    listKeys.mockRejectedValueOnce(new Error()).mockResolvedValue(keyPage([keyFixture()]))
    const wrapper = mountPage()
    await flushPromises()
    await clickText(wrapper, 'common.edit')
    expect(wrapper.text()).toContain('intelligence.loadApiKeysFailed')
    const refreshKeys = () => wrapper.get('#intelligence-config-form').findAll('button').find(button => button.text() === 'common.refresh')!
    await refreshKeys().trigger('click')
    await flushPromises()
    await chooseKey(wrapper, 1)
    await wrapper.get('#intelligence-api-key').setValue('manual-test-secret')
    expect(wrapper.getComponent('#intelligence-key-select').props('modelValue')).toBeNull()
    await refreshKeys().trigger('click')
    await flushPromises()
    expect((wrapper.get('#intelligence-api-key').element as HTMLInputElement).value).toBe('manual-test-secret')
    wrapper.unmount()
  })
  it('新配置默认仅手动60分钟，糖果只允许Responses', async () => {
    const wrapper = mountPage()
    await flushPromises()
    await clickText(wrapper, 'intelligence.add')
    expect(wrapper.getComponent('#intelligence-schedule').props('modelValue')).toBe(false)
    expect(wrapper.getComponent('#intelligence-protocol').props('modelValue')).toBe('responses')
    expect(wrapper.getComponent('#intelligence-protocol').props('disabled')).toBe(true)
    wrapper.getComponent('#intelligence-group').vm.$emit('update:modelValue', 1)
    await flushPromises()
    wrapper.getComponent('#intelligence-model').vm.$emit('update:modelValue', 'model-b')
    await wrapper.get('#intelligence-base-url').setValue('https://gateway.example/v1')
    await wrapper.get('#intelligence-api-key').setValue('test-secret-not-real')
    await wrapper.get('#intelligence-config-form').trigger('submit')
    await flushPromises()
    expect(save).toHaveBeenCalledWith(expect.objectContaining({ group_id: 1, model: 'model-b', schedule_enabled: false, interval_minutes: 60, api_key: 'test-secret-not-real' }), undefined)
    expect(run).not.toHaveBeenCalled()
    wrapper.unmount()
  })
  it('双击立即测试只有一条创建请求，运行中禁止再次提交', async () => {
    appStore.cachedPublicSettings.intelligence_enabled = true
    let resolve!: (value: unknown) => void
    run.mockImplementation(() => new Promise(done => { resolve = done }))
    const wrapper = mountPage()
    await flushPromises()
    const button = wrapper.findAll('button').find(button => button.text() === 'intelligence.run')!
    await button.trigger('click'); await button.trigger('click')
    expect(run).toHaveBeenCalledTimes(1)
    resolve(runFixture({ status: 'queued' }))
    await flushPromises()
    expect(button.attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })
})
