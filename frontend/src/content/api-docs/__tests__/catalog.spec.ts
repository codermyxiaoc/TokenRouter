import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'
import { apiDocCategories, apiDocEndpoints, apiDocPlatforms, apiDocVideoProtocols, filterApiDocEndpoints } from '..'
import { apiDocBaseUrl, apiDocCurl, apiDocEnvironment, apiDocMarkdown } from '../format'

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

  it('Vidu 文档覆盖已注册的四个创建操作和查询，示例图片数量匹配操作', () => {
    // 从真实路由对照文档，防止供应商适配完成后遗漏端点或宣传未提供的删除接口。
    const videoRoutes = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), '../../../../../backend/internal/server/routes/video.go'), 'utf8')
    const registered = [...videoRoutes.matchAll(/\{http\.Method(Post|Get), "(\/ent\/v2[^"\n]*)"\}/g)]
      .map(match => `${match[1]!.toUpperCase()} ${match[2]!.replace(':task_id', '{task_id}')}`).sort()
    const documented = apiDocEndpoints.filter(endpoint => endpoint.videoProtocol === 'vidu')
    expect(documented.map(endpoint => `${endpoint.method} ${endpoint.path}`).sort()).toEqual(registered)
    expect(documented).toHaveLength(5)
    expect(filterApiDocEndpoints('Vidu', 'video')).toHaveLength(5)
    for (const [operation, count] of [['img2video', 1], ['reference2video', 1], ['text2video', 0], ['start-end2video', 2]] as const) {
      const endpoint = documented.find(item => item.id === `vidu-${operation}`)!
      const request = JSON.parse(endpoint.requestExample!)
      expect(request.images?.length ?? 0).toBe(count)
      expect(request.duration).toBe(5)
      expect(request.resolution).toBe('540p')
      expect(apiDocCurl(endpoint)).toContain('Authorization: Bearer $TOKENROUTER_API_KEY')
    }
  })

  it('可美 Seedance 2.5 按文生、首尾帧和普通参考图分别给出可复制参数', () => {
    // 这是供应商参数合同，不把“有图”一律当首帧，也不把首帧的固定比例示例复制给用户。
    const guide = (id: string) => apiDocEndpoints.find(endpoint => endpoint.id === id)!
    const text = guide('kemei-seedance25-text')
    const frames = guide('kemei-seedance25-frames')
    const reference = guide('kemei-seedance25-reference')
    for (const endpoint of [text, frames, reference]) {
      expect(endpoint.path).toBe('/v1/video/generations')
      expect(endpoint.platforms).toEqual(['video'])
      expect(endpoint.videoProtocol).toBe('seedance')
      const body = JSON.parse(endpoint.requestExample!)
      expect(body.model).toBe('doubao-seedance-2-5-260628')
      expect(body.resolution).toBe('720p')
      expect(body.duration).toBe(5)
      expect(endpoint.parameters.find(parameter => parameter.name === 'resolution')!.required).toBe('conditional')
      expect(endpoint.parameters.find(parameter => parameter.name === 'duration')!.required).toBe('conditional')
      expect(body).not.toHaveProperty('content')
      expect(apiDocMarkdown(endpoint, 'https://gateway.example.com')).toContain('创建结果未知时先查原任务')
      expect(apiDocCurl(endpoint)).toContain('Authorization: Bearer $TOKENROUTER_API_KEY')
    }
    expect(JSON.parse(text.requestExample!)).not.toHaveProperty('images')
    expect(JSON.parse(text.requestExample!).ratio).toBe('16:9')
    expect(JSON.parse(frames.requestExample!)).toMatchObject({ ratio: 'adaptive', images: [{ role: 'first_frame' }, { role: 'last_frame' }] })
    expect(JSON.parse(reference.requestExample!)).toMatchObject({ ratio: '16:9', images: [{ role: 'reference_image' }], omni_reference_task_type: 'auto' })
    expect(filterApiDocEndpoints('doubao-seedance-2-5-260628', 'video').map(endpoint => endpoint.id)).toEqual([text.id, frames.id, reference.id])
    expect(filterApiDocEndpoints('first-frame', 'video').some(endpoint => endpoint.id === frames.id)).toBe(true)
  })

  it('同一 videos 路径下秘塔 H3 与 Yezonghui 示例保持不同的 size 和参考图合同', () => {
    const h3 = apiDocEndpoints.find(endpoint => endpoint.id === 'video-metaso-h3')!
    const yezonghui = apiDocEndpoints.find(endpoint => endpoint.id === 'video-yezonghui-create')!
    const h3Body = JSON.parse(h3.requestExample!)
    const yBody = JSON.parse(yezonghui.requestExample!)
    expect(h3.path).toBe('/v1/videos')
    expect(yezonghui.path).toBe(h3.path)
    expect(h3Body.size).toBe('1280x720')
    expect(h3Body.input_reference.image_url).toMatch(/^https:/)
    expect(h3Body).not.toHaveProperty('image_urls')
    expect(yBody).toMatchObject({ seconds: '5', resolution: '720p', size: '16:9' })
    expect(yBody.image_urls).toHaveLength(1)
    expect(yBody).not.toHaveProperty('input_reference')
    expect(yezonghui.notes!.join(' ')).toContain('至少 60 秒')
    expect(yezonghui.notes!.join(' ')).toContain('没有为该方案开放取消')
    expect(h3.notes!.join(' ')).toContain('1366x768')
  })

  it('现代 Kling 与 MiniMax 示例包含对应协议必需的输入结构，Vidu 不混淆方案默认值', () => {
    for (const [id, kind] of [['kling-image', 'first_frame'], ['kling-omni', 'refer_image']] as const) {
      const endpoint = apiDocEndpoints.find(item => item.id === id)!
      const body = JSON.parse(endpoint.requestExample!)
      expect(body.settings).toMatchObject({ duration: 5, resolution: '720p', aspect_ratio: '16:9' })
      expect(body.contents[1]).toMatchObject({ type: kind })
      expect(body).not.toHaveProperty('prompt')
      expect(body).not.toHaveProperty('images')
      expect(apiDocCurl(endpoint)).toContain(endpoint.path.replace('{model}', body.model))
    }
    const minimax = apiDocEndpoints.find(item => item.id === 'minimax-video-create')!
    expect(JSON.parse(minimax.requestExample!)).toMatchObject({ ratio: '16:9', resolution: '768p', content: [{ type: 'text' }] })
    const vidu = apiDocEndpoints.find(item => item.id === 'vidu-img2video')!
    expect(vidu.notes!.join(' ')).toContain('官方与旧透传不继承可美默认值或 Q4 限制')
  })

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
    expect(filterApiDocEndpoints('火山方舟', 'video').map(item => item.id)).toEqual(['kemei-seedance25-text', 'kemei-seedance25-frames', 'kemei-seedance25-reference', 'ark-create', 'ark-query', 'ark-delete'])
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
