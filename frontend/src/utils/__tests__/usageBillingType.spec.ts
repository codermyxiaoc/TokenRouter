import { describe, expect, it } from 'vitest'
import { getUsageBillingType, getUsageBillingTypeLabel } from '../usageBillingType'
import en from '@/i18n/locales/en'
import zh from '@/i18n/locales/zh'

describe('使用记录计费来源', () => {
  it.each([
    { row: { actual_cost: 0.3, billing_type: 1, subscription_amount_usd: 0.2, balance_amount_usd: 0.1 }, expected: 'mixed' },
    { row: { actual_cost: 0.3, billing_type: 1, subscription_amount_usd: 0, balance_amount_usd: 0.3 }, expected: 'balance' },
    { row: { actual_cost: 0.3, billing_type: 0, subscription_amount_usd: 0.3, balance_amount_usd: 0 }, expected: 'subscription' },
    { row: { actual_cost: 0.3, billing_type: 0 }, expected: 'balance' },
    { row: { actual_cost: 0.3, billing_type: 1, subscription_amount_usd: 0, balance_amount_usd: 0 }, expected: 'subscription' },
    { row: { actual_cost: 0, billing_type: 1 }, expected: 'none' },
    { row: { actual_cost: 0, billing_type: 0 }, expected: 'none' },
    { row: { actual_cost: 0.3, billing_type: 9 }, expected: 'unknown' },
    { row: { actual_cost: 0.3, subscription_amount_usd: NaN, balance_amount_usd: Infinity }, expected: 'unknown' },
    { row: {}, expected: 'unknown' },
  ])('按实际金额区分来源，兼容历史与缺失记录：$expected ($row)', ({ row, expected }) => {
    expect(getUsageBillingType(row)).toBe(expected)
  })

  it.each([
    { row: { billing_type: 0 }, chinese: '余额扣费', english: 'Balance' },
    { row: { billing_type: 1 }, chinese: '订阅扣费', english: 'Subscription' },
    { row: { subscription_amount_usd: 0.1, balance_amount_usd: 0.2 }, chinese: '订阅＋余额', english: 'Subscription + Balance' },
    { row: { actual_cost: 0 }, chinese: '未扣费', english: 'No charge' },
    { row: {}, chinese: '未记录', english: 'Not recorded' },
  ])('完整语言包提供实际可读的计费标签：$chinese', ({ row, chinese, english }) => {
    const translate = (locale: typeof zh | typeof en) => (key: string) =>
      key.split('.').reduce((value: any, part) => value?.[part], locale) as string
    expect(getUsageBillingTypeLabel(row, translate(zh))).toBe(chinese)
    expect(getUsageBillingTypeLabel(row, translate(en))).toBe(english)
  })
})
