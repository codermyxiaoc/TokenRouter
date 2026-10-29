# 云端无限画布接口

> 上级目录：[接口文档目录](index.md)；领域约束：[云端画布](../domains/cloud_canvas.md)

## 认证和资源归属

以下接口均要求登录后的用户 JWT，并且所有查询、更新、删除和素材读取都以认证用户作为资源拥有者。客户端不能通过路径参数访问其他用户的画布、素材或执行记录；服务端返回统一错误，不泄露资源是否存在。

## 画布和素材

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `GET` | `/api/v1/creative/canvases` | 分页列出当前用户的画布 |
| `POST` | `/api/v1/creative/canvases` | 创建画布，提交 `title` 和受限 `document` |
| `GET` | `/api/v1/creative/canvases/{id}` | 读取画布和 revision |
| `PUT` | `/api/v1/creative/canvases/{id}` | 按 `revision` 乐观锁更新标题和文档 |
| `DELETE` | `/api/v1/creative/canvases/{id}` | 删除画布元数据 |
| `POST` | `/api/v1/creative/assets` | 上传素材，使用 `multipart/form-data` 的 `file`、`canvas_id` 字段 |
| `GET` | `/api/v1/creative/assets?canvas_id={id}` | 分页列出当前画布素材 |
| `GET` | `/api/v1/creative/assets/{id}` | 读取当前用户私有素材元数据 |
| `GET` / `HEAD` | `/api/v1/creative/assets/{id}/content` | 读取私有素材内容 |
| `DELETE` | `/api/v1/creative/assets/{id}` | 删除当前用户素材 |

文档只允许画布节点、位置、尺寸、连接和受控参数；脚本、远程 URL、凭据、外部插件和任意 HTML 会被拒绝。上传文件按魔数校验 MIME，图片还须完整解码后才能标记就绪，不能通过文件名或合法文件头伪装可用图片。素材保存期由管理员配置 `creative_canvas_asset_retention_hours`（默认 168 小时，允许整数 1–8760）；仅作用于新上传和新归档，旧素材保留原 `expires_at`。到期立即不可读，后台每周清理已过期物理文件；元数据保留为过期记录，便于画布显示和审计。公共设置接口返回当前保存小时数供画布库提示，实际单张素材仍以自身到期时间为准。

## 模型和执行

目录支持 `media_type=image|text|video|audio`。文本项由管理员按分组和模型开启，只返回可调用并有明确 Token 价格的候选，`operations=[generate]`。`text_capabilities` 返回 `max_input_chars=8000`、`max_output_tokens=4096`、`default_max_output_tokens=1024`、`max_reference_images=0`。`kind=text` 的单执行使用 `group_id`、`model`、`prompt`、可选 `operation=generate` 与 `max_output_tokens`；本期不接受媒体参考或图片/视频/音频专用参数。

文本执行通过 `output_text` 返回纯文本结果；它与执行记录一起受用户归属校验，不是公开素材 URL。`CREATIVE_TEXT_SUBMISSION_UNCONFIRMED` 表示受理核对，`CREATIVE_TEXT_SETTLEMENT_PENDING` 表示已返回但用量结算仍在确认；两者保持 `running`，不能据此创建新任务。账务证据和资金回执只在后端保存，不出现在用户执行响应。相同幂等键仍只恢复原参数和原执行，不再次请求文本模型。

`GET /api/v1/creative/models` 返回管理员在创作台设置中启用的图片、文本、视频和音频模型。视频条目返回端点与 `video_capabilities`。独立 Video 平台的 `durations` 为 1–60 整数秒，`resolutions` 为 480p/720p/1080p 与原配置档位的并集，`aspect_ratios` 为常见比例、`adaptive` 与原配置的并集；`allow_custom_resolution` 为 true，允许数字 p 分辨率（1–65536）。比例必须来自 `aspect_ratios`，不接受任意自由比例。`default_duration` 保留原时长最小值，`default_resolution`、`default_aspect_ratio` 保留原配置首项。上述扩展仅在运行时派生，不回写设置；参考图数量限制、模型权限和端点校验不变。旧目录未声明自由输入能力时，前端仍按其原白名单处理。图片条目的 `output_count` 继续表示单任务固定输出数，`output_counts` 表示画布可选总张数：普通模型为 1–8，MJ 为 4/8。

