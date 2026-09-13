import { describe, expect, it, vi, beforeEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import UserDashboardHeatmap from '../UserDashboardHeatmap.vue'
import { usageAPI } from '@/api/usage'
import { formatDateLocalInput } from '@/utils/format'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
      locale: { value: 'en' },
    }),
  }
})

vi.mock('@/composables/useBalanceDisplay', () => ({
  useBalanceDisplay: () => ({
    formatBalanceAmount: (value: number) => String(value),
  }),
}))

vi.mock('@/api/usage', () => ({
  usageAPI: {
    getDashboardTrend: vi.fn(),
  },
}))

// 组件固定拉取近三年数据：今天向前 3*364 天再对齐到周日
const FETCH_DAYS = 3 * 364
// 测试环境测不出容器宽度，组件回退显示最近 53 周
const FALLBACK_WEEKS = 53

const expectedRange = () => {
  const end = new Date()
  const start = new Date(end)
  start.setDate(start.getDate() - FETCH_DAYS)
  start.setDate(start.getDate() - start.getDay())
  return { start, end }
}

const mountHeatmap = async () => {
  const wrapper = mount(UserDashboardHeatmap, {
    global: {
      stubs: { LoadingSpinner: true },
    },
  })
  await flushPromises()
  return wrapper
}

const emptyTrend = {
  trend: [],
  start_date: '',
  end_date: '',
  granularity: 'day',
}

const todayTrendPoint = (date: string) => ({
  date,
  requests: 3,
  input_tokens: 10,
  output_tokens: 20,
  cache_creation_tokens: 0,
  cache_read_tokens: 0,
  total_tokens: 30,
  cost: 0.1,
  actual_cost: 0.2,
})

