import type { ApiDocEndpoint, ApiDocError, ApiDocParameter } from './types'

// 视频文档按实际入口区分响应形状，统一 URL 不代表统一所有供应商的请求参数。
const json = (value: unknown) => JSON.stringify(value, null, 2)
const idempotency: ApiDocParameter = {
  name: 'Idempotency-Key（请求头）', type: 'string', required: false,
  description: '仅独立 Video 任务支持，最多 128 字符。同一 Key、相同路径和完全相同的原始正文返回已有任务；正文空格或键顺序改变也会触发 409。',
}
const taskId: ApiDocParameter = {
  name: 'task_id / video_id（路径）', type: 'string', required: true,
  description: '创建响应中的任务 ID。统一接口使用本站 vid_… ID；原生接口使用供应商 ID。必须使用创建任务的同一站内 API Key。',
}
const videoErrors: ApiDocError[] = [
  { status: 400, code: 'VIDEO_MODEL_REQUIRED / VIDEO_MODEL_CONFLICT', description: '缺少 model，或路径模型与正文 model 不一致。' },
  { status: 400, code: 'VIDEO_PRICING_UNAVAILABLE', description: '模型或分辨率缺少匹配价卡与兜底价，生成不会继续。' },
  { status: 400, code: 'VIDEO_METADATA_CONFLICT', description: '混用多个参数结构，分辨率、时长或参考媒体信息互相冲突。' },
  { status: 400, code: 'VIDEO_TASK_BUDGET_REQUIRED', description: '按秒预留或 Token 秒价预扣需要时长；自动时长请求需由管理员配置任务时长预留上限。' },
  { status: 409, code: 'VIDEO_TASK_CONFLICT', description: '幂等键对应了不同请求，或当前任务状态不允许该操作。' },
  { status: 415, code: 'VIDEO_CONTENT_TYPE', description: '创建独立 Video 任务必须使用 application/json，不接受 multipart。' },
  { status: 429, code: 'VIDEO_TASK_LIMIT', description: '账号在途任务数达到上限，请等待已有任务完成。' },
  { status: 503, code: 'VIDEO_NO_ELIGIBLE_ACCOUNT', description: '没有同时满足模型、端点、分组和并发要求的视频账号。' },
  { status: '4xx / 5xx', code: '上游错误', description: '原生上游错误可能保留原 HTTP 状态和正文；请读取错误体及 X-Video-Task-ID。不要对受理不明的生成请求直接重试。' },
]
const taskErrors: ApiDocError[] = [
  { status: 404, code: 'VIDEO_TASK_NOT_FOUND', description: '任务不存在，或不属于当前用户、API Key、原生协议。' },
  { status: 409, code: 'VIDEO_TASK_CONFLICT', description: '任务状态不允许操作，或原生任务 ID 对应多个归属，无法安全确定任务。' },
  { status: 503, code: 'VIDEO_ACCOUNT_BUSY', description: '原任务账号当前繁忙，稍后查询；不会换账号查询或重新生成。' },
]
const nativeNotes = [
  '这是 Video 平台原生入口。需要绑定 Video 分组的站内 Key，账号还需启用相应端点。请求 JSON 扩展字段保留；模型会按站内映射替换，其余厂商字段不会自动转换。',
  '原生响应保留上游格式和任务 ID；响应头 X-Video-Task-ID 提供本站 vid_… ID，可用于统一任务查询。示例字段由具体上游决定，不是固定面板响应包。',
  '任务记录由后台持久轮询推进；生成完成不等于扣费已完成，统一查询中的 billing_status=settled 才表示已结算。查询和下载不会再次生成。',
]
const billingNotes = [
  '按秒模式预留预计视频费；按次模式预留一次成功任务费。Token 模式可按模型配置先用独立秒价预扣，也可完成后结算。最终按任务创建时冻结的价卡、实际用量和适用倍率结算；参考图片附加费为固定金额。',
  '缺少价格会阻止提交，缺少可信 Token 用量会进入待核对，不能按零费用处理。完成、失败和退款请同时检查 status 与 billing_status。',
  '创建完整收到 HTTP 4xx/5xx 且没有上游任务 ID 时，独立 Video 任务失败并释放预扣。网络中断、响应读取失败或成功响应缺少任务 ID 时，受理仍可能不明，会保留预算待核对；查询错误不会自动退款。',
]
const commonVideoParameters: ApiDocParameter[] = [
  { name: 'model', type: 'string', required: true, description: '模型广场中当前分组可用的模型 ID，必须已有可用账号和匹配视频定价。' },
  { name: 'prompt / content / input', type: 'string / array / object', required: 'conditional', description: '按实际选中上游填写文本或多模态输入。统一入口不会把 prompt 转成 Ark content 或 Wan input。' },
  { name: 'duration', type: 'number', required: 'conditional', description: '视频时长（秒）。上游支持范围由模型决定；自动时长或省略时按秒预扣必须有管理员配置的预留时长。Wan 原生字段通常位于 parameters.duration。' },
  { name: 'resolution', type: 'string', required: 'conditional', description: '按上游支持档位填写，如 480p、720p、768p、1080p；与价卡匹配，不会自动降档。存在分辨率价卡时需要提供明确分辨率。' },
  { name: 'ratio / aspect_ratio', type: 'string', required: false, description: '按供应商约定选择字段，如 16:9 或 9:16；网关不互相改写这两个字段。' },
  { name: 'images / image_urls / content', type: 'array', required: false, description: '按上游协议提供参考图片。若价卡启用图片附加费，按可识别输入数量计费；避免同时提交互相冲突的图片表示。' },
  { name: 'videos / video_url / content', type: 'array / string / array', required: false, description: '按上游协议提供参考视频；同一分辨率统一单价，不区分是否携带参考视频。参考图片附加费按模型配置单独计算。' },
  { name: 'generate_audio / watermark / seed', type: 'boolean / boolean / number', required: false, description: '可选厂商参数，是否支持、默认值和合法范围由模型与上游决定。' },
  idempotency,
]
const unifiedResponse = {
  id: 'vid_example', task_id: 'vid_example', object: 'video.generation', model: 'video-model',
  status: 'queued', billing_status: 'reserved', data: [], created: 1750000000,
}
const videosResponse = {
  id: 'vid_example', object: 'video', model: 'video-model', status: 'queued',
  created_at: 1750000000, billing_status: 'reserved', duration: 8, resolution: '720p',
}
const arkAliases = ['/v3/contents/generations/tasks', '/v1/contents/generations/tasks', '/contents/generations/tasks']
const grokNotes = [
  '仅 Grok 分组可用，账号需具备视频生成资格；模型与参数必须被实际上游支持。与独立 Video 任务的 vid_… 状态及预扣规则不同。',
  'Grok 创建只保存任务和计费信息；后续查询或 content 下载观察到 status=done 且存在 video.url 时才尝试结算。请主动轮询，任务记录列表本身不会查询上游或触发扣费。',
  '必须用原 Key 查询，任务始终回到原账号。归属缓存保留约 24 小时，请及时获取结果；重复查询和下载使用任务幂等结算，不新增生成费用。',
]
const grokErrors: ApiDocError[] = [
  { status: 400, code: 'invalid_request_error', description: '请求体、模型或上游视频参数无效。' },
  { status: 404, code: '任务不可用', description: '任务不存在、归属不匹配，或原任务绑定已过期。' },
  { status: 503, code: 'grok_media_no_eligible_account', description: '没有具备媒体资格且可承接请求的 Grok 账号。' },
  { status: '4xx / 5xx', code: '上游错误', description: '查看原错误内容；创建结果不确定时先核对任务，不要盲目重复生成。' },
]