Grok 视频条目为 `media_type=video`、`video_endpoint=grok`、`operations=[generate]`。其运行能力单独限定 `durations=1–15` 整数秒、`resolutions=[480p,720p,1080p]`，`allow_custom_resolution=false`；比例为常见预设、`adaptive` 与已配置合法预设的并集，不开放自定义比例。这与原 Grok 计费的精确范围一致，保存和提交都拒绝范围外的参数，不静默截成 15 秒或按 480p 报价。创建仍使用 `kind=video`，`params` 携带 `group_id`、请求模型名、`endpoint=grok`、提示词、时长、分辨率、比例和最多一个私有图片引用；不接受 Grok 视频编辑/扩展、视频或音频参考，也不接受 `generate_audio` 或 `watermark` 参数。自动比例 `adaptive` 在 Grok 上游请求中省略 `aspect_ratio`。用户通过统一执行和私有素材接口查询、播放与下载，不直接使用上游 URL 或凭据。

音频条目为 `media_type=audio`、`model=grok-voice-tts`、`operations=[generate]`，固定一个输出。`audio_capabilities` 返回 `voices=[eve,ara,rex,sal,leo]`、`formats=[mp3,wav]`、`speed_min=0.7`、`speed_max=1.5`、`default_voice=eve`、`default_format=mp3`、`default_speed=1`、`default_language=auto` 和 `max_input_chars=15000`。候选与目录同时受管理员白名单、用户分组权限和 Grok Voice 账号能力约束。

`kind=audio` 的单执行 `params` 使用 `group_id`、`model`、`prompt`，以及可选 `operation=generate`、`voice`、`audio_format`、`speed`；声音、格式和语速分别默认 Eve、MP3、1。文本在服务端去首尾空白后按 Unicode 字符数限制，非空且不超过 15000。媒体引用、图片参数、视频参数和图片批次参数不适用于 TTS；没有任意脚本、声音指令或客户端上游密钥字段。站内模型名只用于目录与任务，上游 `/tts` 实际发送 `text`、`voice_id`、`language=auto`、`speed`、`output_format.codec`，不发送 `model`。

自定义视频参数经现有协议适配后按实际分辨率、时长和素材计数报价，不隐式降档或替换参数；模型或协议不支持以及缺价时由原任务链路拒绝。界面允许输入不代表供应商保证支持，已受理任务继续使用创建时的参数与冻结价卡。

`video_capabilities.max_reference_image_bytes` 为可选的本站单张参考图大小上限（字节）。仅当模型/端点的可用候选全部使用秘塔 H3 适配时返回 `2097152`；省略不表示任意上游保证支持大图。前端仅对有可信大小信息、实际将发送的参考图提前提示，未知大小由后端检查。H3 新画布请求超限返回稳定错误码 `CREATIVE_VIDEO_REFERENCE_TOO_LARGE`，用户需手动更换较小参考图；原素材不修改，原视频任务未创建且没有预扣。该策略不应用于正式 API 或已有执行的恢复。

画布请求选择自动比例时，只有实际账号使用秘塔 H3 适配才按已校验参考图方向解析成 16:9 或 9:16；无参考图则使用合法的模型默认比例或 16:9。解析提示只存在于服务端内部，浏览器不能伪造；普通 API 与其他适配的自动比例行为不变。报价与预扣仍使用实际转换的尺寸，旧任务恢复不重算或重新提交。

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `POST` | `/api/v1/creative/executions` | 创建单执行，正文包含 `canvas_id`、`node_id`、`kind`、`params`；须带 `Idempotency-Key` |
| `POST` | `/api/v1/creative/execution-batches` | 原子登记图片批次，正文包含 `canvas_id`、`node_id`、`kind=image`、总张数 `count`、`params`；须带 `Idempotency-Key` |
| `GET` | `/api/v1/creative/executions?canvas_id={id}` | 分页读取当前画布执行 |
| `GET` | `/api/v1/creative/executions/{id}` | 查询单个执行和输出素材 |

