import { describe, expect, it } from 'vitest'
import { additionalProviderForm, applyAdditionalProviderCredentials } from '../credentialsBuilder'

describe('新增 API Key 平台凭据', () => {
  it('保留中继根地址且只覆盖明确填写的协议地址', () => {
    const credentials: Record<string, unknown> = { api_key: 'relay-key', base_url: 'https://relay.example/prefix/v1' }
    const form = additionalProviderForm()
    form.baseUrls.anthropic = ' https://relay.example/messages '
    applyAdditionalProviderCredentials(credentials, 'command_code', form)
    expect(credentials).toEqual({ api_key: 'relay-key', base_url: 'https://relay.example/prefix/v1', account_mode: 'payg', api_protocol: 'adaptive', api_base_urls: { anthropic: 'https://relay.example/messages' } })
    expect(credentials).not.toHaveProperty('protocol_rules')
  })

  it('区分目录自适应与明确清空自定义规则，并可往返编辑', () => {
    const credentials: Record<string, unknown> = { protocol_rules: [], api_protocol: 'responses' }
    const form = additionalProviderForm(credentials)
    expect(form.customRules).toBe(true)
    applyAdditionalProviderCredentials(credentials, 'command_code', form)
    expect(credentials.protocol_rules).toEqual([])
    expect(credentials.api_protocol).toBe('responses')
    form.customRules = false
    applyAdditionalProviderCredentials(credentials, 'command_code', form)
    expect(credentials).not.toHaveProperty('protocol_rules')
  })

  it('TypeSafe 与 Cline 不能继承 Command 的协议覆盖，原平台不变', () => {
    for (const [platform, protocol] of [['typesafe', 'systemone'], ['cline', 'chat_completions']]) {
      const credentials: Record<string, unknown> = { api_base_urls: { responses: 'https://unrelated.example' }, protocol_rules: [] }
      applyAdditionalProviderCredentials(credentials, platform, additionalProviderForm())
      expect(credentials).toEqual({ account_mode: 'payg', api_protocol: protocol })
    }
    const video = { api_protocol: 'video', video_endpoints: ['compat'] }
    applyAdditionalProviderCredentials(video, 'video', additionalProviderForm())
    expect(video).toEqual({ api_protocol: 'video', video_endpoints: ['compat'] })
  })
})
