import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import PluginsView from '../PluginsView.vue'

const {
  listPlugins,
  uploadPlugin,
  enablePlugin,
  savePluginConfig,
  createUISession,
  stepUpRun,
  getPluginConfig,
  getPluginStatus,
  testPlugin,
  showSuccess,
  showError,
} = vi.hoisted(() => ({
  listPlugins: vi.fn(),
  uploadPlugin: vi.fn(),
  enablePlugin: vi.fn(),
  savePluginConfig: vi.fn(),
  createUISession: vi.fn(),
  stepUpRun: vi.fn((action: () => Promise<unknown>) => action()),
  getPluginConfig: vi.fn(),
  getPluginStatus: vi.fn(),
  testPlugin: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    plugins: {
      list: listPlugins,
      upload: uploadPlugin,
      enable: enablePlugin,
      disable: vi.fn(),
      remove: vi.fn(),
      getConfig: getPluginConfig,
      saveConfig: savePluginConfig,
      test: testPlugin,
      status: getPluginStatus,
      createUISession,
    },
  },
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({
    showError,
    showSuccess,
    showInfo: vi.fn(),
  }),
}))

vi.mock('@/composables/useStepUp', () => ({
  useStepUp: () => ({ run: stepUpRun }),
  isStepUpBlocked: () => false,
  isStepUpCancelled: () => false,
  stepUpBlockReason: () => '',
}))

vi.mock('vue-i18n', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({ t: (key: string) => key }),
}))

const plugin = {
  id: 7,
  plugin_key: 'local.test.transport',
  name: 'Test Transport',
  version: '1.0.0',
  description: '',
  author: 'test',
  manifest: {
    schema_version: 1,
    id: 'local.test.transport',
    name: 'Test Transport',
    version: '1.0.0',
    requires: {
      sub2api: '>=0.1.0',
      plugin_protocol: 1,
      transport_api: 1,
      ui_bridge: 1,
    },
    capabilities: [],
    ui: { entrypoint: 'ui/index.html' },
  },
  binary_sha256: 'a'.repeat(64),
  signature_status: 'trusted' as const,
  state: 'disabled' as const,
  last_error: '',
  installed_at: '2026-08-22T00:00:00Z',
  updated_at: '2026-08-22T00:00:00Z',
  bindings: [
    {
      id: 1,
      plugin_id: 7,
      capability: 'openai.oauth.outbound_transport.v1',
      platform: 'openai',
      account_type: 'oauth',
      enabled: false,
      rollout_percent: 100,
    },
  ],
  compatibility: {
    compatible: true,
    tested: true,
    status: 'compatible' as const,
    message: '',
    current_sub2api_version: '0.1.0',
    required_sub2api_version: '>=0.1.0',
    recommended_sub2api_version: '0.1.0',
    plugin_protocol: 1,
    transport_api: 1,
    ui_bridge: 1,
  },
  runtime_healthy: false,
  runtime_message: '',
}

const wrappers: ReturnType<typeof mount<typeof PluginsView>>[] = []

function mountView() {
  const wrapper = mount(PluginsView, {
    attachTo: document.body,
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        BaseDialog: { props: ['show'], emits: ['close'], template: '<div v-if="show"><button data-testid="close-config" @click="$emit(\'close\')">close</button><slot /></div>' },
        Icon: true,
        TotpStepUpDialog: true,
      },
    },
  })
  wrappers.push(wrapper)
  return wrapper
}

async function openPluginUI(wrapper: ReturnType<typeof mountView>) {
  await flushPromises()
  await wrapper.findAll('button').find(item => item.text().includes('admin.plugins.configure'))!.trigger('click')
  await flushPromises()
  const iframe = wrapper.get('iframe')
  await iframe.trigger('load')
  return iframe.element as HTMLIFrameElement
}

function dispatchBridge(iframe: HTMLIFrameElement, type: string, options: { origin?: string; source?: Window; data?: Record<string, unknown> } = {}) {
  window.dispatchEvent(new MessageEvent('message', {
    source: options.source ?? iframe.contentWindow,
    origin: options.origin ?? 'null',
    data: { source: 'sub2api-plugin-ui', bridge_token: 'bridge', type, request_id: 'request-1', ...options.data }
  }))
}

