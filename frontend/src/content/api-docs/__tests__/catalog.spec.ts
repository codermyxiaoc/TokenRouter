import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'
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

  it('MiniMax H3 示例保留原生首帧格式、分辨率大小写和创建查询任务 ID', () => {
    // 参数模板与已确认的 H3 响应保持一致，不把供应商档位转成其他协议的配置。
    const create = apiDocEndpoints.find(endpoint => endpoint.id === 'minimax-video-create')!
    const query = apiDocEndpoints.find(endpoint => endpoint.id === 'minimax-video-query')!
    const request = JSON.parse(create.requestExample!)
    expect(request).toMatchObject({ model: 'MiniMax-H3', resolution: '2K', duration: 5, aigc_watermark: true })
    expect(request.content).toEqual([
      { type: 'text', text: expect.any(String) },
      { type: 'image_url', image_url: { url: 'https://media.example.com/first-frame.png' }, role: 'first_frame' },
    ])
    expect(request).not.toHaveProperty('prompt')
    expect(request).not.toHaveProperty('images')
    const taskID = JSON.parse(create.responseExample).TaskId
    expect(JSON.parse(query.responseExample).task).toMatchObject({ id: taskID, status: 'succeeded', usage: { output_seconds: 5 } })
    expect(query.requestPath).toBe(`/v2/query/video_generation/${taskID}`)
    expect(create.notes!.join(' ')).toContain('上游可访问的真实图片地址')
    expect(create.notes!.join(' ')).toContain('H3-open 若返回 disallowed scheme data')
    expect(create.notes!.join(' ')).toContain('创建 HTTP 200 只表示受理')
    expect(create.parameters.find(parameter => parameter.name === 'resolution')!.description).toContain('保留大小写')
  })

  it('示例根地址去除重复版本后缀、凭据和查询参数，并转义 shell 引号', () => {
    expect(apiDocBaseUrl('https://gateway.example.com/proxy/v1/', 'https://site.example.com')).toBe('https://gateway.example.com/proxy')
    expect(apiDocBaseUrl('https://gateway.example.com/v1?key=secret#fragment', 'https://site.example.com')).toBe('https://gateway.example.com')
    expect(apiDocBaseUrl('https://user:secret@gateway.example.com', 'https://site.example.com')).toBe('https://site.example.com')
    expect(apiDocBaseUrl('javascript:alert(1)', 'https://site.example.com')).toBe('https://site.example.com')
    expect(apiDocEnvironment("https://gateway.example.com/a'b")).toContain("a'\"'\"'b")
  })

  it('Gemini 缓存管理文档与后端实际注册的方法和路径一致', () => {
    // 跨边界核对真实路由，防止只新增文案却公开不存在的端点或别名。
    const gatewayRoutes = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), '../../../../../backend/internal/server/routes/gateway.go'), 'utf8')
    const routes = [...gatewayRoutes.matchAll(/gemini\.(POST|GET|PATCH|DELETE)\("(\/cachedContents[^"\n]*)"/g)]
      .map(match => `${match[1]} /v1beta${match[2]!.replace(':cacheID', '{cacheID}')}`).sort()
    const documented = apiDocEndpoints.filter(endpoint => endpoint.id.startsWith('gemini-cache-') && endpoint.id !== 'gemini-cache-generate')
    expect(documented.map(endpoint => `${endpoint.method} ${endpoint.path}`).sort()).toEqual(routes)
    expect(routes).toHaveLength(5)
    for (const endpoint of documented) {
      expect(endpoint.platforms).toEqual(['gemini'])
      expect(endpoint.auth).toBe('google')
      expect(endpoint.aliases ?? []).toEqual([])
      const markdown = apiDocMarkdown(endpoint, 'https://gateway.example.com')
      expect(markdown).toContain('不支持复合 Key、智能路由 Key')
      expect(markdown).toContain('API Key 和分组隔离')
    }
  })

  it('缓存生成示例使用创建响应中的同一模型和资源名，不泄露上游身份', () => {
    const create = apiDocEndpoints.find(endpoint => endpoint.id === 'gemini-cache-create')!
    const generate = apiDocEndpoints.find(endpoint => endpoint.id === 'gemini-cache-generate')!
    const created = JSON.parse(create.responseExample)
    const request = JSON.parse(generate.requestExample!)
    expect(request.cachedContent).toBe(created.name)
    expect(created.name).toMatch(/^cachedContents\/[a-z0-9]{40}$/)
    expect(generate.requestPath).toBe(`/v1beta/${created.model}:generateContent`)
    expect(created).not.toHaveProperty('account_id')
    expect(created).not.toHaveProperty('upstream_name')
    expect(apiDocMarkdown(generate, 'https://gateway.example.com')).toContain('仍按实际返回的输入、缓存读取及输出用量')
    expect(filterApiDocEndpoints('cachedContents', 'gemini')).toHaveLength(5)
    expect(filterApiDocEndpoints('cachedContent', 'video')).toEqual([])
  })

  it('续期使用 PATCH 与 updateMask，说明按新增时长收费且缩短和删除不退款', () => {
    const patch = apiDocEndpoints.find(endpoint => endpoint.id === 'gemini-cache-update')!
    const remove = apiDocEndpoints.find(endpoint => endpoint.id === 'gemini-cache-delete')!
    expect(apiDocCurl(patch)).toContain('-X PATCH')
    expect(apiDocCurl(patch)).toContain('?updateMask=ttl')
    expect(JSON.parse(patch.requestExample!)).toEqual({ ttl: '7200s' })
    expect(patch.notes!.join(' ')).toContain('新增时长')
    expect(patch.notes!.join(' ')).toContain('缩短有效期不退还')
    expect(patch.parameters.find(parameter => parameter.name === 'ttl')!.description).toContain('不是在原到期时间上追加')
    expect(apiDocCurl(remove)).toContain('-X DELETE')
    expect(apiDocCurl(remove)).not.toContain('--data-raw')
    expect(remove.notes!.join(' ')).toContain('删除不退还')
  })
})
