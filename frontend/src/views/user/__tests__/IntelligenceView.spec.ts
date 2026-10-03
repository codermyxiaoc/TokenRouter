import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import IntelligenceView from '../IntelligenceView.vue'
import { runFixture, testFixture } from '@/components/intelligence/__tests__/fixtures'

const { list, detail, preview } = vi.hoisted(() => ({ list: vi.fn(), detail: vi.fn(), preview: vi.fn() }))
vi.mock('@/api/intelligence', () => ({ intelligenceAPI: { list, detail, preview } }))
vi.mock('vue-i18n', async (importOriginal) => ({ ...(await importOriginal<typeof import('vue-i18n')>()), useI18n: () => ({ t: (key: string) => key }) }))

const mountPage = () => mount(IntelligenceView, { global: { stubs: {
  AppLayout: { template: '<div><slot /></div>' },
  IntelligenceDrawing: { props: ['runId'], template: '<div class="drawing" :data-id="runId" />' },
  IntelligenceRunDialog: { props: ['show', 'run', 'loading', 'error'], template: '<div v-if="show" data-testid="detail">{{ run?.id }} {{ error }}</div>' },
} } })

describe('用户降智检测页面', () => {
  beforeEach(() => { list.mockReset().mockResolvedValue([]); detail.mockReset(); preview.mockReset() })
  it('分组与模型标题聚合，作品最新在左且仅10个，不触发检测提交', async () => {
    const artifacts = Array.from({ length: 12 }, (_, i) => runFixture({ id: `art-${i}`, benchmark: 'drawing', has_artifact: true, created_at: new Date(Date.UTC(2026, 9, 3, 0, i)).toISOString() }))
    list.mockResolvedValue([testFixture(), testFixture({ id: 2, benchmark: 'drawing', artifacts }), testFixture({ id: 3, model: 'model-b' })])
    const wrapper = mountPage()
    await flushPromises()
    expect(wrapper.findAll('h2').map(item => item.text())).toEqual(['Group A'])
    expect(wrapper.findAll('h3').map(item => item.text())).toEqual(['model-a', 'model-b'])
    const drawings = wrapper.findAll('.drawing')
    expect(drawings).toHaveLength(10)
    expect(drawings[0].attributes('data-id')).toBe('art-11')
    expect(drawings[9].attributes('data-id')).toBe('art-2')
    expect(preview).not.toHaveBeenCalled()
    wrapper.unmount()
  })
  it('条目详情只请求点击的run，未返回问题不编造问题', async () => {
    const run = runFixture({ id: 'specific-run' })
    list.mockResolvedValue([testFixture({ runs: [run] })])
    detail.mockResolvedValue({ ...run, answer: 'response' })
    const wrapper = mountPage()
    await flushPromises()
    await wrapper.get('[data-run-id="specific-run"]').trigger('click')
    await flushPromises()
    expect(detail).toHaveBeenCalledWith('specific-run', false, expect.any(AbortSignal))
    expect(wrapper.get('[data-testid="detail"]').text()).toContain('specific-run')
    wrapper.unmount()
  })
  it('重新读取403时清理旧结果，避免展示已撤销访问的分组', async () => {
    list.mockResolvedValueOnce([testFixture()]).mockRejectedValueOnce({ status: 403, message: 'Disabled' })
    const wrapper = mountPage()
    await flushPromises()
    expect(wrapper.text()).toContain('Group A')
    await wrapper.findAll('button').find(button => button.text() === 'common.refresh')!.trigger('click')
    await flushPromises()
    expect(wrapper.text()).not.toContain('Group A')
    expect(wrapper.get('[role="alert"]').text()).toContain('Disabled')
    wrapper.unmount()
  })
})
