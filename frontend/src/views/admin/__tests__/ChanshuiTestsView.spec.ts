import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import ChanshuiTestsView from '../ChanshuiTestsView.vue'
import type { ChanshuiConfig, ChanshuiRun } from '@/api/admin/chanshui'

const { configs, targets, save, run, remove, history, detail, appStore, adminSettingsStore, replace } = vi.hoisted(() => ({
  configs: vi.fn(), targets: vi.fn(), save: vi.fn(), run: vi.fn(), remove: vi.fn(), history: vi.fn(), detail: vi.fn(),
  appStore: { showSuccess: vi.fn(), showError: vi.fn() },
  adminSettingsStore: { chanshuiEnabled: false },
  replace: vi.fn(),
}))
vi.mock('@/api/admin/chanshui', async importOriginal => ({ ...(await importOriginal<typeof import('@/api/admin/chanshui')>()), chanshuiAPI: { configs, targets, save, run, remove, history, detail } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => appStore }))
vi.mock('@/stores/adminSettings', () => ({ useAdminSettingsStore: () => adminSettingsStore }))
vi.mock('vue-router', () => ({ useRouter: () => ({ replace }) }))
vi.mock('vue-i18n', async importOriginal => ({ ...(await importOriginal<typeof import('vue-i18n')>()), useI18n: () => ({ t: (key: string) => key }) }))
const configFixture = (overrides: Partial<ChanshuiConfig> = {}): ChanshuiConfig => ({ id: 1, account_id: 11, account_name: 'Upstream A', model: 'model-a', protocol: 'auto', sections: ['fingerprint', 'iq'], timeout_seconds: 360, enabled: true, schedule_enabled: false, interval_minutes: 60, disable_on_adulteration: false, auto_recover: false, minimum_score: null, created_at: '2026-10-23T00:00:00Z', updated_at: '2026-10-23T00:00:00Z', ...overrides })
const runFixture = (overrides: Partial<ChanshuiRun> = {}): ChanshuiRun => ({ id: 'run-1', config_id: 1, account_id: 11, model: 'model-a', status: 'completed', verdict: 'clean', score: 95, score_complete: true, summary: 'Valid assessment', action_status: 'none', created_at: '2026-10-23T00:00:00Z', updated_at: '2026-10-23T00:00:00Z', ...overrides })
const wrappers: ReturnType<typeof mount>[] = []
const mountPage = () => {
  const wrapper = mount(ChanshuiTestsView, { global: { stubs: {
    AppLayout: { template: '<div><slot /></div>' }, RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' },
    BaseDialog: { name: 'BaseDialog', props: ['show', 'title'], template: '<div v-if="show" class="dialog"><h2>{{ title }}</h2><slot /><slot name="footer" /></div>' },
    Select: { props: ['id', 'modelValue', 'options', 'disabled'], emits: ['update:modelValue'], template: '<div :id="id" />' },
    Toggle: { props: ['id', 'modelValue', 'disabled'], emits: ['update:modelValue'], template: '<div :id="id" />' },
  } } })
  wrappers.push(wrapper)
  return wrapper
}
const click = async (wrapper: ReturnType<typeof mountPage>, key: string) => { await wrapper.findAll('button').find(button => button.text() === key)!.trigger('click'); await flushPromises() }

