import type { ApiDocEndpoint, ApiDocError, ApiDocParameter } from './types'

// 缓存管理只列出已注册的固定分组入口；示例名称必须替换为本站创建响应中的 name。
const cacheID = 'abcdefghijklmnopqrstuv234567abcdefghijkl'
const cacheName = `cachedContents/${cacheID}`
const cacheModel = 'gemini-2.5-flash'
const cacheView = {
  name: cacheName,
  model: `models/${cacheModel}`,
  displayName: '产品手册',
  usageMetadata: { totalTokenCount: 32768 },
  createTime: '2026-10-13T08:00:00Z',
  updateTime: '2026-10-13T08:00:00Z',
  expireTime: '2026-10-13T09:00:00Z',
}
const ownerNote = '仅支持绑定固定 Gemini 分组的站内 API Key；不支持复合 Key、智能路由 Key、Antigravity 专用入口或其他平台分组。团队 Key 仍须通过团队身份、成员资格和分组权限检查，产生费用的操作还受团队及成员额度限制。'
const isolationNote = '缓存按创建时的 API Key 和分组隔离，即使同一用户也不能换另一把 Key 使用。查询、续期、删除和生成引用必须使用原 Key、原分组与本站返回的 name；不要填写上游账号的缓存 ID。'
const managementNote = '列表、查询和删除不产生新费用，余额或消费额度耗尽后仍可操作已付费缓存；Key 停用、IP 限制、团队身份、分组权限及资源归属检查仍然生效。'
const accountNote = '账号必须支持显式缓存：官方 Gemini API Key、启用显式缓存的第三方 Gemini API Key，或 Vertex Service Account JSON 账号。OAuth 账号和 Vertex API Key 账号不支持。'
const cacheIDParameter: ApiDocParameter = { name: 'cacheID', type: 'string', required: true, description: 'Path · 创建响应 name 中 cachedContents/ 后的 ID，不能带完整上游 URL。' }
const cacheErrors: ApiDocError[] = [
  { status: 400, code: 'INVALID_ARGUMENT', description: 'Key 类型、分组、请求格式、TTL 或模型不支持；创建要求有可用的输入及缓存存储报价，延长要求有缓存存储报价。' },
  { status: 403, code: 'PERMISSION_DENIED', description: '缓存不存在、已过期或不属于当前 Key / 分组时统一返回 CachedContent not found (or permission denied)；创建、续期或生成也可能因协议或额度权限被拒绝。' },
  { status: 503, code: 'UNAVAILABLE', description: '没有支持显式缓存的可用账号，或持有缓存的账号暂不可用；不会将同一缓存切换到另一账号。' },
]

