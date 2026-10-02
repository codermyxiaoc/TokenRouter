import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import VideoUsagePricingDetails from '../VideoUsagePricingDetails.vue'
import type { UsageLog, UsageVideoBillingDetails } from '@/types'
import zh from '@/i18n/locales/zh/dashboard'
import en from '@/i18n/locales/en/dashboard'

// 使用真实中英文词条，确认没有翻译键外露和单位错写。
vi.mock('vue-i18n', () => import('../../../../../node_modules/vue-i18n/dist/vue-i18n.mjs'))
vi.mock('@/composables/useBalanceDisplay', () => ({ useBalanceDisplay: () => ({
  formatUsdAmount: (value: number, options: { fractionDigits: number }) => `$${value.toFixed(options.fractionDigits)}`,
}) }))

const billing: UsageVideoBillingDetails = {
  mode: 'video_token', unit: 'million_tokens', unit_price: 8.74, tokens: 80770,
  duration_seconds: 8.25, resolution: '480p', has_reference_video: true,
  reference_image_count: 7, reference_image_free_count: 5, billable_reference_image_count: 2,
  reference_image_unit_price: 0.15, reference_image_cost: 0.3,
}

const mountDetails = (value: UsageVideoBillingDetails | undefined, locale = 'zh') => mount(VideoUsagePricingDetails, {
  props: { row: { billing_mode: 'video_token', video_billing: value } as UsageLog },
  global: { plugins: [createI18n({ legacy: false, locale, messages: { zh, en } })] },
})

describe('视频使用记录费用事实', () => {
  // 参考视频是历史请求元数据，不再作为价格条件；参考图片附加费继续单独展示。
  it.each([false, true])('does not expose legacy reference-video flag %s as a price condition', hasReference => {
    const wrapper = mountDetails({ ...billing, has_reference_video: hasReference })
    expect(wrapper.find('[data-testid="video-usage-videoReference"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="video-usage-videoTokenPrice"]').text()).toContain('$8.74000000 / 1M')
    expect(wrapper.get('[data-testid="video-usage-videoReferenceImageCost"]').text()).toContain('$0.30000000')
    wrapper.unmount()
  })

  // 按次显示单任务价格和固定图费，不能将 Token 或时长误标为计费数量。
  it.each(['zh', 'en'])('shows a per-task charge and image fee in %s', locale => {
    const wrapper = mountDetails({ ...billing, mode: 'video_per_request', unit: 'request', unit_price: 2, tokens: undefined }, locale)
    expect(wrapper.text()).not.toContain('usage.')
    expect(wrapper.get('[data-testid="video-usage-videoRequestPrice"]').text()).toContain(locale === 'zh' ? '$2.00000000 / 次' : '$2.00000000 / request')
    expect(wrapper.get('[data-testid="video-usage-videoBillingRequests"]').text()).toContain('1')
    expect(wrapper.find('[data-testid="video-usage-videoBillingTokens"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="video-usage-videoReferenceImageCost"]').text()).toContain('$0.30000000')
    wrapper.unmount()
  })

  it.each(['zh', 'en'])('renders exact snapshot price, duration and fixed image fee in %s', locale => {
    const wrapper = mountDetails(billing, locale)
    expect(wrapper.text()).not.toContain('usage.')
    expect(wrapper.text()).toContain('$8.74000000 / 1M')
    expect(wrapper.get('[data-testid="video-usage-videoDuration"]').text()).toContain('8.25')
    expect(wrapper.get('[data-testid="video-usage-videoBillingTokens"]').text()).toContain('80,770')
    expect(wrapper.get('[data-testid="video-usage-videoReferenceImageCost"]').text()).toContain('$0.30000000')
    expect(wrapper.get('[data-testid="video-usage-videoReferenceImageFreeCount"]').text()).toContain(locale === 'zh' ? '免费额度' : 'allowance')
    wrapper.unmount()
  })

  // 显式零价和零张数必须展示，缺失的费用规则则保持未知。
  it('preserves zero prices and separates an unconfigured image fee from a free rule', async () => {
    const wrapper = mountDetails({ ...billing, unit_price: 0, tokens: 0, reference_image_count: 0,
      reference_image_unit_price: 0, reference_image_cost: 0, billable_reference_image_count: 0 })
    expect(wrapper.get('[data-testid="video-usage-videoTokenPrice"]').text()).toContain('$0.00000000')
    expect(wrapper.get('[data-testid="video-usage-videoReferenceImages"]').text()).toContain('0张')
    expect(wrapper.get('[data-testid="video-usage-videoReferenceImagePrice"]').text()).toContain('$0.00000000')
    await wrapper.setProps({ row: { billing_mode: 'video_token', video_billing: { ...billing,
      reference_image_free_count: undefined, billable_reference_image_count: undefined,
      reference_image_unit_price: undefined, reference_image_cost: undefined } } as UsageLog })
    expect(wrapper.get('[data-testid="video-usage-videoReferenceImages"]').text()).toContain('7张')
    expect(wrapper.find('[data-testid="video-usage-videoReferenceImagePrice"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="video-usage-videoReferenceImageFreeCount"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('omits unknown historical metadata instead of guessing zero', () => {
    const wrapper = mountDetails(undefined)
    expect(wrapper.text()).toContain('历史定价明细未记录')
    expect(wrapper.text()).not.toContain('$0.00000000')
    expect(wrapper.find('[data-testid="video-usage-videoReferenceImages"]').exists()).toBe(false)
    wrapper.unmount()
  })
})
