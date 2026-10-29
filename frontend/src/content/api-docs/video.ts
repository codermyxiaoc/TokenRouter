import type { ApiDocEndpoint, ApiDocError, ApiDocParameter } from './types'

// 独立 Video 平台只保留统一入口 /v1/videos；每个模型由管理员绑定唯一协议，供应商差异以统一字段与 extra 表达。
const json = (value: unknown) => JSON.stringify(value, null, 2)
const idempotency: ApiDocParameter = {
  name: 'Idempotency-Key（请求头）', type: 'string', required: false,
  description: '仅独立 Video 任务支持，最多 128 字符。同一 Key、相同路径和完全相同的原始正文返回已有任务；正文空格或键顺序改变也会触发 409。',
}
const taskId: ApiDocParameter = {
  name: 'task_id / video_id（路径）', type: 'string', required: true,
  description: '创建响应中的本站 vid_… 任务 ID。必须使用创建任务的同一站内 API Key。',
}
// 旧入口统一返回 410，客户端需迁移到 /v1/videos 三个接口。
const retiredVideoPaths = [
  '/v1/video/generations', '/v1/video/generations/{task_id}', '/video/generations', '/video/generations/{task_id}', '/v1/tasks/{task_id}',
  '/api/v3/contents/generations/tasks', '/api/v3/contents/generations/tasks/{task_id}',
  '/text-to-video/{model}', '/image-to-video/{model}', '/omni-video/{model}', '/v1/videos/text2video', '/v1/videos/omni-video', '/tasks',
  '/api/v1/services/aigc/video-generation/video-synthesis', '/api/v1/tasks/{task_id}',
  '/v2/video_generation', '/v2/query/video_generation/{task_id}',
  '/ent/v2/img2video', '/ent/v2/reference2video', '/ent/v2/text2video', '/ent/v2/start-end2video', '/ent/v2/tasks/{task_id}/creations',
]
const retiredNote = 'Video 分组 Key 调用旧视频入口（/v1/video/generations、/v1/tasks/{id}、方舟 /api/v3/contents/generations/tasks、Kling /text-to-video 等模型路径、Wan /api/v1/services/…、MiniMax /v2/video_generation、Vidu /ent/v2/*）一律返回 HTTP 410 VIDEO_ENDPOINT_RETIRED，请改用 POST /v1/videos、GET /v1/videos/{task_id} 与 GET /v1/videos/{task_id}/content。'
const protocolNote = '上游协议由管理员按模型绑定（内置协议或模板协议），调用方不选择端点；同一模型只对应一种协议。示例中的供应商差异全部用统一字段和 extra 表达，网关在受理前转换为该协议的请求。'
const videoErrors: ApiDocError[] = [
  { status: 400, code: 'VIDEO_MODEL_REQUIRED', description: '缺少 model。' },
  { status: 400, code: 'VIDEO_PARAMETER_UNKNOWN', description: '出现统一请求未定义的顶层字段；供应商专属参数请放入 extra。' },
  { status: 400, code: 'VIDEO_PARAMETER_CONFLICT', description: 'duration 与 seconds 不一致、images 与 input_reference 同时出现，或 extra 中的键与统一字段冲突。' },
  { status: 400, code: 'VIDEO_PARAMETER_INVALID', description: '字段类型、素材角色或数量不合法（如 duration 非正数、images 项缺少 url）。' },
  { status: 400, code: 'VIDEO_PARAMETER_UNSUPPORTED', description: '模型绑定的协议无法表达该参数（如对原生协议传 size、对 Wan 传多张图或参考视频）。在任何预扣和上游调用之前拒绝。' },
  { status: 400, code: 'VIDEO_PRICING_UNAVAILABLE', description: '模型或分辨率缺少匹配价卡与兜底价，生成不会继续。' },
  { status: 400, code: 'VIDEO_TASK_BUDGET_REQUIRED', description: '按秒预留或 Token 秒价预扣需要时长；自动时长请求需由管理员配置任务时长预留上限。' },
  { status: 409, code: 'VIDEO_TASK_CONFLICT', description: '幂等键对应了不同请求，或当前任务状态不允许该操作。' },
  { status: 410, code: 'VIDEO_ENDPOINT_RETIRED', description: '调用了已下线的旧视频入口，请改用 /v1/videos。' },
  { status: 415, code: 'VIDEO_CONTENT_TYPE', description: '创建独立 Video 任务必须使用 application/json，不接受 multipart。' },
  { status: 429, code: 'VIDEO_TASK_LIMIT', description: '账号在途任务数达到上限，请等待已有任务完成。' },
  { status: 503, code: 'VIDEO_NO_ELIGIBLE_ACCOUNT', description: '没有同时满足模型、协议绑定、分组和并发要求的视频账号。' },
  { status: '4xx / 5xx', code: '上游错误', description: '上游拒绝时返回错误体及 X-Video-Task-ID。不要对受理不明的生成请求直接重试。' },
]
const taskErrors: ApiDocError[] = [
  { status: 404, code: 'VIDEO_TASK_NOT_FOUND', description: '任务不存在，或不属于当前用户、API Key。' },
  { status: 409, code: 'VIDEO_TASK_CONFLICT', description: '任务状态不允许操作，无法安全确定任务。' },
  { status: 503, code: 'VIDEO_ACCOUNT_BUSY', description: '原任务账号当前繁忙，稍后查询；不会换账号查询或重新生成。' },
]
const billingNotes = [
  '按秒模式预留预计视频费；按次模式预留一次成功任务费。Token 模式可按模型配置先用独立秒价预扣，也可完成后结算。最终按任务创建时冻结的价卡、实际用量和适用倍率结算；参考图片附加费为固定金额。',
  '缺少价格会阻止提交，缺少可信 Token 用量会进入待核对，不能按零费用处理。完成、失败和退款请同时检查 status 与 billing_status。',
  '创作台可编辑的 1–60 秒和自定义分辨率不代表每个供应商模型都支持。API 示例中的型号也不授予权限；实际 model 使用当前 Key 的 /v1/models 或模型广场返回的名称，模型映射后的型号决定上游限制。',
  '创建完整收到 HTTP 4xx/5xx 且没有上游任务 ID 时，任务失败并释放预扣。新任务若创建超时且没有任务 ID 或持久响应证据，核对 15 分钟后按失败释放，不自动重发。已取得 ID、响应读取失败或缺少分类的任务仍需核对；查询错误不会自动退款。',
]
// 统一请求体字段，与服务端 ParseVideoCanonicalRequest 一致。
const unifiedVideoParameters: ApiDocParameter[] = [
  { name: 'model', type: 'string', required: true, description: '模型广场中当前分组可用的模型 ID，必须已有可用账号、协议绑定和匹配视频定价。' },
  { name: 'prompt', type: 'string', required: 'conditional', description: '视频描述；纯文生必须提供，图生是否必填由模型决定。' },
  { name: 'negative_prompt', type: 'string', required: false, description: '反向提示词；绑定协议不支持时返回 400。' },
  { name: 'duration', type: 'number', required: 'conditional', description: '时长（秒，正数）。OpenAI 别名 seconds 可为数字或数字字符串，两者同时出现必须一致。省略时按模型默认值，按秒预扣需要管理员配置预留时长。' },
  { name: 'resolution', type: 'string', required: 'conditional', description: '分辨率档位，如 480p、720p、768p、1080p；需与价卡匹配，不会自动降档。存在分辨率价卡时需要明确填写。' },
  { name: 'aspect_ratio', type: 'string', required: false, description: '画面比例，如 16:9、9:16、1:1、adaptive；由网关转换为供应商字段（ratio / aspect_ratio / settings.aspect_ratio）。' },
  { name: 'size', type: 'string', required: false, description: 'OpenAI 像素尺寸别名，如 1280x720；仅模型绑定 OpenAI 风格协议（compat / openai_videos）时可用，其它协议请使用 resolution 与 aspect_ratio。' },
  { name: 'images', type: 'array', required: false, description: 'URL 字符串或 {url, role} 对象，role 取 first_frame、last_frame 或 reference_image。OpenAI 别名 input_reference:{image_url} 等价于单张首帧，不能与 images 同时使用。最多 16 个素材。' },
  { name: 'videos', type: 'array', required: false, description: '参考视频，{url, role:"reference_video"} 或 URL 字符串；协议不支持参考视频时返回 400。' },
  { name: 'generate_audio / watermark / seed', type: 'boolean / boolean / integer', required: false, description: '可选通用参数，是否支持与默认值由模型和上游决定。' },
  { name: 'extra', type: 'object', required: false, description: '供应商专属参数，如 extra.omni_reference_task_type；键名不能与统一字段重复，协议不接受的键返回 400。' },
  idempotency,
]
const videosResponse = {
  id: 'vid_example', object: 'video', model: 'video-model', status: 'queued',
  created_at: 1750000000, billing_status: 'reserved', duration: 8, resolution: '720p',
}
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
const pollNote = '保存返回的 vid_… ID，用同一 Key GET /v1/videos/{task_id} 轮询；status=failed 时读取 error，已完成且 billing_status=settled 后可 GET /v1/videos/{task_id}/content 下载。'

