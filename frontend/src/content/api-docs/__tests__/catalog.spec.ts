import { describe, expect, it } from 'vitest'
import { apiDocCategories, apiDocEndpoints, apiDocPlatforms, apiDocVideoProtocols, filterApiDocEndpoints } from '..'
import { apiDocBaseUrl, apiDocCurl, apiDocEnvironment, apiDocMarkdown } from '../format'

describe('公开网关文档契约', () => {
  it('保持稳定且唯一的文档 ID，所有平台和 JSON 示例均有效', () => {
    expect(new Set(apiDocEndpoints.map(item => item.id)).size).toBe(apiDocEndpoints.length)
    expect(apiDocEndpoints.length).toBeGreaterThan(70)
    for (const endpoint of apiDocEndpoints) {
      expect(endpoint.id).toMatch(/^[a-z][a-z0-9-]+$/)
      expect(apiDocCategories.some(category => category.id === endpoint.category)).toBe(true)
      if (endpoint.category === 'video') expect(apiDocVideoProtocols.some(protocol => protocol.id === endpoint.videoProtocol), endpoint.id).toBe(true)
      expect(endpoint.platforms.length).toBeGreaterThan(0)
      expect(endpoint.title).toBeTruthy()
      expect(endpoint.summary).toBeTruthy()
      expect(endpoint.responseExample).toBeTruthy()
      for (const platform of endpoint.platforms) expect(apiDocPlatforms.some(item => item.value === platform), `${endpoint.id}: ${platform}`).toBe(true)
      if (!endpoint.responseLanguage || endpoint.responseLanguage === 'json') expect(() => JSON.parse(endpoint.responseExample), endpoint.id).not.toThrow()
      if (endpoint.requestExample && (!endpoint.requestLanguage || endpoint.requestLanguage === 'json')) expect(() => JSON.parse(endpoint.requestExample!), endpoint.id).not.toThrow()
      expect(endpoint.path).not.toMatch(/^\/api\/v1\/(?:admin|user|auth|payment)\b/)
    }
  })

  it('按平台与完整搜索词交集筛选，同时能找到兼容别名', () => {
    expect(filterApiDocEndpoints('/backend-api/codex/responses', 'openai').some(item => item.id === 'responses')).toBe(true)
    expect(filterApiDocEndpoints('Kling POST', 'video').every(item => item.method === 'POST' && item.title.includes('Kling'))).toBe(true)
    expect(filterApiDocEndpoints('Kling POST', 'video').length).toBeGreaterThan(0)
    expect(filterApiDocEndpoints('Kling', 'deepseek')).toEqual([])
    expect(filterApiDocEndpoints('不存在的端点', null)).toEqual([])
    expect(filterApiDocEndpoints('火山方舟', 'video').map(item => item.id)).toEqual(['ark-create', 'ark-query', 'ark-delete'])
  })

  it('保留原生任务查询参数、鉴权及非 JSON 示例', () => {
    const kling = apiDocEndpoints.find(item => item.id === 'kling-query')!
    expect(apiDocCurl(kling)).toContain('/tasks?task_ids=kling_task_example')
    expect(apiDocCurl(kling)).toContain('Authorization: Bearer $TOKENROUTER_API_KEY')
    const gemini = apiDocEndpoints.find(item => item.id === 'gemini-generate-content')!
    expect(apiDocCurl(gemini)).toContain('x-goog-api-key: $TOKENROUTER_API_KEY')
    const live = apiDocEndpoints.find(item => item.id === 'openai-live')!
    expect(apiDocCurl(live)).toContain('-F')
    expect(apiDocCurl(live)).not.toContain('--data-raw')
    const realtime = apiDocEndpoints.find(item => item.id === 'grok-realtime')!
    expect(apiDocCurl(realtime)).toContain('?model=grok-voice-latest')
  })

  it('导出的文档与页面保留相同参数、响应及说明', () => {
    const endpoint = apiDocEndpoints.find(item => item.id === 'video-create')!
    const markdown = apiDocMarkdown(endpoint, 'https://gateway.example.com')
    expect(markdown).toContain(endpoint.responseExample)
    expect(markdown).toContain(endpoint.requestExample)
    expect(markdown).toContain('## 请求参数')
    expect(markdown).toContain('## 常见错误')
    expect(markdown).toContain("export BASE_URL='https://gateway.example.com'")
    expect(markdown).toContain('sk-your-api-key')
  })

  it('示例根地址去除重复版本后缀、凭据和查询参数，并转义 shell 引号', () => {
    expect(apiDocBaseUrl('https://gateway.example.com/proxy/v1/', 'https://site.example.com')).toBe('https://gateway.example.com/proxy')
    expect(apiDocBaseUrl('https://gateway.example.com/v1?key=secret#fragment', 'https://site.example.com')).toBe('https://gateway.example.com')
    expect(apiDocBaseUrl('https://user:secret@gateway.example.com', 'https://site.example.com')).toBe('https://site.example.com')
    expect(apiDocBaseUrl('javascript:alert(1)', 'https://site.example.com')).toBe('https://site.example.com')
    expect(apiDocEnvironment("https://gateway.example.com/a'b")).toContain("a'\"'\"'b")
  })
})
