import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import VideoTaskBillingDetails from '../VideoTaskBillingDetails.vue'
import zh from '@/i18n/locales/zh/mediaTasks'
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => {
  if (key === 'mediaTasks.videoBilling.deferredHint') return zh.mediaTasks.videoBilling.deferredHint
  if (key === 'mediaTasks.videoBilling.deferredStatus') return zh.mediaTasks.videoBilling.deferredStatus
  const source = key.replace('mediaTasks.videoBilling.sources.', '')
  return source === 'group' || source === 'channel' ? zh.mediaTasks.videoBilling.sources[source] : key
} }) }))

describe('Video task billing', () => {
  // 保留任务冻结单价和参考图片费，历史参考视频标记不再定义另一种单价。
  it.each([false, true])('omits legacy reference-video condition %s from the billing summary', hasReference => {
    const billing = { status: 'settled' as const, mode: 'video' as const, resolution: '720p', has_reference_video: hasReference,
      unit_price: 0.3, unit: 'second' as const, duration_seconds: 8, reserved_amount: 2.7, actual_amount: 2.7, pricing_source: 'channel',
      reference_image_count: 7, reference_image_free_count: 5, billable_reference_image_count: 2,
      reference_image_unit_price: 0.15, reference_image_cost: 0.3 }
    const wrapper = mount(VideoTaskBillingDetails, { props: { billing } })
    const labels = wrapper.findAll('dt').map(field => field.text())
    expect(labels).not.toContain('mediaTasks.videoBilling.reference')
    expect(wrapper.text()).toContain('$0.30000 / s')
    expect(labels).toContain('mediaTasks.videoBilling.referenceImageCost')
    expect(wrapper.text()).toContain('$0.15000')
    wrapper.unmount()
  })

  // 每任务只计一次，失败释放不能显示为已收费，生成时长也不是按次的计费依据。
  it('shows per-task unit and one charge only after settlement', async () => {
    const billing = { status: 'reserved' as const, mode: 'video_per_request' as const, resolution: '', has_reference_video: false,
      unit_price: 2, unit: 'request' as const, duration_seconds: 0, reserved_amount: 2.3, pricing_source: 'group',
      reference_image_count: 7, reference_image_free_count: 5, billable_reference_image_count: 2,
      reference_image_unit_price: 0.15, reference_image_cost: 0.3 }
    const wrapper = mount(VideoTaskBillingDetails, { props: { billing } })
    const valueFor = (key: string) => wrapper.findAll('dt').find(field => field.text() === `mediaTasks.videoBilling.${key}`)?.element.nextElementSibling?.textContent
    expect(valueFor('unitPrice')).toBe('$2.00000 / mediaTasks.videoBilling.requestUnit')
    expect(valueFor('requestCount')).toBe('—')
    expect(valueFor('duration')).toBeUndefined()
    expect(valueFor('generatedDuration')).toBe('—')
    expect(wrapper.find('[data-testid="video-token-prepay-hint"]').exists()).toBe(false)
    await wrapper.setProps({ billing: { ...billing, status: 'settled', actual_amount: 2.3 } })
    expect(valueFor('requestCount')).toBe('1')
    expect(valueFor('actual')).toBe('$2.30000')
    expect(valueFor('referenceImageCost')).toBe('$0.30000')
    await wrapper.setProps({ billing: { ...billing, status: 'released' } })
    expect(valueFor('requestCount')).toBe('—')
    expect(valueFor('actual')).toBe('mediaTasks.videoBilling.notCharged')
    expect(valueFor('originalReserved')).toBe('$2.30000')
    expect(valueFor('reserved')).toBeUndefined()
    expect(wrapper.get('[data-testid="video-billing-released-hint"]').text()).toBe('mediaTasks.videoBilling.releasedHint')
    wrapper.unmount()
  })

  // 2.70 预扣包含固定秒价和图片费，不能提前显示为真实 Token 实扣。
  it('separates fixed duration prepayment from the eventual Token settlement', async () => {
    const billing = { status: 'reserved' as const, mode: 'video_token' as const, resolution: '720p', has_reference_video: false,
      unit_price: 15, unit: 'million_tokens' as const, duration_seconds: 8, reserved_amount: 2.7, pricing_source: 'group',
      token_prepay: true, prepay_price_per_second: 0.3, prepay_duration_seconds: 8, actual_amount: 2.7 }
    const wrapper = mount(VideoTaskBillingDetails, { props: { billing } })
    const valueFor = (key: string) => wrapper.findAll('dt').find(field => field.text() === `mediaTasks.videoBilling.${key}`)?.element.nextElementSibling?.textContent
    expect(valueFor('reserved')).toBe('$2.70000')
    expect(valueFor('prepayPricePerSecond')).toBe('$0.30000')
    expect(valueFor('prepayDurationSeconds')).toBe('8')
    expect(valueFor('actual')).toBe('mediaTasks.videoBilling.unknown')
    expect(wrapper.find('[data-testid="video-token-prepay-hint"]').exists()).toBe(true)
    await wrapper.setProps({ billing: { ...billing, status: 'settled', actual_amount: 0.7059298 } })
    expect(valueFor('actual')).toBe('$0.70593')
    expect(valueFor('originalReserved')).toBe('$2.70000')
    expect(valueFor('reserved')).toBeUndefined()
    await wrapper.setProps({ billing: { ...billing, token_prepay: false } })
    expect(valueFor('prepayPricePerSecond')).toBeUndefined()
  })
  // 参考图片计数和价卡可提前查看，实际费用只有结算成功后才显示。
  it('shows fixed reference-image details without treating pending costs as settled', async () => {
    const billing = { status: 'reserved' as const, mode: 'video_token' as const, resolution: '720p', has_reference_video: false,
      unit_price: 15, unit: 'million_tokens' as const, duration_seconds: 0, reserved_amount: 2, pricing_source: 'group',
      reference_image_count: 5, reference_image_free_count: 2, billable_reference_image_count: 3,
      reference_image_unit_price: 0.05, reference_image_cost: 0.15 }
    const wrapper = mount(VideoTaskBillingDetails, { props: { billing } })
    const valueFor = (key: string) => wrapper.findAll('dt').find(field => field.text() === `mediaTasks.videoBilling.${key}`)?.element.nextElementSibling?.textContent
    expect(valueFor('referenceImageCount')).toBe('5')
    expect(valueFor('referenceImageFreeCount')).toBe('2')
    expect(valueFor('billableReferenceImageCount')).toBe('3')
    expect(valueFor('referenceImageUnitPrice')).toBe('$0.05000')
    expect(valueFor('referenceImageCost')).toBe('mediaTasks.videoBilling.unknown')
    await wrapper.setProps({ billing: { ...billing, status: 'settled', actual_amount: 1.15 } })
    expect(valueFor('referenceImageCost')).toBe('$0.15000')
    await wrapper.setProps({ billing: { ...billing, status: 'released' } })
    expect(valueFor('referenceImageCost')).toBe('mediaTasks.videoBilling.notCharged')
    await wrapper.setProps({ billing: { ...billing, reference_image_unit_price: undefined } })
    expect(valueFor('referenceImageCount')).toBeUndefined()
    wrapper.unmount()
  })
  // 不预留不等于免费；后端沿用 pending/reserved 状态时显示待结算，结算前隐藏实际金额。
  it.each(['pending', 'reserved'] as const)('labels deferred %s billing without displaying zero as a charge', async status => {
    const billing = { status, mode: 'video_token' as const, resolution: '720p', has_reference_video: false,
      unit_price: 15, unit: 'million_tokens' as const, duration_seconds: 0, reserved_amount: 0,
      deferred_billing: true, actual_amount: 0, pricing_source: 'group' }
    const wrapper = mount(VideoTaskBillingDetails, { props: { billing } })
    expect(wrapper.text()).toContain('未预留，按实际用量结算')
    expect(wrapper.text()).toContain('待结算')
    expect(wrapper.text()).toContain('mediaTasks.videoBilling.unknown')
    expect(wrapper.text()).not.toContain('$0.00000')
    await wrapper.setProps({ billing: { ...billing, status: 'settled', actual_amount: 0.00000001 } })
    expect(wrapper.text()).toContain('<$0.00001')
    expect(wrapper.text()).toContain('mediaTasks.videoBilling.notReserved')
    expect(wrapper.text()).not.toContain('mediaTasks.videoBilling.unknown')
    wrapper.unmount()
  })

  // 五位显示保留非零小额提示，零时长是缺失哨兵，来源使用实际语言资源。
  it('rounds video charges to five decimals without making tiny charges appear free', async () => {
    const billing = { status: 'settled' as const, mode: 'video_token' as const, resolution: '480p', has_reference_video: false,
      unit_price: 0.00000003, unit: 'million_tokens' as const, tokens: 1, duration_seconds: 0,
      reserved_amount: 0.00000002, actual_amount: 0.00000001, pricing_source: 'group' }
    const wrapper = mount(VideoTaskBillingDetails, { props: { billing } })
    expect(wrapper.text()).toContain('<$0.00001 / 1M Token')
    expect(wrapper.text()).not.toContain('$0.00000')
    const duration = wrapper.findAll('dt').find(field => field.text() === 'mediaTasks.videoBilling.duration')!
    expect(duration.element.nextElementSibling?.textContent).toBe('—')
    expect(wrapper.text()).toContain('分组定价')
    await wrapper.setProps({ billing: { ...billing, unit_price: 8.74, reserved_amount: 2.4, actual_amount: 0.7059298, pricing_source: 'channel' } })
    expect(wrapper.text()).toContain('$8.74000 / 1M Token')
    expect(wrapper.text()).toContain('$2.40000')
    expect(wrapper.text()).toContain('$0.70593')
    expect(wrapper.text()).not.toContain('$0.7059298')
    expect(wrapper.text()).toContain('渠道定价')
    expect(wrapper.text()).not.toContain('分组定价')
    wrapper.unmount()
  })

  // 任务成功与结算完成独立；缺失 actual_amount 不得显示成零消费。
  it('shows reconciliation, units and reserved versus unknown actual amount', async () => {
    const billing = { status: 'reconciliation' as const, mode: 'video_token' as const, resolution: '720p', has_reference_video: true,
      unit_price: 15, unit: 'million_tokens' as const, tokens: 123456, duration_seconds: 8, reserved_amount: 2, pricing_source: 'group' }
    const wrapper = mount(VideoTaskBillingDetails, { props: { billing } })
    expect(wrapper.text()).toContain('mediaTasks.videoBilling.reconciliationHint')
    expect(wrapper.text()).toContain('$15.00000 / 1M Token')
    expect(wrapper.text()).toContain('$2.00000')
    expect(wrapper.text()).toContain('mediaTasks.videoBilling.unknown')
    await wrapper.setProps({ billing: { ...billing, status: 'settled', actual_amount: 0 } })
    expect(wrapper.text()).toContain('$0.00000')
    expect(wrapper.text()).not.toContain('mediaTasks.videoBilling.unknown')
    wrapper.unmount()
  })
})
