import type { ApiDocEndpoint, ApiDocParameter } from './types'

// 平台范围来自公开网关的实际分派；是否可调用仍取决于当前 Key 的分组与账号能力。
const textPlatforms = ['anthropic', 'openai', 'gemini', 'antigravity', 'grok', 'qoder', 'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go', 'cline', 'command_code']
const countPlatforms = ['anthropic', 'openai', 'gemini', 'grok', 'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go', 'cline', 'command_code']
const responsesCountPlatforms = ['openai', 'grok', 'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go', 'cline', 'command_code']
const modelParameter: ApiDocParameter = { name: 'model', type: 'string', required: true, description: 'Body · 当前 Key 可请求的模型 ID。请以模型目录为准；示例型号不代表本站一定已开通。' }
const messagesParameters: ApiDocParameter[] = [
  modelParameter,
  { name: 'messages', type: 'array', required: true, description: 'Body · 对话历史，元素包含 role（user / assistant）和 content（字符串或内容块数组）；工具结果使用 tool_result 内容块。' },
  { name: 'max_tokens', type: 'integer', required: true, description: 'Body · 最大输出 Token 数；实际范围由模型与上游决定。' },
  { name: 'system', type: 'string | array', required: false, description: 'Body · 系统指令；内容块可携带供应商支持的缓存标记。' },
  { name: 'stream', type: 'boolean', required: false, description: 'Body · true 返回 Anthropic SSE；false 返回完整 JSON。' },
  { name: 'tools / tool_choice', type: 'array / object', required: false, description: 'Body · 工具声明使用 name、description、input_schema；工具是否可用由目标模型决定。客户端执行工具后继续提交 tool_result。' },
  { name: 'temperature / top_p / stop_sequences / thinking', type: 'number / array / object', required: false, description: 'Body · 采样、停止词及思考参数按所选模型支持范围填写，跨协议转换不能保证所有厂商扩展都等价。' },
  { name: 'anthropic-version / anthropic-beta', type: 'string', required: false, description: 'Header · 原生 Anthropic 版本和可选能力声明，例如 anthropic-version: 2023-06-01；并非所有上游都支持相同 beta。' }
]
const messagesRequest = JSON.stringify({ model: 'claude-sonnet-4', max_tokens: 512, messages: [{ role: 'user', content: [{ type: 'text', text: '用一句话介绍你自己。' }] }], stream: false }, null, 2)
const messagesResponse = JSON.stringify({ id: 'msg_example', type: 'message', role: 'assistant', model: 'claude-sonnet-4', content: [{ type: 'text', text: '我是帮助你解决问题的 AI 助手。' }], stop_reason: 'end_turn', usage: { input_tokens: 18, output_tokens: 16 } }, null, 2)
const responsesParameters: ApiDocParameter[] = [
  modelParameter,
  { name: 'input', type: 'string | array', required: true, description: 'Body · 用户输入或完整历史项数组。消息内容支持 input_text；图片、工具调用及工具结果的支持范围取决于上游。' },
  { name: 'instructions', type: 'string', required: false, description: 'Body · 本轮系统指令。继续对话时请按客户端协议重新提供需要保留的指令。' },
  { name: 'stream', type: 'boolean', required: false, description: 'Body · true 返回 Responses SSE 事件；false 返回完整 response 对象。' },
  { name: 'tools / tool_choice / parallel_tool_calls', type: 'array / string | object / boolean', required: false, description: 'Body · 函数工具示例：{type:"function", name:"lookup", parameters:{type:"object",properties:{}}}。函数执行在客户端，托管工具还需模型和账号支持。' },
  { name: 'max_output_tokens / reasoning / text', type: 'integer / object / object', required: false, description: 'Body · 输出预算、思考档位及输出格式；字段和取值需符合目标模型能力。' },
  { name: 'previous_response_id / store', type: 'string / boolean', required: false, description: 'Body · 上游状态续接选项。部分兼容平台使用无状态转换，不支持依赖此字段省略历史；跨平台客户端应保留完整历史。' },
  { name: 'metadata / prompt_cache_key', type: 'object / string', required: false, description: 'Body · 应用元数据与稳定缓存会话标识；不代表网关永久保存对话。' }
]
const responsesRequest = JSON.stringify({ model: 'gpt-6-astra', input: [{ role: 'user', content: [{ type: 'input_text', text: '你好，请简短回答。' }] }], stream: false }, null, 2)
const responsesResponse = JSON.stringify({ id: 'resp_example', object: 'response', status: 'completed', model: 'gpt-6-astra', output: [{ type: 'message', role: 'assistant', content: [{ type: 'output_text', text: '你好！' }] }], usage: { input_tokens: 12, output_tokens: 4, total_tokens: 16 } }, null, 2)
const geminiParameters: ApiDocParameter[] = [
  { name: 'model', type: 'string', required: true, description: 'Path · 模型名称，不在 Body 中重复 model；使用目录中 models/ 后的名称。' },
  { name: 'contents', type: 'array', required: true, description: 'Body · 每项包含 role（user / model）及 parts。文本 part 使用 {text:"你好"}，多模态结构遵循 Gemini 原生协议。' },
  { name: 'systemInstruction', type: 'object', required: false, description: 'Body · 系统指令，例如 {parts:[{text:"简洁回答"}]}。' },
  { name: 'generationConfig', type: 'object', required: false, description: 'Body · temperature、maxOutputTokens、responseMimeType、thinkingConfig 等模型支持的生成配置。图片输出仅在对应图片模型及账号能力可用时支持。' },
  { name: 'tools / toolConfig / safetySettings', type: 'array / object / array', required: false, description: 'Body · Gemini 原生工具、工具选择和安全配置。Antigravity 兼容转换有独立能力限制，不能假定所有内置工具均可组合。' }
]
const geminiRequest = JSON.stringify({ contents: [{ role: 'user', parts: [{ text: '用一句话介绍你自己。' }] }], generationConfig: { maxOutputTokens: 512 } }, null, 2)
const geminiResponse = JSON.stringify({ candidates: [{ content: { role: 'model', parts: [{ text: '我是一个 AI 助手。' }] }, finishReason: 'STOP', index: 0 }], usageMetadata: { promptTokenCount: 12, candidatesTokenCount: 8, totalTokenCount: 20 } }, null, 2)

