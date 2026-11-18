import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import IntelligenceRunDialog from '../IntelligenceRunDialog.vue'
import { runFixture } from './fixtures'

vi.mock('vue-i18n', async (importOriginal) => ({ ...(await importOriginal<typeof import('vue-i18n')>()), useI18n: () => ({ t: (key: string) => key }) }))
const mountDialog = (run = runFixture()) => mount(IntelligenceRunDialog, {
  props: { show: true, run, loading: false, error: '' },
  global: { stubs: { BaseDialog: { template: '<div><slot /></div>' }, IntelligenceDrawing: true } },
})

describe('检测详情保留真实观测', () => {
  it('其他鹈鹕模型的质量和作品均可查看，判定标明仅供参考', () => {
    const wrapper = mountDialog(runFixture({ model: 'gpt-6.2-astra', benchmark: 'drawing', verdict: 'reference', assessment_quality: 'suspicious', has_artifact: true }))
    expect(wrapper.text()).toContain('gpt-6.2-astra')
    expect(wrapper.text()).toContain('intelligence.statuses.reference')
    expect(wrapper.text()).toContain('intelligence.qualities.suspicious')
    expect(wrapper.text()).toContain('intelligence.drawingModelHint')
    expect(wrapper.text()).toContain('intelligence.previewTitle')
    wrapper.unmount()
  })
  it('旧记录未区分降智和存疑时，仅保留参考标签', () => {
    const wrapper = mountDialog(runFixture({ model: 'custom-model', benchmark: 'drawing', verdict: 'failed' }))
    expect(wrapper.text()).toContain('intelligence.statuses.reference')
    expect(wrapper.text()).not.toContain('intelligence.qualities.degraded')
    expect(wrapper.text()).not.toContain('intelligence.qualities.suspicious')
    wrapper.unmount()
  })
  it('接口没有题目与答案时不显示虚构标题或内容，缺少统计显示未知', () => {
    const wrapper = mountDialog()
    expect(wrapper.text()).toContain('intelligence.noAnswer')
    expect(wrapper.findAll('h4')).toHaveLength(0)
    expect(wrapper.findAll('dd').map(item => item.text())).toEqual(['model-a', '—', '—', '—', '—'])
    wrapper.unmount()
  })
  it('只提供答案时只渲染答案，明确零统计保留为零', () => {
    const wrapper = mountDialog(runFixture({ answer: '**answer only**', input_tokens: 0, output_tokens: 10, duration_ms: 0 }))
    expect(wrapper.findAll('h4').map(item => item.text())).toEqual(['intelligence.answer'])
    expect(wrapper.get('strong').text()).toBe('answer only')
    expect(wrapper.text()).not.toContain('intelligence.noAnswer')
    expect(wrapper.findAll('dd').map(item => item.text())).toEqual(['model-a', '0.0 s', '0', '10', '—'])
    wrapper.unmount()
  })
  it.each(['error', 'unknown'] as const)('状态%s使用独立文案，不能误报未通过', status => {
    const wrapper = mountDialog(runFixture({ status, verdict: status }))
    expect(wrapper.text()).toContain(`intelligence.statuses.${status}`)
    expect(wrapper.text()).not.toContain('intelligence.statuses.incorrect')
    expect(wrapper.find('.bg-amber-400').exists()).toBe(true)
    wrapper.unmount()
  })
})