describe('管理员掺水检测', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    configs.mockReset().mockResolvedValue({ enabled: true, configs: [configFixture()] })
    adminSettingsStore.chanshuiEnabled = false
    replace.mockReset().mockResolvedValue(undefined)
    targets.mockReset().mockResolvedValue([{ id: 11, name: 'Upstream A', platform: 'openai', type: 'apikey', supported: true, models: ['model-a', 'model-b'] }, { id: 12, name: 'Missing key', platform: 'openai', type: 'apikey', supported: false, reason: 'Missing API key', models: [] }, { id: 13, name: 'OAuth B', platform: 'openai', type: 'oauth', supported: false, models: [] }, { id: 14, name: 'Claude disabled', platform: 'anthropic', type: 'apikey', supported: true, warning: 'Disabled account: direct manual check only', models: ['claude-sonnet-5'] }, { id: 15, name: 'Native API key', platform: 'gemini', type: 'apikey', supported: false, reason: 'Compatible endpoint required', models: [] }])
    save.mockReset().mockResolvedValue(configFixture()); run.mockReset().mockResolvedValue(runFixture({ status: 'queued', verdict: 'pending', score: null, score_complete: false })); remove.mockReset().mockResolvedValue(undefined)
    history.mockReset().mockResolvedValue([]); detail.mockReset().mockResolvedValue(runFixture()); appStore.showError.mockReset(); appStore.showSuccess.mockReset()
  })
  afterEach(() => { wrappers.splice(0).forEach(wrapper => wrapper.unmount()); vi.useRealTimers() })
  it.each([false, undefined, 'true'])('接口开关未明确开启 %s 时隐藏页面并退回系统设置', async enabled => {
    configs.mockResolvedValue({ enabled, configs: [configFixture()] })
    adminSettingsStore.chanshuiEnabled = true
    const wrapper = mountPage(); await flushPromises()
    expect(wrapper.find('[data-config-id]').exists()).toBe(false)
    expect(wrapper.findAll('button')).toHaveLength(0)
    expect(adminSettingsStore.chanshuiEnabled).toBe(false)
    expect(replace).toHaveBeenCalledWith('/admin/settings?tab=features')
    expect(run).not.toHaveBeenCalled()
    expect(save).not.toHaveBeenCalled()
  })
  it('已开启时可维护配置，不读取凭据或自动运行', async () => {
    const wrapper = mountPage(); await flushPromises()
    expect(adminSettingsStore.chanshuiEnabled).toBe(true)
    expect(replace).not.toHaveBeenCalled()
    await click(wrapper, 'common.edit')
    for (const id of ['chanshui-platform', 'chanshui-account', 'chanshui-model', 'chanshui-protocol']) expect(wrapper.getComponent(`#${id}`).props('disabled')).toBe(true)
    expect(wrapper.getComponent('#chanshui-platform').props('modelValue')).toBe('openai')
    expect(wrapper.getComponent('#chanshui-protocol').props('modelValue')).toBe('auto')
    expect(wrapper.getComponent('#chanshui-protocol').props('options')[0]).toEqual({ value: 'auto', label: 'chanshui.protocolAuto' })
    expect(wrapper.find('input[type="password"]').exists()).toBe(false)
    await wrapper.get('#chanshui-config-form').trigger('submit'); await flushPromises()
    expect(save).toHaveBeenCalledWith(expect.objectContaining({ account_id: 11, model: 'model-a', minimum_score: null }), 1)
    expect(save.mock.calls[0][0]).not.toHaveProperty('api_key')
    expect(save.mock.calls[0][0]).not.toHaveProperty('group_id')
    expect(run).not.toHaveBeenCalled()
  })
  it('页面打开后轮询发现关闭会立即隐藏已有列表及弹窗', async () => {
    const wrapper = mountPage(); await flushPromises()
    await click(wrapper, 'common.edit')
    expect(wrapper.find('#chanshui-config-form').exists()).toBe(true)
    configs.mockResolvedValue({ enabled: false, configs: [configFixture()] })
    await vi.advanceTimersByTimeAsync(5000); await flushPromises()
    expect(wrapper.find('[data-config-id]').exists()).toBe(false)
    expect(wrapper.find('#chanshui-config-form').exists()).toBe(false)
    expect(adminSettingsStore.chanshuiEnabled).toBe(false)
    expect(replace).toHaveBeenCalledWith('/admin/settings?tab=features')
    expect(run).not.toHaveBeenCalled()
  })
  it('新配置按平台列出所有 API Key 账号，切换平台清空身份并提示不可提交原因', async () => {
    const wrapper = mountPage(); await flushPromises(); await click(wrapper, 'chanshui.add')
    expect(wrapper.getComponent('#chanshui-protocol').props('disabled')).toBe(false)
    expect(wrapper.getComponent('#chanshui-account').props('disabled')).toBe(true)
    expect(wrapper.getComponent('#chanshui-platform').props('options')).toEqual([
      { value: 'openai', label: 'GPT / OpenAI (2)' },
      { value: 'anthropic', label: 'Claude (1)' },
      { value: 'gemini', label: 'Gemini (1)' },
    ])
    wrapper.getComponent('#chanshui-platform').vm.$emit('update:modelValue', 'anthropic'); await flushPromises()
    expect(wrapper.getComponent('#chanshui-account').props('options').map((item: { value: number }) => item.value)).toEqual([14])
    wrapper.getComponent('#chanshui-account').vm.$emit('update:modelValue', 14); await flushPromises()
    wrapper.getComponent('#chanshui-model').vm.$emit('update:modelValue', 'claude-sonnet-5'); await flushPromises()
    expect(wrapper.text()).toContain('Disabled account: direct manual check only')
    wrapper.getComponent('#chanshui-platform').vm.$emit('update:modelValue', 'openai'); await flushPromises()
    expect(wrapper.getComponent('#chanshui-account').props('modelValue')).toBe(0)
    expect(wrapper.getComponent('#chanshui-model').props('modelValue')).toBe('')
    expect(wrapper.getComponent('#chanshui-account').props('options').map((item: { value: number }) => item.value)).toEqual([11, 12])
    wrapper.getComponent('#chanshui-account').vm.$emit('update:modelValue', 12); await flushPromises()
    expect(wrapper.text()).toContain('Missing API key')
    await wrapper.get('#chanshui-config-form').trigger('submit'); expect(save).not.toHaveBeenCalled()
    wrapper.getComponent('#chanshui-account').vm.$emit('update:modelValue', 11); await flushPromises()
    wrapper.getComponent('#chanshui-model').vm.$emit('update:modelValue', 'model-b'); await flushPromises()
    expect(wrapper.get('#chanshui-timeout').element).toHaveProperty('value', '360')
    await wrapper.get('#chanshui-config-form').trigger('submit'); await flushPromises()
    expect(save).toHaveBeenCalledWith(expect.objectContaining({ account_id: 11, model: 'model-b', timeout_seconds: 360, enabled: true, schedule_enabled: false, disable_on_adulteration: false, minimum_score: null }), undefined)
  })
  it.each([9, 601, 10.5, ''])('拒绝无效超时 %s', async value => {
    const wrapper = mountPage(); await flushPromises(); await click(wrapper, 'common.edit')
    await wrapper.get('#chanshui-timeout').setValue(value); await wrapper.get('#chanshui-config-form').trigger('submit'); await flushPromises()
    expect(save).not.toHaveBeenCalled()
  })
  it('校验定时间隔、评分阈值和至少一个检测项目', async () => {
    const wrapper = mountPage(); await flushPromises(); await click(wrapper, 'common.edit')
    wrapper.getComponent('#chanshui-schedule').vm.$emit('update:modelValue', true); await flushPromises()
    await wrapper.get('#chanshui-interval').setValue(4); await wrapper.get('#chanshui-config-form').trigger('submit'); expect(save).not.toHaveBeenCalled()
    await wrapper.get('#chanshui-interval').setValue(5); await wrapper.get('#chanshui-score').setValue(101); await wrapper.get('#chanshui-config-form').trigger('submit'); expect(save).not.toHaveBeenCalled()
    await wrapper.get('#chanshui-score').setValue('');
    await wrapper.get('[data-section="fingerprint"]').setValue(false); await wrapper.get('[data-section="iq"]').setValue(false)
    await wrapper.get('#chanshui-config-form').trigger('submit'); expect(save).not.toHaveBeenCalled()
    await wrapper.get('[data-section="iq"]').setValue(true); await wrapper.get('#chanshui-score').setValue(0)
    await wrapper.get('#chanshui-config-form').trigger('submit'); await flushPromises()
    expect(save).toHaveBeenCalledWith(expect.objectContaining({ minimum_score: 0, sections: ['iq'], interval_minutes: 5 }), 1)
  })
  it('提交期间与排队期间防止重复点击；错误不自动重发', async () => {
    configs.mockResolvedValue({ enabled: true, configs: [configFixture()] })
    let resolveRun!: (value: ChanshuiRun) => void
    run.mockImplementationOnce(() => new Promise(resolve => { resolveRun = resolve }))
    const wrapper = mountPage(); await flushPromises()
    const button = wrapper.findAll('button').find(item => item.text() === 'chanshui.run')!
    await button.trigger('click'); await button.trigger('click'); expect(run).toHaveBeenCalledTimes(1)
    resolveRun(runFixture({ status: 'queued', verdict: 'pending' })); await flushPromises()
    expect(button.attributes('disabled')).toBeDefined()
    configs.mockResolvedValue({ enabled: true, configs: [configFixture()] }); await vi.advanceTimersByTimeAsync(5000); await flushPromises()
    run.mockRejectedValueOnce(new Error('network failure')); await button.trigger('click'); await flushPromises()
    expect(appStore.showError).toHaveBeenCalledOnce()
    await vi.advanceTimersByTimeAsync(10000); expect(run).toHaveBeenCalledTimes(2)
  })
  it('不完整分数不显示通过，并显示实际自动处置结果', async () => {
    configs.mockResolvedValue({ enabled: true, configs: [configFixture({ latest_run: runFixture({ score_complete: false, action_status: 'skipped_changed' }) })] })
    const wrapper = mountPage(); await flushPromises()
    expect(wrapper.text()).toContain('chanshui.incomplete')
    expect(wrapper.text()).not.toContain('chanshui.verdicts.clean')
    expect(wrapper.text()).toContain('chanshui.actions.skipped_changed')
  })
  it('按账号展示模型标题，各配置历史隔离，轮询只读取列表摘要', async () => {
    const first = runFixture({ id: 'first' })
    const second = runFixture({ id: 'second', config_id: 2 })
    const third = runFixture({ id: 'third', config_id: 3, account_id: 14 })
    configs.mockResolvedValue({ enabled: true, configs: [
      configFixture({ recent_runs: [first, second], latest_run: first }),
      configFixture({ id: 2, protocol: 'chat', recent_runs: [second] }),
      configFixture({ id: 3, account_id: 14, account_name: 'Claude disabled', model: 'claude-opus-5-5', recent_runs: [third] }),
    ] })
    const wrapper = mountPage(); await flushPromises()
    expect(wrapper.findAll('article')).toHaveLength(2)
    expect(wrapper.findAll('article h2').map(item => item.text())).toEqual(['Upstream A #11', 'Claude disabled #14'])
    expect(wrapper.findAll('section h3').map(item => item.text())).toEqual(['model-a', 'model-a', 'claude-opus-5-5'])
    expect(wrapper.get('[data-config-id="1"]').findAll('[data-run-id]').map(item => item.attributes('data-run-id'))).toEqual(['first'])
    expect(wrapper.get('[data-config-id="2"]').findAll('[data-run-id]').map(item => item.attributes('data-run-id'))).toEqual(['second'])
    await vi.advanceTimersByTimeAsync(10000); await flushPromises()
    expect(configs).toHaveBeenCalledTimes(3)
    expect(history).not.toHaveBeenCalled()
    expect(detail).not.toHaveBeenCalled()
    await wrapper.get('[data-run-id="third"]').trigger('click'); await flushPromises()
    expect(detail).toHaveBeenCalledWith('third', expect.any(AbortSignal))
  })
  it('恢复默认关闭，开启时验证前置规则而不悄悄更改原配置', async () => {
    const wrapper = mountPage(); await flushPromises(); await click(wrapper, 'common.edit')
    expect(wrapper.getComponent('#chanshui-recover').props('modelValue')).toBe(false)
    wrapper.getComponent('#chanshui-recover').vm.$emit('update:modelValue', true); await flushPromises()
    expect(wrapper.text()).toContain('chanshui.autoRecoverRequirements')
    expect(wrapper.getComponent('#chanshui-schedule').props('modelValue')).toBe(false)
    expect(wrapper.getComponent('#chanshui-adulteration').props('modelValue')).toBe(false)
    await wrapper.get('#chanshui-config-form').trigger('submit'); expect(save).not.toHaveBeenCalled()
    wrapper.getComponent('#chanshui-schedule').vm.$emit('update:modelValue', true); await flushPromises()
    await wrapper.get('#chanshui-config-form').trigger('submit'); expect(save).not.toHaveBeenCalled()
    await wrapper.get('#chanshui-score').setValue(67)
    await wrapper.get('[data-section="fingerprint"]').setValue(false)
    await wrapper.get('#chanshui-config-form').trigger('submit'); expect(save).not.toHaveBeenCalled()
    await wrapper.get('[data-section="fingerprint"]').setValue(true)
    await wrapper.get('#chanshui-config-form').trigger('submit'); await flushPromises()
    expect(save).toHaveBeenCalledWith(expect.objectContaining({ auto_recover: true, schedule_enabled: true, minimum_score: 67, disable_on_adulteration: false }), 1)
  })
  it('编辑保留恢复规则且展示实际恢复结果', async () => {
    configs.mockResolvedValue({ enabled: true, configs: [configFixture({ auto_recover: true, schedule_enabled: true, disable_on_adulteration: true, latest_run: runFixture({ action_status: 'recovered' }) })] })
    const wrapper = mountPage(); await flushPromises()
    expect(wrapper.text()).toContain('chanshui.recoverySummary')
    expect(wrapper.text()).toContain('chanshui.actions.recovered')
    await click(wrapper, 'common.edit')
    expect(wrapper.getComponent('#chanshui-recover').props('modelValue')).toBe(true)
    await wrapper.get('#chanshui-config-form').trigger('submit'); await flushPromises()
    expect(save).toHaveBeenCalledWith(expect.objectContaining({ auto_recover: true, schedule_enabled: true }), 1)
  })
  it('旧版72分终态中的排队推理题显示明确原因，不当作答错或完整结果', async () => {
    const partial = runFixture({ score: 72, score_complete: false, report: { probes: [
      ...Array.from({ length: 8 }, (_, index) => ({ id: `Q${index + 1}`, name: `Q${index + 1}`, status: 'queued', ms: 0 })),
      { id: 'tools', name: 'Tools', status: 'running', ms: 0 },
    ], parts: [
      { key: 'iq', applicable: true, known: true, points: 0, max_points: 100, coefficient: 0.28, contribution: 0 },
      { key: 'tools', applicable: true, known: true, points: 0, max_points: 100, coefficient: 0.1, contribution: 0 },
    ] } })
    configs.mockResolvedValue({ enabled: true, configs: [configFixture({ latest_run: partial })] })
    detail.mockResolvedValue(partial)
    const wrapper = mountPage(); await flushPromises(); await click(wrapper, 'chanshui.viewIncomplete')
    expect(wrapper.text()).toContain('chanshui.iqIncomplete')
    expect(wrapper.findAll('td').filter(item => item.text() === 'chanshui.probeNotExecuted')).toHaveLength(8)
    expect(wrapper.text()).toContain('chanshui.probeNotFinished')
    expect(wrapper.text()).not.toContain('chanshui.verdicts.clean')
    expect(wrapper.text()).toContain('chanshui.partialScore')
    for (const key of ['iq', 'tools']) {
      const cells = wrapper.get(`[data-part-key="${key}"]`).findAll('td')
      expect(cells[1].text()).toBe('chanshui.incomplete')
      expect(cells[2].text()).toBe('—')
    }
    expect(wrapper.text()).not.toContain('0 / 100')
    expect(partial.score).toBe(72)
    expect(partial.report!.parts![0].known).toBe(true)
    expect(run).not.toHaveBeenCalled()
  })
  it('账号恢复等待和模型复测通过分开展示，不冒充账号已恢复', async () => {
    configs.mockResolvedValue({ enabled: true, configs: [configFixture({ auto_recover: true, recovery_pending: true, schedule_enabled: true, disable_on_adulteration: true, latest_run: runFixture({ action_status: 'recovery_pending' }) })] })
    const wrapper = mountPage(); await flushPromises()
    expect(wrapper.text()).toContain('chanshui.recoveryPending')
    expect(wrapper.text()).toContain('chanshui.actions.recovery_pending')
    expect(wrapper.text()).not.toContain('chanshui.actions.recovered')
  })
  it('详情只渲染纯文本，切换记录和关闭弹窗后忽略迟到响应', async () => {
    configs.mockResolvedValue({ enabled: true, configs: [configFixture({ latest_run: runFixture() })] })
    let resolveDetail!: (value: ChanshuiRun) => void
    detail.mockImplementationOnce(() => new Promise(resolve => { resolveDetail = resolve }))
    const wrapper = mountPage(); await flushPromises()
    await wrapper.findAll('button').find(button => button.text().includes('chanshui.verdicts.clean'))!.trigger('click'); await flushPromises()
    const signal = detail.mock.calls[0][1] as AbortSignal
    wrapper.findAllComponents({ name: 'BaseDialog' }).at(-1)?.vm.$emit('close')
    await flushPromises()
    expect(signal.aborted).toBe(true)
    resolveDetail(runFixture({ summary: '<img src=x onerror=alert(1)>late' })); await flushPromises()
    expect(wrapper.find('img').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('late')
    detail.mockResolvedValueOnce(runFixture({ summary: '<img src=x onerror=alert(1)>escaped', action_status: 'disabled', report: { skipped: [{ id: 'web', reason: '检测服务跳过此板块，未参与判断' }] } }))
    await wrapper.findAll('button').find(button => button.text().includes('chanshui.verdicts.clean'))!.trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('<img src=x onerror=alert(1)>escaped')
    expect(wrapper.find('img').exists()).toBe(false)
    expect(wrapper.text()).toContain('chanshui.actions.disabled')
    expect(wrapper.text()).toContain('chanshui.sectionNames.web: 检测服务跳过此板块，未参与判断')
    expect(wrapper.text()).not.toContain('[object Object]')
  })
  it('删除需要确认且错误留在弹窗，成功后刷新列表', async () => {
    const wrapper = mountPage(); await flushPromises(); await click(wrapper, 'common.delete')
    expect(remove).not.toHaveBeenCalled(); expect(wrapper.text()).toContain('chanshui.removeConfirm')
    remove.mockRejectedValueOnce(new Error('conflict'))
    const confirm = () => wrapper.findAll('button').filter(button => button.text() === 'common.delete').at(-1)!
    await confirm().trigger('click'); await flushPromises(); expect(wrapper.text()).toContain('conflict')
    await confirm().trigger('click'); await flushPromises(); expect(remove).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).not.toContain('chanshui.removeConfirm')
  })
  it('卸载取消所有正在读取的请求及轮询', async () => {
    configs.mockImplementation(() => new Promise(() => {})); targets.mockImplementation(() => new Promise(() => {}))
    const wrapper = mountPage(); await flushPromises(); wrapper.unmount()
    expect((configs.mock.calls[0][0] as AbortSignal).aborted).toBe(true)
    expect((targets.mock.calls[0][0] as AbortSignal).aborted).toBe(true)
    await vi.advanceTimersByTimeAsync(15000); expect(configs).toHaveBeenCalledTimes(1)
  })
})
