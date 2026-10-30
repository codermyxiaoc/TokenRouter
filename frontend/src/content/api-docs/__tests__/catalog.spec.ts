import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'
import { Marked } from 'marked'
import { apiDocCategories, apiDocEndpoints, apiDocPlatforms, apiDocVideoProtocols, filterApiDocEndpoints } from '..'
import { apiDocBaseUrl, apiDocCurl, apiDocEnvironment, apiDocFence, apiDocMarkdown } from '../format'
import { buildApiDocCatalog } from '../catalog'

describe('公开网关文档契约', () => {
  it('异步图片文档与真实查询错误码一致，并在页面与导出中说明停止轮询条件', () => {
    // 错误码直接对照后端定义，防止客户端按错误的文档码无限轮询旧任务。
    const taskService = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), '../../../../../backend/internal/service/image_task.go'), 'utf8')
    const actualNotFoundCode = taskService.match(/ErrImageTaskNotFound\s*=\s*infraerrors\.New\(http\.StatusNotFound, "([^"]+)"/)?.[1]
    expect(actualNotFoundCode).toBeTruthy()
    const query = apiDocEndpoints.find(endpoint => endpoint.id === 'images-task')!
    expect(query.method).toBe('GET')
    expect(query.errors).toContainEqual(expect.objectContaining({ status: 404, code: actualNotFoundCode }))
    const result = JSON.parse(query.responseExample)
    expect(result.status).toBe('completed')
    expect(result.expires_at - result.completed_at).toBe(24 * 60 * 60)

    for (const id of ['images-generate-async', 'images-edit-async', 'images-task']) {
      const endpoint = apiDocEndpoints.find(item => item.id === id)!
      const notes = endpoint.notes!.join(' ')
      for (const rule of ['status=processing', 'status=completed', 'status=failed', 'HTTP 200', 'HTTP 404', `error.code=${actualNotFoundCode}`, 'expires_at', '停止轮询', '移出待查询队列', '轮询不会再次生成或收费']) {
        expect(notes, `${id}: ${rule}`).toContain(rule)
        expect(apiDocMarkdown(endpoint, 'https://gateway.example.com'), `${id} 导出: ${rule}`).toContain(rule)
      }
    }
  })

  it('Video 分组只公开 /v1/videos，旧入口全部列入 410 迁移文档', () => {
    // 从真实路由对照文档：旧入口只能出现在迁移页，不能再作为可调用的 Video 端点宣传。
    const videoRoutes = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), '../../../../../backend/internal/server/routes/video.go'), 'utf8')
    const registered = [...new Set([...videoRoutes.matchAll(/\{http\.Method\w+, "([^"]+)"\}/g)]
      .map(match => match[1]!.replace(':task_id', '{task_id}').replace('/*model_id', '/{model}')))]
    expect(registered.length).toBeGreaterThan(15)
    const retired = apiDocEndpoints.find(endpoint => endpoint.id === 'video-retired-endpoints')!
    expect(retired.responseStatus).toBe(410)
    expect(JSON.parse(retired.responseExample).code).toBe('VIDEO_ENDPOINT_RETIRED')
    const documentedRetired = [retired.path, ...(retired.aliases ?? [])]
    for (const path of registered) expect(documentedRetired, path).toContain(path)
    const videoPages = apiDocEndpoints.filter(endpoint => endpoint.category === 'video' && endpoint.platforms.includes('video') && endpoint.id !== 'video-retired-endpoints')
    expect(videoPages.length).toBeGreaterThanOrEqual(4)
    for (const endpoint of videoPages) {
      expect(endpoint.path, endpoint.id).toMatch(/^\/v1\/videos(?:\/\{video_id\}(?:\/content)?)?$/)
      expect(endpoint.errors?.some(error => error.code === 'VIDEO_ENDPOINT_RETIRED') || endpoint.notes?.some(note => note.includes('VIDEO_ENDPOINT_RETIRED')) || endpoint.method === 'GET', endpoint.id).toBe(true)
    }
    // 创建示例只使用统一请求体字段，供应商专属参数进入 extra。
    const allowed = new Set(['model', 'prompt', 'negative_prompt', 'duration', 'resolution', 'aspect_ratio', 'size', 'images', 'videos', 'generate_audio', 'watermark', 'seed', 'extra', 'seconds', 'input_reference'])
    for (const endpoint of videoPages.filter(item => item.method === 'POST')) {
      const body = JSON.parse(endpoint.requestExample!)
      for (const key of Object.keys(body)) expect(allowed.has(key), `${endpoint.id}: ${key}`).toBe(true)
      expect(apiDocCurl(endpoint)).toContain('Authorization: Bearer $TOKENROUTER_API_KEY')
    }
  })


  it('Yezonghui 示例使用档位与比例，不使用像素 size', () => {
    const yezonghui = apiDocEndpoints.find(endpoint => endpoint.id === 'video-yezonghui-create')!
    const yBody = JSON.parse(yezonghui.requestExample!)
    expect(yezonghui.path).toBe('/v1/videos')
    expect(yBody).toMatchObject({ duration: 5, resolution: '720p', aspect_ratio: '16:9' })
    expect(yBody).not.toHaveProperty('size')
    expect(yBody.images).toHaveLength(1)
    expect(yBody).not.toHaveProperty('input_reference')
    expect(yezonghui.notes!.join(' ')).toContain('至少 60 秒')
    expect(yezonghui.notes!.join(' ')).toContain('没有为该方案开放取消')
  })

  // 视频目录只保留 OpenAI Videos 与 Grok 两个二级分类，已删除的厂商指南不再出现。
  it('视频二级分类只保留 OpenAI Videos 与 Grok', () => {
    expect(apiDocVideoProtocols.map(protocol => protocol.id)).toEqual(['openai', 'grok'])
    const removed = ['kemei-seedance25-text', 'seedance-official-create', 'video-metaso-h3', 'kling-image', 'wan-create', 'minimax-video-create', 'vidu-img2video', 'ark-create']
    for (const id of removed) expect(apiDocEndpoints.some(endpoint => endpoint.id === id), id).toBe(false)
  })

  it('保持稳定且唯一的文档 ID，所有平台和 JSON 示例均有效', () => {
    expect(new Set(apiDocEndpoints.map(item => item.id)).size).toBe(apiDocEndpoints.length)
    expect(apiDocEndpoints.length).toBeGreaterThan(60)
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
    expect(filterApiDocEndpoints('Kling', 'deepseek')).toEqual([])
    expect(filterApiDocEndpoints('不存在的端点', null)).toEqual([])
  })

  it('保留原生任务查询参数、鉴权及非 JSON 示例', () => {
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

  // 每篇接口文档渲染后参数表是一张完整表格，代码块与列表不被拆散。
  it('全部接口文档的 Markdown 能正确渲染为表格与代码块', () => {
    const parser = new Marked({ gfm: true })
    for (const endpoint of apiDocEndpoints) {
      const html = parser.parse(apiDocMarkdown(endpoint, 'https://gateway.example.com')) as string
      const doc = new DOMParser().parseFromString(html, 'text/html')
      if (endpoint.parameters.length) {
        expect(doc.querySelectorAll('table'), endpoint.id).toHaveLength(1)
        expect(doc.querySelectorAll('tbody tr'), endpoint.id).toHaveLength(endpoint.parameters.length)
      }
      expect(doc.querySelector('pre code')?.textContent, endpoint.id).toBeTruthy()
      expect(doc.body.textContent, endpoint.id).not.toContain('```')
      if (endpoint.notes?.length) expect(doc.querySelectorAll('ul').length, endpoint.id).toBeGreaterThan(0)
    }
    expect(apiDocFence('a\n```\nb', 'json')).toMatch(/^````json\n/)
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

  // 服务端据内置层级判断上级是否存在、是否超过两级和上级隐藏，两边的内置分类必须一致。
  it('后端内置分类层级与前端目录一致', () => {
    const source = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), '../../../../../backend/internal/service/api_docs.go'), 'utf8')
    const block = source.match(/builtinAPIDocCategoryParents = map\[string\]string\{([\s\S]*?)\r?\n\}/)?.[1] ?? ''
    const backend = Object.fromEntries([...block.matchAll(/"([a-z0-9-]+)":\s*"([a-z0-9-]*)"/g)].map(match => [match[1], match[2]]))
    const frontend: Record<string, string> = { guides: '' }
    for (const category of apiDocCategories) frontend[category.id] = ''
    for (const protocol of apiDocVideoProtocols) frontend[`video-${protocol.id}`] = 'video'
    expect(backend).toEqual(frontend)
  })

  // 内置文档沿用默认分类时，服务端据此判断正文是否随分类隐藏，两边的内置文档归属必须一致。
  it('后端内置文档默认分类与前端目录一致', () => {
    const source = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), '../../../../../backend/internal/service/api_docs_builtin_documents.go'), 'utf8')
    const block = source.match(/builtinAPIDocDocumentCategories = map\[string\]string\{([\s\S]*?)\r?\n\}/)?.[1] ?? ''
    const backend = Object.fromEntries([...block.matchAll(/"([a-z0-9-]+)":\s*"([a-z0-9-]+)"/g)].map(match => [match[1], match[2]]))
    const catalog = buildApiDocCatalog({ categories: [], documents: [] }, true)
    const frontend = Object.fromEntries(catalog.documents.filter(doc => doc.builtin).map(doc => [doc.id, doc.defaultCategoryId]))
    expect(Object.keys(frontend).length).toBeGreaterThan(60)
    expect(backend).toEqual(frontend)
  })
})
