import type { RechargeBonusTier } from '@/types/payment'

export type { RechargeBonusTier }

export type RechargeBonusMode = 'bonus' | 'discount'

export const MAX_RECHARGE_BONUS_TIERS = 20
export const MAX_RECHARGE_BONUS_PERCENT = 1000

const AMOUNT_EPSILON = 1e-9

// 后台编辑态：两个值都允许留空（未完成的行在提交时丢弃）。
export interface RechargeBonusTierDraft {
  min_amount: number | null
  bonus_percent: number | null
}

export interface RechargeBonusInterval {
  from: number
  /** null 表示开区间（≥ from） */
  to: number | null
  percent: number
}

export interface RechargeBonusQuote {
  /** 输入或计算结果非法时不得展示为零报价，也不得进入提交门禁 */
  valid: boolean
  mode: RechargeBonusMode
  /** 命中档位的百分比；未命中或未产生优惠时为 0 */
  percent: number
  /** 网关收款基数（支付币种，不含手续费）；赠金模式 = 输入金额，折扣模式 = 折后金额 */
  payBase: number
  /** 到账基数（输入金额 × 倍率，USD），不含赠送 */
  base: number
  /** 免费额度（USD）：赠金模式为额外赠送，折扣模式为未付费却到账的部分 */
  bonus: number
  /** 到账总额（USD） */
  credited: number
  tier: RechargeBonusTier | null
}

interface RechargeDecimal {
  coefficient: bigint
  scale: number
}

// 从每个输入的十进制表示开始运算，不能先浮点相乘再补 EPSILON：40.3 × 25% 会丢失半分边界。
function rechargeDecimal(value: number): RechargeDecimal {
  const [mantissa = '0', exponent = '0'] = value.toString().split('e')
  const [integer = '0', fraction = ''] = mantissa.split('.')
  const scale = fraction.length - Number(exponent)
  const coefficient = BigInt(integer + fraction)
  return scale < 0
    ? { coefficient: coefficient * 10n ** BigInt(-scale), scale: 0 }
    : { coefficient, scale }
}

function multiplyRechargeDecimal(left: RechargeDecimal, right: RechargeDecimal): RechargeDecimal {
  return { coefficient: left.coefficient * right.coefficient, scale: left.scale + right.scale }
}

function addRechargeDecimal(left: RechargeDecimal, right: RechargeDecimal): RechargeDecimal {
  const scale = Math.max(left.scale, right.scale)
  return {
    coefficient: left.coefficient * 10n ** BigInt(scale - left.scale)
      + right.coefficient * 10n ** BigInt(scale - right.scale),
    scale,
  }
}

function normalizeRechargeDigits(digits: number): number {
  // 币种精度来自 Intl；限制意外输入，避免非法指数或无限扩大的 BigInt 运算。
  return Number.isInteger(digits) && digits >= 0 && digits <= 100 ? digits : 2
}

function roundRechargeDecimal(value: RechargeDecimal, digits = 2): number {
  const places = normalizeRechargeDigits(digits)
  if (value.scale <= places) return Number(`${value.coefficient}e-${value.scale}`)
  const divisor = 10n ** BigInt(value.scale - places)
  const negative = value.coefficient < 0n
  const absolute = negative ? -value.coefficient : value.coefficient
  // 与后端 Decimal.Round 一致，恰好一半时远离零；直到最终展示才转回 number。
  const rounded = absolute / divisor + (absolute % divisor * 2n >= divisor ? 1n : 0n)
  return Number(`${negative ? -rounded : rounded}e-${places}`)
}

export function roundRechargeAmount(value: number, digits = 2): number {
  if (!Number.isFinite(value)) return 0
  return roundRechargeDecimal(rechargeDecimal(value), digits)
}

function hasAtMostTwoDecimals(value: number): boolean {
  return Math.abs(roundRechargeAmount(value) - value) < AMOUNT_EPSILON
}

export function normalizeRechargeBonusMode(raw: unknown): RechargeBonusMode {
  return String(raw ?? '').trim().toLowerCase() === 'discount' ? 'discount' : 'bonus'
}

export function isRechargeBonusMinAmountValid(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 && hasAtMostTwoDecimals(value)
}

export function isRechargeBonusPercentValid(value: unknown): value is number {
  return (
    typeof value === 'number' &&
    Number.isFinite(value) &&
    value >= 0 &&
    value <= MAX_RECHARGE_BONUS_PERCENT &&
    hasAtMostTwoDecimals(value)
  )
}

// 折扣模式下百分比必须 < 100，否则实付为 0 或负数；与后端 ValidateRechargeBonusTiersForMode 一致。
export function isRechargeBonusPercentValidForMode(value: unknown, mode: RechargeBonusMode): value is number {
  if (!isRechargeBonusPercentValid(value)) return false
  return mode !== 'discount' || value < 100
}

