import { describe, expect, it } from 'vitest'
import { planValiditySuffix } from '../validity'

const t = (key: string): string =>
  ({
    'payment.perMonth': '月',
    'payment.days': '天',
    'payment.weeks': '周',
    'payment.months': '个月',
  })[key] ?? key

const suffix = (validity_days: number, validity_unit: string) =>
  planValiditySuffix({ validity_days, validity_unit }, t)

describe('planValiditySuffix', () => {
  // 管理端曾保存复数 months，必须与单数 month 使用相同展示语义。
  it('renders admin-form plural months correctly', () => {
    expect(suffix(1, 'months')).toBe('月')
    expect(suffix(3, 'months')).toBe('3个月')
  })

  it('renders singular month the same way', () => {
    expect(suffix(1, 'month')).toBe('月')
    expect(suffix(6, 'month')).toBe('6个月')
  })

  // 后端把 week/weeks 按七天换算，用户侧必须展示为周数。
  it('renders weeks as weeks instead of mislabeled days', () => {
    expect(suffix(2, 'weeks')).toBe('2周')
    expect(suffix(1, 'week')).toBe('1周')
  })

  it('renders day-based and legacy units as days', () => {
    expect(suffix(30, 'days')).toBe('30天')
    expect(suffix(30, 'day')).toBe('30天')
    expect(suffix(30, '')).toBe('30天')
  })

  // 后端不识别的单位按天处理，展示也必须保持一致。
  it('falls back to days for units billing does not honor', () => {
    expect(suffix(1, 'year')).toBe('1天')
    expect(suffix(365, 'unknown')).toBe('365天')
  })

  it('normalizes casing and whitespace', () => {
    expect(suffix(1, ' Months ')).toBe('月')
    expect(suffix(2, 'WEEKS')).toBe('2周')
  })
})
