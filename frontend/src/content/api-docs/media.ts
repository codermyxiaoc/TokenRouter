import type { ApiDocEndpoint, ApiDocParameter } from './types'

// 图片、批量任务和音频文档以已注册网关路由为边界；示例不含部署密钥。
const json = (value: unknown) => JSON.stringify(value, null, 2)
const parameter = (name: string, type: string, required: boolean | 'conditional', description: string): ApiDocParameter => ({ name, type, required, description })
const imageParameters: ApiDocParameter[] = [
  parameter('model', 'string', true, 'Body：当前分组可用的图片模型 ID；请从模型目录确认。'),
  parameter('prompt', 'string', true, 'Body：非空图片描述；编辑时描述期望的修改。'),
  parameter('n', 'integer', false, 'Body：生成张数，默认 1，必须为正数；上限由模型和账号能力决定。'),
  parameter('size', 'string', false, 'Body：图片尺寸，例如 1024x1024；可用尺寸、auto 和分辨率范围由上游决定。'),
  parameter('quality', 'string', false, 'Body：质量档位，例如 auto；并非所有模型支持相同枚举。'),
  parameter('response_format', 'string', false, 'Body：url 或 b64_json；具体结果格式取决于模型和账号。'),
  parameter('background / output_format', 'string', false, 'Body：支持这些选项的模型可指定透明背景、PNG/JPEG/WebP 等输出格式。'),
  parameter('output_compression / partial_images', 'integer', false, 'Body：压缩和流式预览选项；仅适用于支持相应原生能力的模型。'),
  parameter('stream', 'boolean', false, 'Body：同步入口可按模型能力请求 SSE；异步入口必须为 false 或省略。')
]
const imageResponse = json({ created: 1790900000, data: [{ url: 'https://images.example.com/output.png' }] })
const imageNotes = [
  '仅 OpenAI 和 Grok 分组支持这些 Images URL；Gemini 图片使用原生 GenerateContent 或批量图片接口。',
  '需要分组允许图片生成且账号支持相应图片能力；模型、尺寸和高级字段以当前配置及上游为准。',
  '响应示例展示 URL 结果；b64_json 模式返回 data[].b64_json，usage 仅在上游提供或适配器可恢复时出现。',
  '一次图片生成可能耗时较长。同步超时不代表上游未生成；避免盲目重复提交，可使用异步入口。'
]
const imageErrors = [
  { status: 400, code: 'invalid_request_error', description: '模型、图片、n、stream 类型或其他参数不合法。' },
  { status: 403, code: 'permission_error', description: '分组未开启图片生成。' },
  { status: 404, code: 'not_found_error', description: '当前分组平台不支持 Images 接口。' },
  { status: 503, code: 'api_error', description: '无满足模型与图片能力的可用账号。' }
]
const editParameters: ApiDocParameter[] = [
  ...imageParameters,
  parameter('images[].image_url', 'string', 'conditional', 'JSON Body：OpenAI 编辑格式，传入图片 URL 或 data URL。不能使用 images[].file_id。'),
  parameter('mask.image_url', 'string', false, 'JSON Body：可选遮罩 URL；不支持 mask.file_id。'),
  parameter('image / image[] / mask', 'file', 'conditional', 'Multipart Form：上传待编辑图片与可选遮罩。JSON 与 multipart 二选一；Grok 原生编辑字段按实际上游支持保留。')
]
const imageTaskResponse = { id: 'imgtask_example', task_id: 'imgtask_example', object: 'image.generation.task', status: 'processing', created_at: 1790900000, expires_at: 1790986400, poll_url: '/v1/images/tasks/imgtask_example' }
const asyncNotes = [
  '需要管理员配置并启用图片对象存储；未启用时返回 404。提交成功返回 HTTP 202、Location 和 Retry-After: 3。',
  '使用创建任务的同一个站内 API Key 查询。异步沿用同步接口的模型、路由和计费规则；不接受 stream=true。',
  '完成和失败都通过任务状态表示。查询失败任务仍可能返回 HTTP 200，请检查 status、http_status 和 error。',
  '创建和完成时分别保留 24 小时查询记录；图片链接有效期独立于任务期限。已持久化结果可跨重启查询，执行中的上游调用不能在重启后安全续跑。',
  '执行中断或结果转存失败时可能已产生上游用量，不应仅根据 failed 自动重试。轮询不会再次生成或收费。'
]
const batchID = parameter('id', 'string', true, 'Path：创建批量任务返回的 id；将示例 batch_example 替换为真实值，并使用创建任务的同一个站内 API Key。')
const batchResponse = { id: 'batch_example', object: 'image.batch', task_name: '风景图片', status: 'queued', model: 'gemini-2.5-flash-image', provider: 'gemini_api', item_count: 1, success_count: 0, fail_count: 0, estimated_cost: 0.02, hold_amount: 0.024, actual_cost: null, created_at: 1790900000, submitted_at: 1790900000, settled_at: null }
const batchErrors = [
  { status: 404, code: 'BATCH_IMAGE_DISABLED / BATCH_IMAGE_NOT_FOUND', description: '批量功能未启用，或任务不存在、不属于当前用户与 API Key。' },
  { status: 403, code: 'BATCH_IMAGE_GROUP_DISABLED', description: '当前分组未允许批量图片。' },
  { status: 400, code: 'BATCH_IMAGE_INVALID_ITEMS', description: '请求条目、尺寸或参数不合法。' },
  { status: 402, code: 'BATCH_IMAGE_INSUFFICIENT_BALANCE', description: '余额不足以冻结预留金额。' },
  { status: 502, code: 'BATCH_IMAGE_NO_ACCOUNT_AVAILABLE', description: '没有兼容的 Gemini API Key 或 Vertex 账号。' }
]
const batchNotes = [
  '这是本站批量图片任务协议，不是 OpenAI /v1/batches 协议，也不同于 /images/generations/async。',
  '需要全局启用、分组允许批量图片、兼容账号以及完整定价。先冻结预留金额，再按成功结果结算并释放差额。',
  '示例金额仅展示字段结构，不代表实际价格。请以 estimated_cost、hold_amount、actual_cost 与站内价卡为准。',
  '所有批量 URL 仅注册 /v1 前缀，不提供无前缀别名。查询、取消、下载和删除必须使用原 API Key。'
]
const voiceID = parameter('voice_id', 'string', true, 'Path：上游返回的自定义声音 ID；示例 abc123xy 需要替换为真实值。')
const voiceErrors = [
  { status: 404, code: 'not_found_error', description: '非 Grok 分组，或声音资源在当前上游账号下不存在。' },
  { status: '400 / 403', code: '上游错误', description: '参数不支持或上游账号未获相应语音/自定义声音能力。' },
  { status: 503, code: 'api_error', description: '没有可用的 Grok 账号。' }
]
const voiceNotes = [
  '仅 Grok 分组支持。请求方法、Content-Type、请求体和上游响应原样中继，具体可用字段由上游账号能力决定。',
  '自定义声音资源属于上游账号；当前接口不为 voice_id 固定调度账号。需要稳定访问同一声音时，应使用绑定同一上游账号的分组。',
  '未注册 /v1/audio/speech、/v1/audio/transcriptions、/v1/audio/translations、/v1/tts/voices 或流式 TTS WebSocket 别名。'
]

