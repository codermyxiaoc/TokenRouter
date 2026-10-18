import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick, ref } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { useTheme } from '@/composables/useTheme'
import CreativeStudioView from '../CreativeStudioView.vue'

vi.mock('@/stores/auth', async () => {
  const { reactive } = await import('vue')
  const state = reactive({ user: { id: 7 } as { id: number } | null, token: 'test-token-7' as string | null })
  return { useAuthStore: () => state }
})

vi.mock('@/composables/useTheme', async () => {
  const { ref } = await import('vue')
  const isDark = ref(false)
  return { useTheme: () => ({ isDark }) }
})

// 挂载真实 iframe 并派发窗口消息，覆盖宿主会话桥的认证与生命周期边界。
describe('CreativeStudioView 会话桥集成', () => {
  const auth = useAuthStore() as unknown as { user: { id: number } | null; token: string | null }
  const isDark = useTheme().isDark as ReturnType<typeof ref<boolean>>
  let wrapper: VueWrapper | undefined

  function renderView() {
    const i18n = createI18n({
      legacy: false,
      locale: 'zh-CN',
      messages: {
        'zh-CN': { creative: { title: () => '创作台', sessionExpired: () => '登录已过期' } },
        en: { creative: { title: () => 'Creative studio', sessionExpired: () => 'Session expired' } },
      },
    })
    wrapper = mount(CreativeStudioView, {
      attachTo: document.body,
      global: { plugins: [i18n], stubs: { AppLayout: { template: '<main><slot /></main>' } } },
    })
    return { wrapper, i18n }
  }

  function receive(type: string, source: Window | null, origin = window.location.origin) {
    window.dispatchEvent(new MessageEvent('message', {
      origin, source, data: { namespace: 'tokenrouter-canvas', type },
    }))
  }

  beforeEach(() => {
    vi.useFakeTimers()
    auth.user = { id: 7 }
    auth.token = 'test-token-7'
    isDark.value = false
  })

  afterEach(() => {
    wrapper?.unmount()
    wrapper = undefined
    vi.restoreAllMocks()
    vi.useRealTimers()
    document.body.innerHTML = ''
  })

  it('仅向当前同源画布发送会话，ready 后停止握手重发', async () => {
    const { wrapper } = renderView()
    const iframe = wrapper.get('iframe')
    const child = (iframe.element as HTMLIFrameElement).contentWindow!
    const post = vi.spyOn(child, 'postMessage')
    await iframe.trigger('load')
    expect(post).toHaveBeenLastCalledWith({
      namespace: 'tokenrouter-canvas', type: 'session', userId: 7,
      token: 'test-token-7', locale: 'zh-CN', theme: 'light',
    }, window.location.origin)
    receive('ready', child)
    const count = post.mock.calls.length
    vi.advanceTimersByTime(5000)
    expect(post).toHaveBeenCalledTimes(count)
  })

  it('拒绝其他来源、其他窗口及未知消息，不泄露令牌或修改过期状态', async () => {
    const { wrapper } = renderView()
    const child = (wrapper.get('iframe').element as HTMLIFrameElement).contentWindow!
    const post = vi.spyOn(child, 'postMessage')
    receive('ready', child, 'https://attacker.invalid')
    receive('ready', window)
    receive('expired', child, 'https://attacker.invalid')
    receive('expired', window)
    receive('session', child)
    await nextTick()
    expect(post).not.toHaveBeenCalled()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
  })

  it('同一用户刷新令牌和主题语言只更新会话，不重建画布', async () => {
    const { wrapper, i18n } = renderView()
    const iframe = wrapper.get('iframe').element as HTMLIFrameElement
    const post = vi.spyOn(iframe.contentWindow!, 'postMessage')
    auth.token = 'refreshed-token-7'
    isDark.value = true
    i18n.global.locale.value = 'en'
    await nextTick()
    expect(wrapper.get('iframe').element).toBe(iframe)
    expect(post).toHaveBeenLastCalledWith(expect.objectContaining({
      userId: 7, token: 'refreshed-token-7', locale: 'en', theme: 'dark',
    }), window.location.origin)
  })

  it('切换用户销毁旧画布且旧窗口不能请求新用户令牌', async () => {
    const { wrapper } = renderView()
    const oldFrame = wrapper.get('iframe').element as HTMLIFrameElement
    const oldChild = oldFrame.contentWindow!
    const oldPost = vi.spyOn(oldChild, 'postMessage')
    auth.user = { id: 8 }
    auth.token = 'test-token-8'
    // 模拟同一事件循环内旧页面发来的 ready，覆盖 DOM 替换之前的短暂窗口。
    receive('ready', oldChild)
    await nextTick()
    const newFrame = wrapper.get('iframe').element as HTMLIFrameElement
    const newPost = vi.spyOn(newFrame.contentWindow!, 'postMessage')
    expect(newFrame).not.toBe(oldFrame)
    expect(oldPost.mock.calls.some(([message]) => message.userId === 8)).toBe(false)
    oldPost.mockClear()
    receive('ready', oldChild)
    expect(oldPost).not.toHaveBeenCalled()
    expect(newPost).not.toHaveBeenCalled()
    receive('ready', newFrame.contentWindow)
    expect(newPost).toHaveBeenLastCalledWith(expect.objectContaining({
      userId: 8, token: 'test-token-8',
    }), window.location.origin)
  })

  it('当前画布过期只提示重新登录，更新令牌清除提示', async () => {
    const { wrapper } = renderView()
    const child = (wrapper.get('iframe').element as HTMLIFrameElement).contentWindow!
    receive('ready', child)
    receive('expired', child)
    await nextTick()
    expect(wrapper.get('[role="alert"]').text()).toBe('登录已过期')
    auth.token = 'refreshed-token-7'
    await nextTick()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
  })

  it('登出立即移除画布且不再向旧窗口发送会话', async () => {
    const { wrapper } = renderView()
    const child = (wrapper.get('iframe').element as HTMLIFrameElement).contentWindow!
    const post = vi.spyOn(child, 'postMessage')
    auth.user = null
    auth.token = null
    await nextTick()
    expect(wrapper.find('iframe').exists()).toBe(false)
    vi.advanceTimersByTime(5000)
    receive('ready', child)
    expect(post).not.toHaveBeenCalled()
  })

  it('卸载时通知画布登出并移除消息监听和定时器', async () => {
    const view = renderView().wrapper
    const child = (view.get('iframe').element as HTMLIFrameElement).contentWindow!
    const post = vi.spyOn(child, 'postMessage')
    view.unmount()
    wrapper = undefined
    expect(post).toHaveBeenLastCalledWith({ namespace: 'tokenrouter-canvas', type: 'logout' }, window.location.origin)
    post.mockClear()
    vi.advanceTimersByTime(5000)
    receive('ready', child)
    expect(post).not.toHaveBeenCalled()
    expect(vi.getTimerCount()).toBe(0)
  })
})
