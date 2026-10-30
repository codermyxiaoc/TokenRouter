# Grok / xAI 上游

TokenRouter 支持 Grok OAuth 订阅账号和标准 xAI API Key 账号，并通过 OpenAI 兼容的 Responses、Chat Completions、Messages 和 WebSocket 入口转发请求。Grok 分组还支持图片生成/编辑、视频生成/编辑/扩展、视频状态查询、原生搜索和 Voice API。

本文覆盖账号凭据、聊天/媒体转发、媒体资格、异步视频归属、模型目录和运行时变量，不定义 xAI 套餐价格，也不把上游当前返回的所有动态模型固化为兼容承诺。

## 章节导航

- [基本信息](#基本信息)：修改路由或默认上游时读取。
- [账号配置](#账号配置)：修改 OAuth/API Key 导入与刷新时读取。
- [管理员连接测试](#管理员连接测试)：修改测试模式、素材限制或结果预览时读取。
- [客户端协议](#客户端协议)：修改 Responses、Chat 或 Messages 准入时读取。
- [媒体请求格式](#媒体请求格式)：修改图片/视频 body 转换时读取。
- [媒体账号资格](#媒体账号资格)：修改付费探测和调度隔离时读取。
- [搜索与语音](#搜索与语音)：修改搜索、TTS、STT、自定义 Voice 或 Realtime 时读取。
- [任务归属与结算](#任务归属与结算)：修改视频查询、下载或用量记录时读取。
- [客户端配置](#客户端配置)：核对生成给客户端的 base URL。
- [默认模型目录](#默认模型目录)：修改内置模型与别名时读取。
- [环境变量](#环境变量)：修改 OAuth 或上游运行参数时读取。

## 基本信息

- 平台名：`grok`
- 账号类型：OAuth 订阅账号、API Key 账号
- 主要网关入口：`/v1/responses`、`/responses`、Chat Completions、Messages、Responses WebSocket、`/v1/web_search`、`/v1/x_search`、`/v1/tts`、`/v1/stt`、`/v1/custom-voices` 和 `/v1/realtime`；Voice/搜索也提供不带 `/v1` 的同名入口
- API Key 账号默认上游地址：`https://api.x.ai/v1`

## 客户端协议

Grok 分组支持 Anthropic Messages、OpenAI Responses 和 Chat Completions，新建时默认启用 Responses 与 Chat；三项都可关闭，迁移前已有分组启用三项。文本协议禁用时会在账号选择、计费、重试和 fallback 前返回协议原生 `403`。

Grok Responses 上游可能注入严格客户端不认识的 `event: ping` 帧。流式转发会把 data 未声明冲突事件类型的 ping 改写为 `: ping` SSE 注释，既保留连接活性，也避免中断 Grok CLI 或 Codex CLI；普通事件、未知字段帧和终止用量事件保持原样进入公共流处理链路。

标准 Grok Responses 流若仅有前导和完全不含输出、用量、错误的成功终态，按静默拒绝触发输出前故障转移，不写成零用量成功记录；已有语义输出、usage 或明确错误的终态继续原有处理。

Responses WebSocket 是 Grok/OpenAI 的原生传输能力，不由兼容 Responses 开关扩展到其它平台。图片和视频继续使用独立媒体资格与分组策略，不受文本协议集合直接控制。

<a id="grok_account_contract"></a>
## 账号配置

管理员可在控制台选择 OAuth 或 API Key 创建账号。OAuth 账号可通过浏览器授权、refresh token 或 SSO cookie 创建和重新授权；创建 Grok 分组并绑定账号后，用户即可生成分组 API Key。OAuth state 和 PKCE 会话优先保存在 Redis，并通过一次性消费标记阻止多实例重复兑换；Redis 写入失败时才使用进程内短期回退。SSO cookie、邮箱密码等临时输入只能用于兑换 Build OAuth token，不能写入账号凭据、响应或日志。

SSO 设备同意页的隐藏 `consent_token` 在本次流程内提取并随批准请求提交，同时发送已验证 xAI 同意页的 Origin/Referer。旧版无令牌页面仍可兑换；跨域重定向继续逐跳校验，不把临时会话信息带到非受信任主机。

Grok CLI 最终传输身份与账单/用量查询统一使用 `grok-pager`、`interactive`、认证响应标记和包含当前系统/架构的 pager/shell User-Agent。身份仅在精确的 CLI 代理主机写入；回退到公开 API 时清除 CLI 专用头，不改变管理员指定 Base URL。

邮箱密码授权由进程配置 `gateway.grok.password_auth_enabled` 控制，默认关闭且管理端不展示入口。即使显式开启，服务也只接受密码到 SSO、再到 OAuth token 的临时转换。成功重新授权会清除 Grok 的软性消费上限重新授权标记，并以凭据快照/CAS 规则更新账号，避免旧请求覆盖新 token。

账号未保存显式 base URL 时，数据库运行时设置 `grok_default_base_url_mode` 决定文本请求使用 CLI 代理、公共 API 或三个区域 API；账号显式端点始终优先。`grok_default_text_model` 作为需要默认文本模型的请求及 Claude Messages 映射的目标；`grok_cross_client_model_map_enabled` 开启后，Grok 分组的 Anthropic Messages 派发会将 Claude 模型 ID 映射到该目标，不影响 Responses 或 Chat Completions 中的其他模型。三项设置热更新运行时映射快照，不能把媒体模型继承为文本价格或文本默认模型。

其它通用账号类型即使可由兼容导入层保存，也没有 Grok 正式凭据和转发契约；`cosy` 明确只属于 Qoder。完整分类见[上游账号能力矩阵](upstream_account_matrix.md)。

Grok 使用 OpenAI 兼容能力适配层，但高级调度器由分组而不是平台全局开关决定。最终目标 Group 的 `scheduler_type=advanced` 时，Grok 在既有模型、媒体、账号状态、配额和 transport 硬过滤后复用通用 Top-K 评分；`basic` 保持原有选择顺序。Grok 的请求/令牌额度快照、媒体付费资格和 HTTP bridge 仍是平台专属资格，不能因通用评分缺少这些可选信号而被错误排除。

OAuth 访问令牌 JWT 的数字或字符串 `tier` 是账号档位的首选信号；刷新后新 JWT 有声明时覆盖旧凭据，没有声明时才保留已有值。账单月限额可进一步确认已知付费档位。供应商含糊的 `SuperGrokPro` 只有在 24 小时内观察到 `grok-4.5` Responses 的 Heavy 请求/令牌窗口时才显示为 Heavy，其他模型的新快照会延续但不会刷新该信号；过期或来自其它模型的窗口不能升级档位。明确的 Free、SuperGrok Lite、SuperGrok Plus 和 Heavy 信号不走这条推断。管理端账号徽章和用量条都使用同一规范档位，并在额度快照变化后刷新。

## 媒体请求格式

JSON 图片编辑和视频生成请求可在 `image`、`images`、`reference_images` 与 `mask` 对象中提供参考图片。与 xAI 直接兼容的请求应使用 `url` 字段；历史 `image_url` 字段仍可使用，TokenRouter 会在转发前把它规范化为 `url`。如果两者同时存在，则保留非空的 `url`；空白 `url` 会回退使用 `image_url`。multipart 图片编辑中的上传文件也会转换为 `url` 形式的 data URL。

创作台的 Grok 图片 `edit` 使用 xAI 官方 JSON `POST /v1/images/edits`，而不是 OpenAI 风格 multipart：单图请求使用 `image: {"type":"image_url","url":"data:image/png;base64,..."}`，多图请求使用 `images` 数组，最多 3 张源图；请求保留 `model`、`prompt`、`resolution`、`aspect_ratio`，并设置 `response_format: "b64_json"`，响应从 `data[].b64_json` 解析为创作台输出。`generate` 仍使用 `/v1/images/generations`。

创作台 Grok 视频使用专用目录端点 `grok`，通过进程内私有桥调用原正式视频生成路径；参考图来自已鉴权的画布私有素材，最多一张，以 `image: {"type":"image_url","url":"data:..."}` 发送。模型、提示词、时长和分辨率沿用正式参数，明确比例发送为 `aspect_ratio`，画布自动比例则省略该字段。该入口只提供视频生成，不扩展为编辑或延长；候选、管理员配置与参数约定见[画布视频模型设置](cloud_canvas.md#video_model_settings)。

## 媒体账号资格

新的 Grok 图片或视频生成请求会执行媒体专用的账号资格检查。API Key 账号保持可用；OAuth 账号必须由 xAI 计费探测提供明确的付费资格证据。Free、禁止访问、缺少观测、观测格式错误或结论不明确的 OAuth 账号都不会承接新的媒体生成请求。尚无观测的 OAuth 账号会在第一次转发媒体请求前执行探测，导入账号时也会主动先执行计费探测。聊天请求和已有视频任务的状态查询不受这项隔离影响。当分组中没有合格账号时，媒体端点返回 HTTP `503`，错误类型为 `grok_media_no_eligible_account`。

管理员可通过账号创建或更新 API 的 `extra.grok_media_eligible` 覆盖自动判定：`false` 表示排除，`true` 表示强制允许；更新时传 `null` 可删除覆盖并恢复基于探测结果的自动判定，省略该字段则保留现有覆盖。仅出现每周用量周期不能作为付费层级证据。图片接口返回成功时必须包含至少一个实际图片输出；空的 HTTP `200` 响应会触发账号 failover，不会作为成功生成结果计数或返回。

Grok 兼容账号对所选端点返回 HTTP `405` 时，表示该账号不支持当前端点；在尚未向客户端输出内容时请求会切换到其它账号，非池模式账号同时临时排除 30 分钟，避免粘性会话反复命中。公共池账号继续跳过默认账号冷却，`405` 也不会被误记为模型级冷却。

媒体错误分类中的硬额度或账户资格不足优先于池模式重试配置，不能因 pool 允许该状态码而在同一账号反复重试；请求级瞬时容量限制仍保留原有有界同号重试。视频创建完整错误响应若已有任务 ID，先保存原账号归属并返回该错误，不再换号；仅明确拒绝且无 ID 的创建允许按既有故障策略恢复，受理不明、通用错误与传输中断不重发生成。

## 搜索与语音

`POST /v1/web_search` 和 `POST /v1/x_search` 只允许 Grok 分组，接收查询或 `input` 和最多 20 条结果。两者在选择账号前进入共同内容审计，随后复用常规 Grok 账号资格、并发等待和最多四次账号尝试。`web_search` 使用原生 Responses `web_search` 工具；`x_search` 强制使用 `x_search`，并接受 `allowed_x_handles`、`excluded_x_handles`、`from_date`、`to_date` 及图片/视频理解开关。两者只返回实际工具来源 URL 对应的结果，分别以 `grok-web-search` 和 `grok-x-search` 记录计费模型。每次调用使用独立服务端 request ID 作为结算幂等键；不得用查询、IP 或 User-Agent 哈希合并相同搜索。Responses/Chat 路径会保留原生 `x_search` 工具字段，并从上游 usage 或工具事件恢复 `SearchCount` 作为 token 费用之外的附加费。

Voice HTTP 入口包括 TTS、STT 和自定义 Voice 的创建、读取、修改、删除与音频下载；`GET /v1/realtime` 代理 xAI Voice WebSocket。所有入口只允许 Grok 分组，并在整个会话持有并发槽。TTS 按字符、STT 按音频时长生成 `AudioUsage`；Realtime 必须先在任一中继方向观察到包含非空音频负载的事件，才按连接会话时长生成用量，握手失败、纯文本或只有转录事件的会话不结算。正常或常见断开仍要结算此前已确认的音频会话。Voice 和搜索分别使用分组显式价格，`NULL` 回退代码默认价，显式 `0` 表示免费，且都使用基础分组倍率，不混入文本 token 价格。

语音地址与其他 xAI API 共用 Base URL 规范化：只填写主机和端口时补齐 `/v1`，已有 `/v1` 不重复追加，自定义反向代理路径（例如 `/proxy/xai/v1`）保留；官方主机仍只接受官方版本路径。随后追加 `tts`、`stt`、`realtime` 或已逐段编码的 `custom-voices/...`。画布 TTS、账号测试和正式 Voice 入口必须使用同一构造函数，不能直接对裸主机拼接 `/tts`。默认或官方 CLI 代理地址继续按既有规则使用官方 Voice 服务，出站安全策略不变；404 不触发自动切换生成端点或重发。

云端画布另提供管理员启用的 `grok-voice-tts` 目录项，它是站内虚拟名称，上游仍发送无 `model` 字段的 `/tts` 请求。首期限定五种内置声音、MP3/WAV、0.7–1.5 倍语速、自动语言和最多 15000 Unicode 字符；不套用 OpenAI `/audio/speech` 参数。该入口使用 CreativeRun 的持久预扣与恢复，正式 Voice API 仍保持原成功后扣费流程。详见[画布接口](cloud_canvas.md)及[生成与计费](../domains/cloud_canvas.md#execution)。

公开模型广场中明确识别的 Grok Voice 型号按同一语音价格解析显示分组实际单价：TTS 每百万字符、STT 每小时、Realtime 每分钟；详见[语音价格展示](model_catalog_and_marketplace.md#marketplace_audio_pricing)。分组设置已经生效的价格无需为了展示重新录入渠道，展示逻辑不参与扣费。

<a id="grok_account_tests"></a>
## 管理员连接测试

账号连接测试支持 `text`、`image`、`video`、`search`、`tts`、`stt`、`realtime` 七种模式，统一请求契约见 [管理员账号连接测试](http_api.md#account_connection_tests)。前端沿用项目选择框，图片、视频模式分别过滤模型目录；搜索和语音使用各自默认模型，不沿用上次选择的图片或文字模型。

| 模式 | 验证内容 |
| --- | --- |
| 文字 | 保留原 Responses 流式测试及原有额度观测逻辑 |
| 图片 | `/images/generations`；提供 `image_data_url` 时改用 `/images/edits`，返回图片预览 |
| 视频 | 创建一次 `/videos/generations` 任务，在同一账号上轮询状态并预览结果，不因轮询失败再次创建 |
| 网页搜索 | 验证 `/web_search` 对应的 Responses 原生 `web_search` 工具，必须观测到搜索调用才判为成功 |
| TTS | `/tts` 返回非空音频后显示播放器，JSON 错误或非音频不算成功 |
| STT | `/stt` 上传音频并展示转录；未上传时用短静音 WAV，仅验证连通性 |
| Realtime | 验证 `/realtime` WebSocket 握手与首个事件，不发送音频；首帧等待超时明确提示只确认连接可达，立即关闭或错误事件判为失败 |

图片素材限 4 MiB，必须是至少 8×8 的 PNG/JPEG/GIF/WebP；音频限 6 MiB，并校验支持的音频容器签名。只接受 base64 data URL，不由服务端下载任意素材地址。新媒体测试总超时 90 秒，视频轮询最多 60 秒，浏览器关闭或切换账号会取消当前测试；已经提交到上游的视频任务可能继续生成，超时提示保留任务 ID。

这些请求使用被测试账号的凭据、代理、TLS 和受保护请求头覆写，绕过用户网关结算，不创建用户使用记录或媒体任务记录；实际生成、搜索和语音请求仍可能消耗上游账号额度。生成操作不自动重试。测试选项和素材不保存到账号配置，成功后的账号恢复及上游额度观测沿用现有规则。

<a id="grok_video_ownership"></a>
## 任务归属与结算

已有视频任务的查询使用固定 owner 账号的专用选择入口，禁止健康评分、并发繁忙或排队失败触发粘性逃逸；仅等待原账号的并发槽，失败返回原有查询错误，不换账号冒用任务。查询不刷新文本粘性 TTL、不删除任务绑定。调度器已取得的槽在账号/归属/媒体资格校验前立即交给请求生命周期管理，所有早退、换号、取消和转发结束都幂等释放；下一次媒体生成尝试不得携带上一账号的槽。

新视频请求成功后会从上游响应的 `request_id`、`id` 或 `task_id`（包括 `data.*`、`video.*` 嵌套形态）提取任务标识，且保留既有 `request_id` / `id` 优先级；服务按规范化后的任务标识 + `user_id + api_key_id` 保存所选分组和账号绑定。后续状态与 content 下载必须回到创建任务的账号，不能重新随机调度；复合 Key 的映射后来被删除时，服务仍可从持久/缓存绑定构造只用于旧任务查询的最小 Grok 分组视图。查询已有任务不要求账号仍具备“新媒体生成”资格，但仍校验 Key、用户和任务归属。

视频 content 先确认任务状态，再使用服务端上游凭据代理下载，并安全透传 Range/内容头。新异步视频创建不立即扣费，在提交成功响应之前将原用户、付款人、Key、分组、账号、模型、分辨率、时长及资金选择快照保存到 PostgreSQL `legacy_video_tasks`。Redis 仍兼容旧绑定，但已持久化任务的查询授权不再受 24 小时缓存有效期限制。已知数据库故障在创建前拒绝；上游已受理而本地保存失败时返回任务 ID 与禁止自动重发的错误，不能伪装成成功入库。

`LegacyVideoRuntime` 每五秒限量领取到期任务，每实例最多四个并行查询，使用 120 秒数据库租约和 90 秒执行预算。后台固定查询创建任务的原账号，不依赖用户继续轮询，不受客户端断连影响，也不重新提交生成。明确完成、失败、取消及过期会同步任务列表；超时、404 或无法识别的状态保留最近状态并退避重试，不据此假定生成失败。Grok 创建/编辑/扩展的完整上游响应预算为 120 秒，状态查询为 30 秒；创建传输失败禁止自动换号重发，创建不自动跟随重定向。

只有官方 `status=done` 且 `video.url` 为非空白字符串的结果才具备结算条件；对象、数组、数字或布尔值不能作为生成成果，也不能将任务标为完成。后台先保存真实终态用量，再持久化首次计算出的报价和扣款命令；使用任务级 `grok-video:<id>` 幂等键结算。实际资金分配回执与扣款在同一数据库事务保存，使用记录失败时重放原回执补写，不能再次扣费或按后来变化的倍率生成错误记录。客户端状态查询和内容下载仅负责响应及观测，不再领取 Redis 一次性扣款锁。明确零价与缺少价格分别处理：配置的免费价格仍可完成，缺价必须保留待核对并重试。

平台日／周／月消费额度与扣款、请求去重及原扣费回执在同一数据库事务中更新，金额只取真实余额消费。进程在提交后退出时，恢复使用原回执补写日志，不再增量修改平台统计；提交前失败则钱款与统计一起回滚。查询额度直接使用数据库，旧 Redis 绝对快照与退出刷写不再覆盖持久统计。完整窗口与升级边界见[用户平台额度](../domains/platform_quotas.md#platform_quota_settlement_and_flush)。

已经受理的任务不会因 Key 删除而免除原付款责任。终态证据与报价已持久化后，原账号删除也不妨碍按冻结命令和账本回执补齐结算与使用记录；仅跳过已失效账号的额度计数，用户、订阅和团队扣款继续执行。这个例外只适用于拥有持久任务归属的旧视频恢复命令，普通请求仍保持账号存在性校验。尚需查询上游或首次生成报价时缺少原账号，仍保留待核对，不换号查询或猜测价格。

升级自动导入最近七天尚未结束或未结算的已知任务，仅恢复原任务查询。历史缺少资金快照、原账号/付款人已不可恢复、上游已丢失任务结果的情况须人工核对，禁止猜测价格或付款用户。数据库恢复记录不保存提示词、原始响应、凭据或签名媒体地址；短期预览仍使用独立 Redis 快照。列表读取始终是只读投影，不触发上游调用或扣费。状态完成不代表账务已结算，金额关联使用记录判断。

上游创建与本站持久化无法组成跨供应商事务；上游不支持幂等创建/按客户端 ID 查找时，进程在受理后、获得并保存任务 ID 前退出仍会产生未知结果。此类请求不得通过自动重发来“恢复”，需要供应商订单证据核对。

创作台 Grok 视频继续使用上述原任务、原价卡和完成后账务，不转入 `VideoTaskService` 或 `video_hold/capture/release`。画布在发送前持久化执行栅栏，原 `legacy_video_tasks` 保存与画布关联在同一事务完成；查询和 content 下载核对原用户、隐藏执行 Key、分组、账号及任务，只有 `completed` 且 `settled` 的结果进入私有素材归档。画布的未知受理、重启恢复和存储失败不会重新生成或再次计费；完整契约见[Grok 视频执行与恢复](../domains/cloud_canvas.md#grok_video_execution)。

## 客户端配置

用户可在 API Key 页面通过“使用密钥”生成 Grok Build CLI、Codex CLI 或 OpenCode 配置。现有 `config.toml` 应先备份，再合并新模型配置。Codex 配置使用环境变量保存 TokenRouter Key，显式设置 `requires_openai_auth=false`，并以 HTTP/SSE Responses 模式关闭 WebSocket；不能要求用户再登录 ChatGPT，也不能把密钥写进仓库。

Grok Build CLI 的模型配置必须指向 TokenRouter 对外地址（以 `/v1` 结尾），不能直接使用 `api.x.ai` 或内部 OAuth 代理地址。OAuth 流量默认转发到 Grok CLI 订阅代理。

## 默认模型目录

- `grok-4.7`
- `grok-4.6`
- `grok-4.5`
- `grok-4.3`
- `grok-build-0.1`
- `grok-composer-2.5-fast`
- `grok-4.20-0309-reasoning`
- `grok-4.20-0309-non-reasoning`
- `grok-4.20-multi-agent-0309`
- `grok-imagine`
- `grok-imagine-image`
- `grok-imagine-image-quality`
- `grok-imagine-edit`
- `grok-imagine-video`
- `grok-imagine-video-1.5`

文本 Responses 的 `grok`、`grok-latest` 使用运行时默认文本模型，未配置时为 `grok-4.6`；带明确版本的 `grok-4.5-latest`、`grok-4.6-latest`、`grok-4.7-latest` 分别归一化为对应版本。模型市场目录查询复用这套已知文本别名，不启用 GPT/Claude 跨客户端映射，其它内置别名及历史协议 ID 兼容规则由 `internal/pkg/xai/models.go` 维护。模型列表默认展示当前目录，并结合账号模型映射/范围和 API Key 别名；未知模型保持透传，以支持管理员配置的 xAI 兼容上游。未知 Grok 文本族在没有显式定价时按现有 `grok-4.6` 静态价回退，该计费回退不代表继承能力；图片、视频、Voice 和搜索等非文本族不会误用该价格。

Grok 4.7 使用独立文本默认价：低于 200K 输入上下文时每百万输入/缓存读取/输出分别为 $2/$0.5/$6，达到 200K 后分别为 $4/$1/$12；显式渠道价格仍优先，新增型号不更改现有默认文本模型。管理端额度诊断在账号冷却期间仍可执行，但保持凭据校验、刷新与实际请求调度隔离。

<a id="grok_observed_models"></a>
OAuth 账号在额度查询成功后，后台尽力拉取上游 `/v1/models`，把模型 ID 保存为账号 `extra.grok_observed_models`（含来源端点指纹 `scope`）。目录只接受顶层数组或 `data`/`models` 数组，ID 选取规则与手动同步一致；HTTP 200 的错误包、非数组、空目录或超过模型列表读取上限的响应都不写入。同一来源 6 小时内不重复拉取，切换 base URL 或账号类型、时间非法或在未来时重新同步；失败日志只记录分类与状态码。该快照与 Antigravity 的 `upstream_model_metadata` 都只由同步写入，账号编辑在行锁内保留数据库最新值，忽略表单中的旧值或注入值。快照当前不参与路由与计费。

## 环境变量

- `XAI_OAUTH_CLIENT_ID`
- `XAI_OAUTH_SCOPE`
- `XAI_OAUTH_REDIRECT_URI`
- `XAI_OAUTH_AUTHORIZE_URL`
- `XAI_OAUTH_TOKEN_URL`
- `XAI_BASE_URL`
- `XAI_GROK_CLI_VERSION`：覆盖 Grok CLI 客户端版本；内置版本为 `1.0.46`、最低允许版本为 `1.0.13`；覆盖值必须是规范 SemVer 且不得低于下限，转发与账单采用同一校验

进程配置 `gateway.grok` 还包含 Free OAuth 账号的本地滚动窗口软门禁：默认 24 小时、500000 token、95% 停调阈值和 60 秒统计缓存。只有明确标记为 Free 的账号参与；未知或付费层级以及数据库/统计失败均 fail-open。管理端主动额度查询和导入探测不经过该软门禁。

自定义 base URL 和媒体/billing 子路径都必须通过同一 URL allowlist/SSRF 校验。环境变量中的 client secret、token 和上游 URL 不得进入前端配置或错误响应。

相关文档：[上游账号能力矩阵](upstream_account_matrix.md)、[网关请求生命周期](../architecture/gateway_request_lifecycle.md)、[路由与结算](../domains/routing_and_billing.md)、[HTTP 接口边界](http_api.md)、[接口目录](index.md)。
