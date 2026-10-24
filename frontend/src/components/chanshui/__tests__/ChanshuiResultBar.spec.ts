import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import type { ChanshuiRun } from '@/api/admin/chanshui'
import ChanshuiResultBar from '../ChanshuiResultBar.vue'
import { hasPartScore, runColor, runLabelKey } from '../results'

vi.mock('vue-i18n', async importOriginal => ({ ...(await importOriginal<typeof import('vue-i18n')>()), useI18n: () => ({ t: (key: string, args?: { score?: number }) => `${key}${args?.score !== undefined ? `:${args.score}` : ''}` }) }))
const fixture = (overrides: Partial<ChanshuiRun> = {}): ChanshuiRun => ({ id: 'one', config_id: 1, account_id: 11, model: 'claude-opus-5-5', status: 'completed', verdict: 'clean', score: 88, score_complete: true, action_status: 'none', created_at: '2026-10-23T00:00:00Z', updated_at: '2026-10-23T00:00:00Z', ...overrides })

describe('掺水检测历史色块', () => {
  it('单次结果只占60槽中的一格，空槽不可点击且不表示失败', async () => {
    const run = fixture()
    const wrapper = mount(ChanshuiResultBar, { props: { runs: [run] } })
    expect(wrapper.findAll('[data-empty-slot]')).toHaveLength(59)
    expect(wrapper.findAll('button')).toHaveLength(1)
    expect(wrapper.get('[data-result-bar]').classes()).toContain('grid-cols-[repeat(60,minmax(0,1fr))]')
    expect(wrapper.get('[data-empty-slot]').attributes('aria-hidden')).toBe('true')
    const button = wrapper.get('button')
    expect(button.classes()).toContain('bg-emerald-500')
    expect(button.attributes('title')).toContain('chanshui.scoreValue:88')
    expect(button.attributes('title')).toContain('chanshui.verdicts.clean')
    await button.trigger('click')
    expect(wrapper.emitted('select')).toEqual([[run]])
  })

  it('空记录保留60个空槽，没有假结果和详情入口', () => {
    const wrapper = mount(ChanshuiResultBar, { props: { runs: [] } })
    expect(wrapper.findAll('[data-empty-slot]')).toHaveLength(60)
    expect(wrapper.findAll('button')).toHaveLength(0)
    expect(wrapper.text()).toContain('chanshui.noRuns')
  })

  it('保留最近60次并按真实时间左旧右新去重，原输入不变', () => {
    const runs = Array.from({ length: 62 }, (_, index) => fixture({ id: `run-${index}`, created_at: new Date(Date.UTC(2026, 9, 23, 0, index)).toISOString() })).reverse()
    const original = [...runs]
    const wrapper = mount(ChanshuiResultBar, { props: { runs: [...runs, runs[0]!] } })
    expect(wrapper.findAll('[data-empty-slot]')).toHaveLength(0)
    expect(wrapper.findAll('button')).toHaveLength(60)
    expect(wrapper.findAll('button')[0].attributes('data-run-id')).toBe('run-2')
    expect(wrapper.findAll('button').at(-1)?.attributes('data-run-id')).toBe('run-61')
    expect(runs).toEqual(original)
  })

  it('不完整、未知和执行错误不显示正常或掺水', () => {
    expect(runColor(fixture({ score_complete: false }))).toBe('bg-amber-400')
    expect(runLabelKey(fixture({ score_complete: false }))).toBe('chanshui.incomplete')
    expect(runColor(fixture({ status: 'error', verdict: 'error' }))).toBe('bg-gray-400')
    expect(runLabelKey(fixture({ status: 'error', verdict: 'error' }))).toBe('chanshui.statuses.error')
    expect(runColor(fixture({ verdict: 'adulterated' }))).toBe('bg-red-500')
    expect(runColor(fixture({ status: 'running', verdict: 'pending', score: null, score_complete: false }))).toBe('bg-blue-400')
  })
  it('推理分项须八题均完成，真实已完成的零分仍展示', () => {
    const part = { key: 'iq', applicable: true, known: true, points: 0, max_points: 100, coefficient: 0.28, contribution: 0 }
    const probes = Array.from({ length: 8 }, (_, index) => ({ id: `Q${index + 1}`, name: `Q${index + 1}`, status: 'done' }))
    expect(hasPartScore(part, { probes })).toBe(true)
    expect(hasPartScore(part, {})).toBe(false)
    expect(hasPartScore(part, { probes: probes.slice(0, 7) })).toBe(false)
    expect(hasPartScore(part, { probes: [...probes.slice(0, 7), probes[0]!] })).toBe(false)
    for (const status of ['queued', 'running', 'error']) {
      expect(hasPartScore(part, { probes: [...probes.slice(0, 7), { ...probes[7]!, status }] })).toBe(false)
    }
    expect(hasPartScore({ ...part, known: false }, { probes })).toBe(false)
  })
  it('其它分项已有未完成探针时也不展示伪零分和贡献', () => {
    const part = { key: 'tools', applicable: true, known: true, points: 0, max_points: 100, coefficient: 0.1, contribution: 0 }
    expect(hasPartScore(part, { probes: [{ id: 'tools', name: 'Tools', status: 'done' }] })).toBe(true)
    expect(hasPartScore(part, { probes: [{ id: 'tools', name: 'Tools', status: 'running' }] })).toBe(false)
    expect(hasPartScore({ ...part, applicable: false }, {})).toBe(false)
  })
})
