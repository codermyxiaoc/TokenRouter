import { describe, expect, it } from 'vitest'

import {
  calculateRechargeBonus,
  describeRechargeBonusIntervals,
  formatRechargeBonusNumber,
  isDuplicateRechargeBonusMinAmount,
  isRechargeBonusPercentValidForMode,
  matchRechargeBonusTier,
  normalizeRechargeBonusMode,
  normalizeRechargeBonusTiers,
  quoteRechargeBonus,
  roundRechargeAmount,
  sanitizeRechargeBonusTiersForSubmit,
} from '../rechargeBonus'

const tiers = [
  { min_amount: 100, bonus_percent: 20 },
  { min_amount: 500, bonus_percent: 30 },
  { min_amount: 1000, bonus_percent: 35 },
]

describe('rechargeBonus helpers', () => {
  it('normalizes settings payload: drops invalid, dedupes, sorts ascending', () => {
    expect(
      normalizeRechargeBonusTiers([
        { min_amount: 500, bonus_percent: 30 },
        { min_amount: -1, bonus_percent: 5 },
        { min_amount: 100, bonus_percent: 20 },
        { min_amount: '100', bonus_percent: 99 },
        { min_amount: 50, bonus_percent: 5000 },
        { min_amount: 10.123, bonus_percent: 1 },
        null,
        'junk',
      ]),
    ).toEqual([
      { min_amount: 100, bonus_percent: 20 },
      { min_amount: 500, bonus_percent: 30 },
    ])
    expect(normalizeRechargeBonusTiers(undefined)).toEqual([])
  })

  it('sanitizes drafts for submit: incomplete rows are dropped', () => {
    expect(
      sanitizeRechargeBonusTiersForSubmit([
        { min_amount: 1000, bonus_percent: 35 },
        { min_amount: null, bonus_percent: 10 },
        { min_amount: 100, bonus_percent: null },
        { min_amount: 100, bonus_percent: 20 },
        { min_amount: 0, bonus_percent: 0 },
      ]),
    ).toEqual([
      { min_amount: 0, bonus_percent: 0 },
      { min_amount: 100, bonus_percent: 20 },
      { min_amount: 1000, bonus_percent: 35 },
    ])
    expect(sanitizeRechargeBonusTiersForSubmit(null)).toEqual([])
  })

  it('detects duplicate thresholds across rows', () => {
    const drafts = [
      { min_amount: 100, bonus_percent: 20 },
      { min_amount: 100, bonus_percent: 30 },
      { min_amount: null, bonus_percent: 30 },
    ]
    expect(isDuplicateRechargeBonusMinAmount(drafts, 1)).toBe(true)
    expect(isDuplicateRechargeBonusMinAmount(drafts, 2)).toBe(false)
  })

  it('matches the highest threshold not above the payment amount', () => {
    expect(matchRechargeBonusTier(tiers, 99.99)).toBeNull()
    expect(matchRechargeBonusTier(tiers, 100)?.bonus_percent).toBe(20)
    expect(matchRechargeBonusTier(tiers, 499.99)?.bonus_percent).toBe(20)
    expect(matchRechargeBonusTier(tiers, 500)?.bonus_percent).toBe(30)
    expect(matchRechargeBonusTier(tiers, 5000)?.bonus_percent).toBe(35)
    expect(matchRechargeBonusTier(tiers, 0)).toBeNull()
    expect(matchRechargeBonusTier([{ min_amount: 0.3, bonus_percent: 1 }], 0.1 + 0.2)).not.toBeNull()
  })

  it('calculates bonus on the credited base with cent rounding', () => {
    expect(calculateRechargeBonus(100, 20)).toBe(20)
    expect(calculateRechargeBonus(33.33, 15)).toBe(5)
    expect(calculateRechargeBonus(100, 0)).toBe(0)
  })

  it.each([
    [40.3, 25, 10.08],
    [20.15, 50, 10.08],
    [2.05, 10, 0.21],
  ])('rounds the exact decimal bonus for %s at %s percent', (base, percent, expected) => {
    // 先用浮点相乘会把半分边界变小，导致前端报价比后端少一分钱。
    expect(calculateRechargeBonus(base, percent)).toBe(expected)
  })

  it.each([
    [10.075, 2, 10.08],
    [-10.075, 2, -10.08],
    [1.005, 2, 1.01],
    [0.0005, 3, 0.001],
    [0.0049, 2, 0],
  ])('uses decimal half-away-from-zero rounding for %s at %s places', (amount, digits, expected) => {
    expect(roundRechargeAmount(amount, digits)).toBe(expected)
  })

  it('rounds the credited base before bonus calculation and supports scientific notation', () => {
    expect(quoteRechargeBonus([], 40.3, 0.25)).toMatchObject({ base: 10.08, credited: 10.08 })
    expect(quoteRechargeBonus([{ min_amount: 0, bonus_percent: 25 }], 40.3)).toMatchObject({
      base: 40.3, bonus: 10.08, credited: 50.38,
    })
    expect(quoteRechargeBonus([{ min_amount: 0, bonus_percent: 25 }], 40.3, 0.25)).toMatchObject({
      base: 10.08, bonus: 2.52, credited: 12.6,
    })
    expect(quoteRechargeBonus([], 1e21, 1e-21)).toMatchObject({ base: 1, credited: 1 })
    expect(quoteRechargeBonus([], 1e-7, 100000)).toMatchObject({ base: 0.01, credited: 0.01 })
  })

  it.each([
    [40.3, 75, 2, 10.08],
    [10, 17.15, 2, 8.29],
    [101, 50, 0, 51],
    [2.001, 50, 3, 1.001],
  ])('rounds the discount on %s at %s percent to %s currency places', (amount, percent, digits, expected) => {
    expect(quoteRechargeBonus([{ min_amount: 0, bonus_percent: percent }], amount, {
      mode: 'discount', currencyDigits: digits,
    }).payBase).toBe(expected)
  })

  it('rounds discounted payment and paid credit before computing the free credit difference', () => {
    expect(quoteRechargeBonus([{ min_amount: 0, bonus_percent: 75 }], 40.3, {
      mode: 'discount', multiplier: 0.25,
    })).toMatchObject({ base: 10.08, payBase: 10.08, bonus: 7.56, credited: 10.08 })
  })

  it('quotes threshold by payment amount and bonus by credited base', () => {
    expect(quoteRechargeBonus(tiers, 100)).toMatchObject({ percent: 20, base: 100, bonus: 20, credited: 120 })
    // 1000 CNY × 0.14 = 140 USD 到账基数；按输入金额 1000 命中 35% 档位。
    expect(quoteRechargeBonus(tiers, 1000, 0.14)).toMatchObject({ percent: 35, base: 140, bonus: 49, credited: 189 })
    // 700 CNY 对应 98 USD 到账基数；按输入金额 700 命中 30% 档位。
    expect(quoteRechargeBonus(tiers, 700, 0.14)).toMatchObject({ percent: 30, base: 98, bonus: 29.4, credited: 127.4 })
    expect(quoteRechargeBonus(tiers, 50)).toMatchObject({ percent: 0, base: 50, bonus: 0, credited: 50, tier: null })
    expect(quoteRechargeBonus([], 100)).toMatchObject({ percent: 0, bonus: 0, credited: 100 })
  })

  it('quotes discount mode: credit stays, pay base shrinks, free part recorded as bonus', () => {
    expect(quoteRechargeBonus(tiers, 500, { mode: 'discount' })).toMatchObject({
      mode: 'discount', percent: 30, payBase: 350, base: 500, bonus: 150, credited: 500,
    })
    // 倍率 0.14：1000 CNY 到账 140 USD，35% off 实付 650 CNY，免费部分 140 − 91 = 49 USD
    expect(quoteRechargeBonus(tiers, 1000, { mode: 'discount', multiplier: 0.14 })).toMatchObject({
      percent: 35, payBase: 650, base: 140, bonus: 49, credited: 140,
    })
    // 币种精度：JPY 无小数
    expect(quoteRechargeBonus([{ min_amount: 1, bonus_percent: 15 }], 101, { mode: 'discount' }).payBase).toBe(85.85)
    expect(quoteRechargeBonus([{ min_amount: 1, bonus_percent: 15 }], 101, { mode: 'discount', currencyDigits: 0 }).payBase).toBe(86)
    // 折扣 ≥ 100% 视为无优惠（fail-safe）
    expect(quoteRechargeBonus([{ min_amount: 1, bonus_percent: 100 }], 100, { mode: 'discount' })).toMatchObject({ percent: 0, payBase: 100, credited: 100 })
    // 未命中
    expect(quoteRechargeBonus(tiers, 50, { mode: 'discount' })).toMatchObject({ percent: 0, payBase: 50, credited: 50, bonus: 0 })
  })

  it('normalizes mode and validates percent per mode', () => {
    expect(normalizeRechargeBonusMode('discount')).toBe('discount')
    expect(normalizeRechargeBonusMode(' Discount ')).toBe('discount')
    expect(normalizeRechargeBonusMode('bonus')).toBe('bonus')
    expect(normalizeRechargeBonusMode(undefined)).toBe('bonus')
    expect(normalizeRechargeBonusMode('junk')).toBe('bonus')
    expect(isRechargeBonusPercentValidForMode(100, 'bonus')).toBe(true)
    expect(isRechargeBonusPercentValidForMode(100, 'discount')).toBe(false)
    expect(isRechargeBonusPercentValidForMode(99.99, 'discount')).toBe(true)
  })

  it('describes intervals with an implicit no-bonus head segment', () => {
    expect(describeRechargeBonusIntervals(tiers)).toEqual([
      { from: 0, to: 100, percent: 0 },
      { from: 100, to: 500, percent: 20 },
      { from: 500, to: 1000, percent: 30 },
      { from: 1000, to: null, percent: 35 },
    ])
    expect(describeRechargeBonusIntervals([{ min_amount: 0, bonus_percent: 5 }])).toEqual([
      { from: 0, to: null, percent: 5 },
    ])
    expect(describeRechargeBonusIntervals([])).toEqual([])
  })

  it('formats numbers without trailing zeros', () => {
    expect(formatRechargeBonusNumber(20)).toBe('20')
    expect(formatRechargeBonusNumber(12.5)).toBe('12.5')
    expect(formatRechargeBonusNumber(100.1)).toBe('100.1')
  })
})