export const videoEndpoints: ApiDocEndpoint[] = [
  {
    id: 'video-create', category: 'video', videoProtocol: 'openai', title: 'Video · 创建 OpenAI Videos 任务',
    summary: '使用 /v1/videos 创建异步 JSON 视频任务。Video 分组返回本站任务对象；同名 Grok 入口仍使用 xAI 任务格式。',
    platforms: ['video', 'grok'], method: 'POST', path: '/v1/videos', aliases: ['/videos'], auth: 'bearer', contentType: 'application/json',
    parameters: commonVideoParameters,
    requestExample: json({ model: 'video-model', prompt: '一只小猫在雪地追逐雪花，电影镜头', duration: 8, resolution: '720p', ratio: '16:9' }),
    responseExample: json(videosResponse), responseStatus: 200,
    responseDescription: '示例是 Video 分组的成功受理响应；Grok 分组通常返回 request_id，见 Grok 视频生成。受理成功只代表任务已创建。',
    notes: [
      'Video 账号必须明确勾选 OpenAI Videos（/v1/videos），且模型允许使用该端点，才能调用此入口。未启用时该入口不可用，不会回退到其他端点。请求体遵循该上游端点的参数约定。',
      '状态为 queued（排队）、in_progress（生成中）、completed（完成）、failed（失败）。受理不明或过期还会返回 task_status，不能把 in_progress 直接当成可重试。',
      '接口只接收 JSON；没有开放 GET /v1/videos 集合列表、multipart、remix 或 DELETE /v1/videos/{id}。任务列表请使用站内任务记录页面。',
      ...billingNotes,
    ], errors: videoErrors,
  },
  {
    id: 'video-query', category: 'video', videoProtocol: 'openai', title: 'Video · 查询 OpenAI Videos 任务',
    summary: '用本站任务 ID 查询进度和账务状态。Grok 任务 ID 同路径仍由原 Grok 链路处理。',
    platforms: ['video', 'grok'], method: 'GET', path: '/v1/videos/{video_id}', requestPath: '/v1/videos/vid_example', aliases: ['/videos/{video_id}'], auth: 'bearer', parameters: [taskId],
    responseExample: json({ ...videosResponse, status: 'completed', billing_status: 'settled', completed_at: 1750000060, usage: { completion_tokens: 80770 } }),
    responseDescription: 'HTTP 200 表示查到任务；status=failed 仍可返回 HTTP 200，并附 error.code 和 error.message。duration、resolution、usage 仅在已知时返回。',
    notes: ['billing_status=settled 表示已结算，released 表示预留已释放，reconciliation 表示待核对，reserved 表示预算仍预留。生成状态和账务状态应分开判断。', 'Video 任务有后台恢复轮询。重复查询不会重新 POST 生成；原生任务也可通过 X-Video-Task-ID 对应的本站 ID 查询。', ...grokNotes.slice(1)], errors: taskErrors,
  },
  {
    id: 'video-content', category: 'video', videoProtocol: 'openai', title: 'Video · 下载或播放视频',
    summary: '读取原任务的视频字节流，保留鉴权，不把上游账号凭据交给调用方。',
    platforms: ['video', 'grok'], method: 'GET', path: '/v1/videos/{video_id}/content', requestPath: '/v1/videos/vid_example/content', aliases: ['/videos/{video_id}/content'], auth: 'bearer',
    parameters: [taskId, { name: 'Range（请求头）', type: 'string', required: false, description: '单段字节范围，例如 bytes=0-1048575；不能使用多段 Range。' }],
    responseExample: 'HTTP/1.1 200 OK\nContent-Type: video/mp4\nCache-Control: private, no-store\n\n<视频二进制内容>', responseLanguage: 'text',
    responseDescription: '完整内容通常为 200，部分内容为 206，范围不可满足为 416。不是 JSON 响应。',
    notes: ['独立 Video 仅允许已完成且 billing_status=settled 的任务下载，未完成或待核对返回 409；本接口不补查上游任务、不补扣。', 'Grok content 会先查询原任务状态，并可能触发首次完成结算。两种分组的行为不同。', '独立 Video 下载支持最长两分钟、最大 512 MiB 的只读流。先用带 Bearer 的服务端或 SDK 请求下载，再按需要保存；不要把站内密钥放进视频 URL。'],
    errors: [...taskErrors, { status: 400, code: 'VIDEO_CONTENT_RANGE_INVALID', description: 'Range 不是合法单段字节范围。' }, { status: 502, code: 'VIDEO_CONTENT_UNAVAILABLE', description: '产物地址过期、不安全或无法获取有效视频内容。' }],
  },
  {
    id: 'video-compat-create', category: 'video', videoProtocol: 'openai', title: 'Video · 兼容视频创建',
    summary: '原有 /v1/video/generations 统一 URL，参数继续遵循实际供应商，返回本站异步任务。',
    platforms: ['video'], method: 'POST', path: '/v1/video/generations', aliases: ['/video/generations'], auth: 'bearer', contentType: 'application/json', parameters: commonVideoParameters,
    requestExample: json({ model: 'video-model', prompt: '落日下的海岸，镜头缓慢推进', duration: 8, resolution: '720p', ratio: '16:9' }),
    responseExample: json(unifiedResponse), responseStatus: 200,
    notes: ['此处是单数 video；/v1/videos/generations 是 Grok 入口，不能互换。', '账号必须明确勾选 OpenAI Videos（/v1/video/generations），且模型允许使用该端点；未启用时不会回退到其他端点。仅启用原生协议时请调用对应原生路径，不会自动互转 input/content/prompt。', '返回 vid_… ID；不要把本地 ID 直接用于供应商网站查询。轮询使用本平台的 /v1/video/generations/{task_id} 或 /v1/tasks/{task_id}。', ...billingNotes], errors: videoErrors,
  },
  {
    id: 'video-compat-query', category: 'video', videoProtocol: 'openai', title: 'Video · 兼容任务查询',
    summary: '查询 Video 任务与费用释放、结算状态；返回 video.generation 对象。',
    platforms: ['video'], method: 'GET', path: '/v1/video/generations/{task_id}', requestPath: '/v1/video/generations/vid_example', aliases: ['/video/generations/{task_id}', '/v1/tasks/{task_id}'], auth: 'bearer', parameters: [taskId],
    responseExample: json({ ...unifiedResponse, status: 'completed', billing_status: 'settled', data: [{ url: 'https://media.example.com/output.mp4' }], usage: { completion_tokens: 80770 } }),
    notes: ['可能状态包含 pending、queued、processing、completed、failed、cancelled、unknown、expired；与 /v1/videos 的四态表达不同。', 'data[].url 仅在生成完成且存在可公开输出的产物地址时提供，可能带时效；没有 URL 不代表可以忽略 billing_status。', 'unknown 为受理不明，expired 可能需要成本核对；保留原任务 ID 联系管理员，不要直接再创建。', '任务查询本身不重新生成；持久任务和账务恢复由后台推进。'], errors: taskErrors,
  },
  {
    id: 'video-compat-cancel', category: 'video', videoProtocol: 'openai', title: 'Video · 取消任务',
    summary: '请求取消尚未结束的任务；只有实际上游支持且确认取消后，才释放预留。',
    platforms: ['video'], method: 'DELETE', path: '/v1/video/generations/{task_id}', requestPath: '/v1/video/generations/vid_example', aliases: ['/video/generations/{task_id}', '/v1/tasks/{task_id}'], auth: 'bearer', parameters: [taskId],
    responseExample: json({ ...unifiedResponse, status: 'cancelled', billing_status: 'released' }),
    responseDescription: '示例表示上游已确认取消且释放成功；取消请求不保证任务停止，也不保证立即退款。',
    notes: ['当前仅 compat 和 Seedance 端点支持取消；OpenAI Videos、Kling、Wan、MiniMax 未开放取消契约。', '任务已完成、已失败、已取消、处于冲突状态或没有上游 ID 时可能返回 409。上游 HTTP 200 中的业务错误或仍在处理状态不等于取消成功。', '确认 cancelled/canceled/deleted、Seedance 成功空删除包或 compat 204 才能作为取消依据；失败释放使用同一任务账本，重复请求不会重复退款。'],
    errors: [...taskErrors, { status: 400, code: 'VIDEO_CANCEL_UNSUPPORTED', description: '该任务实际使用的上游端点不支持取消。' }],
  },
  {
    id: 'ark-create', category: 'video', videoProtocol: 'seedance', title: 'Seedance / 方舟 · 创建原生任务',
    summary: '使用 Ark content 多模态数组提交视频任务，保留方舟原生请求与响应。',
    platforms: ['video', 'openai'], method: 'POST', path: '/api/v3/contents/generations/tasks', aliases: arkAliases, auth: 'bearer', contentType: 'application/json',
    parameters: [
      { name: 'model', type: 'string', required: true, description: '已配置的视频模型或方舟推理接入点 ID。' },
      { name: 'content', type: 'array', required: true, description: '非空多模态数组。文本为 {type:"text",text:"..."}，图片/视频等按模型原生格式提供 type 与对应 URL 对象。' },
      { name: 'duration', type: 'number', required: 'conditional', description: '秒数或模型允许的自动时长值，范围由模型决定；自动时长需满足本站预算配置。' },
      { name: 'resolution / ratio', type: 'string', required: 'conditional', description: '按模型支持范围填写分辨率与比例；分辨率需匹配已配置价卡。' },
      { name: 'generate_audio / watermark / seed', type: 'boolean / boolean / number', required: false, description: '方舟模型扩展参数，保持原生格式。' }, idempotency,
    ],
    requestExample: json({ model: 'seedance-model-or-endpoint-id', content: [{ type: 'text', text: '小猫在雪地里追逐雪花' }], duration: 8, resolution: '720p', ratio: '16:9', watermark: false }),
    responseExample: json({ id: 'cgt_example' }), responseDescription: '上游成功状态和响应原样返回，通常通过 id 获取供应商任务 ID。Video 链路另提供 X-Video-Task-ID。',
    notes: [nativeNotes[1], 'Video 分组使用独立任务、持久轮询与视频价卡。OpenAI 分组要求 API Key 账号显式启用 Seedance 能力并配置 Base URL，走旧链路，不能套用独立 Video 预扣规则。', '旧 OpenAI Seedance 任务需客户端在归属有效期内主动查询：成功状态 succeeded 且 usage.completion_tokens>0 才按输出 Token 计费。任务记录页面不会替代这次查询；Idempotency-Key 的本站去重能力仅适用于 Video 链路。'], errors: [...videoErrors, { status: 503, code: 'TaskStorageUnavailable', description: '旧 OpenAI Seedance 已创建任务但归属存储失败，响应携带任务 ID；保留 ID 联系管理员，不可再次提交生成。' }],
  },
  {
    id: 'ark-query', category: 'video', videoProtocol: 'seedance', title: 'Seedance / 方舟 · 查询原生任务',
    summary: '取得方舟原生状态、产物和 usage；原生 ID 与创建 Key 必须匹配。',
    platforms: ['video', 'openai'], method: 'GET', path: '/api/v3/contents/generations/tasks/{task_id}', requestPath: '/api/v3/contents/generations/tasks/cgt_example', aliases: arkAliases.map(path => `${path}/{task_id}`), auth: 'bearer', parameters: [taskId],
    responseExample: json({ id: 'cgt_example', model: 'seedance-model-or-endpoint-id', status: 'succeeded', content: { video_url: 'https://media.example.com/output.mp4' }, usage: { completion_tokens: 80770 } }),
    notes: ['原生排队、运行、成功和失败状态遵循供应商响应，例如 queued、running、succeeded、failed；不要把 HTTP 200 直接当生成成功。', 'Video 后台会持续查询并按冻结价格结算；旧 OpenAI Seedance 依赖调用方查询，归属及待结算快照约保留 24 小时。', '原生响应中的上游模型名不还原为客户端别名；产物链接可能过期，获取后按需保存。'], errors: taskErrors,
  },
  {
    id: 'ark-delete', category: 'video', videoProtocol: 'seedance', title: 'Seedance / 方舟 · 删除原生任务',
    summary: '向原任务账号提交原生删除请求，保留供应商状态及响应。',
    platforms: ['video', 'openai'], method: 'DELETE', path: '/api/v3/contents/generations/tasks/{task_id}', requestPath: '/api/v3/contents/generations/tasks/cgt_example', aliases: arkAliases.map(path => `${path}/{task_id}`), auth: 'bearer', parameters: [taskId],
    responseExample: 'HTTP/1.1 204 No Content', responseLanguage: 'text', responseStatus: 204,
    responseDescription: '空成功响应仅为一种供应商结果；实际 HTTP 状态与正文以上游为准。',
    notes: ['Video 链路需确认删除成功才释放预算；已生成完成的任务不能因为删除请求而撤销结算。', '旧 OpenAI Seedance 链路没有独立 Video 的预扣账本，删除不构成已结算用量退款。', '保留任务 ID 核对响应，不对上游 HTTP 200 中的业务失败作成功取消处理。'], errors: taskErrors,
  },
  ...([
    { id: 'kling-text', title: 'Kling · 模型路径文生视频', path: '/text-to-video/{model}', image: false, omni: false },
    { id: 'kling-image', title: 'Kling · 模型路径图生视频', path: '/image-to-video/{model}', image: true, omni: false },
    { id: 'kling-omni', title: 'Kling · 模型路径 Omni 视频', path: '/omni-video/{model}', image: false, omni: true },
    { id: 'kling-text2video', title: 'Kling · 原生 text2video', path: '/v1/videos/text2video', image: false, omni: false },
    { id: 'kling-native-omni', title: 'Kling · 原生 Omni 视频', path: '/v1/videos/omni-video', image: false, omni: true },
  ] as const).map((route): ApiDocEndpoint => ({
    id: route.id, category: 'video', videoProtocol: 'kling', title: route.title, summary: '显式选择 Kling 操作路径，视频参数按该上游的原生 JSON 合同提供。',
    platforms: ['video'], method: 'POST', path: route.path, requestPath: route.path.replace('{model}', 'kling-video-model'), auth: 'bearer', contentType: 'application/json',
    parameters: [
      { name: 'model', type: 'string', required: !route.path.includes('{model}'), description: '模型路径已提供 model 时正文可省略；两处同时提供必须一致。固定路径必须提供非空 model，不能只用 model_name 替代本站模型字段。' },
      { name: 'prompt', type: 'string', required: 'conditional', description: '描述视频内容；具体操作是否必填由上游模型决定。' },
      { name: 'image / image_tail / image_list', type: 'string / array', required: route.image ? true : false, description: '图生、首尾帧或 Omni 参考图按上游选择对应字段，本站保留原结构，不自动把图片数组转换成另一种协议。' },
      { name: 'duration', type: 'number', required: 'conditional', description: '本站计费需要可解析的数字秒数；合法生成时长由上游模型决定。' },
      { name: 'resolution / aspect_ratio / mode', type: 'string', required: 'conditional', description: '分辨率需匹配本站价格。mode 如 std/pro 按上游定义；本站不自动把 mode 推断为具体分辨率。' },
      { name: '其他厂商字段', type: 'JSON', required: false, description: route.omni ? '例如 Omni 的参考视频、音频及 camera_control，遵循实际厂商文档。' : '如 negative_prompt、camera_control、seed，是否支持由具体上游决定。' }, idempotency,
    ],
    requestExample: json({ model: 'kling-video-model', prompt: '海边灯塔，镜头缓慢向前', duration: 5, resolution: '720p', ...(route.image ? { image: 'https://media.example.com/first-frame.png' } : {}) }),
    responseExample: json({ code: 0, data: { task_id: 'kling_task_example', task_status: 'submitted' } }),
    responseDescription: '原生回包示例，任务 ID 常见于 data.task_id；上游有不同结构时保持原样。',
    notes: [...nativeNotes, '这些是不同操作入口，不是可随意互换的别名。后续统一用 GET /tasks?task_ids={task_id} 查询；Kling 取消和私有文件下载端点没有在本项目开放。'], errors: videoErrors,
  })),
  {
    id: 'kling-query', category: 'video', videoProtocol: 'kling', title: 'Kling · 查询单个任务', summary: '使用 task_ids 查询原生任务结果，每次只允许一个 ID。',
    platforms: ['video'], method: 'GET', path: '/tasks', requestPath: '/tasks?task_ids=kling_task_example', auth: 'bearer',
    parameters: [{ name: 'task_ids（查询参数）', type: 'string', required: true, description: '单个供应商任务 ID，例如 kling_task_example；不接受逗号分隔或重复 task_ids 参数。' }],
    responseExample: json({ code: 0, data: [{ task_id: 'kling_task_example', task_status: 'succeed', task_result: { videos: [{ url: 'https://media.example.com/output.mp4' }] } }] }),
    notes: ['请求示例：GET /tasks?task_ids=kling_task_example。供应商结果可为数组或对象，原样返回。', '查询始终使用创建账号和原任务协议，不广播查询其它账号；任务不存在或归属不匹配时返回 404。', '本站没有注册 Kling DELETE /tasks；即使外部文档提供取消接口，也不能直接当成本平台能力。'],
    errors: [...taskErrors, { status: 400, code: 'VIDEO_TASK_ID_REQUIRED', description: '必须且只能提供一个 task_ids。' }],
  },
  {
    id: 'wan-create', category: 'video', videoProtocol: 'wan', title: '万相 / Wan · 创建原生任务', summary: '保留 input 与 parameters 嵌套结构，提交原生异步视频合成。',
    platforms: ['video'], method: 'POST', path: '/api/v1/services/aigc/video-generation/video-synthesis', auth: 'bearer', contentType: 'application/json',
    parameters: [
      { name: 'model', type: 'string', required: true, description: '当前分组已配置的 Wan 视频模型 ID。' },
      { name: 'input', type: 'object', required: true, description: '厂商输入对象，例如 prompt、img_url、video_url；支持范围以当前模型为准。' },
      { name: 'input.prompt', type: 'string', required: 'conditional', description: '文本提示词，图生视频等操作是否必填由厂商决定。' },
      { name: 'parameters', type: 'object', required: 'conditional', description: '视频生成参数。本站优先读取此对象中的计费维度，不能用冲突的顶层字段覆盖。' },
      { name: 'parameters.duration', type: 'number', required: 'conditional', description: '生成时长（秒），按实际模型可选范围填写。' },
      { name: 'parameters.resolution / parameters.size', type: 'string', required: 'conditional', description: '分辨率按实际厂商字段填写，并确保与本站价卡匹配。' }, idempotency,
    ],
    requestExample: json({ model: 'wan-video-model', input: { prompt: '山间湖泊，云雾缓慢漂移' }, parameters: { duration: 5, resolution: '720p' } }),
    responseExample: json({ output: { task_id: 'wan_task_example', task_status: 'PENDING' }, request_id: 'upstream_trace_example' }),
    notes: [...nativeNotes, '网关向上游创建请求添加 X-DashScope-Async: enable。调用方仍只需使用本站 Bearer Key。', 'output.task_id 才是任务 ID；顶层 request_id 是追踪编号，不可用来轮询。本站未开放 Wan 原生取消端点。'], errors: videoErrors,
  },
  {
    id: 'wan-query', category: 'video', videoProtocol: 'wan', title: '万相 / Wan · 查询原生任务', summary: '读取 output.task_status 和完成视频地址。',
    platforms: ['video'], method: 'GET', path: '/api/v1/tasks/{task_id}', requestPath: '/api/v1/tasks/wan_task_example', auth: 'bearer', parameters: [taskId],
    responseExample: json({ output: { task_id: 'wan_task_example', task_status: 'SUCCEEDED', video_url: 'https://media.example.com/output.mp4' }, usage: { duration: 5 }, request_id: 'upstream_trace_example' }),
    notes: ['使用创建响应 output.task_id；典型状态 PENDING、RUNNING、SUCCEEDED、FAILED 以供应商响应为准。', '此原生路径不同于 GET /v1/tasks/{id}；后者接收本站 vid_… ID 并返回统一任务对象。', '供应商参数、结果和用量字段原样返回；账务可通过 X-Video-Task-ID 的统一查询或任务记录查看。'], errors: taskErrors,
  },
  {
    id: 'minimax-video-create', category: 'video', videoProtocol: 'minimax', title: 'MiniMax · 创建原生视频任务', summary: '按已配置 MiniMax 视频上游的 /v2 协议提交异步生成，不进入 MiniMax 文本分组。',
    platforms: ['video'], method: 'POST', path: '/v2/video_generation', auth: 'bearer', contentType: 'application/json',
    parameters: [
      { name: 'model', type: 'string', required: true, description: '当前 Video 分组允许且已有定价的模型 ID，例如管理员配置的 MiniMax-H3-Max。' },
      { name: 'content / prompt', type: 'array / string', required: 'conditional', description: '按实际 MiniMax 上游协议提供多模态 content 或 prompt；网关不转换两种表示。content 存在时参考视频与图片计数优先从该数组读取。' },
      { name: 'duration', type: 'number', required: 'conditional', description: '生成秒数，按模型限制填写，预扣预算读取该数字。' },
      { name: 'resolution', type: 'string', required: 'conditional', description: '如上游支持的 768p，必须与站内配置匹配；不会自动映射为 720p。' },
      { name: 'aigc_watermark / 其他字段', type: 'boolean / JSON', required: false, description: '模型支持的水印或媒体扩展字段，原样转发。' }, idempotency,
    ],
    requestExample: json({ model: 'MiniMax-H3-Max', content: [{ type: 'text', text: '小猫在雪地里追逐雪花' }], duration: 8, resolution: '768p', aigc_watermark: false }),
    responseExample: json({ task_id: 'minimax_task_example', base_resp: { status_code: 0, status_msg: 'success' } }),
    notes: [...nativeNotes, '这里记录本站已经注册的 /v2 协议，不承诺兼容任意 MiniMax 官方或第三方的其它版本路径。', '本站没有注册该协议的原生取消或文件检索 API。任务完成后使用响应中可用的产物链接；需要统一下载时可用 X-Video-Task-ID 对应的 /v1/videos/{id}/content，前提是本站已取得可读取的视频地址并完成结算。'], errors: videoErrors,
  },
  {
    id: 'minimax-video-query', category: 'video', videoProtocol: 'minimax', title: 'MiniMax · 查询原生视频任务', summary: '使用创建响应 task_id 查询生成状态，保留上游结果。',
    platforms: ['video'], method: 'GET', path: '/v2/query/video_generation/{task_id}', requestPath: '/v2/query/video_generation/minimax_task_example', auth: 'bearer', parameters: [taskId],
    responseExample: json({ task_id: 'minimax_task_example', status: 'Success', data: { video_url: 'https://media.example.com/output.mp4' }, base_resp: { status_code: 0, status_msg: 'success' } }),
    responseDescription: '这是支持直接视频地址的上游响应示例；实际状态大小写、结果字段和 usage 由所接入上游决定。',
    notes: ['同时检查 HTTP 状态、base_resp 等业务错误和生成状态。HTTP 200 本身不代表生成成功。', '若上游只提供私有 file_id，本站不会凭空合成下载链接；当前没有独立 MiniMax 文件下载网关接口，需管理员适配实际供应商的结果合同。'], errors: taskErrors,
  },
  {
    id: 'grok-video-create', category: 'video', videoProtocol: 'grok', title: 'Grok · 视频生成', summary: 'Grok / xAI 异步文生或图生视频，返回供应商任务 ID。',
    platforms: ['grok'], method: 'POST', path: '/v1/videos/generations', aliases: ['/videos/generations'], auth: 'bearer', contentType: 'application/json',
    parameters: [
      { name: 'model', type: 'string', required: true, description: '分组可用的 Grok 视频模型，例如 grok-imagine-video-1.5。' },
      { name: 'prompt', type: 'string', required: true, description: '视频内容描述，具体限制由上游模型决定。' },
      { name: 'duration / resolution / aspect_ratio', type: 'number / string / string', required: false, description: '使用 xAI 支持的时长、分辨率与比例，不把统一 Video 的 ratio 字段直接视为同义。' },
      { name: 'image / images / reference_images', type: 'object / array', required: false, description: '图生视频参考图，推荐使用 {url:"https://..."} 对象；历史 image_url 字段仍会规范化为 url。' },
    ],
    requestExample: json({ model: 'grok-imagine-video-1.5', prompt: '海浪缓缓拍打沙滩', duration: 8, resolution: '720p', aspect_ratio: '16:9' }),
    responseExample: json({ request_id: 'grok_task_example' }), responseDescription: '原生任务响应示例，实际 HTTP 状态及字段由上游返回。Grok 分组也可用 POST /v1/videos 或 /videos 创建。', notes: grokNotes, errors: grokErrors,
  },
  ...([
    { id: 'grok-video-edit', title: 'Grok · 视频编辑', path: '/v1/videos/edits', alias: '/videos/edits', prompt: '将视频调整为温暖的夕阳光线' },
    { id: 'grok-video-extend', title: 'Grok · 视频扩展', path: '/v1/videos/extensions', alias: '/videos/extensions', prompt: '继续向前移动镜头，展现海岸线' },
  ] as const).map((route): ApiDocEndpoint => ({
    id: route.id, category: 'video', videoProtocol: 'grok', title: route.title, summary: '向支持该操作的 Grok 上游提交原生异步视频请求。',
    platforms: ['grok'], method: 'POST', path: route.path, aliases: [route.alias], auth: 'bearer', contentType: 'application/json',
    parameters: [
      { name: 'model', type: 'string', required: true, description: '上游允许编辑或扩展的 Grok 视频模型。' },
      { name: 'prompt', type: 'string', required: 'conditional', description: '描述编辑效果或新增视频内容，是否必填取决于上游。' },
      { name: 'video', type: 'object', required: true, description: '按 xAI 实际模型协议提供原视频对象，例如 {url:"https://..."}；视频必须能被上游读取。' },
      { name: 'duration / 其他参数', type: 'number / JSON', required: false, description: '支持范围与字段遵循实际 Grok 操作，网关不把编辑和扩展转换为普通生成。' },
    ],
    requestExample: json({ model: 'grok-imagine-video-1.5', prompt: route.prompt, video: { url: 'https://media.example.com/input.mp4' } }),
    responseExample: json({ request_id: 'grok_task_example' }), notes: [...grokNotes, '查询可使用相应操作路径 /{request_id} 或 /v1/videos/{request_id}，内容下载在其后追加 /content。'], errors: grokErrors,
  })),
  {
    id: 'grok-video-query', category: 'video', videoProtocol: 'grok', title: 'Grok · 查询生成、编辑或扩展任务', summary: '保持原账号归属，查询原生 xAI 视频状态；首次成功结果可触发结算。',
    platforms: ['grok'], method: 'GET', path: '/v1/videos/generations/{request_id}', requestPath: '/v1/videos/generations/grok_task_example',
    aliases: ['/videos/generations/{request_id}', '/v1/videos/edits/{request_id}', '/videos/edits/{request_id}', '/v1/videos/extensions/{request_id}', '/videos/extensions/{request_id}'], auth: 'bearer',
    parameters: [{ name: 'request_id（路径）', type: 'string', required: true, description: '创建响应中的供应商 request_id/id/task_id，继续使用原 Key。' }],
    responseExample: json({ status: 'done', model: 'grok-imagine-video-1.5', video: { url: 'https://media.example.com/output.mp4', duration: 8 } }),
    notes: ['也支持共享 GET /v1/videos/{request_id} 和 /videos/{request_id}。典型 pending → done；真实状态与错误按上游返回。', ...grokNotes.slice(1)], errors: grokErrors,
  },
  {
    id: 'grok-video-content', category: 'video', videoProtocol: 'grok', title: 'Grok · 获取生成、编辑或扩展内容', summary: '通过原任务账号代理视频文件，支持 Range；首次观察到成功时可能完成结算。',
    platforms: ['grok'], method: 'GET', path: '/v1/videos/generations/{request_id}/content', requestPath: '/v1/videos/generations/grok_task_example/content',
    aliases: ['/videos/generations/{request_id}/content', '/v1/videos/edits/{request_id}/content', '/videos/edits/{request_id}/content', '/v1/videos/extensions/{request_id}/content', '/videos/extensions/{request_id}/content'], auth: 'bearer',
    parameters: [{ name: 'request_id（路径）', type: 'string', required: true, description: '供应商任务 ID。' }, { name: 'Range（请求头）', type: 'string', required: false, description: '可选字节范围，例如 bytes=0-1048575。' }],
    responseExample: 'HTTP/1.1 200 OK\nContent-Type: video/mp4\n\n<视频二进制内容>', responseLanguage: 'text',
    notes: ['也可使用共享 /v1/videos/{request_id}/content 或 /videos/{request_id}/content。返回媒体流，不是 JSON。', '接口会先确认原任务状态，再代理视频内容；上游凭据不会暴露给调用方。链接过期或任务绑定失效时可能无法下载，请及时保存产物。'], errors: grokErrors,
  },
]