// 供应商指南：同一 POST /v1/videos，差异只在模型绑定的协议与统一字段取值。
interface VendorGuide {
  id: string
  protocol: ApiDocEndpoint['videoProtocol']
  title: string
  summary: string
  body: Record<string, unknown>
  notes: string[]
  parameters?: ApiDocParameter[]
}
const vendorGuide = (guide: VendorGuide): ApiDocEndpoint => ({
  id: guide.id, category: 'video', videoProtocol: guide.protocol, title: guide.title, summary: guide.summary,
  platforms: ['video'], method: 'POST', path: '/v1/videos', auth: 'bearer', contentType: 'application/json',
  parameters: guide.parameters ?? unifiedVideoParameters,
  requestExample: json(guide.body),
  responseExample: json({ ...videosResponse, model: String(guide.body.model) }), responseStatus: 200,
  responseDescription: '本站统一任务响应示例；queued 只表示受理，后续 failed 也可能由上游参数校验产生。',
  notes: [...guide.notes, protocolNote, pollNote, ...billingNotes], errors: videoErrors,
})

// 可美 Seedance 2.5 的角色和比例规则用统一字段表达。
const kemeiSeedanceNotes = [
  '适用于模型绑定预置模板 profile:kemei-seedance 或 profile:kemei-compat 的可美 Seedance 2.5（含 doubao-seedance-2-5-260628）。',
  '2.5 使用 480p、720p 或 1080p；时长为 4–30 整数秒或 -1（模型选择时长）。-1 不是不计费：按秒预算或 Token 秒价预扣仍要求管理员配置足够的时长上限。不要把 2.0 的范围或本站面板上限套到 2.5。',
  '图片可以是供应商可访问的 HTTPS URL，或该供应商支持的图片 Data URL。只有字符串的单图默认作为首帧；角色有要求时使用 {url, role}，不要依赖图片数量猜测。',
  '看到 first-frame / first-last-frame 的比例错误时，检查图片角色并把 aspect_ratio 设为 adaptive。已确认失败后修正参数应使用新的幂等键；创建结果未知时先查原任务，不能直接重发生成。',
  '可美参考生在 extra.omni_reference_task_type 中使用 auto；视频编辑 edit 要求 aspect_ratio=adaptive、duration=-1，延长 extend 要求 aspect_ratio=adaptive。',
]