describe('UserDashboardHeatmap', () => {
  beforeEach(() => {
    vi.mocked(usageAPI.getDashboardTrend).mockReset()
  })

  it('按近三年整周范围请求按日趋势数据', async () => {
    vi.mocked(usageAPI.getDashboardTrend).mockResolvedValue(emptyTrend)

    await mountHeatmap()

    const { start, end } = expectedRange()
    expect(usageAPI.getDashboardTrend).toHaveBeenCalledWith({
      start_date: formatDateLocalInput(start),
      end_date: formatDateLocalInput(end),
      granularity: 'day',
    })
  })

  it('回退时渲染 53 周网格，接口未返回的日期补零为无用量档', async () => {
    const { end } = expectedRange()
    const today = formatDateLocalInput(end)
    vi.mocked(usageAPI.getDashboardTrend).mockResolvedValue({
      ...emptyTrend,
      trend: [todayTrendPoint(today)],
    })

    const wrapper = await mountHeatmap()

    // 可见格子固定为 53 整周
    const cells = wrapper.findAll('[data-testid="heatmap-cell"]')
    expect(cells.length).toBe(FALLBACK_WEEKS * 7)

    // 有用量的今天为最高档且只有一天；未来占位格不可见
    const greenCells = cells.filter((c) => c.classes().includes('bg-green-700'))
    expect(greenCells.length).toBe(1)
    const futureCount = cells.filter((c) => c.classes().includes('invisible')).length
    expect(cells.filter((c) => c.classes().includes('bg-gray-100')).length).toBe(cells.length - futureCount - 1)
  })

  it('悬停格子时显示当天用量，无用量日期显示无用量', async () => {
    const { end } = expectedRange()
    const today = formatDateLocalInput(end)
    vi.mocked(usageAPI.getDashboardTrend).mockResolvedValue({
      ...emptyTrend,
      trend: [todayTrendPoint(today)],
    })

    const wrapper = await mountHeatmap()
    const cells = wrapper.findAll('[data-testid="heatmap-cell"]')
    const todayIndex = cells.findIndex((c) => c.classes().includes('bg-green-700'))
    expect(todayIndex).toBeGreaterThan(0)

    await cells[todayIndex].trigger('mouseenter')
    const tooltip = wrapper.get('[data-testid="heatmap-tooltip"]')
    expect(tooltip.text()).toContain('dashboard.requests')
    expect(tooltip.text()).toContain('dashboard.tokens')
    expect(tooltip.text()).toContain('dashboard.heatmapCost')
    expect(tooltip.text()).toContain('0.2')
    expect(tooltip.attributes('aria-hidden')).toBe('false')
    expect(tooltip.attributes('style')).toContain('left 400ms cubic-bezier(0.25, 1, 0.5, 1)')
    expect(tooltip.attributes('style')).toContain('width 400ms cubic-bezier(0.25, 1, 0.5, 1)')

    await wrapper.get('[data-testid="heatmap-grid-wrap"]').trigger('mouseleave')
    expect(wrapper.get('[data-testid="heatmap-tooltip"]').attributes('aria-hidden')).toBe('true')

    // 前一天无用量
    await cells[todayIndex - 1].trigger('mouseenter')
    expect(wrapper.get('[data-testid="heatmap-tooltip"]').text()).toContain('dashboard.heatmapNoUsage')
  })

  it('在相邻格子之间移动时保持 tooltip 可见，不触发闪烁隐藏', async () => {
    const { end } = expectedRange()
    const today = formatDateLocalInput(end)
    vi.mocked(usageAPI.getDashboardTrend).mockResolvedValue({
      ...emptyTrend,
      trend: [todayTrendPoint(today)],
    })

    const wrapper = await mountHeatmap()
    const cells = wrapper.findAll('[data-testid="heatmap-cell"]')
    const todayIndex = cells.findIndex((c) => c.classes().includes('bg-green-700'))
    await cells[todayIndex].trigger('mouseenter')
    const tooltipElement = wrapper.get('[data-testid="heatmap-tooltip"]').element

    await cells[todayIndex - 1].trigger('mouseenter')

    expect(wrapper.get('[data-testid="heatmap-tooltip"]').element).toBe(tooltipElement)
    expect(wrapper.get('[data-testid="heatmap-tooltip"]').attributes('aria-hidden')).toBe('false')
  })

  it('左侧渲染周一/周三/周五的星期标签', async () => {
    vi.mocked(usageAPI.getDashboardTrend).mockResolvedValue(emptyTrend)

    const wrapper = await mountHeatmap()
    // 周一/周三/周五用窄格式渲染（英文为 M/W/F），放在网格第 1 列的第 2/4/6 行
    const labels = [1, 3, 5].map((d) =>
      new Date(2024, 0, d).toLocaleDateString('en', { weekday: 'narrow' })
    )
    for (const [i, dayOfWeek] of [1, 3, 5].entries()) {
      const el = wrapper.find(`[style*="grid-column: 1"][style*="grid-row: ${dayOfWeek + 2}"]`)
      expect(el.exists()).toBe(true)
      expect(el.text()).toBe(labels[i])
    }
  })

  it('月份标签间隔不足 3 列时跳过显示', async () => {
    vi.mocked(usageAPI.getDashboardTrend).mockResolvedValue(emptyTrend)

    const wrapper = await mountHeatmap()
    // 月份标签都渲染在网格第 1 行，列号从 2 开始
    const labelCols = wrapper
      .findAll('[style*="grid-row: 1"]')
      .map((el) => Number(/grid-column:\s*(\d+)/.exec(el.attributes('style') || '')?.[1]))
      .filter((n) => Number.isFinite(n))
    expect(labelCols.length).toBeGreaterThan(0)
    for (let i = 1; i < labelCols.length; i++) {
      expect(labelCols[i] - labelCols[i - 1]).toBeGreaterThanOrEqual(3)
    }
  })

  it('reload 会重新请求数据', async () => {
    vi.mocked(usageAPI.getDashboardTrend).mockResolvedValue(emptyTrend)

    const wrapper = await mountHeatmap()
    expect(usageAPI.getDashboardTrend).toHaveBeenCalledTimes(1)

    await (wrapper.vm as unknown as { reload: () => Promise<void> }).reload()
    await flushPromises()
    expect(usageAPI.getDashboardTrend).toHaveBeenCalledTimes(2)
  })
})