同一用户、同一画布和同一幂等键只能创建一个执行；不同请求体使用同键返回冲突。图片执行复用创作台原有 provider、预扣、扣费和退款账本；独立 Video 视频复用视频任务服务及其预扣结算，Grok 视频复用正式 Grok 网关与 `LegacyVideoRuntime` 的完成后结算，画布层不重复记账。任务恢复先查已有发送栅栏和上游任务关联，不重发未知受理请求；删除画布不会取消已经提交的上游任务，也不会绕过既有结算流程。

新执行登记沿用每用户 2 GiB 的素材与在途任务预算，视频子执行预留 512 MiB，其它子执行预留 128 MiB。已有任务预留导致暂时无法接收整批时返回 HTTP `429 CANVAS_EXECUTION_LIMIT`，提示等待已有任务完成；实际素材加本次所需预算本身已经超限时仍返回 `413 CANVAS_STORAGE_LIMIT`。拒绝发生在执行登记和上游提交前，使用预扣的媒体也不会先冻结费用；不留下半批任务、不自动重发，同一幂等键的已受理任务仍优先恢复，不受新的容量检查阻断。

音频执行复用创作台任务引擎及其资金账本，声音、格式、语速与文本进入参数指纹。同步上游返回不明确时以 `running` 和 `CREATIVE_AUDIO_SUBMISSION_UNCONFIRMED` 提示核对，不能另建请求或直接当作已退款；重启恢复保留提交栅栏，不盲目重发。成功结果通过同一私有素材接口归档、播放和下载。

视频执行的 `status=running` 不一定表示上游正在生成。`error_code` 可为 `CREATIVE_VIDEO_SUBMISSION_UNCONFIRMED`、`CREATIVE_VIDEO_SETTLEMENT_PENDING`、`CREATIVE_VIDEO_RELEASE_PENDING` 或 `CREATIVE_VIDEO_RECONCILIATION`，分别表示受理待确认、结算待处理、预扣释放待处理和其它任务费用核对；`error_message` 提供安全说明。这些非终态提示不表示任务失败或已经退款，客户端继续按原执行轮询，不能另建任务。正常进度和终态会更新或清除提示，终态判断仍以 `status` 为准。