export const mediaEndpoints: ApiDocEndpoint[] = [
  {
    id: 'gemini-images', category: 'images', title: 'Gemini 原生图片生成与编辑', summary: '通过 GenerateContent 的图片模态生成图片，或携带 inlineData 参考图进行编辑。',
    platforms: ['gemini'], method: 'POST', path: '/v1beta/models/{model}:generateContent', auth: 'google', contentType: 'application/json', requestPath: '/v1beta/models/gemini-2.5-flash-image:generateContent',
    parameters: [
      parameter('model', 'string', true, 'Path：模型目录中支持图片输出的 Gemini 模型 ID；仅能识图的文本模型不一定支持生图。'),
      parameter('contents', 'array<object>', true, 'Body：Gemini 原生消息列表，通常使用 role=user 与 parts。'),
      parameter('contents[].parts[].text', 'string', true, 'Body：图片生成或编辑提示词。'),
      parameter('contents[].parts[].inlineData', 'object', false, 'Body：参考图片，包含 mimeType（如 image/png）与 data（纯 Base64 字符串，不加 data: 前缀）；每张参考图作为单独 part。'),
      parameter('generationConfig.responseModalities', 'array<string>', true, 'Body：图片请求使用 ["TEXT", "IMAGE"]，请求文本与图片输出。'),
      parameter('generationConfig.imageConfig.aspectRatio', 'string', false, 'Body：图片宽高比，例如 1:1；支持范围由所选模型决定。'),
      parameter('generationConfig.imageConfig.imageSize', 'string', false, 'Body：输出分辨率档位，例如 1K、2K、4K；仅在所选模型支持时设置，必须位于 imageConfig 内，不能直接写在 generationConfig 下。')
    ],
    requestExample: json({ contents: [{ role: 'user', parts: [{ text: '生成一幅水彩山景，画面中央是一间木屋' }] }], generationConfig: { responseModalities: ['TEXT', 'IMAGE'], imageConfig: { aspectRatio: '1:1' } } }),
    responseExample: json({ candidates: [{ content: { role: 'model', parts: [{ text: '已生成图片。' }, { inlineData: { mimeType: 'image/png', data: '<图片文件的 Base64 数据>' } }] }, finishReason: 'STOP' }], usageMetadata: { promptTokenCount: 20, candidatesTokenCount: 1000, totalTokenCount: 1020 } }),
    responseDescription: '图片位于 candidates[].content.parts[].inlineData；将 data 按 Base64 解码，使用 mimeType 保存图片。文本、图片数量与 usageMetadata 以实际响应为准。',
    notes: [
      '这与 Gemini 文本接口使用同一路径，区别在于模型和 responseModalities。不是 /v1/images/generations，也不会返回 OpenAI 的 data[].url。',
      '编辑图片时在 contents[].parts 中追加 {"inlineData":{"mimeType":"image/png","data":"<参考图片的纯 Base64>"}}；用实际图片内容替换占位符。示例响应中的 Base64 占位符不能直接解码为图片。',
      '需要分组启用 Gemini GenerateContent 并有可用图片账号/模型。API Key、OAuth 和 Vertex 的真实图片能力可能不同。',
      '无需图片尺寸档位时省略 imageSize。可用档位依模型而异，不能把批量图片接口仅支持 1K 的限制套用到此原生接口。',
      '原生响应按实际图片 part 记录图片用量；HTTP 200 仍可能没有图片，请检查 candidates、promptFeedback 与 finishReason。'
    ],
    errors: [{ status: 400, code: 'INVALID_ARGUMENT', description: '图片、contents 或生成配置不合法，或模型拒绝当前输入。' }, { status: 403, code: 'PERMISSION_DENIED', description: '分组未允许 GenerateContent 或上游图片权限不足。' }, { status: 404, code: 'NOT_FOUND', description: '指定图片模型不存在或不可用。' }, { status: 429, code: 'RESOURCE_EXHAUSTED', description: '并发或上游额度限制。' }]
  },
  {
    id: 'images-generate', category: 'images', title: '生成图片', summary: '使用 OpenAI Images 格式生成图片，响应为图片 URL 或 Base64。',
    platforms: ['openai', 'grok'], method: 'POST', path: '/v1/images/generations', aliases: ['/images/generations'], auth: 'bearer', contentType: 'application/json',
    parameters: imageParameters, requestExample: json({ model: 'gpt-image-2', prompt: '一幅山间日出的水彩画', n: 1, size: '1024x1024', response_format: 'url' }),
    responseExample: imageResponse, responseDescription: '非流式成功响应；高级选项、usage 与 SSE 事件依模型和账号能力而异。', notes: imageNotes, errors: imageErrors
  },
  {
    id: 'images-edit', category: 'images', title: '编辑图片', summary: '提交参考图片与提示词，返回编辑后的图片。',
    platforms: ['openai', 'grok'], method: 'POST', path: '/v1/images/edits', aliases: ['/images/edits'], auth: 'bearer', contentType: 'application/json 或 multipart/form-data',
    parameters: editParameters, requestHeaders: { 'Content-Type': 'application/json' },
    requestExample: json({ model: 'gpt-image-2', prompt: '把背景改为日落时的海边', images: [{ image_url: 'https://images.example.com/input.png' }], n: 1 }),
    responseExample: imageResponse, notes: [...imageNotes, 'JSON 示例为 OpenAI 编辑形状。multipart 客户端应让 SDK 自动生成 boundary，不要手写不带 boundary 的 Content-Type。'], errors: imageErrors
  },
  {
    id: 'images-generate-async', category: 'images', title: '异步生成图片', summary: '立即返回图片任务 ID，再查询结果，避免长连接等待生成。',
    platforms: ['openai', 'grok'], method: 'POST', path: '/v1/images/generations/async', aliases: ['/images/generations/async'], auth: 'bearer', contentType: 'application/json',
    parameters: imageParameters, requestExample: json({ model: 'gpt-image-2', prompt: '一幅山间日出的水彩画', size: '1024x1024', n: 1 }),
    responseStatus: 202, responseExample: json(imageTaskResponse), notes: asyncNotes, errors: [...imageErrors, { status: 503, code: 'image_task_busy', description: '异步待处理任务容量已满，按 Retry-After 提示稍后再提交。' }]
  },
  {
    id: 'images-edit-async', category: 'images', title: '异步编辑图片', summary: '将图片编辑放到后台执行，使用同一个 Key 查询任务。',
    platforms: ['openai', 'grok'], method: 'POST', path: '/v1/images/edits/async', aliases: ['/images/edits/async'], auth: 'bearer', contentType: 'application/json 或 multipart/form-data',
    parameters: editParameters, requestHeaders: { 'Content-Type': 'application/json' }, requestExample: json({ model: 'gpt-image-2', prompt: '将天空修改为星空', images: [{ image_url: 'https://images.example.com/input.png' }] }),
    responseStatus: 202, responseExample: json(imageTaskResponse), notes: asyncNotes, errors: imageErrors
  },
  {
    id: 'images-task', category: 'images', title: '查询异步图片任务', summary: '查询处理状态、错误和最终图片结果。',
    platforms: ['openai', 'grok'], method: 'GET', path: '/v1/images/tasks/{task_id}', aliases: ['/images/tasks/{task_id}'], auth: 'bearer',
    parameters: [parameter('task_id', 'string', true, 'Path：异步提交返回的 task_id；用真实任务 ID 替换示例 imgtask_example。')], requestPath: '/v1/images/tasks/imgtask_example',
    responseExample: json({ ...imageTaskResponse, status: 'completed', http_status: 200, image_url: 'https://images.example.com/output.png', result: { created: 1790900120, data: [{ url: 'https://images.example.com/output.png' }] }, completed_at: 1790900120 }),
    responseDescription: '处理中 status=processing；失败 status=failed，并读取 http_status 与 error。image_url 是首张图片，完整数据在 result。', notes: asyncNotes,
    errors: [{ status: 404, code: 'not_found_error', description: '任务不存在、已过期或不属于当前 API Key。' }]
  },
  {
    id: 'image-batch-create', category: 'images', title: '创建批量图片任务', summary: '一次提交多条图片提示词，由 Gemini API 或 Vertex 批量执行。',
    platforms: ['gemini'], method: 'POST', path: '/v1/images/batches', auth: 'bearer', contentType: 'application/json',
    parameters: [
      parameter('Idempotency-Key', 'string', false, 'Header：建议为一次逻辑提交设置唯一键；相同键与相同内容复用任务，不同内容返回 409。'),
      parameter('model', 'string', true, 'Body：从 /v1/images/batches/models 获取可用模型。'),
      parameter('provider', 'string', false, 'Body：gemini_api 或 vertex；省略时自动选择兼容账号。'),
      parameter('task_name', 'string', false, 'Body：任务名称，省略时自动生成。'),
      parameter('parent_batch_id', 'string', false, 'Body：可选父任务 ID，用于关联批次。'),
      parameter('items', 'array<object>', true, 'Body：非空条目列表；每条包含 prompt，可选 custom_id、output_count 和 reference_images。默认最大条目与输出总数均为 200，可由部署配置调整。'),
      parameter('items[].prompt', 'string', true, 'Body：非空提示词；长度受部署配置限制。'),
      parameter('items[].custom_id', 'string', false, 'Body：批次内唯一 ID，省略时自动生成。'),
      parameter('items[].output_count', 'integer', false, 'Body：默认 1，默认每条最多 4；大于 1 时展开为带编号后缀的子条目。'),
      parameter('items[].reference_images', 'array<object>', false, 'Body：每张包含 mime_type（image/png、image/jpeg、image/webp）以及 Base64 字符串 data 或 gs:// 的 file_uri，二选一；不是任意 HTTP 图片链接。'),
      parameter('image_size', 'string', false, 'Body：当前仅接受 1K；省略使用默认值。'),
      parameter('aspect_ratio', 'string', false, 'Body：宽高比，由实际模型验证。'),
      parameter('response_mime_type', 'string', false, 'Body：输出 MIME，默认 image/png。'),
      parameter('metadata', 'object<string,string>', false, 'Body：可选业务元数据。')
    ],
    requestExample: json({ model: 'gemini-2.5-flash-image', provider: 'gemini_api', task_name: '风景图片', image_size: '1K', items: [{ custom_id: 'landscape_1', prompt: '一幅水彩山景', output_count: 1 }] }),
    responseExample: json(batchResponse), responseDescription: '创建成功为 HTTP 200。任务状态可为 queued、running、processing_results、settling、completed、failed、cancelled、output_deleted。',
    notes: batchNotes, errors: [...batchErrors, { status: 409, code: 'BATCH_IMAGE_IDEMPOTENCY_CONFLICT', description: '同一个幂等键被用于不同请求。' }]
  },
  {
    id: 'image-batch-models', category: 'images', title: '批量图片模型目录', summary: '列出当前 Key 可用于批量图片的模型和提供方。',
    platforms: ['gemini'], method: 'GET', path: '/v1/images/batches/models', auth: 'bearer', parameters: [],
    responseExample: json({ object: 'list', data: [{ id: 'gemini-2.5-flash-image', object: 'image.batch.model', provider: 'gemini_api' }] }),
    notes: ['模型按当前 Key、分组和兼容账号过滤；复合 Key 的模型可能带分组前缀。'], errors: batchErrors
  },
  {
    id: 'image-batch-list', category: 'images', title: '批量图片任务列表', summary: '按状态、名称、下载状态或时间查询当前 Key 的批次。',
    platforms: ['gemini'], method: 'GET', path: '/v1/images/batches', auth: 'bearer', requestPath: '/v1/images/batches?limit=20&cursor=0',
    parameters: [
      parameter('status', 'string', false, 'Query：queued、running、processing_results、completed、failed、cancelled、output_deleted；省略或 all 为全部。'),
      parameter('task_name', 'string', false, 'Query：任务名称筛选。'),
      parameter('downloaded', 'boolean', false, 'Query：true 为已下载，false 为未下载；省略为全部。'),
      parameter('from / to', 'string', false, 'Query：RFC3339 时间或 YYYY-MM-DD 日期。'),
      parameter('limit', 'integer', false, 'Query：默认 20，有效范围 1–100。'),
      parameter('cursor', 'string', false, 'Query：数字偏移量字符串；初始为 0，下一页加上本页数量。')
    ], responseExample: json({ object: 'list', data: [batchResponse], has_more: false }), notes: batchNotes, errors: batchErrors
  },
  {
    id: 'image-batch-get', category: 'images', title: '查询批量图片任务', summary: '读取生成进度、成功失败数量和冻结/实际费用。',
    platforms: ['gemini'], method: 'GET', path: '/v1/images/batches/{id}', auth: 'bearer', parameters: [batchID], requestPath: '/v1/images/batches/batch_example',
    responseExample: json({ ...batchResponse, status: 'completed', success_count: 1, actual_cost: 0.02, settled_at: 1790900300 }), notes: batchNotes, errors: batchErrors
  },
  {
    id: 'image-batch-items', category: 'images', title: '批量图片子任务', summary: '分页获取每条提示词的状态、图片数量与错误。',
    platforms: ['gemini'], method: 'GET', path: '/v1/images/batches/{id}/items', auth: 'bearer', requestPath: '/v1/images/batches/batch_example/items?limit=100&cursor=0',
    parameters: [batchID, parameter('status', 'string', false, 'Query：pending、succeeded、failed；省略为全部。'), parameter('limit', 'integer', false, 'Query：默认 100，有效范围 1–500。'), parameter('cursor', 'string', false, 'Query：数字偏移量字符串，初始 0。')],
    responseExample: json({ object: 'list', data: [{ custom_id: 'landscape_1', status: 'succeeded', prompt_preview: '一幅水彩山景', mime_type: 'image/png', file_extension: 'png', image_count: 1, error: null }], has_more: false }), notes: batchNotes, errors: batchErrors
  },
  {
    id: 'image-batch-content', category: 'images', title: '下载批量任务中的图片', summary: '读取指定子任务的一张图片，返回图片二进制。',
    platforms: ['gemini'], method: 'GET', path: '/v1/images/batches/{id}/items/{custom_id}/content', auth: 'bearer', requestPath: '/v1/images/batches/batch_example/items/landscape_1/content?image_index=0',
    parameters: [batchID, parameter('custom_id', 'string', true, 'Path：子任务列表返回的 ID。'), parameter('image_index', 'integer', false, 'Query：图片索引，从 0 开始；默认 0。')],
    requestLanguage: 'bash', requestExample: 'curl "$BASE_URL/v1/images/batches/batch_example/items/landscape_1/content?image_index=0" \\\n  -H "Authorization: Bearer $TOKENROUTER_API_KEY" \\\n  --output landscape.png',
    responseExample: 'HTTP/1.1 200 OK\nContent-Type: image/png\nContent-Disposition: attachment; filename="landscape_1.png"\n\n<图片二进制>', responseLanguage: 'text',
    notes: ['使用真实 id 与 custom_id 替换示例 batch_example、landscape_1；响应是二进制，示例用 --output 保存到文件。', '成功下载会标记批次下载时间。使用支持请求头的下载客户端，不要把 API Key 放到 URL。'],
    errors: [{ status: 409, code: 'BATCH_IMAGE_NOT_READY / BATCH_IMAGE_ITEM_FAILED', description: '批次尚未完成，或该子任务未成功。' }, { status: 410, code: 'BATCH_IMAGE_OUTPUT_DELETED', description: '结果已清理。' }, { status: 400, code: 'BATCH_IMAGE_ITEM_IMAGE_INDEX_OUT_OF_RANGE', description: '图片索引无效。' }]
  },
  {
    id: 'image-batch-download', category: 'images', title: '下载批量图片 ZIP', summary: '流式下载图片归档及结果清单。',
    platforms: ['gemini'], method: 'GET', path: '/v1/images/batches/{id}/download', auth: 'bearer', requestPath: '/v1/images/batches/batch_example/download',
    parameters: [batchID, parameter('max_items', 'integer', false, 'Query：归档条数上限；超过此值返回错误，不会截取部分成功图片。仍受服务端归档大小和条数限制。')],
    requestLanguage: 'bash', requestExample: 'curl "$BASE_URL/v1/images/batches/batch_example/download" \\\n  -H "Authorization: Bearer $TOKENROUTER_API_KEY" \\\n  --output images.zip',
    responseExample: 'HTTP/1.1 200 OK\nContent-Type: application/zip\nContent-Disposition: attachment; filename="batch_example.zip"\n\n<ZIP 二进制及结果清单>', responseLanguage: 'text',
    notes: ['下载成功会更新下载标记。归档过大时改用逐项下载。归档包含成功图片及结果清单，当前不按 status 查询参数过滤。'], errors: [{ status: 409, code: 'BATCH_IMAGE_NOT_READY', description: '任务尚未完成。' }, { status: 400, code: 'BATCH_IMAGE_ZIP_TOO_MANY_ITEMS / BATCH_IMAGE_DOWNLOAD_TOO_LARGE', description: '归档超过服务器限制。' }, { status: 429, code: 'BATCH_IMAGE_DOWNLOAD_LIMITED', description: '并发下载超限。' }]
  },
  {
    id: 'image-batch-cancel', category: 'images', title: '取消批量图片任务', summary: '请求上游取消任务，并按最终确认结果处理费用。',
    platforms: ['gemini'], method: 'POST', path: '/v1/images/batches/{id}/cancel', auth: 'bearer', parameters: [batchID], requestPath: '/v1/images/batches/batch_example/cancel',
    responseExample: json({ ...batchResponse, status: 'cancelled' }), responseDescription: '返回当前任务对象；取消请求成功不保证上游所有条目都未执行，应继续查询最终状态与 actual_cost。', notes: batchNotes, errors: [...batchErrors, { status: 502, code: 'BATCH_IMAGE_CANCEL_FAILED', description: '上游取消失败或当前任务无法取消。' }]
  },
  {
    id: 'image-batch-delete', category: 'images', title: '删除批量任务记录', summary: '移除已终结的批次记录；不会取消仍在运行的任务。',
    platforms: ['gemini'], method: 'DELETE', path: '/v1/images/batches/{id}', auth: 'bearer', parameters: [batchID], responseStatus: 204, requestPath: '/v1/images/batches/batch_example',
    responseExample: 'HTTP/1.1 204 No Content', responseLanguage: 'text', notes: ['此操作删除记录，不会改写已完成的结算。运行中的任务应先取消并等待终结。'],
    errors: [{ status: 409, code: 'BATCH_IMAGE_RECORD_DELETE_NOT_READY', description: '任务未终结，暂不可删除。' }, { status: 404, code: 'BATCH_IMAGE_NOT_FOUND', description: '任务不存在或不属于当前 Key。' }]
  },
  {
    id: 'image-batch-delete-outputs', category: 'images', title: '清理批量任务产物', summary: '删除已完成批次的输出图片，保留任务和费用记录。',
    platforms: ['gemini'], method: 'DELETE', path: '/v1/images/batches/{id}/outputs', auth: 'bearer', parameters: [batchID], requestPath: '/v1/images/batches/batch_example/outputs',
    responseExample: json({ ...batchResponse, status: 'output_deleted', output_deleted_at: 1790900400 }), notes: ['仅已完成任务可清理。清理后图片不可继续下载；不会退还已产生的生成费用。'],
    errors: [{ status: 409, code: 'BATCH_IMAGE_OUTPUT_DELETE_NOT_READY', description: '任务尚未完成。' }, { status: 502, code: 'BATCH_IMAGE_CLEANUP_FAILED', description: '上游结果清理失败。' }]
  },
  {
    id: 'grok-tts', category: 'audio', title: 'Grok 文字转语音', summary: '将文本转换为音频，直接返回音频字节。',
    platforms: ['grok'], method: 'POST', path: '/v1/tts', aliases: ['/tts'], auth: 'bearer', contentType: 'application/json',
    parameters: [parameter('text', 'string', true, 'Body：朗读文本。'), parameter('voice_id', 'string', false, 'Body：上游内置声音或已创建的自定义声音 ID。'), parameter('language', 'string', false, 'Body：上游支持的语言代码，例如 en。')],
    requestExample: json({ text: 'Hello from TokenRouter.', voice_id: 'Ara', language: 'en' }),
    responseExample: 'HTTP/1.1 200 OK\nContent-Type: audio/mpeg\n\n<音频二进制>', responseLanguage: 'text',
    notes: [...voiceNotes, 'TTS 根据实际请求文本字符数和分组语音价结算，不按普通聊天 Token 价格计算。'], errors: voiceErrors
  },
  {
    id: 'grok-stt', category: 'audio', title: 'Grok 语音转文字', summary: '上传音频，返回原生转录 JSON。',
    platforms: ['grok'], method: 'POST', path: '/v1/stt', aliases: ['/stt'], auth: 'bearer', contentType: 'multipart/form-data',
    parameters: [parameter('file', 'file', true, 'Multipart Form：待转录的音频文件。'), parameter('model', 'string', false, 'Multipart Form：转录模型，项目连通性测试使用 grok-stt。'), parameter('language', 'string', false, 'Multipart Form：上游支持的语言代码。')],
    requestLanguage: 'bash', requestExample: 'curl "$BASE_URL/v1/stt" \\\n  -H "Authorization: Bearer $TOKENROUTER_API_KEY" \\\n  -F "file=@speech.wav" \\\n  -F "model=grok-stt" \\\n  -F "language=en"',
    responseExample: json({ text: 'Hello from TokenRouter.' }), responseDescription: '响应保持上游转录结构，可能包含语言、时长等额外字段。', notes: [...voiceNotes, 'STT 按可识别音频时长和分组语音价结算。'], errors: voiceErrors
  },
  {
    id: 'grok-voice-create', category: 'audio', title: '创建自定义声音', summary: '将参考录音提交到所选 Grok 上游创建声音。',
    platforms: ['grok'], method: 'POST', path: '/v1/custom-voices', aliases: ['/custom-voices'], auth: 'bearer', contentType: 'multipart/form-data',
    parameters: [parameter('file', 'file', true, 'Multipart Form：声音参考录音。'), parameter('name', 'string', false, 'Multipart Form：声音名称。'), parameter('language / gender / tone / use_case', 'string', false, 'Multipart Form：上游支持的声音元数据。')],
    requestLanguage: 'bash', requestExample: 'curl "$BASE_URL/v1/custom-voices" \\\n  -H "Authorization: Bearer $TOKENROUTER_API_KEY" \\\n  -F "file=@reference.wav" \\\n  -F "name=My Narrator" \\\n  -F "language=en"',
    responseExample: json({ voice_id: 'abc123xy', name: 'My Narrator', created_at: '2026-10-01T00:00:00Z' }),
    notes: [...voiceNotes, '网关开放路由不代表上游账号已开通声音创建权限；创建能力与素材要求由上游控制。'], errors: voiceErrors
  },
  {
    id: 'grok-voice-list', category: 'audio', title: '列出自定义声音', summary: '读取当前调度到的 Grok 上游账号中的声音列表。',
    platforms: ['grok'], method: 'GET', path: '/v1/custom-voices', aliases: ['/custom-voices'], auth: 'bearer', parameters: [],
    responseExample: json({ voices: [{ voice_id: 'abc123xy', name: 'My Narrator' }], pagination_token: null }),
    notes: [...voiceNotes, '当前代理没有转发声音列表的查询参数；不要依赖分页参数被传到上游。列表结构保持上游格式。'], errors: voiceErrors
  },
  {
    id: 'grok-voice-get', category: 'audio', title: '查询自定义声音', summary: '获取指定声音元数据。',
    platforms: ['grok'], method: 'GET', path: '/v1/custom-voices/{voice_id}', aliases: ['/custom-voices/{voice_id}'], auth: 'bearer', parameters: [voiceID], requestPath: '/v1/custom-voices/abc123xy',
    responseExample: json({ voice_id: 'abc123xy', name: 'My Narrator', language: 'en' }), notes: voiceNotes, errors: voiceErrors
  },
  {
    id: 'grok-voice-update', category: 'audio', title: '更新自定义声音', summary: '修改声音的上游元数据。',
    platforms: ['grok'], method: 'PATCH', path: '/v1/custom-voices/{voice_id}', aliases: ['/custom-voices/{voice_id}'], auth: 'bearer', contentType: 'application/json', requestPath: '/v1/custom-voices/abc123xy',
    parameters: [voiceID, parameter('name / description / tone', 'string', false, 'Body：需要更新的声音元数据；可用字段以实际上游为准。')],
    requestExample: json({ description: '用于简短旁白', tone: 'calm' }), responseExample: json({ voice_id: 'abc123xy', name: 'My Narrator', description: '用于简短旁白', tone: 'calm' }), notes: voiceNotes, errors: voiceErrors
  },
  {
    id: 'grok-voice-delete', category: 'audio', title: '删除自定义声音', summary: '从当前上游账号删除声音资源。',
    platforms: ['grok'], method: 'DELETE', path: '/v1/custom-voices/{voice_id}', aliases: ['/custom-voices/{voice_id}'], auth: 'bearer', parameters: [voiceID], requestPath: '/v1/custom-voices/abc123xy',
    responseExample: '成功响应的状态码和响应体按上游原样返回；没有本站统一删除对象。', responseLanguage: 'text', notes: [...voiceNotes, '删除后使用该声音的语音请求可能失败。'], errors: voiceErrors
  },
  {
    id: 'grok-voice-audio', category: 'audio', title: '下载自定义声音音频', summary: '获取声音资源的上游音频内容。',
    platforms: ['grok'], method: 'GET', path: '/v1/custom-voices/{voice_id}/audio', aliases: ['/custom-voices/{voice_id}/audio'], auth: 'bearer', parameters: [voiceID], requestPath: '/v1/custom-voices/abc123xy/audio',
    requestLanguage: 'bash', requestExample: 'curl "$BASE_URL/v1/custom-voices/abc123xy/audio" \\\n  -H "Authorization: Bearer $TOKENROUTER_API_KEY" \\\n  --output voice-audio.bin',
    responseExample: 'HTTP/1.1 200 OK\nContent-Type: audio/wav\n\n<上游返回的音频二进制>', responseLanguage: 'text', notes: [...voiceNotes, 'Content-Type 以实际响应为准，不保证统一 WAV。'], errors: voiceErrors
  },
  {
    id: 'grok-realtime', category: 'audio', title: 'Grok 实时语音 WebSocket', summary: '建立 xAI Voice 实时会话并双向转发音频事件。',
    platforms: ['grok'], method: 'WS', path: '/v1/realtime', aliases: ['/realtime'], auth: 'websocket', requestPath: '/v1/realtime?model=grok-voice-latest',
    parameters: [parameter('model', 'string', false, 'Query：默认 grok-voice-latest。'), parameter('Upgrade / Connection', 'string', true, 'Header：由 WebSocket 客户端生成升级请求；使用可设置 Authorization 请求头的客户端。')],
    requestLanguage: 'json', requestExample: json({ type: 'input_audio_buffer.append', audio: '<按上游协商格式编码的 Base64 音频>' }),
    responseStatus: 101, responseExample: 'HTTP/1.1 101 Switching Protocols\nUpgrade: websocket\nConnection: Upgrade\n\n<后续为 xAI 原生双向 WebSocket 事件>', responseLanguage: 'text',
    notes: ['示例 JSON 是升级后的事件，不是 HTTP 请求体。音频格式和 session 配置遵循实际 xAI Voice 协议。', '仅 Grok 分组；会话期间持有并发槽。观测到真实音频后按连接时长结算，纯文本、仅转录事件或握手失败不计语音会话费。', '此端点不是 Responses WebSocket，也不提供 OpenAI /v1/realtime 会话转换。'],
    errors: [{ status: 426, code: 'invalid_request_error', description: '未使用 WebSocket 升级。' }, ...voiceErrors, { status: 502, code: 'upstream_error', description: '上游实时握手失败。' }]
  },
  {
    id: 'grok-web-search', category: 'tools', title: 'Grok 网页搜索', summary: '通过 Grok 原生搜索工具查询网页来源。',
    platforms: ['grok'], method: 'POST', path: '/v1/web_search', aliases: ['/web_search'], auth: 'bearer', contentType: 'application/json',
    parameters: [parameter('query / input', 'string', true, 'Body：二选一；query 优先，必须有非空查询文本。'), parameter('max_results', 'integer', false, 'Body：默认 5，最多 20；非正数使用默认值。')],
    requestExample: json({ query: '异步 HTTP API 的轮询设计', max_results: 5 }),
    responseExample: json({ query: '异步 HTTP API 的轮询设计', results: [{ url: 'https://example.com/article', title: '异步接口设计', snippet: '使用任务 ID 轮询状态。' }], provider: 'grok-native', max_results: 5 }),
    notes: ['结果仅包含可恢复的真实工具来源，可能少于 max_results 或为空。按分组搜索价格结算。'],
    errors: [{ status: 400, code: 'invalid_request_error', description: 'query/input 为空或 JSON 不合法。' }, { status: 404, code: 'not_found_error', description: '非 Grok 分组。' }, { status: 502, code: 'web_search_error', description: '上游搜索失败。' }]
  },
  {
    id: 'grok-x-search', category: 'tools', title: 'Grok X 搜索', summary: '搜索 X 内容，并可限定账号、日期与媒体理解选项。',
    platforms: ['grok'], method: 'POST', path: '/v1/x_search', aliases: ['/x_search'], auth: 'bearer', contentType: 'application/json',
    parameters: [parameter('query / input', 'string', true, 'Body：二选一的搜索文本，query 优先。'), parameter('max_results', 'integer', false, 'Body：默认 5，最多 20。'), parameter('allowed_x_handles / excluded_x_handles', 'array<string>', false, 'Body：允许或排除的 X 账号；组合限制由上游验证。'), parameter('from_date / to_date', 'string', false, 'Body：上游支持的日期范围。'), parameter('enable_image_understanding / enable_video_understanding', 'boolean', false, 'Body：启用搜索结果中的图片/视频理解。')],
    requestExample: json({ query: 'AI API updates', max_results: 5, allowed_x_handles: ['example'], from_date: '2026-09-01', to_date: '2026-09-30', enable_image_understanding: false }),
    responseExample: json({ query: 'AI API updates', results: [{ url: 'https://x.com/example/status/123', title: 'API update', snippet: 'A matching post.' }], provider: 'grok-native', max_results: 5 }),
    notes: ['强制使用 x_search 工具，返回结构与网页搜索相同；按分组 X 搜索价格结算。'],
    errors: [{ status: 400, code: 'invalid_request_error', description: '查询或上游工具参数不合法。' }, { status: 404, code: 'not_found_error', description: '非 Grok 分组。' }, { status: 502, code: 'web_search_error', description: '上游 X 搜索失败。' }]
  },
  {
    id: 'openai-live', category: 'audio', title: 'OpenAI Live 创建会话', summary: '创建 ChatGPT Live WebRTC 会话，返回 SDP answer 与控制通道地址。',
    platforms: ['openai'], method: 'POST', path: '/v1/live', aliases: ['/backend-api/codex/realtime/calls'], auth: 'bearer', contentType: 'application/json 或 multipart/form-data',
    parameters: [parameter('sdp', 'string', true, 'Body 或 Multipart Form：WebRTC 客户端实际生成的 SDP offer，不能为空。'), parameter('session', 'object', true, 'JSON Body：会话对象；multipart 时传 JSON 字符串。可包含 model 和工具等原生配置。'), parameter('session.model', 'string', false, 'Body：默认 gpt-live，受 Key、渠道和账号模型映射及能力约束。')],
    requestLanguage: 'bash', requestExample: 'curl "$BASE_URL/v1/live" \\\n  -H "Authorization: Bearer $TOKENROUTER_API_KEY" \\\n  -F "sdp=<offer.sdp" \\\n  -F \'session={"model":"gpt-live"}\'',
    responseExample: 'HTTP/1.1 200 OK\nContent-Type: application/sdp\nLocation: /v1/live/call_example\n\n<上游 SDP answer，交给 WebRTC 客户端设置远端描述>', responseLanguage: 'text',
    notes: ['需要 OpenAI 分组开启 Live，并有可用 Live 账号。普通聊天账号或开启 Responses 不代表 Live 一定可用。', '使用返回 Location 连接控制 WebSocket；Codex 别名创建时 Location 为 /backend-api/codex/{call_id}。此响应不是 JSON。'],
    errors: [{ status: 400, code: 'invalid_request_error', description: 'SDP 为空或 session 不是合法 JSON 对象。' }, { status: 403, code: 'permission_error', description: '分组 Live 未启用或客户端不符合账号策略。' }, { status: 429, code: 'rate_limit_error', description: 'Live 并发达到上限。' }, { status: 503, code: 'api_error', description: '没有可用 Live 账号或依赖服务暂不可用。' }]
  },
  {
    id: 'openai-live-sideband', category: 'audio', title: 'OpenAI Live 控制通道', summary: '连接已有 Live 会话的 sideband WebSocket。',
    platforms: ['openai'], method: 'WS', path: '/v1/live/{call_id}', aliases: ['/backend-api/codex/{call_id}'], auth: 'websocket', requestPath: '/v1/live/call_example',
    parameters: [parameter('call_id', 'string', true, 'Path：创建会话响应 Location 中的 ID；必须使用相同 Key、用户/团队和分组身份。'), parameter('Upgrade / Connection', 'string', true, 'Header：WebSocket 升级头，由客户端自动生成。')],
    responseStatus: 101, responseExample: 'HTTP/1.1 101 Switching Protocols\nUpgrade: websocket\nConnection: Upgrade\n\n<Live 控制事件流>', responseLanguage: 'text',
    notes: ['使用创建接口实际返回的 Location，不要把任意 Codex 子路径当作 call_id。会话控制事件按 Live 原生协议转发。'],
    errors: [{ status: 403, code: 'permission_error', description: 'Live 未启用或会话属于其他身份。' }, { status: 404, code: 'not_found_error', description: '会话不存在或已不可用。' }]
  }
]
