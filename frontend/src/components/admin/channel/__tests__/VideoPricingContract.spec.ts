import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import VideoTokenPrepay from '../VideoTokenPrepay.vue'
import VideoPricingMatrix from '../VideoPricingMatrix.vue'
import VideoTokenPrepaySummary from '@/components/marketplace/VideoTokenPrepaySummary.vue'
import VideoImageInputPricingSummary from '@/components/marketplace/VideoImageInputPricingSummary.vue'
import zhChannels from '@/i18n/locales/zh/admin/channels'
import enChannels from '@/i18n/locales/en/admin/channels'
import zhMisc from '@/i18n/locales/zh/misc'
import enMisc from '@/i18n/locales/en/misc'

// 与已有词条渲染测试一致：源码词条使用带编译器版本，生产包使用预编译词条。
vi.mock('vue-i18n', () => import('../../../../../node_modules/vue-i18n/dist/vue-i18n.mjs'))

// 真实语言资源也参与渲染，防止只校验翻译键而遗漏固定倍率和终态退补说明。
describe('Video pricing contract copy', () => {
  // 新模式的中英文单位必须能真实渲染，附加图费仍保持固定价。
  it.each(['zh', 'en'] as const)('renders per-request units and image fees in %s', locale => {
    const i18n = createI18n({ legacy: false, locale, messages: {
      zh: { admin: zhChannels, ...zhMisc, common: { delete: '删除' } }, en: { admin: enChannels, ...enMisc, common: { delete: 'Delete' } },
    } })
    const global = { plugins: [i18n] }
    const editor = mount(VideoPricingMatrix, { props: { mode: 'video_per_request', modelValue: [{ resolution: '768p', price: 2 }] }, global })
    expect(editor.text()).not.toContain('admin.channels.')
    expect(editor.text()).toContain(locale === 'zh' ? '$/次' : '$/request')
    expect(editor.text()).not.toContain('$/s')
    expect(editor.findAll('input[type="number"]')).toHaveLength(1)
    expect(editor.text()).not.toContain(locale === 'zh' ? '含参考视频' : 'With reference video')
    expect(editor.text()).not.toContain(locale === 'zh' ? '无参考视频' : 'Without reference video')
    const images = mount(VideoImageInputPricingSummary, { props: { pricing: { pricing_mode: 'video_per_request', price_status: 'priced', video_image_input_pricing: { free_images: 5, price: 0.15 } } }, global })
    expect(images.text()).toContain('$0.15000 USD')
    expect(images.text()).toContain(locale === 'zh' ? '不参与任何倍率' : 'no multipliers')
    editor.unmount()
    images.unmount()
  })

  it.each(['zh', 'en'] as const)('renders fixed USD prepayment and image fees in %s', locale => {
    const i18n = createI18n({ legacy: false, locale, messages: {
      zh: { admin: zhChannels, ...zhMisc }, en: { admin: enChannels, ...enMisc },
    } })
    const global = { plugins: [i18n] }
    const editor = mount(VideoTokenPrepay, { props: { modelValue: { price_per_second: 0.3 } }, global })
    expect(editor.text()).not.toContain('admin.channels.')
    expect(editor.text()).toContain('USD')
    for (const word of locale === 'zh' ? ['渠道', '视频', '分组', '套餐', '余额', '多退少补'] : ['channel', 'video', 'group', 'plan', 'balance', 'refund or additional charge']) {
      expect(editor.text()).toContain(word)
    }
    const pricing = { pricing_mode: 'video_token' as const, price_status: 'priced' as const,
      video_token_prepay: { price_per_second: 0.3 }, video_image_input_pricing: { free_images: 5, price: 0.15 } }
    const prepay = mount(VideoTokenPrepaySummary, { props: { pricing }, global })
    const images = mount(VideoImageInputPricingSummary, { props: { pricing }, global })
    expect(prepay.text()).not.toContain('marketplace.')
    expect(prepay.text()).toContain('$0.30000 USD')
    expect(prepay.text()).toContain(locale === 'zh' ? '非最终价格' : 'not the final price')
    expect(images.text()).toContain('$0.15000 USD')
    expect(images.text()).toContain('5')
    expect(images.text()).toContain(locale === 'zh' ? '不参与任何倍率' : 'no multipliers')
    editor.unmount()
    prepay.unmount()
    images.unmount()
  })
})
