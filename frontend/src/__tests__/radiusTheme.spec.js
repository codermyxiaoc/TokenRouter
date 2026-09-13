import { describe, expect, it } from 'vitest'

import tailwindConfig from '../../tailwind.config.js'

// 固定全站圆角契约，避免新增组件重新引入过大的默认圆角。
describe('OpenRouter 圆角主题', () => {
  const radius = tailwindConfig.theme.borderRadius

  it('提供紧凑、控件、表面和弹窗四级语义令牌', () => {
    expect(radius).toMatchObject({
      compact: '4px',
      control: '6px',
      surface: '8px',
      dialog: '12px'
    })
  })

  it('将兼容工具类收敛到 4px、6px 和 8px', () => {
    expect(radius).toMatchObject({
      DEFAULT: '4px',
      sm: '4px',
      md: '6px',
      lg: '6px',
      xl: '8px',
      '2xl': '8px',
      '3xl': '8px',
      '4xl': '8px'
    })
  })

  it('保留无圆角和全圆工具类', () => {
    expect(radius.none).toBe('0px')
    expect(radius.full).toBe('9999px')
  })
})