function minAmountKey(value: number): string {
  return roundRechargeAmount(value).toFixed(2)
}

function collectTiers(candidates: { min_amount: unknown; bonus_percent: unknown }[]): RechargeBonusTier[] {
  const seen = new Set<string>()
  const out: RechargeBonusTier[] = []
  for (const item of candidates) {
    const minAmount = item.min_amount
    const percent = item.bonus_percent
    if (!isRechargeBonusMinAmountValid(minAmount) || !isRechargeBonusPercentValid(percent)) continue
    const key = minAmountKey(minAmount)
    if (seen.has(key)) continue
    seen.add(key)
    out.push({ min_amount: minAmount, bonus_percent: percent })
  }
  out.sort((a, b) => a.min_amount - b.min_amount)
  return out
}

// 读路径宽松归一（checkout-info / 后台 GET 回填）：非法行丢弃，同阈值保留先出现，按阈值升序。
export function normalizeRechargeBonusTiers(raw: unknown): RechargeBonusTier[] {
  if (!Array.isArray(raw)) return []
  const candidates: { min_amount: unknown; bonus_percent: unknown }[] = []
  for (const item of raw) {
    if (!item || typeof item !== 'object') continue
    const record = item as { min_amount?: unknown; bonus_percent?: unknown }
    candidates.push({
      min_amount: record.min_amount === null || record.min_amount === undefined || record.min_amount === '' ? NaN : Number(record.min_amount),
      bonus_percent: record.bonus_percent === null || record.bonus_percent === undefined || record.bonus_percent === '' ? NaN : Number(record.bonus_percent),
    })
  }
  return collectTiers(candidates)
}

// 提交清洗：留空/非法的行整行丢弃，同阈值保留先出现，按阈值升序。
export function sanitizeRechargeBonusTiersForSubmit(
  tiers: RechargeBonusTierDraft[] | null | undefined,
): RechargeBonusTier[] {
  if (!Array.isArray(tiers)) return []
  return collectTiers(
    tiers.map((tier) => ({
      min_amount: tier?.min_amount === null || tier?.min_amount === undefined ? NaN : Number(tier.min_amount),
      bonus_percent: tier?.bonus_percent === null || tier?.bonus_percent === undefined ? NaN : Number(tier.bonus_percent),
    })),
  )
}

// 编辑器即时校验：同一阈值在其他行已出现时返回 true。
export function isDuplicateRechargeBonusMinAmount(tiers: RechargeBonusTierDraft[], index: number): boolean {
  const current = tiers[index]?.min_amount
  if (!isRechargeBonusMinAmountValid(current)) return false
  const key = minAmountKey(current)
  return tiers.some((tier, i) => i !== index && isRechargeBonusMinAmountValid(tier.min_amount) && minAmountKey(tier.min_amount) === key)
}

export type RechargeBonusTierError = '' | 'invalidMinAmount' | 'invalidPercent' | 'invalidDiscountPercent' | 'duplicateMinAmount'

// 编辑提示与提交门禁共用校验，不能把管理员填错的现有档位静默删除。
export function rechargeBonusTierError(tiers: RechargeBonusTierDraft[], index: number, mode: RechargeBonusMode): RechargeBonusTierError {
  const tier = tiers[index]
  if (!tier) return ''
  if (tier.min_amount !== null && !isRechargeBonusMinAmountValid(tier.min_amount)) return 'invalidMinAmount'
  if (tier.bonus_percent !== null && !isRechargeBonusPercentValidForMode(tier.bonus_percent, mode)) {
    return mode === 'discount' && isRechargeBonusPercentValidForMode(tier.bonus_percent, 'bonus')
      ? 'invalidDiscountPercent' : 'invalidPercent'
  }
  if (isDuplicateRechargeBonusMinAmount(tiers, index)) return 'duplicateMinAmount'
  return ''
}

// 命中规则：取不超过支付金额的最大阈值档位；与后端 matchRechargeBonusTier 一致。
export function matchRechargeBonusTier(tiers: RechargeBonusTier[], paymentAmount: number): RechargeBonusTier | null {
  if (!Number.isFinite(paymentAmount) || paymentAmount <= 0) return null
  let matched: RechargeBonusTier | null = null
  for (const tier of tiers) {
    if (paymentAmount + AMOUNT_EPSILON < tier.min_amount) continue
    if (!matched || tier.min_amount > matched.min_amount) matched = tier
  }
  return matched
}

