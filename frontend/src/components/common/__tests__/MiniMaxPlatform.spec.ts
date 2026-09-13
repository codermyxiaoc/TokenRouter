import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import PlatformIcon from '../PlatformIcon.vue'
import { modelIconData } from '@/utils/modelIconData'
import { platformBadgeClass, platformLabel } from '@/utils/platformColors'
import { allModels, getModelsByPlatform } from '@/composables/useModelWhitelist'
import { CN_BASE_URL_PRESETS, cnBalanceCellVisible, cnQuotaCellVisible } from '@/components/account/credentialsBuilder'

describe('MiniMax 平台目录', () => {
  it('使用供应商自己的模型目录，保留 Qoder 路由别名', () => {
    expect(getModelsByPlatform('minimax')).toEqual([
      'MiniMax-M3', 'MiniMax-M2.7', 'MiniMax-M2.7-highspeed',
      'MiniMax-M2.5', 'MiniMax-M2.5-highspeed', 'MiniMax-M2.1',
      'MiniMax-M2.1-highspeed', 'MiniMax-M2'
    ])
    expect(getModelsByPlatform('qoder', 'global')).toContain('minimax-m3')
    expect(getModelsByPlatform('qoder', 'cn')).toContain('minimax-m2.7')
    expect(allModels.map(model => model.value)).toContain('abab6.5-chat')
  })

  it('使用现有品牌图标和独立平台颜色', () => {
    const wrapper = mount(PlatformIcon, { props: { platform: 'minimax' } })
    expect(wrapper.get('path').attributes('d')).toBe(modelIconData.minimax.paths[0])
    expect(platformLabel('minimax')).toBe('MiniMax')
    expect(platformBadgeClass('minimax')).toContain('rose')
  })

  it.each(['payg', 'coding'] as const)('%s 模式提供国际、国内和历史兼容域名的三种协议', (mode) => {
    for (const protocol of ['chat_completions', 'anthropic', 'responses']) {
      const hosts = CN_BASE_URL_PRESETS.minimax
        .filter(preset => preset.mode === mode && preset.protocol === protocol)
        .map(preset => new URL(preset.url).hostname)
      expect(hosts).toEqual(['api.minimax.io', 'api.minimax.cn', 'api.minimaxi.com'])
    }
  })

  it('仅 Coding Plan 提供原生用量窗口', () => {
    expect(cnQuotaCellVisible('minimax', 'coding')).toBe(true)
    expect(cnQuotaCellVisible('minimax', 'payg')).toBe(false)
    expect(cnBalanceCellVisible('minimax', 'payg')).toBe(false)
  })
})