describe('管理员插件页二次验证', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    stepUpRun.mockImplementation((action: () => Promise<unknown>) => action())
    listPlugins.mockResolvedValue([plugin])
    uploadPlugin.mockResolvedValue(plugin)
    enablePlugin.mockResolvedValue(plugin)
    savePluginConfig.mockResolvedValue({ enabled: true })
    getPluginConfig.mockResolvedValue({})
    getPluginStatus.mockResolvedValue({ healthy: true, message: 'ok', status_json: '{"requests":2}' })
    testPlugin.mockResolvedValue({ success: true, message: 'ok', latency_ms: 1 })
    createUISession.mockResolvedValue({
      url: '/api/v1/plugin-ui/token/index.html#bridge_token=bridge',
      bridge_token: 'bridge',
      ui_bridge_version: 1,
      expires_at: '2026-08-22T01:00:00Z',
    })
  })

  afterEach(() => {
    wrappers.splice(0).forEach(wrapper => wrapper.unmount())
    vi.restoreAllMocks()
  })

  it('状态轮询无需二次验证且不会弹出宿主提示', async () => {
    const wrapper = mountView()
    const iframe = await openPluginUI(wrapper)
    const postMessage = vi.spyOn(iframe.contentWindow!, 'postMessage')
    expect(iframe.getAttribute('sandbox')).toBe('allow-scripts')
    dispatchBridge(iframe, 'plugin.status')
    await flushPromises()
    expect(getPluginStatus).toHaveBeenCalledWith(7)
    expect(stepUpRun).not.toHaveBeenCalled()
    expect(showSuccess).not.toHaveBeenCalled()
    expect(showError).not.toHaveBeenCalled()
    expect(postMessage).toHaveBeenCalledWith(expect.objectContaining({ type: 'plugin.status.result', ok: true }), '*')
  })

  it.each(['config.save', 'config.test'])('%s 保持二次验证', async type => {
    const wrapper = mountView()
    const iframe = await openPluginUI(wrapper)
    dispatchBridge(iframe, type, { data: { config: { enabled: true } } })
    await flushPromises()
    expect(stepUpRun).toHaveBeenCalledTimes(1)
    if (type === 'config.save') expect(savePluginConfig).toHaveBeenCalledWith(7, { enabled: true })
    else expect(testPlugin).toHaveBeenCalledWith(7)
  })

  it('拒绝非插件窗口、非沙箱来源或错误会话令牌', async () => {
    const wrapper = mountView()
    const iframe = await openPluginUI(wrapper)
    dispatchBridge(iframe, 'plugin.status', { source: window })
    dispatchBridge(iframe, 'plugin.status', { origin: 'https://other.example' })
    dispatchBridge(iframe, 'plugin.status', { data: { bridge_token: 'wrong' } })
    dispatchBridge(iframe, 'plugin.status', { data: { request_id: '' } })
    await flushPromises()
    expect(getPluginStatus).not.toHaveBeenCalled()
  })

  it('配置加载跨 iframe 导航后丢弃旧响应，即使请求 ID 被复用', async () => {
    let resolveOld!: (value: Record<string, unknown>) => void
    let resolveNew!: (value: Record<string, unknown>) => void
    getPluginConfig.mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve }))
      .mockImplementationOnce(() => new Promise(resolve => { resolveNew = resolve }))
    const wrapper = mountView()
    const iframe = await openPluginUI(wrapper)
    const postMessage = vi.spyOn(iframe.contentWindow!, 'postMessage')
    dispatchBridge(iframe, 'config.load')
    dispatchBridge(iframe, 'config.load')
    expect(getPluginConfig).toHaveBeenCalledTimes(1)
    await wrapper.get('iframe').trigger('load')
    dispatchBridge(iframe, 'config.load')
    expect(getPluginConfig).toHaveBeenCalledTimes(2)
    resolveOld({ secret: 'old' })
    await flushPromises()
    expect(postMessage).not.toHaveBeenCalled()
    resolveNew({ secret: 'new' })
    await flushPromises()
    expect(postMessage).toHaveBeenCalledWith(expect.objectContaining({ config: { secret: 'new' } }), '*')
  })

  it('只读状态失败向插件返回错误而不弹二次验证', async () => {
    getPluginStatus.mockRejectedValueOnce(new Error('runtime unavailable'))
    const wrapper = mountView()
    const iframe = await openPluginUI(wrapper)
    const postMessage = vi.spyOn(iframe.contentWindow!, 'postMessage')
    dispatchBridge(iframe, 'plugin.status')
    await flushPromises()
    expect(postMessage).toHaveBeenCalledWith(expect.objectContaining({ ok: false, error: 'runtime unavailable' }), '*')
    expect(stepUpRun).not.toHaveBeenCalled()
    expect(showError).not.toHaveBeenCalled()
  })

  it('等待二次验证时关闭配置页不会再提交插件配置', async () => {
    let verify!: () => void
    const verification = new Promise<void>(resolve => { verify = resolve })
    stepUpRun.mockImplementationOnce(async action => {
      await verification
      return action()
    })
    const wrapper = mountView()
    const iframe = await openPluginUI(wrapper)
    dispatchBridge(iframe, 'config.save', { data: { config: { secret: 'draft' } } })
    await wrapper.get('[data-testid="close-config"]').trigger('click')
    verify()
    await flushPromises()
    expect(savePluginConfig).not.toHaveBeenCalled()
  })

  it('配置页关闭后丢弃延迟返回的 UI 会话', async () => {
    let resolveSession!: (session: unknown) => void
    createUISession.mockImplementationOnce(() => new Promise(resolve => { resolveSession = resolve }))
    const wrapper = mountView()
    await flushPromises()
    await wrapper.findAll('button').find(item => item.text().includes('admin.plugins.configure'))!.trigger('click')
    await wrapper.get('[data-testid="close-config"]').trigger('click')
    resolveSession({ url: '/old-plugin-ui', bridge_token: 'old-token', ui_bridge_version: 1 })
    await flushPromises()
    expect(wrapper.find('iframe').exists()).toBe(false)
  })

  it('启用插件通过 step-up 控制器执行', async () => {
    const wrapper = mountView()
    await flushPromises()

    const button = wrapper.findAll('button').find((item) => item.text().includes('admin.plugins.enable'))
    expect(button).toBeDefined()
    await button!.trigger('click')
    await flushPromises()

    expect(stepUpRun).toHaveBeenCalledTimes(1)
    expect(enablePlugin).toHaveBeenCalledWith(7, 100, false)
  })

  it('上传插件通过 step-up 控制器执行', async () => {
    const wrapper = mountView()
    await flushPromises()
    const input = wrapper.get('input[type="file"]')
    Object.defineProperty(input.element, 'files', {
      configurable: true,
      value: [new File(['plugin'], 'transport.s2plugin', { type: 'application/zip' })],
    })

    await input.trigger('change')
    await flushPromises()

    expect(stepUpRun).toHaveBeenCalledTimes(1)
    expect(uploadPlugin).toHaveBeenCalledTimes(1)
  })
})