// 赠送额度 = 到账基数 × 百分比，保留两位小数；与后端 calculateRechargeBonus 一致。
export function calculateRechargeBonus(baseCredited: number, percent: number): number {
  if (!Number.isFinite(baseCredited) || !Number.isFinite(percent) || baseCredited <= 0 || percent <= 0) return 0
  const product = multiplyRechargeDecimal(rechargeDecimal(baseCredited), rechargeDecimal(percent))
  return roundRechargeDecimal({ ...product, scale: product.scale + 2 })
}

export interface RechargeBonusQuoteOptions {
  multiplier?: number
  mode?: RechargeBonusMode
  /** 支付币种小数位，折扣模式实付基数按此精度四舍五入 */
  currencyDigits?: number
}

// 充值页报价：阈值按支付金额比较；赠金模式按到账基数加赠送，折扣模式按百分比减实付。
// 与后端 quoteRechargeBonus 一致（含折扣 ≥ 100% 的 fail-safe）。
export function quoteRechargeBonus(
  tiers: RechargeBonusTier[],
  paymentAmount: number,
  options: RechargeBonusQuoteOptions | number = {},
): RechargeBonusQuote {
  const opts: RechargeBonusQuoteOptions = typeof options === 'number' ? { multiplier: options } : options
  const mode = opts.mode ?? 'bonus'
  // 非有限数必须保留失败语义，不能归零后被渠道“无限额”或空输入逻辑接受。
  if (!Number.isFinite(paymentAmount) || paymentAmount <= 0 || (opts.multiplier !== undefined && !Number.isFinite(opts.multiplier))) {
    return { valid: false, mode, percent: 0, payBase: NaN, base: NaN, bonus: NaN, credited: NaN, tier: null }
  }
  const amount = paymentAmount
  const rate = typeof opts.multiplier === 'number' && Number.isFinite(opts.multiplier) && opts.multiplier > 0 ? opts.multiplier : 1
  const digits = normalizeRechargeDigits(opts.currencyDigits ?? 2)
  const amountDecimal = rechargeDecimal(amount)
  const rateDecimal = rechargeDecimal(rate)
  const base = roundRechargeDecimal(multiplyRechargeDecimal(amountDecimal, rateDecimal))
  const quote: RechargeBonusQuote = { valid: Number.isFinite(base) && base > 0, mode, percent: 0, payBase: amount, base, bonus: 0, credited: base, tier: null }
  if (!quote.valid) return quote
  const tier = matchRechargeBonusTier(tiers, amount)
  if (!tier || !Number.isFinite(tier.bonus_percent) || tier.bonus_percent <= 0) return quote
  quote.tier = tier
  if (mode === 'discount') {
    if (tier.bonus_percent >= 100) return quote
    const remainingPercent = addRechargeDecimal(rechargeDecimal(100), rechargeDecimal(-tier.bonus_percent))
    const discounted = multiplyRechargeDecimal(amountDecimal, remainingPercent)
    const payBase = roundRechargeDecimal({ ...discounted, scale: discounted.scale + 2 }, digits)
    if (payBase <= 0 || payBase >= amount) return quote
    const paidCredit = roundRechargeDecimal(multiplyRechargeDecimal(rechargeDecimal(payBase), rateDecimal))
    quote.payBase = payBase
    quote.bonus = Math.max(0, roundRechargeDecimal(addRechargeDecimal(rechargeDecimal(base), rechargeDecimal(-paidCredit))))
    quote.percent = tier.bonus_percent
    return quote
  }
  const bonus = calculateRechargeBonus(base, tier.bonus_percent)
  if (bonus <= 0) return quote
  if (!Number.isFinite(bonus)) return { ...quote, valid: false, credited: Infinity }
  quote.bonus = bonus
  quote.credited = roundRechargeDecimal(addRechargeDecimal(rechargeDecimal(base), rechargeDecimal(bonus)))
  quote.valid = Number.isFinite(quote.credited) && quote.credited > 0
  quote.percent = tier.bonus_percent
  return quote
}

// 区间预览：把升序阈值列表展开为 [from, to) 区间；首档阈值 > 0 时补一段「无优惠」。
export function describeRechargeBonusIntervals(tiers: RechargeBonusTier[]): RechargeBonusInterval[] {
  const sorted = [...tiers].sort((a, b) => a.min_amount - b.min_amount)
  const out: RechargeBonusInterval[] = []
  if (sorted.length === 0) return out
  if (sorted[0]!.min_amount > 0) {
    out.push({ from: 0, to: sorted[0]!.min_amount, percent: 0 })
  }
  sorted.forEach((tier, index) => {
    out.push({ from: tier.min_amount, to: sorted[index + 1]?.min_amount ?? null, percent: tier.bonus_percent })
  })
  return out
}

// 展示用：去掉多余的小数 0（20 → "20"，12.5 → "12.5"）。
export function formatRechargeBonusNumber(value: number): string {
  if (!Number.isFinite(value)) return '0'
  return String(Number(value.toFixed(2)))
}
