import { ref } from 'vue'
import { describe, expect, it } from 'vitest'
import type { Account, ClaudeModel } from '@/types'
import type { AccountTestType } from '@/types/accountTest'
import { useAccountTestOptions } from '../useAccountTestOptions'

// 两个管理入口共用此矩阵，原生 Antigravity 图片测试不能被前端禁用。
describe('Antigravity 图片测试入口', () => {
  it.each(['oauth', 'apikey', 'upstream'])('%s 账号允许显式图片测试', (type) => {
    const options = useAccountTestOptions(ref({ platform: 'antigravity', type } as Account), ref<ClaudeModel[]>([]), ref('gemini-3.1-flash-image'), ref<AccountTestType>('image'), (key) => key)
    expect(options.testTypeOptions.value.find((item) => item.value === 'image')).toMatchObject({ disabled: false })
  })
  it('其它不支持图片的平台保持禁用', () => {
    const options = useAccountTestOptions(ref({ platform: 'anthropic', type: 'oauth' } as Account), ref<ClaudeModel[]>([]), ref('claude-sonnet-4-6'), ref<AccountTestType>('text'), (key) => key)
    expect(options.testTypeOptions.value.find((item) => item.value === 'image')).toMatchObject({ disabled: true })
  })
})
