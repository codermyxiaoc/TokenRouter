import { describe, expect, it } from 'vitest'
import {
  BILLING_MODE_IMAGE,
  BILLING_MODE_TOKEN,
  BILLING_MODE_VIDEO,
  BILLING_MODE_VIDEO_SECOND,
  BILLING_MODE_VIDEO_REQUEST,
  BILLING_MODE_VIDEO_TOKEN,
  BILLING_MODE_VIDEO_PER_REQUEST,
  getBillingModeLabel,
  getDisplayBillingMode,
  isImageUsage
} from '../billingMode'

describe('billingMode helpers', () => {
  // 独立按次任务不能因带参考图片而显示图片计费，普通 per_request 保持原义。
  it('recognizes Video per-task mode even without a detail snapshot', () => {
    const row = { billing_mode: BILLING_MODE_VIDEO_PER_REQUEST, image_count: 7 }
    expect(isImageUsage(row)).toBe(false)
    expect(getDisplayBillingMode(row)).toBe(BILLING_MODE_VIDEO_PER_REQUEST)
    expect(getBillingModeLabel(getDisplayBillingMode(row), key => key)).toBe('admin.channels.billingMode.videoPerRequest')
    expect(getDisplayBillingMode({ ...row, video_billing: { unit: 'request' } })).toBe(BILLING_MODE_VIDEO_REQUEST)
    expect(getDisplayBillingMode({ image_count: 0, billing_mode: 'per_request' })).toBe('per_request')
  })

  // 旧 Grok 的 video 可能按次，只有历史快照证明单位时才给出明确标签。
  it.each([
    ['second', BILLING_MODE_VIDEO_SECOND],
    ['request', BILLING_MODE_VIDEO_REQUEST],
    ['million_tokens', BILLING_MODE_VIDEO_TOKEN],
  ] as const)('uses a recorded video unit %s without reinterpreting per-request records', (unit, expected) => {
    expect(getDisplayBillingMode({ image_count: 0, billing_mode: BILLING_MODE_VIDEO, video_billing: { unit } })).toBe(expected)
    expect(getDisplayBillingMode({ image_count: 0, billing_mode: 'per_request', video_billing: { unit } })).toBe('per_request')
  })
  it('prefers explicit video mode over image_count', () => {
    expect(
      getDisplayBillingMode({ image_count: 1, billing_mode: BILLING_MODE_VIDEO })
    ).toBe(BILLING_MODE_VIDEO)
    expect(isImageUsage({ image_count: 1, billing_mode: BILLING_MODE_VIDEO })).toBe(false)
  })

  it('infers image when image_count set and mode missing', () => {
    expect(getDisplayBillingMode({ image_count: 2, billing_mode: null })).toBe(BILLING_MODE_IMAGE)
  })

  it('keeps token mode even with image_count', () => {
    expect(
      getDisplayBillingMode({ image_count: 1, billing_mode: BILLING_MODE_TOKEN })
    ).toBe(BILLING_MODE_TOKEN)
  })
})