export const textEndpoints: ApiDocEndpoint[] = [
  {
    id: 'messages', category: 'text', title: 'Anthropic Messages',
    summary: '使用 Anthropic 消息格式进行文本、多模态或工具调用。按 Key 所选分组原生转发或转换为对应平台协议。',
    platforms: textPlatforms, method: 'POST', path: '/v1/messages', auth: 'anthropic', contentType: 'application/json',
    parameters: messagesParameters, requestExample: messagesRequest, responseExample: messagesResponse,
    responseDescription: '示例为非流式成功响应。流式模式依次接收 message_start、content_block_*、message_delta、message_stop；发生错误时应处理 error 事件。',
    notes: ['分组必须允许 Anthropic Messages 入站协议；Video 分组不提供文本生成。', '模型 ID、图片、thinking、缓存和工具能力以当前分组及上游为准。函数工具调用只返回调用信息，不会自动连接客户端文件系统。', 'Qoder、Gemini、Grok、国产供应商和 OpenCode 使用各自转换链路；OpenCode Jev 必须走 System One 专用接口。'],
    errors: [{ status: 400, code: 'invalid_request_error', description: '模型或消息格式错误，参数与所选模型不兼容。' }, { status: 403, code: 'permission_error', description: '分组未允许 Messages 协议或其他分组策略拒绝。' }, { status: 503, code: 'api_error', description: '当前模型没有可调度账号；检查分组、账号状态和并发。' }]
  },
  {
    id: 'messages-count-tokens', category: 'tools', title: 'Messages 输入 Token 统计',
    summary: '使用 Messages 请求格式预估输入 Token，不生成回复；部分平台返回本地估算值。',
    platforms: countPlatforms, method: 'POST', path: '/v1/messages/count_tokens', aliases: ['/messages/count_tokens'], auth: 'anthropic', contentType: 'application/json',
    parameters: messagesParameters.filter(parameter => !['max_tokens', 'stream', 'temperature / top_p / stop_sequences / thinking'].includes(parameter.name)),
    requestExample: JSON.stringify({ model: 'claude-sonnet-4', messages: [{ role: 'user', content: '你好' }] }, null, 2),
    responseExample: JSON.stringify({ input_tokens: 12 }, null, 2),
    notes: ['Antigravity、Qoder 和 Anthropic Bedrock 不支持此计数接口。/antigravity/v1/messages/count_tokens 虽保留路由，但明确返回 404。', 'Grok、OpenCode 和国产兼容平台可采用本地估算；OpenAI 可能桥接 Responses input_tokens 或回退估算。计数结果不应当作最终收费账单。', '按平台执行 Messages 协议准入、权限与额度检查；客户端应保留本地估算回退。'],
    errors: [{ status: 404, code: 'not_found_error', description: '当前平台或账号类型不支持该接口。' }, { status: 400, code: 'invalid_request_error', description: '缺少 model 或消息结构无效。' }]
  },
  {
    id: 'chat-completions', category: 'text', title: 'Chat Completions',
    summary: 'OpenAI Chat 兼容的对话接口，适合使用 messages 的 SDK 与应用。',
    platforms: textPlatforms, method: 'POST', path: '/v1/chat/completions', aliases: ['/chat/completions'], auth: 'bearer', contentType: 'application/json',
    parameters: [modelParameter,
      { name: 'messages', type: 'array', required: true, description: 'Body · 角色和内容数组，role 通常为 system、developer、user、assistant、tool。工具结果需关联 tool_call_id；具体角色支持随模型变化。' },
      { name: 'stream / stream_options', type: 'boolean / object', required: false, description: 'Body · stream=true 返回 SSE；stream_options.include_usage 可请求终态 usage，实际输出受上游支持限制。' },
      { name: 'max_tokens / max_completion_tokens', type: 'integer', required: false, description: 'Body · 输出预算，按模型选用相应字段。' },
      { name: 'temperature / top_p / stop', type: 'number / string | array', required: false, description: 'Body · 采样与停止参数；推理模型可能限制这些参数。' },
      { name: 'tools / tool_choice', type: 'array / string | object', required: false, description: 'Body · 函数工具采用 {type:"function",function:{name,description,parameters}}；收到 tool_calls 后由客户端执行并回传结果。' },
      { name: 'response_format / reasoning_effort', type: 'object / string', required: false, description: 'Body · 结构化输出及思考档位，仅适用于支持的模型。' }
    ],
    requestExample: JSON.stringify({ model: 'gpt-6-astra', messages: [{ role: 'user', content: '你好' }], stream: false }, null, 2),
    responseExample: JSON.stringify({ id: 'chatcmpl_example', object: 'chat.completion', created: 1760000000, model: 'gpt-6-astra', choices: [{ index: 0, message: { role: 'assistant', content: '你好！' }, finish_reason: 'stop' }], usage: { prompt_tokens: 12, completion_tokens: 4, total_tokens: 16 } }, null, 2),
    responseDescription: 'stream=true 时返回 chat.completion.chunk 数据帧，并通常以 data: [DONE] 结束；工具调用增量在 choices[].delta.tool_calls。',
    notes: ['分组必须允许 Chat Completions；无需根据上游平台改写本站路径。', '请求的多模态和工具字段仍受目标模型能力限制；Jev 不支持此入口。'],
    errors: [{ status: 403, code: 'protocol_not_allowed', description: '该分组未开启 Chat Completions 入站协议。' }, { status: 400, code: 'invalid_request_error', description: 'messages、模型或工具调用历史格式错误。' }]
  },
  {
    id: 'responses', category: 'text', title: 'Responses',
    summary: 'OpenAI Responses 兼容生成接口，可用于文本、多模态、工具调用及支持的 Codex 客户端。',
    platforms: textPlatforms, method: 'POST', path: '/v1/responses', aliases: ['/responses', '/backend-api/codex/responses'], auth: 'bearer', contentType: 'application/json',
    parameters: responsesParameters, requestExample: responsesRequest, responseExample: responsesResponse,
    responseDescription: '流式模式返回 response.created、response.output_text.delta、工具调用事件及 response.completed / response.failed 等终态。HTTP 200 或某一个进度事件不代表生成成功。',
    notes: ['分组必须允许 Responses 协议。账号配置可能使请求走原生 Responses 或转换后的 Chat / Messages。', '客户端应完整消费终态和 usage；在流已经产生输出后，不应将断流简单当成未调用成功而自动重放。', '这是 POST 生成接口；网关没有注册通用 GET /v1/responses/{id} 的历史响应检索接口。'],
    errors: [{ status: 403, code: 'protocol_not_allowed', description: '分组未开启 Responses 入站协议。' }, { status: 400, code: 'invalid_request_error', description: 'model、input 或工具历史结构不符合目标协议。' }, { status: 503, code: 'api_error', description: '模型无可用账号、容量不足或可重试上游故障未能恢复。' }]
  },
  {
    id: 'responses-compact', category: 'text', title: 'Responses 上下文压缩',
    summary: '供支持远程压缩的 OpenAI / Codex 客户端缩减长会话上下文。',
    platforms: ['openai'], method: 'POST', path: '/v1/responses/compact', aliases: ['/responses/compact', '/backend-api/codex/responses/compact'], auth: 'bearer', contentType: 'application/json',
    parameters: [modelParameter,
      { name: 'input', type: 'array', required: true, description: 'Body · 待压缩的完整历史输入项，包含消息与已完成的工具调用/结果。' },
      { name: 'instructions', type: 'string', required: false, description: 'Body · 压缩时保留的系统指令，按客户端和上游协议提供。' },
      { name: 'reasoning / service_tier', type: 'object / string', required: false, description: 'Body · 上游支持的思考与服务档位配置。' }
    ], requestExample: JSON.stringify({ model: 'gpt-6-astra', input: [{ role: 'user', content: '需要保留的较长会话内容。' }] }, null, 2),
    responseExample: JSON.stringify({ id: 'resp_compact_example', output: [{ type: 'compaction', encrypted_content: '<上游返回的不透明压缩内容>' }], usage: { input_tokens: 1000, output_tokens: 100 } }, null, 2),
    responseDescription: '示例仅展示压缩结果的关键字段；原生响应由上游决定。encrypted_content 是不透明内容，应按客户端协议原样用于后续对话。',
    notes: ['需要账号具备 Compact 能力；普通文本可用不代表压缩端点可用。', '新式 Codex remote compaction v2 可能在普通流式 /responses 中发送 compaction_trigger，它与此旧版独立压缩入口并存。', 'Responses 安全子路径可进入通配路由，但不意味着所有上游都实现任意子资源。Qoder 明确拒绝 Responses 子路径。'],
    errors: [{ status: 503, code: 'compact_not_supported', description: '没有可承接压缩请求的账号。' }, { status: 404, code: 'not_found_error', description: '路径不安全、所选平台不支持子路径，或上游未实现压缩。' }]
  },
  {
    id: 'responses-input-tokens', category: 'tools', title: 'Responses 输入 Token 统计',
    summary: '使用 Responses 请求体统计输入 Token；不能执行此预检的兼容账号会使用本地估算。',
    platforms: responsesCountPlatforms, method: 'POST', path: '/v1/responses/input_tokens', aliases: ['/responses/input_tokens', '/backend-api/codex/responses/input_tokens'], auth: 'bearer', contentType: 'application/json',
    parameters: responsesParameters.filter(parameter => ['model', 'input', 'instructions', 'tools / tool_choice / parallel_tool_calls'].includes(parameter.name)),
    requestExample: JSON.stringify({ model: 'gpt-6-astra', input: '你好' }, null, 2),
    responseExample: JSON.stringify({ object: 'response.input_tokens', input_tokens: 12 }, null, 2),
    notes: ['只做输入预检，不生成回复、不记录生成用量；仍执行认证、分组协议、模型和扣费资格检查。', '计数可能为本地近似值，不应视为实际收费。最终用量以真正生成请求的 usage 和本站记录为准。', 'Anthropic、Gemini、Antigravity、Qoder 分组不支持这个 Responses 子端点。'],
    errors: [{ status: 404, code: 'not_found_error', description: '当前平台不支持 Responses 输入计数。' }, { status: 502, code: 'upstream_error', description: '上游响应缺少有效 input_tokens 或请求失败且无法回退。' }]
  },
  {
    id: 'responses-websocket', category: 'text', title: 'Responses WebSocket',
    summary: '建立持久 WebSocket 连接，以 response.create 消息发起模型调用。',
    platforms: ['openai', 'grok'], method: 'WS', path: '/v1/responses', aliases: ['/responses', '/backend-api/codex/responses'], auth: 'websocket',
    parameters: [
      { name: 'Authorization', type: 'string', required: true, description: '握手 Header · Bearer 站内 API Key；使用可发送认证头的服务端 WebSocket 客户端，不要把 Key 放入 URL。' },
      { name: 'type', type: 'string', required: true, description: 'JSON 消息 · 首条生成消息使用 response.create。' },
      { ...modelParameter, description: 'JSON 消息 · 模型 ID，必须可用于当前账号的 Responses WS 通道。' },
      { name: 'input / tools / previous_response_id', type: 'string | array / array / string', required: false, description: 'JSON 消息 · 其余内容按 Responses WS 协议提供。续接状态和可用字段受上游与传输模式限制。' }
    ], requestExample: JSON.stringify({ type: 'response.create', model: 'gpt-6-astra', input: [{ role: 'user', content: [{ type: 'input_text', text: '你好' }] }] }, null, 2),
    responseExample: JSON.stringify({ type: 'response.output_text.delta', response_id: 'resp_example', output_index: 0, content_index: 0, delta: '你好' }, null, 2),
    responseStatus: 101, responseDescription: 'HTTP 握手成功返回 101，后续示例为单条 WS JSON 帧。必须继续读取 response.completed / response.failed 等终态，不能把 delta 当成完整回复。',
    notes: ['GET 同路径用于 Upgrade，普通 HTTP GET 不会生成文本。WebSocket 的账号和分组能力检查独立于普通 HTTP Responses。', '只有 OpenAI / Grok 平台进入 WS 处理器；具体账号传输配置仍可能拒绝连接。'],
    errors: [{ status: 404, code: 'not_found_error', description: '当前平台未提供 Responses WebSocket。' }, { status: 426, code: 'invalid_request_error', description: '没有发送有效的 WebSocket Upgrade 请求。升级后的无效首帧会通过 WS 关闭码或错误帧报告。' }, { status: 503, code: 'api_error', description: 'WS 账号或连接容量不可用。' }]
  },
  {
    id: 'embeddings', category: 'text', title: 'Embeddings 文本向量',
    summary: '将文本转为向量，适用于检索、聚类或语义匹配；仅 OpenAI 分组可用。',
    platforms: ['openai'], method: 'POST', path: '/v1/embeddings', aliases: ['/embeddings'], auth: 'bearer', contentType: 'application/json',
    parameters: [modelParameter,
      { name: 'input', type: 'string | array', required: true, description: 'Body · 待编码文本或文本数组；token ID 输入是否可用由所选上游模型决定。' },
      { name: 'encoding_format', type: 'string', required: false, description: 'Body · float 或 base64，以模型支持范围为准。' },
      { name: 'dimensions', type: 'integer', required: false, description: 'Body · 支持缩减维度的模型可指定输出维数。' },
      { name: 'user', type: 'string', required: false, description: 'Body · 可选的应用用户标识，不要填入敏感凭据。' }
    ], requestExample: JSON.stringify({ model: 'text-embedding-3-small', input: '一只猫坐在窗边。', encoding_format: 'float' }, null, 2),
    responseExample: JSON.stringify({ object: 'list', data: [{ object: 'embedding', index: 0, embedding: [0.012, -0.018, 0.025] }], model: 'text-embedding-3-small', usage: { prompt_tokens: 8, total_tokens: 8 } }, null, 2),
    responseDescription: '向量示例仅保留三个元素便于阅读，真实长度取决于模型与 dimensions。',
    notes: ['还需账号具有 Embeddings 端点能力与有效的模型定价；普通 OAuth 文本账号不代表自动支持向量。'],
    errors: [{ status: 404, code: 'not_found_error', description: '分组不是 OpenAI 或上游不支持 Embeddings。' }, { status: 400, code: 'invalid_request_error', description: '输入、维数或编码格式不支持。' }]
  },
  {
    id: 'models-list', category: 'models', title: '可用模型列表',
    summary: '返回当前 Key 可访问的模型目录，用于 SDK 模型选择。目录受分组、渠道、账号映射和可见规则共同约束。',
    platforms: [...textPlatforms, 'video'], method: 'GET', path: '/v1/models', aliases: ['/models'], auth: 'bearer', parameters: [],
    responseExample: JSON.stringify({ object: 'list', data: [{ id: 'gpt-6-astra', object: 'model', type: 'model', created: 1704067200, owned_by: 'openai', display_name: 'GPT-6 Astra' }] }, null, 2),
    responseDescription: 'data 为模型数组，始终以 id 发起调用；display_name、created、owned_by、能力等附加字段可能因平台而不同。',
    notes: ['复合 Key / 智能路由 Key 聚合其有权访问的候选分组；API Key 精确模型别名也可能出现在目录中。', '空列表表示当前权限和配置下没有可展示模型，不代表上游服务宕机。', 'Codex 客户端可使用无前缀 /models；旧 /backend-api/codex/models 已移除，不要使用该路径。模型目录不是上游全量模型列表的无条件代理。']
  },
  {
    id: 'models-retrieve', category: 'models', title: '查询单个模型', summary: '从与模型列表相同的可见目录中精确查询模型，不泄露其他分组的型号。',
    platforms: [...textPlatforms, 'video'], method: 'GET', path: '/v1/models/{model}', aliases: ['/models/{model}'], auth: 'bearer',
    parameters: [{ name: 'model', type: 'string', required: true, description: 'Path · 从列表取得的完整 id，包含供应商前缀或复合 Key 前缀时应完整保留。' }],
    responseExample: JSON.stringify({ id: 'gpt-6-astra', object: 'model', display_name: 'GPT-6 Astra', owned_by: 'openai' }, null, 2),
    errors: [{ status: 404, code: 'model_not_found', description: '模型不存在或当前 Key 不可见；两者统一返回 404。' }]
  },
  {
    id: 'key-usage', category: 'models', title: 'Key 用量与可用额度', summary: '查询站内 Key 的用量、余额或订阅资金来源，适合 CC Switch 等客户端展示。',
    platforms: [...textPlatforms, 'video'], method: 'GET', path: '/v1/usage', aliases: ['/antigravity/v1/usage'], auth: 'bearer',
    parameters: [
      { name: 'days', type: 'integer', required: false, description: 'Query · 日用量统计范围，允许 1–90。' },
      { name: 'start_date / end_date', type: 'string', required: false, description: 'Query · 模型统计的日期范围；未提供时使用近 30 天。仅日期的 end_date 包含该日。' },
      { name: 'timezone', type: 'string', required: false, description: 'Query · 日期解析时区，例如 Asia/Shanghai。' }
    ], responseExample: JSON.stringify({ mode: 'unrestricted', isValid: true, planName: '钱包余额', remaining: 20, unit: 'USD', balance: 20, billing: { mode: 'balance', source: 'balance', preferred_subscription_id: null, available: true, unit: 'USD' } }, null, 2),
    responseDescription: '示例为钱包模式。有限额 Key 返回 quota_limited 与 quota / rate_limits；订阅模式返回 subscription。usage、daily_usage、model_stats 为按当前 Key 查询的可选统计。',
    notes: ['这是本站账本和权益视图，不是上游余额查询；unit 以站点配置为准。', '指定订阅的 Key 不会因套餐失效而在此接口隐式回退钱包；应检查 billing.available 与 billing.source。', 'Antigravity 别名仍只查询当前 Key 的本地额度，不授予额外权限。'],
    errors: [{ status: 400, code: 'invalid_request_error', description: 'days 不在 1–90 范围内。' }, { status: 500, code: 'api_error', description: '无法读取资金来源。' }]
  },
  {
    id: 'gemini-models-list', category: 'models', title: 'Gemini 原生模型目录', summary: '为 Gemini SDK 返回 models 数组和 supportedGenerationMethods 等原生元数据。',
    platforms: ['gemini', 'antigravity'], method: 'GET', path: '/v1beta/models', aliases: ['/antigravity/v1beta/models'], auth: 'google', parameters: [],
    responseExample: JSON.stringify({ models: [{ name: 'models/gemini-2.5-pro', displayName: 'Gemini 2.5 Pro', supportedGenerationMethods: ['generateContent', 'streamGenerateContent'] }] }, null, 2),
    notes: ['普通 /v1beta 入口要求 Gemini 分组；Antigravity 使用 /antigravity/v1beta 前缀。', '依据 Key 类型、分组自定义目录、账号 AI Studio 查询或兼容回退目录返回模型；不能仅由模型出现在回退目录推定其当前一定可调度。', '生成协议开关不阻止此 GET 目录查询。'],
    errors: [{ status: 400, code: 'INVALID_ARGUMENT', description: '当前 Key 分组不是 Gemini，且没有使用 Antigravity 专用入口。' }, { status: 503, code: 'UNAVAILABLE', description: '没有可查询目录的可用账号。' }]
  },
  {
    id: 'gemini-models-retrieve', category: 'models', title: 'Gemini 单模型信息', summary: '查询指定模型的 Gemini 格式元数据。',
    platforms: ['gemini', 'antigravity'], method: 'GET', path: '/v1beta/models/{model}', aliases: ['/antigravity/v1beta/models/{model}'], auth: 'google',
    parameters: [{ name: 'model', type: 'string', required: true, description: 'Path · Gemini 模型名称，例如 gemini-2.5-pro。' }],
    responseExample: JSON.stringify({ name: 'models/gemini-2.5-pro', displayName: 'Gemini 2.5 Pro', supportedGenerationMethods: ['generateContent', 'streamGenerateContent'] }, null, 2),
    notes: ['原生元数据可能来自上游或兼容回退；模型是否可生成还须通过分组、模型和账号资格检查。Antigravity 使用其专用前缀。'],
    errors: [{ status: 400, code: 'INVALID_ARGUMENT', description: '分组平台或模型路径不合法。' }, { status: 404, code: 'NOT_FOUND', description: '模型不可用或上游没有该模型。' }]
  },
  {
    id: 'gemini-generate-content', category: 'text', title: 'Gemini GenerateContent', summary: '使用 Gemini 原生 contents / parts 请求体生成内容，保持 Google 响应结构。',
    platforms: ['gemini', 'antigravity'], method: 'POST', path: '/v1beta/models/{model}:generateContent', aliases: ['/v1beta/models/{model}/generateContent', '/antigravity/v1beta/models/{model}:generateContent', '/antigravity/v1beta/models/{model}/generateContent'], auth: 'google', contentType: 'application/json',
    parameters: geminiParameters, requestExample: geminiRequest, responseExample: geminiResponse,
    notes: ['普通入口要求 Gemini 分组；Antigravity 使用专用前缀。生成还需分组开启 Gemini GenerateContent 协议。', '本端点也承载兼容的 Gemini 图片生成模型：通过 generationConfig.responseModalities 等原生参数请求，输出可能包含 inlineData。不要将它与 OpenAI Images 请求体混用。', '正常完成原因、内容安全阻断和 HTTP 错误含义不同；请同时检查 candidates、finishReason 与 promptFeedback。'],
    errors: [{ status: 403, code: 'PERMISSION_DENIED', description: '分组未允许 Gemini 生成协议或权限不足。' }, { status: 400, code: 'INVALID_ARGUMENT', description: '模型路径、parts 或 generationConfig 不符合上游要求。' }]
  },
  {
    id: 'gemini-stream-content', category: 'text', title: 'Gemini 流式生成', summary: '通过 streamGenerateContent 动作逐步返回 Gemini 原生内容片段。',
    platforms: ['gemini', 'antigravity'], method: 'POST', path: '/v1beta/models/{model}:streamGenerateContent', aliases: ['/v1beta/models/{model}/streamGenerateContent', '/antigravity/v1beta/models/{model}:streamGenerateContent', '/antigravity/v1beta/models/{model}/streamGenerateContent'], auth: 'google', contentType: 'application/json',
    parameters: [...geminiParameters, { name: 'alt', type: 'string', required: false, description: 'Query · Gemini SDK 常使用 alt=sse 请求 SSE 传输；以客户端及上游兼容方式为准。' }],
    requestExample: geminiRequest, responseExample: 'data: {"candidates":[{"content":{"role":"model","parts":[{"text":"你好"}]},"index":0}]}\n\ndata: {"candidates":[{"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":2,"totalTokenCount":14}}\n\n', responseLanguage: 'text',
    responseDescription: '示例为 SSE。各片段保持 Gemini 形状，不是 OpenAI choices 或 Responses 事件；正确读取结束状态与末尾 usageMetadata。',
    notes: ['不要在请求体中仅设置 stream=true 来替代动作名称。Antigravity 使用专用路径，账号与分组能力要求同非流式接口。'],
    errors: [{ status: 429, code: 'RESOURCE_EXHAUSTED', description: '并发或额度不足；若流已开启，错误可能出现在流内。' }, { status: 503, code: 'UNAVAILABLE', description: '当前没有可用生成账号。' }]
  },
  {
    id: 'gemini-count-tokens', category: 'tools', title: 'Gemini 原生 Token 统计', summary: '通过 countTokens 动作预检 Gemini 输入；Antigravity 当前仅提供零值兼容响应，不提供真实计数。',
    platforms: ['gemini', 'antigravity'], method: 'POST', path: '/v1beta/models/{model}:countTokens', aliases: ['/v1beta/models/{model}/countTokens', '/antigravity/v1beta/models/{model}:countTokens', '/antigravity/v1beta/models/{model}/countTokens'], auth: 'google', contentType: 'application/json',
    parameters: [geminiParameters[0]!, { name: 'contents', type: 'array', required: true, description: 'Body · 待统计的 Gemini contents / parts。其他计数参数遵循上游支持的 countTokens 格式。' }],
    requestExample: JSON.stringify({ contents: [{ role: 'user', parts: [{ text: '你好' }] }] }, null, 2), responseExample: JSON.stringify({ totalTokens: 3 }, null, 2),
    responseDescription: '示例为支持真实计数的 Gemini 路径。选到 Antigravity 账号时固定返回 {"totalTokens":0}，这是兼容占位，不能表示输入没有 Token。',
    notes: ['这里是 Gemini 原生动作，与不支持 Antigravity 的 /messages/count_tokens 不是同一接口。', 'Antigravity countTokens 不请求上游、不统计输入，只返回 totalTokens:0；专用 Antigravity 路径以及 Gemini 混合调度选到 Antigravity 时都适用，客户端应自行估算。', 'Gemini 原生账号的精确计数和回退行为取决于账号及上游支持；仍受 Gemini 协议门禁和鉴权约束，预检值不能替代最终用量和扣费。'],
    errors: [{ status: 400, code: 'INVALID_ARGUMENT', description: '计数请求格式或模型名错误。' }, { status: 404, code: 'NOT_FOUND', description: '目标账号上游不支持此动作。' }]
  },
  {
    id: 'antigravity-messages', category: 'text', title: 'Antigravity 专用 Messages', summary: '使用 Anthropic 协议并强制选择 Antigravity 账号，适用于需要固定 Antigravity 链路的客户端。',
    platforms: ['antigravity'], method: 'POST', path: '/antigravity/v1/messages', auth: 'anthropic', contentType: 'application/json',
    parameters: messagesParameters, requestExample: messagesRequest, responseExample: messagesResponse,
    notes: ['认证仍使用站内 Key，不使用 Google / Antigravity OAuth 凭据。强制平台不绕过分组协议、模型和额度权限。', '仅选择 Antigravity 账号，不参与普通 Anthropic / Gemini 混合调度。', '/antigravity/v1/messages/count_tokens 当前明确返回 404；客户端请保留本地估算。'],
    errors: [{ status: 403, code: 'permission_error', description: '分组未允许 Messages 或模型访问被拒绝。' }, { status: 503, code: 'api_error', description: '没有满足资格的 Antigravity 账号。' }]
  },
  {
    id: 'antigravity-models', category: 'models', title: 'Antigravity 专用模型目录', summary: '返回当前 Key / 分组可请求的 Antigravity 模型。',
    platforms: ['antigravity'], method: 'GET', path: '/antigravity/v1/models', aliases: ['/antigravity/models'], auth: 'bearer', parameters: [],
    responseExample: JSON.stringify({ object: 'list', data: [{ id: 'claude-sonnet-4', type: 'model', display_name: 'Claude Sonnet 4' }] }, null, 2),
    notes: ['这是 Anthropic / OpenAI 兼容的 data 数组；Gemini SDK 请使用 /antigravity/v1beta/models。', '实际可见型号来自当前分组、映射和账号能力，示例型号仅用于说明结构。']
  },
  {
    id: 'antigravity-model-retrieve', category: 'models', title: 'Antigravity 单模型查询', summary: '在 Antigravity 专用可见模型目录中查询一个模型。',
    platforms: ['antigravity'], method: 'GET', path: '/antigravity/v1/models/{model}', aliases: ['/antigravity/models/{model}'], auth: 'bearer',
    parameters: [{ name: 'model', type: 'string', required: true, description: 'Path · 专用目录返回的完整模型 ID。' }], responseExample: JSON.stringify({ id: 'claude-sonnet-4', type: 'model', display_name: 'Claude Sonnet 4' }, null, 2),
    errors: [{ status: 404, code: 'model_not_found', description: '模型不存在或当前 Key 不可见。' }]
  },
  {
    id: 'opencode-systemone', category: 'tools', title: 'TypeSafe / Jev System One', summary: 'TypeSafe 或 OpenCode Zen Jev 的同步结构化决策接口，使用 state 和 questions，保持原生结果。',
    platforms: ['opencode_go', 'typesafe'], method: 'POST', path: '/v1/systemone', aliases: ['/systemone'], auth: 'bearer', contentType: 'application/json',
    parameters: [
      { name: 'model', type: 'string', required: true, description: 'Body · TypeSafe 分组使用 jev-latest；OpenCode Zen 分组使用 jev-1.13 或 jev-1.13-free；GO 账号不支持。' },
      { name: 'state', type: 'JSON 值', required: true, description: 'Body · 必须提供待判断的状态内容，具体数据结构遵循 System One 上游规范。' },
      { name: 'questions', type: 'object', required: true, description: 'Body · 非空问题对象，键为问题 ID；type 支持 noul、choice、score；choice 的 criteria 为对象，score 为非空档位数组。重复键、大小写混淆与非法结构会在请求前拒绝。' },
      { name: 'stream', type: 'boolean', required: false, description: 'Body · 只允许 false 或省略；不支持流式。' }
    ], requestExample: JSON.stringify({ model: 'jev-1.13', state: 'classify', questions: { is_urgent: { type: 'noul', instructions: 'Is this urgent?' } } }, null, 2),
    responseExample: JSON.stringify({ model: 'jev-1.13', answers: { is_urgent: { type: 'noul', value: false } }, usage: { input_tokens: 12, output_tokens: 1 } }, null, 2),
    notes: ['问题示例用于展示结构，完整问题类型由上游校验。不能把 Messages / Chat / Responses 请求体直接发送到这里。', '不支持图片、音频、视频；网关依据真实 usage 记录计费，并按账号规则派生缓存会话头。'],
    errors: [{ status: 400, code: 'invalid_request_error', description: '模型、state、questions 不合法，或 stream=true。' }, { status: 404, code: 'not_found_error', description: '当前分组不是 TypeSafe / OpenCode，或没有可用的 System One 账号。' }]
  },
  {
    id: 'alpha-search', category: 'tools', title: 'Codex Alpha Search', summary: 'OpenAI / Codex Responses Lite 使用的独立搜索接口，不是普通 Chat 或 Responses 请求体。',
    platforms: ['openai'], method: 'POST', path: '/v1/alpha/search', aliases: ['/alpha/search', '/backend-api/codex/alpha/search'], auth: 'bearer', contentType: 'application/json',
    parameters: [modelParameter,
      { name: 'id', type: 'string', required: false, description: 'Body · 客户端搜索会话 ID。' },
      { name: 'commands', type: 'object', required: 'conditional', description: 'Body · Codex 搜索命令，例如 {search_query:[{q:"查询内容"}]}；具体命令由上游支持的 SearchRequest 协议决定。' },
      { name: 'input / settings / reasoning', type: 'array / object / object', required: false, description: 'Body · Codex 原生上下文、搜索配置与思考参数，按客户端和上游协议提供，不保证所有兼容中继支持。' }
    ], requestExample: JSON.stringify({ model: 'gpt-6-astra', id: 'search-session-example', commands: { search_query: [{ q: '公开技术文档' }] }, settings: { external_web_access: true } }, null, 2),
    responseExample: JSON.stringify({ output: '搜索结果摘要。', results: [{ type: 'text_result', ref_id: 'turn0search0', url: 'https://example.com/docs', title: '文档' }] }, null, 2),
    responseDescription: '示例为 Responses 搜索兼容回退结果的关键结构。原生 Alpha Search 成功响应按上游 JSON 保留，不能假定所有上游字段完全相同。',
    notes: ['仅 OpenAI 分组且账号具备 Alpha Search 能力；普通模型可用不等于独立搜索端点可用。', '特定 OAuth / PAT 场景可使用既有 Responses 搜索回退；不能将该能力视为任意第三方 API Key 均支持。'],
    errors: [{ status: 400, code: 'invalid_request_error', description: '缺少 model 或请求不是有效 JSON。' }, { status: 404, code: 'not_found_error', description: '非 OpenAI 分组或目标上游没有此工具接口。' }, { status: 503, code: 'api_error', description: '没有满足独立搜索能力的账号。' }]
  }
]