Grok 视频复用受理、结算与账务核对状态，但没有独立 Video 的预扣释放阶段。原任务失败且已确认 `not_billable` 时返回 `CREATIVE_GROK_VIDEO_FAILED` 和未扣费说明；原任务完成且 `settled` 后才归档并返回成功。完整恢复与原身份约束见[Grok 视频执行与恢复](../domains/cloud_canvas.md#grok_video_execution)。

图片批次返回 `{batch_id, executions}`，批次 ID 为该请求幂等键。每个子执行的 `params` 包含服务端生成的 `batch_id`、从零开始的 `batch_index`、子任务总数 `batch_size` 和总输出数 `batch_output_count`，不允许客户端伪造这些内部字段。一个 MJ 四图任务对应一个子执行，八图对应两个。服务端按用户锁检查整批容量和幂等后一次提交，失败不会留下半批；响应丢失后使用原参数与原键可恢复整批，修改张数会返回冲突。轮询按批次键与节点 ID 聚合，成功图分别归档，失败子任务按原账务规则释放。

<a id="video_model_settings"></a>
### 视频模型设置

`creative_model_settings` 的每个视频配置可带 `usage_notes` 使用说明，按“分组 + 请求模型”独立保存到原设置 JSON。字段为纯文本，最多 2000 个 Unicode 码点，统一 CRLF/CR 为 LF、去除首尾空白并保留正文换行；除换行和 Tab 外的控制字符及非法 UTF-8 拒绝保存。缺省、空字符串或全空白表示不展示说明，保存空值可清除旧说明；非视频条目不接受非空说明。该字段只用于用户提示，不参与生成正文、能力校验、任务指纹或计费，不能通过说明放宽模型限制。

`GET /api/v1/creative/models` 对当前用户可用的独立 Video 与 Grok 视频条目返回已配置的 `usage_notes`；空值省略，旧客户端和旧配置保持兼容。模型未开放、分组无权限或功能关闭时，说明与模型一并不可见；候选接口不按模型名或别名猜测、生成说明。前端按纯文本显示，不执行 HTML 或 Markdown。

管理员视频模型候选保持请求模型名（包括账号映射别名），按模型在账号上绑定的协议归类：内置协议返回其端点，绑定声明式模板的模型返回 `unified`；同一模型在不同账号绑定不同协议时列出多个端点，并返回首个端点的 `video_endpoint`、`video_capabilities` 作为表单实际初始值。画布提交时不再按端点拼装请求，统一请求由所选账号的协议转换；这些值仅为端点默认参数，不保证供应商支持。保存视频设置时，缺省或空的时长、分辨率、比例集合分别补齐所选端点的既有默认值，保留已填写参数顺序与显式 `max_reference_images=0`。非法非空参数仍拒绝，错误标明分组和模型。端点、账号映射、价格和生成请求的校验不因默认补齐而放宽；切换端点不能套用另一个端点的候选默认值。

`GET /api/v1/admin/settings/creative-model-candidates` 为符合条件的 Grok 分组返回视频候选，固定 `platform=grok`、`media_type=video`、`video_endpoint=grok` 和 `video_endpoints=[grok]`，默认 5 秒、720p、16:9、最多一张参考图。候选要求分组处于启用状态并开放媒体生成、存在已知具备媒体资格的可调度 OAuth/API Key 账号、映射后的最终模型明确属于 Grok 视频且价格可解析；账号/渠道模型限制、别名和定价歧义检查继续生效。候选扫描只读现有资格，不发起付费生成或上游资格探测。运行能力按 Grok 专用的 1–15 秒和三个固定分辨率派生，不把表单默认值当作上游能力保证，也不套用独立 Video 的自定义扩展。

`grok` 只属于创作台 Grok 分组；保存时不能给 Grok 分组配置独立 Video 端点，也不能给独立 Video 分组配置 `grok`。该值不扩展独立 Video 账号的端点枚举。前端根据服务端 `media_type` 展示视频参数，不通过模型名称自行判定媒体类型。

## 前端交付

`/canvas-app/index.html` 是与 Vue 宿主同源的 React 子应用。宿主只向 iframe 发送当前用户的运行时 JWT，子应用不把令牌写入 localStorage、sessionStorage 或画布文档。父子窗口同时校验消息来源和消息发送方，登出、切换用户或组件卸载时清理内存会话和对象 URL。

<a id="video_reference_delivery"></a>
## 视频供应商的短期参考图交付

仅要求公网 HTTP(S) 图片链接的视频协议可在原有素材归属校验后，通过 `VideoReferenceService` 将图片内容转换成单图短期能力票据。管理员测试也复用此服务；普通视频方案继续沿用原有图片传递方式。该服务只接收调用方已获授权的 PNG、JPEG、WebP 数据，不接受文件路径、资产 ID 或远程 URL，不下载任意目标，也不改变私有原图或桶权限。

票据读取路径为 `GET /api/v1/video-references/{ticket}` 和 `HEAD /api/v1/video-references/{ticket}`。没有对浏览器开放的票据签发、列表或批量读取接口。随机 256 位票据只授权对应图片，Redis 键只保存票据摘要；URL 不携带面板 JWT、用户 API Key 或供应商凭据。所有内容响应禁止缓存，并设置 `nosniff`、`no-referrer` 和隔离 CSP。

绝对 URL 基址只读取后台前端基础 URL（`frontend_url`，未设置时回落到 `server.frontend_url`），不使用请求 Host 或代理转发头。必须配置可信、上游可访问的 HTTP(S) 公网地址，拒绝空地址、localhost、内网地址字面量、内部主机名、URL 凭据、查询参数和片段。即使前端基础 URL 带页面路径，交付链接也只取其协议、主机与端口，使用本站固定的根路径 `/api/v1`；实际 DNS、公网路由与反向代理可达性由部署保证。未配置、Redis 不可用、素材无效或配额不足时，应在创建上游任务和预扣前拒绝，不以提交失败再退款代替前置检查。

临时原图与读取次数存在共享 Redis 中，应用重启和多实例可继续读取；此功能要求所有实例使用同一 Redis，Redis 数据丢失或被淘汰时链接失效，不会重新调用生成接口。固定有效期为 2 小时，GET 与 HEAD 共用最多 32 次原子读取预算，读取不延长有效期。每次最多 16 张、合计 32 MiB；每个可信用户身份每自然小时最多签发 64 张、64 MiB。整批先验证、再原子检查配额并签发，拒绝时不留下部分公开图片。到期自动清除 Redis 临时副本；原画布素材仍按原七天规则管理。