export const videoEndpoints: ApiDocEndpoint[] = [
  {
    id: 'video-create', category: 'video', videoProtocol: 'openai', title: 'Video · 创建视频任务',
    summary: '独立 Video 分组唯一的创建入口：POST /v1/videos 提交统一请求体，网关按模型绑定的协议转换。同名 Grok 入口仍使用 xAI 任务格式。',
    platforms: ['video', 'grok'], method: 'POST', path: '/v1/videos', aliases: ['/videos'], auth: 'bearer', contentType: 'application/json',
    parameters: unifiedVideoParameters,
    requestExample: json({ model: 'video-model', prompt: '一只小猫在雪地追逐雪花，电影镜头', duration: 8, resolution: '720p', aspect_ratio: '16:9', images: [{ url: 'https://media.example.com/first-frame.png', role: 'first_frame' }] }),
    responseExample: json(videosResponse), responseStatus: 200,
    responseDescription: '示例是 Video 分组的成功受理响应；Grok 分组通常返回 request_id，见 Grok 视频生成。受理成功只代表任务已创建。',
    notes: [
      protocolNote,
      '未知顶层字段、别名冲突以及绑定协议无法表达的参数都会在任何扣费之前返回 400；供应商专属参数放入 extra。',
      'OpenAI 兼容别名：seconds 等价 duration，input_reference.image_url 等价单张首帧；size 仅在 OpenAI 风格协议下使用。',
      retiredNote,
      '状态为 queued（排队）、in_progress（生成中）、completed（完成）、failed（失败）。受理不明或过期还会返回 task_status，不能把 in_progress 直接当成可重试。',
      '接口只接收 JSON；没有开放 GET /v1/videos 集合列表、multipart、remix 或取消接口。任务列表请使用站内任务记录页面。',
      ...billingNotes,
    ], errors: videoErrors,
  },
  {
    id: 'video-query', category: 'video', videoProtocol: 'openai', title: 'Video · 查询视频任务',
    summary: '用本站任务 ID 查询进度和账务状态。Grok 任务 ID 同路径仍由原 Grok 链路处理。',
    platforms: ['video', 'grok'], method: 'GET', path: '/v1/videos/{video_id}', requestPath: '/v1/videos/vid_example', aliases: ['/videos/{video_id}'], auth: 'bearer', parameters: [taskId],
    responseExample: json({ ...videosResponse, status: 'completed', billing_status: 'settled', completed_at: 1750000060, usage: { completion_tokens: 80770 } }),
    responseDescription: 'HTTP 200 表示查到任务；status=failed 仍可返回 HTTP 200，并附 error.code 和 error.message。seconds、size、duration、resolution、usage 仅在已知时返回，实际终态秒数用于按秒结算。',
    notes: ['billing_status=settled 表示已结算，released 表示预留已释放，reconciliation 表示待核对，reserved 表示预算仍预留。生成状态和账务状态应分开判断。', 'Video 任务有后台恢复轮询。重复查询不会重新生成。', retiredNote, ...grokNotes.slice(1)], errors: taskErrors,
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
    id: 'video-retired-endpoints', category: 'video', videoProtocol: 'openai', title: 'Video · 旧视频入口迁移（410）',
    summary: '旧统一入口与各供应商原生入口已对 Video 分组下线，返回 HTTP 410 VIDEO_ENDPOINT_RETIRED。',
    platforms: ['video'], method: 'POST', path: '/v1/video/generations', aliases: retiredVideoPaths.slice(1), auth: 'bearer', contentType: 'application/json',
    parameters: [],
    responseExample: json({ code: 'VIDEO_ENDPOINT_RETIRED', message: 'This video endpoint has been retired; use POST /v1/videos, GET /v1/videos/{task_id} and GET /v1/videos/{task_id}/content' }),
    responseStatus: 410,
    responseDescription: '所有旧入口都不再创建、查询或取消任务；已有 vid_… 任务请改用 GET /v1/videos/{task_id} 查询。',
    notes: [
      retiredNote,
      `已下线的路径：${retiredVideoPaths.join('；')}。`,
      '迁移方式：把原供应商请求改写为统一请求体后 POST /v1/videos。方舟 content、Wan input/parameters、Kling contents/settings、Vidu images 等结构由网关按模型绑定的协议生成；文生、图生或参考生的选择由管理员为模型绑定的协议与操作路径决定，同一模型不能按请求切换操作。',
      '旧 DELETE 取消接口同时下线，统一入口暂未开放取消。',
    ],
    errors: [{ status: 410, code: 'VIDEO_ENDPOINT_RETIRED', description: '旧视频入口已下线。' }],
  },
  ...([
    { id: 'kemei-seedance25-text', title: 'Seedance 2.5 · 可美文生视频', summary: '没有首帧图片时，按可美合同指定输出比例、时长与分辨率。', ratio: '16:9', images: [], reference: false },
    { id: 'kemei-seedance25-frames', title: 'Seedance 2.5 · 可美首帧与首尾帧', summary: '首帧控制画面，输出比例跟随首帧；aspect_ratio 必须为 adaptive。', ratio: 'adaptive', images: [{ url: 'https://media.example.com/first-frame.png', role: 'first_frame' }, { url: 'https://media.example.com/last-frame.png', role: 'last_frame' }], reference: false },
    { id: 'kemei-seedance25-reference', title: 'Seedance 2.5 · 可美参考图生成', summary: '明确使用 reference_image 参考角色，避免单张图片被识别成首帧。', ratio: '16:9', images: [{ url: 'https://media.example.com/character.png', role: 'reference_image' }], reference: true },
  ] as const).map(guide => vendorGuide({
    id: guide.id, protocol: 'seedance', title: guide.title, summary: guide.summary,
    body: { model: 'doubao-seedance-2-5-260628', prompt: '人物沿海岸缓步前行，镜头平稳跟随', ...(guide.images.length ? { images: guide.images } : {}), aspect_ratio: guide.ratio, resolution: '720p', duration: 5, generate_audio: true, ...(guide.reference ? { extra: { omni_reference_task_type: 'auto' } } : {}) },
    notes: [...kemeiSeedanceNotes, ...(guide.id === 'kemei-seedance25-frames' ? ['只用首帧时删除 last_frame 那一项，仍使用 adaptive。希望输出 16:9 时先将首帧裁剪为 16:9；把比例硬改成 16:9 不能代替裁剪。'] : [])],
  })),
  vendorGuide({
    id: 'seedance-official-create', protocol: 'seedance', title: 'Seedance · 官方协议生成',
    summary: '模型绑定 seedance.seedance_v1 时，网关把 prompt 与带角色的 images/videos 转换为方舟 content 数组。',
    body: { model: 'seedance-model', prompt: '小猫在雪地里追逐雪花', images: [{ url: 'https://media.example.com/first-frame.png', role: 'first_frame' }], duration: 5, resolution: '720p', watermark: false },
    notes: [
      '单张无角色图片按首帧处理，多张无角色图片按 reference_image 处理；参考视频使用 videos（role=reference_video）。',
      '官方 2.5 首帧/首尾帧应省略 aspect_ratio 或明确 adaptive；普通 reference_image 与纯文生不套用这一限制。官方 2.5 不支持 frames；2.0 Mini/Fast 的官方分辨率仅 480p/720p。以映射后的真实型号判断。',
      'OpenAI 分组的旧 Seedance 方舟原生入口仍保留，见“Seedance / 方舟 · 创建原生任务（OpenAI 分组）”；Video 分组 Key 调用方舟路径返回 410。',
    ],
  }),
  vendorGuide({
    id: 'video-yezonghui-create', protocol: 'openai', title: 'Video · Yezonghui 模型调用',
    summary: '模型绑定预置模板 profile:yezonghui 时，resolution 是档位、aspect_ratio 是比例，不使用 OpenAI 像素 size。',
    body: { model: 'configured-video-model', prompt: '人物在海边缓步前行', duration: 5, resolution: '720p', aspect_ratio: '16:9', images: ['https://media.example.com/first-frame.png'] },
    notes: [
      'resolution 必须为实际支持且可报价的档位（如 720p）；比例用 aspect_ratio 表达，自动比例省略该字段，不要把 1280x720 当作比例。',
      '参考素材只接受供应商可访问的 HTTP(S) 公共链接，不接受 Base64、Data URL 或需要登录的私有链接；各媒体能力依模型而定。创作台私有素材会通过受控短期链接交付，但该能力不向任意 API URL 开放。',
      '上游查询间隔至少 60 秒，频繁刷新不会加速生成；完成且已结算后即使完成响应没有公开 URL 也不应重新创建。本站没有为该方案开放取消。',
    ],
  }),
  vendorGuide({
    id: 'video-metaso-h3', protocol: 'minimax', title: 'MiniMax H3 · 秘塔兼容调用',
    summary: '模型绑定预置模板 profile:metaso-h3 时，可使用 OpenAI 像素 size 与单张参考图别名。',
    body: { model: 'MiniMax-H3', prompt: '人物沿海岸缓步前行', seconds: '5', size: '1280x720', input_reference: { image_url: 'https://media.example.com/first-frame.png' } },
    notes: [
      '1280x720、720x1280 对应 768p；1792x1024、1024x1792 对应 2k。此处是协议约定，不能改成 1366x768 或按短边推断价格；也可直接提供一致的 resolution 与 aspect_ratio。',
      '只支持一张参考图：使用 input_reference 或单项 images，不能两者同时提供。',
      '创作台的自动比例会依据首图方向选择横屏或竖屏；该方案单张原图超过本站 2 MiB 发送上限时提示手动换图，不自动压缩。',
    ],
  }),
  ...([
    { id: 'kling-text', title: 'Kling · 文生视频', path: '/text-to-video/{model}', images: [] as { url: string; role: string }[] },
    { id: 'kling-image', title: 'Kling · 图生视频', path: '/image-to-video/{model}', images: [{ url: 'https://media.example.com/first-frame.png', role: 'first_frame' }] },
    { id: 'kling-omni', title: 'Kling · 可美 Omni 视频', path: '/omni-video/{model}', images: [{ url: 'https://media.example.com/reference.png', role: 'reference_image' }] },
  ] as const).map(route => vendorGuide({
    id: route.id, protocol: 'kling', title: route.title,
    summary: `模型绑定 Kling 协议且操作路径为 ${route.path}；请求仍为统一 POST /v1/videos。`,
    body: { model: 'kling-video-model', prompt: '海边灯塔，镜头缓慢向前', ...(route.images.length ? { images: route.images } : {}), duration: 5, resolution: '720p', aspect_ratio: '16:9' },
    notes: [
      'Kling 文生与图生是不同操作：管理员为模型绑定 /text-to-video/{model} 或 /image-to-video/{model}，同一模型不能按请求切换。官方 kling_v1 只支持这两种路径；可美的文生、图生与全能视频（/omni-video/{model}）分别是预置模板 profile:kemei-kling-text、profile:kemei-kling-image、profile:kemei-kling-omni。',
      route.id === 'kling-image'
        ? '图生首帧必须提供且只能一张（role=first_frame），尾帧可选（role=last_frame）。'
        : route.id === 'kling-omni'
          ? '可美 Omni 的参考图使用 role=reference_image（转换为 refer_image），参考视频使用 videos（role=reference_video，转换为 feature_video，要求音频关闭）；不接受参考音频 URL。其它厂商参数放入 extra。'
          : '文生不带图片；其它厂商字段（如 camera_control）放入 extra，是否支持由具体上游决定。',
      '比例不能是 adaptive。duration、resolution、aspect_ratio 由网关写入 Kling settings，不代表每个 Kling 型号支持任意时长。',
    ],
  })),
  vendorGuide({
    id: 'wan-create', protocol: 'wan', title: '万相 / Wan · 生成视频',
    summary: '模型绑定 Wan 协议时，网关把统一字段转换为 input 与 parameters 嵌套结构。',
    body: { model: 'wan-video-model', prompt: '山间湖泊，云雾缓慢漂移', images: ['https://media.example.com/first-frame.png'], duration: 5, resolution: '720p' },
    notes: [
      '官方协议只支持单张首帧（无角色或 role=first_frame），不支持多图、尾帧或参考视频；可美方案的多参考输入以带角色的 images 表达，首尾帧不能混合普通参考素材。',
      '分辨率和时长必须符合具体 Wan 型号，不能照搬 Seedance 的范围。网关负责添加上游异步请求头。',
    ],
  }),
  vendorGuide({
    id: 'minimax-video-create', protocol: 'minimax', title: 'MiniMax · 生成视频',
    summary: '模型绑定 minimax.minimax_v2 或可美方案时，网关把 prompt 与 images 转换为 content。',
    body: { model: 'MiniMax-H3-Max', prompt: '小猫在雪地里追逐雪花', duration: 8, resolution: '768p', aspect_ratio: '16:9', watermark: false },
    notes: [
      'MiniMax-H3、H3-Max 及其它别名必须以实际账号模型为准；模型名相近不代表使用同一协议或支持同一时长、分辨率。',
      '官方 V2 必须有 resolution；按实际模型选择 768p 等合法档位，不能用 720p 替代缺失价格。不支持参考视频。',
      '可美 MiniMax 文生需要明确 aspect_ratio；图生保留首帧/尾帧/参考图角色，不擅自补 16:9。秘塔 H3 的像素 size 用法见“MiniMax H3 · 秘塔兼容调用”。',
    ],
  }),
  // Vidu 操作由模型绑定的路径决定，指南按操作给出统一请求示例。
  ...([
    { operation: 'img2video', title: '图生视频', images: ['https://media.example.com/first-frame.png'] },
    { operation: 'reference2video', title: '参考生视频', images: ['https://media.example.com/reference.png'] },
    { operation: 'text2video', title: '文生视频', images: [] },
    { operation: 'start-end2video', title: '首尾帧视频', images: ['https://media.example.com/first-frame.png', 'https://media.example.com/last-frame.png'] },
  ] as const).map(route => vendorGuide({
    id: `vidu-${route.operation}`, protocol: 'vidu', title: `Vidu · ${route.title}`,
    summary: `模型绑定 Vidu 协议且操作路径为 /ent/v2/${route.operation}；请求仍为统一 POST /v1/videos。`,
    body: { model: 'viduq3-pro', prompt: '镜头缓缓向前，展现安静的海岸线', ...(route.images.length ? { images: route.images } : {}), duration: 5, resolution: '540p', aspect_ratio: '16:9' },
    notes: [
      '操作由管理员为模型绑定的路径决定：img2video 恰好 1 张图、reference2video 至少 1 张、start-end2video 恰好 2 张（按首帧、尾帧顺序提供 URL）、text2video 不能带图。同一模型不能按请求切换操作。官方 Vidu 协议按顺序转发图片 URL，不接受 last_frame 角色；可美方案保留角色。',
      '示例按可美方案填写 5 秒、540p、16:9；这是可美默认值，不是所有 Vidu 型号的统一要求。官方与旧透传不继承可美默认值或 Q4 限制；可美方案中的 Q4 仅支持 img2video 和 reference2video。',
      'generate_audio 映射为 Vidu audio；参考音频、错峰模式等厂商参数放入 extra。上游官方 Token 鉴权由服务端处理，调用方只使用本站 Bearer Key。',
    ],
  })),
  // OpenAI 分组的旧 Seedance 方舟入口不属于独立 Video 平台，仍保留原生协议。
  ...([
    { id: 'ark-create', method: 'POST', title: 'Seedance / 方舟 · 创建原生任务（OpenAI 分组）', summary: 'OpenAI 分组 API Key 账号启用 Seedance 能力后，使用 Ark content 多模态数组提交任务。', path: '/api/v3/contents/generations/tasks' },
    { id: 'ark-query', method: 'GET', title: 'Seedance / 方舟 · 查询原生任务（OpenAI 分组）', summary: '取得方舟原生状态、产物和 usage；原生 ID 与创建 Key 必须匹配。', path: '/api/v3/contents/generations/tasks/{task_id}' },
    { id: 'ark-delete', method: 'DELETE', title: 'Seedance / 方舟 · 删除原生任务（OpenAI 分组）', summary: '向原任务账号提交原生删除请求，保留供应商状态及响应。', path: '/api/v3/contents/generations/tasks/{task_id}' },
  ] as const).map((route): ApiDocEndpoint => ({
    id: route.id, category: 'video', videoProtocol: 'seedance', title: route.title, summary: route.summary,
    platforms: ['openai'], method: route.method, path: route.path,
    ...(route.method === 'POST' ? { contentType: 'application/json' } : { requestPath: route.path.replace('{task_id}', 'cgt_example') }),
    auth: 'bearer',
    parameters: route.method === 'POST' ? [
      { name: 'model', type: 'string', required: true, description: '已配置的视频模型或方舟推理接入点 ID。' },
      { name: 'content', type: 'array', required: true, description: '非空多模态数组。文本为 {type:"text",text:"..."}，图片/视频等按模型原生格式提供。' },
      { name: 'duration / resolution / ratio', type: 'number / string / string', required: 'conditional', description: '按模型支持范围填写。' },
    ] : [taskId],
    ...(route.method === 'POST' ? { requestExample: json({ model: 'seedance-model-or-endpoint-id', content: [{ type: 'text', text: '小猫在雪地里追逐雪花' }], duration: 8, resolution: '720p', ratio: '16:9' }) } : {}),
    responseExample: route.method === 'DELETE' ? 'HTTP/1.1 204 No Content' : json(route.method === 'POST' ? { id: 'cgt_example' } : { id: 'cgt_example', status: 'succeeded', content: { video_url: 'https://media.example.com/output.mp4' }, usage: { completion_tokens: 80770 } }),
    ...(route.method === 'DELETE' ? { responseLanguage: 'text', responseStatus: 204 } : {}),
    notes: [
      '仅适用于 OpenAI 分组且显式启用 Seedance 能力的 API Key 账号；Video 分组 Key 调用该路径返回 410 VIDEO_ENDPOINT_RETIRED，请改用 POST /v1/videos（见“Seedance · 官方协议生成”）。',
      '旧 OpenAI Seedance 任务需客户端在归属有效期内主动查询：成功状态 succeeded 且 usage.completion_tokens>0 才按输出 Token 计费；归属及待结算快照约保留 24 小时。',
    ],
    errors: [{ status: 404, code: 'TaskNotFound', description: '任务不存在或归属不匹配。' }, { status: 503, code: 'TaskStorageUnavailable', description: '已创建任务但归属存储失败，响应携带任务 ID；保留 ID 联系管理员，不可再次提交生成。' }],
  })),
  {
    id: 'grok-video-create', category: 'video', videoProtocol: 'grok', title: 'Grok · 视频生成', summary: 'Grok / xAI 异步文生或图生视频，返回供应商任务 ID。',
    platforms: ['grok'], method: 'POST', path: '/v1/videos/generations', aliases: ['/videos/generations'], auth: 'bearer', contentType: 'application/json',
    parameters: [
      { name: 'model', type: 'string', required: true, description: '分组可用的 Grok 视频模型，例如 grok-imagine-video-1.5。' },
      { name: 'prompt', type: 'string', required: true, description: '视频内容描述，具体限制由上游模型决定。' },
      { name: 'duration / resolution / aspect_ratio', type: 'number / string / string', required: false, description: '使用 xAI 支持的时长、分辨率与比例。' },
      { name: 'image / images / reference_images', type: 'object / array', required: false, description: '图生视频参考图，推荐使用 {url:"https://..."} 对象；历史 image_url 字段仍会规范化为 url。' },
    ],
    requestExample: json({ model: 'grok-imagine-video-1.5', prompt: '海浪缓缓拍打沙滩', duration: 8, resolution: '720p', aspect_ratio: '16:9' }),
    responseExample: json({ request_id: 'grok_task_example' }), responseDescription: '原生任务响应示例，实际 HTTP 状态及字段由上游返回。Grok 分组也可用 POST /v1/videos 或 /videos 创建。', notes: [...grokNotes, '单图示例可增加 image: {url:"https://media.example.com/first-frame.png"}；不要使用独立 Video 的 images 角色协议替代 xAI 字段。', '本项目创作台的 Grok 视频范围为 1–15 整数秒和 480p/720p/1080p；其它 API 参数仍须被实际 Grok 模型及本站计价支持，不把独立 Video 的 1–60 秒或 Seedance 2.5 的 30 秒直接照搬。'], errors: grokErrors,
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