export const geminiCacheEndpoints: ApiDocEndpoint[] = [
  {
    id: 'gemini-cache-create', category: 'tools', title: 'Gemini 创建显式缓存',
    summary: '先缓存长文本或多模态上下文，再在 Gemini 原生生成请求中通过 cachedContent 引用。',
    platforms: ['gemini'], method: 'POST', path: '/v1beta/cachedContents', auth: 'google', contentType: 'application/json',
    parameters: [
      { name: 'model', type: 'string', required: true, description: 'Body · 缓存使用的 Gemini 模型，例如 models/gemini-2.5-flash；后续生成必须使用同一模型。' },
      { name: 'contents', type: 'array', required: true, description: 'Body · 待缓存内容，元素使用 Gemini role / parts 结构。文本、文件和最小 Token 数要求由目标模型与上游决定。' },
      { name: 'systemInstruction / tools / toolConfig', type: 'object / array / object', required: false, description: 'Body · 需要一同缓存的系统指令、工具声明和配置；按上游模型支持范围提供。' },
      { name: 'displayName', type: 'string', required: false, description: 'Body · 自定义展示名称，最多 128 个字符。' },
      { name: 'ttl', type: 'string', required: false, description: 'Body · 有效期，例如 3600s，必须大于 0；与 expireTime 二选一。均省略时通常为 3600 秒，且不超过站点和账号允许的上限。' },
      { name: 'expireTime', type: 'string', required: false, description: 'Body · RFC 3339 格式的未来到期时间，与 ttl 二选一；最长有效期受站点及账号限制。' },
    ],
    requestExample: JSON.stringify({ model: `models/${cacheModel}`, displayName: '产品手册', contents: [{ role: 'user', parts: [{ text: '请替换为需要重复使用的完整长文档，并满足目标模型的最小缓存 Token 数要求。' }] }], ttl: '3600s' }, null, 2),
    responseExample: JSON.stringify(cacheView, null, 2),
    responseDescription: '示例仅展示结构与占位用量。保存返回的 name 和 expireTime；name 是本站缓存资源名，不是上游账号资源名。',
    notes: [ownerNote, accountNote, isolationNote,
      '创建会收取实际缓存 Token 的输入费用，并按上游确认且不超过请求上限的实际有效期收取存储费用。存储用量按 Token × 小时计算，单价、分组和订阅倍率沿用本站计费设置；缺少输入或存储报价时，会在请求上游前拒绝创建。上游返回已过期的缓存会被清理并拒绝交付。',
      '缺少足够长的内容、上游不支持缓存、TTL 超限均可能失败。示例中的短占位文本应替换为实际长内容。创建不是自动幂等操作，请勿在结果不明确时盲目重复提交。',
      '创建和延长需要分组允许 Gemini GenerateContent 协议，并通过站内余额、订阅、Key 配额及并发检查。'],
    errors: [...cacheErrors, { status: 429, code: 'RESOURCE_EXHAUSTED', description: '并发、速率或上游额度达到限制，请降低并发后重试。' }],
  },
  {
    id: 'gemini-cache-list', category: 'tools', title: 'Gemini 显式缓存列表',
    summary: '分页查询当前 Key 在当前分组下尚未过期的本站缓存。',
    platforms: ['gemini'], method: 'GET', path: '/v1beta/cachedContents', requestPath: '/v1beta/cachedContents?pageSize=50', auth: 'google',
    parameters: [
      { name: 'pageSize', type: 'integer', required: false, description: 'Query · 默认 50；0 使用默认值，最多 1000，负数或非整数会被拒绝。' },
      { name: 'pageToken', type: 'string', required: false, description: 'Query · 使用上一页返回的 nextPageToken；不要自行构造。未返回 nextPageToken 表示没有下一页。' },
    ],
    responseExample: JSON.stringify({ cachedContents: [cacheView], nextPageToken: 'MTIz' }, null, 2),
    responseDescription: '按创建顺序倒序分页。没有记录时可返回 {}，客户端应将缺省 cachedContents 视为空数组；nextPageToken 仅在有下一页时出现。',
    notes: [ownerNote, isolationNote, managementNote, '列表只读取本站归属记录，不透传上游账号的全部缓存，因此不会展示该账号上其他用户的缓存。'], errors: cacheErrors,
  },
  {
    id: 'gemini-cache-get', category: 'tools', title: 'Gemini 查询显式缓存',
    summary: '查看指定缓存的模型、用量与到期时间，不返回缓存正文。',
    platforms: ['gemini'], method: 'GET', path: '/v1beta/cachedContents/{cacheID}', requestPath: `/v1beta/${cacheName}`, auth: 'google',
    parameters: [cacheIDParameter], responseExample: JSON.stringify(cacheView, null, 2),
    notes: [ownerNote, isolationNote, managementNote, '查询返回本站保存的缓存元数据。上游提前删除资源或账号暂不可用时，生成仍可能失败，请同时处理生成接口的错误。'], errors: cacheErrors,
  },
  {
    id: 'gemini-cache-update', category: 'tools', title: 'Gemini 修改缓存有效期',
    summary: '调整缓存到期时间；内容、模型与展示名称不能修改。',
    platforms: ['gemini'], method: 'PATCH', path: '/v1beta/cachedContents/{cacheID}', requestPath: `/v1beta/${cacheName}?updateMask=ttl`, auth: 'google', contentType: 'application/json',
    parameters: [cacheIDParameter,
      { name: 'updateMask', type: 'string', required: false, description: 'Query · 推荐使用 ttl；也支持 expireTime。不能更新模型、内容或 displayName。' },
      { name: 'ttl', type: 'string', required: 'conditional', description: 'Body · 从本次请求开始计算的有效期，例如 7200s；与 expireTime 二选一且必须提供其中一个。不是在原到期时间上追加秒数。' },
      { name: 'expireTime', type: 'string', required: 'conditional', description: 'Body · RFC 3339 格式的未来到期时间，与 ttl 二选一；不得超过站点及账号允许的有效期上限。' },
    ],
    requestExample: JSON.stringify({ ttl: '7200s' }, null, 2),
    responseExample: JSON.stringify({ ...cacheView, updateTime: '2026-10-13T08:10:00Z', expireTime: '2026-10-13T10:10:00Z' }, null, 2),
    notes: [ownerNote, isolationNote, '延长时只对超出原到期时间的新增时长收取存储费用，不重复收取创建输入费用；目标到期时间在调用前确定，网络延迟不会增加收费时长。缩短有效期不退还此前存储费用。', '同一缓存的有效期修改与删除串行处理。遇到 409 时等待正在处理的操作结束，再查询当前到期时间，避免连续重复续期。'],
    errors: [...cacheErrors, { status: 409, code: 'ABORTED', description: '该缓存正在续期或恢复处理，暂时不能再次修改。' }],
  },
  {
    id: 'gemini-cache-delete', category: 'tools', title: 'Gemini 删除显式缓存',
    summary: '删除当前 Key 拥有的指定缓存，成功后不能再引用。',
    platforms: ['gemini'], method: 'DELETE', path: '/v1beta/cachedContents/{cacheID}', requestPath: `/v1beta/${cacheName}`, auth: 'google',
    parameters: [cacheIDParameter], responseExample: '{}',
    notes: [ownerNote, isolationNote, managementNote, '删除不退还已经收取的输入费用和存储费用。缓存过期或删除后，继续查询或引用统一返回 403，而不是空成功响应。'],
    errors: [...cacheErrors, { status: 409, code: 'ABORTED', description: '该缓存正在续期、删除或恢复处理，请等待操作结束后再试。' }],
  },
  {
    id: 'gemini-cache-generate', category: 'text', title: 'Gemini 引用显式缓存生成',
    summary: '使用创建缓存时返回的 name，与本轮问题一起发起 Gemini 原生生成。',
    platforms: ['gemini'], method: 'POST', path: '/v1beta/models/{model}:generateContent', requestPath: `/v1beta/models/${cacheModel}:generateContent`, auth: 'google', contentType: 'application/json',
    parameters: [
      { name: 'model', type: 'string', required: true, description: 'Path · 与创建缓存时相同的模型；使用模型 ID，不带 models/ 前缀。' },
      { name: 'cachedContent', type: 'string', required: true, description: 'Body · 本站创建缓存响应中的完整 name，例如 cachedContents/abc123。' },
      { name: 'contents', type: 'array', required: true, description: 'Body · 本轮新增的问题，使用 Gemini role / parts 结构，不必重复发送已缓存的长文档。' },
      { name: 'generationConfig', type: 'object', required: false, description: 'Body · 模型支持的生成配置，例如 maxOutputTokens。' },
    ],
    requestExample: JSON.stringify({ cachedContent: cacheName, contents: [{ role: 'user', parts: [{ text: '根据缓存的产品手册，总结安装步骤。' }] }], generationConfig: { maxOutputTokens: 512 } }, null, 2),
    responseExample: JSON.stringify({ candidates: [{ content: { role: 'model', parts: [{ text: '安装步骤如下……' }] }, finishReason: 'STOP' }], usageMetadata: { promptTokenCount: 32788, cachedContentTokenCount: 32768, candidatesTokenCount: 100, totalTokenCount: 32888 } }, null, 2),
    notes: [ownerNote, isolationNote, '同样可在 /v1beta/models/{model}:streamGenerateContent 的请求体中提供 cachedContent；流式响应格式与普通 Gemini 原生生成一致。', '引用缓存会固定使用持有该缓存的账号，不会换到另一个上游账号；模型必须与缓存模型一致，缓存不可用时应重新创建。', '生成调用仍按实际返回的输入、缓存读取及输出用量和本站价格计费，不因缓存已付存储费而免费；创建与续期的输入/存储费属于独立费用。'],
    errors: cacheErrors,
  },
]
