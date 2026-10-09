import { describe, expect, it } from 'vitest'
import {
  isRechargeBonusMinAmountValid,
  isRechargeBonusPercentValidForMode,
  normalizeRechargeBonusMode,
  normalizeRechargeBonusTiers,
  quoteRechargeBonus,
  rechargeBonusTierError,
  roundRechargeAmount,
  sanitizeRechargeBonusTiersForSubmit,
} from '../rechargeBonus'

// 仅使用内存输入，覆盖指数、数值边界和真实 JSON 可表达的坏配置。
describe('充值报价攻击输入', () => {
  it.each([NaN, Infinity, -Infinity, -1, -Number.MAX_VALUE, 0])('对非法金额 %s 显式返回无效报价且不抛异常', (amount) => {
    expect(quoteRechargeBonus([{ min_amount: 0, bonus_percent: 20 }], amount)).toMatchObject({
      valid: false, payBase: NaN, base: NaN, bonus: NaN, credited: NaN,
    })
  })

  it.each(['bonus', 'discount'] as const)('拒绝 %s 到账倍率乘积溢出', (mode) => {
    expect(quoteRechargeBonus([{ min_amount: 0, bonus_percent: 20 }], Number.MAX_VALUE, { multiplier: 2, mode }).valid).toBe(false)
  })

  it.each([NaN, Infinity, -Infinity])('非有限倍率 %s 不能静默回退到一倍', (multiplier) => {
    expect(quoteRechargeBonus([], 100, { multiplier }).valid).toBe(false)
  })

  it('拒绝赠金导致的到账总额溢出', () => {
    expect(quoteRechargeBonus([{ min_amount: 0, bonus_percent: 100 }], 1e308).valid).toBe(false)
  })

  it.each([Number.MIN_VALUE, 1e-308, 1e-100, 1e-10, 0.001, 0.0049999])('极小正数 %s 不产生负数或 BigInt 异常', (amount) => {
    for (const mode of ['bonus', 'discount'] as const) {
      const result = quoteRechargeBonus([{ min_amount: 0, bonus_percent: 99.99 }], amount, { mode })
      expect(result.credited).toBe(0)
      expect(result.payBase).toBeGreaterThanOrEqual(0)
    }
  })

  it.each([Number.MAX_VALUE, 1e308, 1e100, 1e21, Number.MAX_SAFE_INTEGER])('极大有限值 %s 不触发 BigInt 转换错误', (amount) => {
    for (const mode of ['bonus', 'discount'] as const) {
      expect(() => quoteRechargeBonus([{ min_amount: 0, bonus_percent: 99.99 }], amount, {
        multiplier: Number.MAX_VALUE, mode,
      })).not.toThrow()
    }
  })

  it.each([Infinity, -Infinity, NaN, -1, 1.5, 101, 1e9])('异常币种精度 %s 回退为两位', (digits) => {
    expect(roundRechargeAmount(1.005, digits)).toBe(1.01)
  })

  it.each([null, undefined, '', '{}', {}, false, 0])('坏顶层配置 %j 不形成档位', (raw) => {
    expect(normalizeRechargeBonusTiers(raw)).toEqual([])
  })

  it.each(['NaN', 'Infinity', '-1', '0.001', '1e309', '<img src=x onerror=alert(1)>'])('坏金额字符串 %s 被丢弃', (value) => {
    expect(normalizeRechargeBonusTiers([{ min_amount: value, bonus_percent: 20 }])).toEqual([])
  })

  it.each([NaN, Infinity, -Infinity, -0.01, 1000.01, 0.001])('坏赠金百分比 %s 被拒绝', (value) => {
    expect(isRechargeBonusPercentValidForMode(value, 'bonus')).toBe(false)
  })

  it.each([100, 100.01, 1000])('折扣百分比 %s 不能把实付变成零或负数', (value) => {
    expect(isRechargeBonusPercentValidForMode(value, 'discount')).toBe(false)
    expect(quoteRechargeBonus([{ min_amount: 0, bonus_percent: value }], 100, { mode: 'discount' }))
      .toMatchObject({ payBase: 100, bonus: 0, credited: 100 })
  })

  it.each(['__proto__', 'constructor', '<script>alert(1)</script>', null, {}])('异常模式 %j 回退为赠金', (mode) => {
    expect(normalizeRechargeBonusMode(mode)).toBe('bonus')
  })

  it('重复阈值拒绝保存，未完成草稿不产生零金额优惠', () => {
    const tiers = [{ min_amount: 10, bonus_percent: 20 }, { min_amount: 10, bonus_percent: 30 }]
    expect(rechargeBonusTierError(tiers, 0, 'bonus')).toBe('duplicateMinAmount')
    expect(sanitizeRechargeBonusTiersForSubmit([{ min_amount: null, bonus_percent: 20 }])).toEqual([])
    expect(isRechargeBonusMinAmountValid(-0.01)).toBe(false)
  })
})
