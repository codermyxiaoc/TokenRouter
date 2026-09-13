import { describe, expect, it } from 'vitest'

import enAccounts from '@/i18n/locales/en/admin/accounts'
import enChannels from '@/i18n/locales/en/admin/channels'
import zhAccounts from '@/i18n/locales/zh/admin/accounts'
import zhChannels from '@/i18n/locales/zh/admin/channels'

describe('model routing copy', () => {
  it('keeps the account rule wording aligned in Chinese and English', () => {
    expect(zhAccounts.accounts.modelRestriction).toBe('账号模型规则（可选）')
    expect(zhAccounts.accounts.modelWhitelist).toBe('最终模型白名单')
    expect(zhAccounts.accounts.modelMapping).toBe('账号模型映射')
    expect(zhAccounts.accounts.supportsAllModels).toBe('账号级不限制')
    expect(zhAccounts.accounts.modelRestrictionCombinedHint).toContain('渠道规则、平台能力和上游实际支持范围仍然生效')
    expect(zhAccounts.accounts.openai.modelRestrictionDisabledByPassthrough).toContain('最终模型白名单和账号模型映射')
    expect(zhAccounts.accounts.syncUpstreamModelsNoChanges).toContain('最终模型白名单')

    expect(enAccounts.accounts.modelRestriction).toBe('Account Model Rules (Optional)')
    expect(enAccounts.accounts.modelWhitelist).toBe('Final Model Whitelist')
    expect(enAccounts.accounts.modelMapping).toBe('Account Model Mapping')
    expect(enAccounts.accounts.supportsAllModels).toBe('No account-level restriction')
    expect(enAccounts.accounts.openai.modelRestrictionDisabledByPassthrough).toContain('final model whitelist and account model mapping')
    expect(enAccounts.accounts.syncUpstreamModelsNoChanges).toContain('final model whitelist')
  })

  it('keeps the channel stages and three bases aligned', () => {
    expect(zhChannels.channels.form.modelMappingHint).toBe('客户端请求模型 -> 渠道映射 -> 账号映射 -> 最终上游模型')
    expect(zhChannels.channels.form.billingModelSourceChannelMapped).toBe('渠道映射后的模型（默认）')
    expect(zhChannels.channels.form.billingModelSourceRequested).toBe('客户端请求模型')
    expect(zhChannels.channels.form.billingModelSourceUpstream).toBe('账号最终上游模型')

    expect(enChannels.channels.form.modelMappingHint).toBe('Client request model -> channel mapping -> account mapping -> final upstream model')
    expect(enChannels.channels.form.billingModelSourceHintUpstream).toContain('filtered individually')
  })
})
