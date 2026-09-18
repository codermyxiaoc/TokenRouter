import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { shallowMount, flushPromises } from '@vue/test-utils'
import ProxiesView from '../ProxiesView.vue'

const { list, update, getAllWithCount } = vi.hoisted(() => ({ list: vi.fn(), update: vi.fn(), getAllWithCount: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { proxies: { list, update, getAllWithCount } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({ t: (key: string) => key }) }))

const mountView = () => shallowMount(ProxiesView, { global: { stubs: {
  AppLayout: { template: '<div><slot /></div>' },
  TablePageLayout: { template: '<div><slot name="table" /></div>' },
  DataTable: { props: ['data'], template: '<div><slot v-for="row in data" name="cell-actions" :row="row" /></div>' },
  BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' }
} } })
let wrapper: ReturnType<typeof mountView>
beforeEach(() => {
  vi.clearAllMocks()
  list.mockResolvedValue({ items: [{ id: 9, name: 'proxy', protocol: 'http', host: 'proxy.example', port: 8080, username: 'saved-user', password: '', status: 'active', fallback_mode: 'proxy', backup_proxy_id: 12, expiry_warn_days: 7 }], total: 1, pages: 1 })
  getAllWithCount.mockResolvedValue([])
  update.mockResolvedValue({})
})
afterEach(() => wrapper?.unmount())

describe('代理凭据编辑语义', () => {
  it.each([false, true])('密码是否编辑 %s 决定保留或显式清空', async (edited) => {
    wrapper = mountView()
    await flushPromises()
    await wrapper.findAll('button').find((b) => b.text() === 'common.edit')!.trigger('click')
    const form = wrapper.get('#edit-proxy-form')
    const inputs = form.findAll('input[type="text"]')
    await inputs[2].setValue('   ')
    if (edited) await form.get('input[type="password"]').setValue('')
    await form.trigger('submit')
    await flushPromises()
    expect(update).toHaveBeenCalledTimes(1)
    const [id, payload] = update.mock.calls[0]
    expect(id).toBe(9)
    expect(payload.username).toBe('')
    if (edited) expect(payload.password).toBe('')
    else expect(payload).not.toHaveProperty('password')
    expect(payload.fallback_mode).toBe('proxy')
    expect(payload.backup_proxy_id).toBe(12)
    expect(payload.expiry_warn_days).toBe(7)
  })
})
