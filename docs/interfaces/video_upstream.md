# 独立 Video 上游

本文拥有 `platform=video` 的账号配置、原生协议和 HTTP 兼容边界。任务持久化、预算预留与结算由[媒体任务](../domains/media_tasks.md)和[路由与计费](../domains/routing_and_billing.md)拥有。旧 [Grok 视频](grok_upstream.md)与 [OpenAI 账号的 Seedance 能力](seedance_upstream.md)继续使用原链路；新增 Video 不改变其计价或准入。

## 章节导航

- [账号与模型端点](#video_accounts)：配置独立平台、多个端点、模型绑定与预算上限。
- [入站和上游协议](#video_protocols)：核对原生路径、参数保真、任务 ID 与取消语义。
- [OpenAI 视频入口与内容](#video_openai_content)：核对 `/v1/videos`、共享 Grok 分派及凭据隔离的流式下载。
- [验证边界](#验证边界)：理解本地协议测试的保证范围。

<a id="video_accounts"></a>
## 账号与模型端点

Video 仅接受 API Key 账号，且 Video 账号与 Video 分组双向隔离。创建、编辑、批量更新和导入共用校验；跳过混合渠道提示不能绕过隔离。分组使用 `basic` 调度，显式提交 `advanced` 返回客户端错误，隐藏的文本调度配置不参与视频请求。

账号 `credentials` 示例：

```json
{
  "api_key": "供应商密钥",
  "base_url": "https://relay.example",
  "video_endpoints": ["compat", "seedance", "kling"],
  "video_model_bindings": {"ark-model": ["compat", "seedance"], "kling-model": "kling"},
  "video_base_urls": {"seedance": "https://ark.example/api/v3"},
  "video_model_paths": {"kling-model": "/omni-video/{model}"},
  "video_max_pending_tasks": 10,
  "video_max_duration_seconds": 15
}
```

`video_endpoints` 至少包含一项，可选 `compat`、`openai_videos`、`seedance`、`kling`、`wan`、`minimax`。创建表单不默认勾选端点，管理员必须明确选择；编辑保留已保存的选择，缺失配置不自动补为 `compat`。`compat` 保持上游 `/v1/video/generations`，`openai_videos` 使用上游 `/v1/videos`，旧账号不自动增加或迁移端点。`video_base_urls` 按端点覆盖共享 `base_url`；地址必须是没有内嵌凭据、查询参数或 fragment 的 HTTP(S) URL，出站继续经过现有上游 URL 安全校验、代理和 TLS 传输边界。受保护的 Header Override 可用于 Video API Key。

模型先经过客户端别名、分组/渠道映射和账号模型映射，再用**最终上游模型名**查询 `video_model_bindings`。该字段同时接受旧字符串单端点和新字符串数组，例如 `"ark-model": ["compat", "seedance"]`。数组必须非空、无重复，且全部属于账号已勾选端点；模型未配置绑定时继承账号端点。创建和编辑表单允许同一模型填写多行不同端点，保存时按模型聚合；只填写一条仍保存旧字符串格式。模型目录继续使用配置的绑定、白名单和映射，不补聊天模型默认值。

自适应选择在实际转发、调度资格判断和模型广场中共用同一规则：原生入口只选择该请求对应的已启用协议；统一 `/v1/video/generations` 必须明确启用 `compat`，`/v1/videos` 必须明确启用 `openai_videos`，且均须属于最终模型的允许端点集合。两个 OpenAI 入口互不授权，也不回退到唯一原生端点。没有合格账号时在建任务、预扣和上游 POST 之前拒绝请求，端点不匹配返回 `VIDEO_ENDPOINT_MODEL_MISMATCH`。不会按模型名称、列表顺序或请求字段猜测供应商，也不会因上游报错重新提交生成。

两种 OpenAI 入口都只替换实际模型，未知扩展字段继续保真，不把厂商参数转换为另一种结构。内部未提供协议的旧调用只视为 `unified`，仍须明确启用 `compat`；它不是账号默认能力。

例如同一个模型配置 `["compat", "seedance"]` 后，客户端的 `/v1/video/generations` 转发到上游同路径，`/api/v3/contents/generations/tasks` 转发到上游 Seedance 原生路径，`/v1/videos` 不可用。历史 `"model": "seedance"` 只允许 Seedance 原生入口；需要开放兼容入口时，必须同时在账号端点和该模型绑定中增加相应协议。已受理任务仍使用冻结协议查询、取消和结算，不受后来取消端点勾选影响。

Kling 原生入口直接提供 `/text-to-video/{model}`、`/image-to-video/{model}`、`/omni-video/{model}`、`/v1/videos/text2video` 或 `/v1/videos/omni-video` 操作路径，无需从请求体猜测文生或图生模式。遗留 `video_model_paths` 继续受受控模板校验，但不能借此开放任何未勾选的 OpenAI 入口。

`video_max_pending_tasks` 默认 10、范围 1–1000。可选的正数 `video_max_duration_seconds` 支持省略或用 `null` 显式清空；按秒计费或已开启 Token 秒价预扣的自动时长任务必须配置它，显式零和非法值仍拒绝。账号不再提供 Token 预留上限；遗留 `video_max_output_tokens` 对新任务无效，保存时清理，已受理任务仍按自身快照结算。Token 预扣由模型价卡的 `video_token_prepay` 决定，固定秒价不乘倍率，完成后按实际 Token 退补差；关闭则完成后原子检查资金和额度并扣费。预算不改写供应商参数或裁剪用量，缺少可信用量不能作零费用结算。详见[任务结算](../domains/video_tasks.md#video_task_billing)。

<a id="video_protocols"></a>
## 入站和上游协议

统一创建为 `POST /v1/video/generations`，裸别名为 `/video/generations`。**请求体遵循实际选中上游的 JSON 参数约定**，不把 `content`、`input`、`parameters`、`images` 等互相转换。上游端点由入站协议、账号能力和最终模型的允许端点集合共同决定。创建必须能从规范的唯一 `model` 字段或受支持的模型路径解析模型；路径与正文同时提供时必须一致。

| 上游端点 | 创建 | 原生查询 | 原生取消 |
| --- | --- | --- | --- |
| compat | `/v1/video/generations` | `/v1/video/generations/:id` | 同路径 `DELETE` |
| openai_videos | `/v1/videos` | `/v1/videos/:id`；内容为 `/:id/content` | 不支持 |
| seedance | `/api/v3/contents/generations/tasks` | 同根路径 `/:id` | 同路径 `DELETE` |
| kling | 配置的受控模型路径，或两个 `/v1/videos/*` 操作 | `/tasks?task_ids=:id` | 不支持 |
| wan | `/api/v1/services/aigc/video-generation/video-synthesis` | `/api/v1/tasks/:id` | 不支持 |
| minimax | `/v2/video_generation` | `/v2/query/video_generation/:id` | 不支持 |

Ark 同时保留 `/v3`、`/v1` 和裸路径别名。Video 分组进入新任务服务；OpenAI 分组进入旧 Seedance 处理器。共享 `/v1/videos` 创建入口按分组平台分派，Grok 继续原处理器；其 `/v1/videos/generations` 等复数操作路径保持原行为，Kling 的两个明确操作路径优先精确分类。

统一入口使用本地任务 ID；统一查询为 `GET /v1/video/generations/:id` 或 `GET /v1/tasks/:id`，两条路径都支持 `DELETE` 取消。原生创建和原生查询保留供应商任务 ID 与原始响应体，另用 `X-Video-Task-ID` 返回本地 ID。Kling 查询只接受一个 `task_ids`，不允许批量查询越过逐任务归属判断。查询/取消校验原用户、原 Key 和原端点归属；已经创建的任务不再要求当前映射仍包含原分组，当前 Key 仍须通过身份、状态和 IP 等鉴权。任务使用冻结的版本化 URL/操作路径及原账号，不重新调度。原生 ID 歧义不能以其它账号试探解决。普通 Key 改到 OpenAI 分组后，Ark 原生路径继续按旧规则分流，可使用本地任务 ID 从统一路径取回新 Video 任务。

转发只替换认证及实际模型字段/模型路径；其它 JSON 参数和未知扩展字段保持原样。重复 JSON 键、大小写歧义和超过 64 层的嵌套会被拒绝，避免网关与上游解析值不同。通过任务归属校验的响应保留上游 HTTP 状态、原始 JSON/错误正文及允许透传的响应头，不执行客户端模型别名还原或套面板 envelope；任务响应强制 `Cache-Control: no-store`。创建请求禁止自动重定向，3xx 的 `Location` 不下发客户端，其他共享上游请求的重定向策略不受影响。Wan 的 `request_id` 是追踪号，任务 ID 从 `output.task_id` 读取。

已有任务的查询和取消统一核对协议规范 ID 字段；明确不匹配、冲突 ID 或包含其他任务的任务列表不得作为本任务观测。重复的规范 ID 键或其父容器同样视为归属歧义，避免不同 JSON 解析器分别读取首值、末值；此消歧不扩展到普通厂商扩展或非 JSON 错误正文。整包拒绝意味着状态、用量、媒体和原始响应均不落入当前任务，也不触发捕获或释放；后台按查询失败退避，取消及已知污染的历史原包读取返回安全错误，不暴露外来正文。合法无 ID 查询响应仍可接受，供应商追踪号及未知扩展字段不被猜测为任务 ID。

创建响应若包含多个不同的有效规范任务 ID，首次返回及幂等重放使用安全 `502 / VIDEO_TASK_RESPONSE_MISMATCH`，不透出冲突原包。内部保存诊断响应，但不选择存疑 ID 发起查询，按受理不明保留预算；即使创建 HTTP 为 4xx/5xx，也不能将这种冲突当作完全无 ID 的拒绝退款。

MiniMax 原生响应兼容平铺字段及 `task` 对象封装。封装存在时从 `task.id`、`task.status`、`task.content.url` 及 `task.usage` 提取任务、状态、产物和用量，保留通过归属校验的原始响应体；`usage.output_seconds` 优先于普通时长字段，只接受有限正数，不用输入秒数或合计秒数代替。该封装和新增字段只用于 MiniMax，其他协议的同名扩展不参与状态或计费。封装查询包中的任务 ID 明确不匹配时整包拒绝，不能保存或返回外来原包，也不能据此捕获或释放当前任务预算。完成、失败和取消仍复用原有幂等结算规则，过期继续待对账。已观测到该封装用正数输出秒数配合 `completion_tokens`、`prompt_tokens`、`total_tokens` 三项明确零值表示 Token 占位；该组合的零 Token 保持未知，Token 模型须待可靠用量后结算，按秒模型仍使用真实输出秒数。平铺原生明确零及其他原生端点的既有零值规则保持不变。

新封装只读取本任务的规范字段，不遍历其中的 `data`/`tasks`，也不借用外层或扩展 `output` 的状态、产物及用量。缺少状态时保持未知观测；任务 ID 沿用 `id` 与 `task_id` 只有一个有效值或两者一致的兼容，两者都缺少时仍保留旧平铺位置明确返回的受理 ID，避免把已受理响应误判为无 ID 拒绝。嵌套 `id`/`task_id` 冲突、提供的 ID 均无法解析或查询 ID 不符时不应用该观测，已知调用方或外层受理 ID 仍保留。空封装中的外层业务错误只有未取得任何任务 ID 才按既有规则认定创建拒绝，查询 HTTP 错误继续保持原处理方式。

内部提取 `resolution`、`duration`、`has_reference_video` 和可信用量，不给未知分辨率或时长补默认值。选定上游协议后才解析输入：Wan 的 `parameters`/`input` 优先于扁平字段，MiniMax/Ark 的 `content` 存在时只读取该正文的参考视频。未知 compat 厂商混用结构且元数据冲突时拒绝提交，`openai_videos` 沿用同一保守提取规则。只有实际视频输入（例如 `video_url`、`reference_videos`、`content` 中的 `type=video_url`）设置参考视频标记；图片和音频不设置。该标记保留输入事实和历史兼容，不参与新视频价格选择；每个分辨率统一一个单价。Token 用量缺失与可信零严格区分，终态可信用量才可用于结算；兼容中继可能用零占位缺失 usage，所以 compat 和 openai_videos 的零 Token 仍视为未知，原生明确零可以保留。

参考图片数量仅从已知输入字段提取：MiniMax/Ark 优先 `content`，Wan 优先 `input`；支持图片数组、首尾帧和协议白名单中的参考图。同数组中重复 URL 按输入项数计算，`images`/`image_urls` 明确别名数量一致时只计一次，数量不同或多种表示混用且无法确定优先级时标记为未知。明确无图为零；输出图片及未知扩展字段不参与计数。此步骤不改写请求体，也不额外拒绝原本可转发的请求；只有启用图片附加费且无法确定张数时拒绝报价，避免漏收或重复收取。

创建沿用鉴权、内容策略、用户和账号并发、渠道模型限制及消费资格检查；不以旧“允许图片生成”开关限制 Video。选号失败可以尝试尚未提交的其他合格账号，一旦付费 POST 发出，网络错误或 5xx 的受理结果可能未知，不自动换账号、换组或重提。

独立 Video 的退款策略区分创建与查询：完整收到创建 HTTP 4xx/5xx 且没有任务 ID 时返回原错误，并将本地任务记为失败、幂等释放预扣；平台承担上游可能已经受理的成本。带任务 ID 的错误保留归属供原账号查询，明确失败/取消终态按任务规则释放。网络中断或响应读取失败继续待核对，查询 HTTP 错误不会触发退款；旧任务只按持久化的原创建响应恢复，不使用后续查询包推断创建失败。

取消必须得到供应商确认。显式 `cancelled`/`canceled`/`deleted` 才是取消观测；空响应的 Ark 成功删除和 compat `204` 可确认取消。HTTP 200 中的业务错误、处理中或已经成功状态不能直接退款。`expired` 没有足够契约证明未产生费用，保留该观测并等待对账。

<a id="video_openai_content"></a>
## OpenAI 视频入口与内容

Video 分组使用 `POST /v1/videos` 创建、`GET /v1/videos/:id` 查询、`GET /v1/videos/:id/content` 播放或下载，兼容不带 `/v1` 的别名。创建要求账号与模型明确允许 `openai_videos`，只接受 JSON，使用站内 Bearer Key；请求参数以实际选中上游为准，例如 `model`、`prompt`、`duration`、`resolution`、`ratio`、`images`。已创建任务的查询和内容读取继续遵循原任务归属与冻结协议，不依赖当前账号勾选。这是所提供异步 JSON 合同的接入，不自动开放集合枚举、multipart、remix 或未声明的取消 API。

该入口返回本站 `vid_…` ID、`object=video` 和 `queued / in_progress / completed / failed` 状态，另保留 `billing_status`、实际已知的时长、分辨率及 Token 用量；失败提供安全错误摘要。受理不明仍保留预算待对账，不因显示四态而退款或重新提交。旧统一和供应商原生响应格式保持不变。查询沿用持久后台轮询、预扣与幂等结算，同一任务重复查询或下载不产生新的生成请求。

共享路径仅创建 Video 分组任务，Grok 分组继续原链路；管理请求通过本地 `vid_` ID 精确查找新任务，后续允许原有效 Key 切换到其它有效分组后取回。复合/智能 Key 查询不要求重新选组，但仍核对原行为用户和原 Key。普通 Key 仅当前 Video 分组的本地任务查询可免消费准入，切换到其它平台后继续其原消费门禁，不能让碰巧以 `vid_` 开头的旧 Grok ID 绕过原检查；原有分组、状态、IP 与身份门禁保持不变。智能路由仍允许 Grok 与 Video 各自符合条件的候选，模型字段处理按本次所选平台区分，不能让新路径改变旧 Grok 的别名恢复。

`content` 是只读流，必须任务生成完成且账务已结算；未完成或待对账返回冲突，不触发上游状态查询或补扣。优先读取持久化的安全公共视频地址；若上游是 `openai_videos` 且未返回直接地址，则使用冻结端点、原账号和原供应商任务 ID 请求固定 `/v1/videos/:id/content`。带凭据请求禁止自动重定向；重定向目标单独经过公共地址与 DNS 拨号校验，以无凭据请求访问，不转发调用方 Key、上游 Authorization 或 Cookie。

面板预览通过自身的对象权限签发任务专属票据，与站内 Key 的 `content` 入口分开鉴权。独立 Video 的签票和每次播放都读取持久任务，核对任务 ID、原用户、原 Key、`completed`、`settled` 及完成后 24 小时期限；Redis 媒体快照不替代持久状态检查。无直接地址且冻结端点为 `openai_videos` 的任务同样可签发最长十分钟的票据，票据期限不超过结果期限。预览元数据及签票不访问上游；实际播放才复用上述只读内容传输，服务端凭据仅发往冻结的供应商内容路径，不进入浏览器、票据或公共 CDN 请求。图片、Grok 和 Seedance 预览的原有公共地址边界不变，详见[任务结果预览](../domains/media_tasks.md#media_task_preview)。

流式下载支持单段 `Range`，最多 512 MiB、两分钟，API 下载与面板播放共享本机最多 16 条下载流；只输出允许的视频类型和必要的内容头，供应商错误、跳转、Cookie 等不透传。响应禁止缓存并带 `nosniff` 和 `no-referrer`，内容传输不调用生成、任务状态查询、恢复或结算。新端点未声明删除语义，因此不为 `/v1/videos/:id` 注册 DELETE，账号旧统一取消入口也不能把未支持的操作伪装为退款成功。视频价卡、参考图固定收费、倍率和失败释放规则继续由已有任务账本负责。

## 验证边界

协议 fixtures 使用本地 HTTP 替身验证六类端点路径、认证、参数保真、原生响应、ID 提取和取消状态，不产生真实付费请求。用户提供的附件包含中继协议示例，不能自动等同于已验证的供应商官方完整契约；未给出的取消、缺失用量和过期费用语义保持保守处理。部署到具体上游前应核对其支持的模型、原生操作路径和终态 usage 字段。

相关入口：[接口目录](index.md)、[HTTP 边界](http_api.md)、[账号矩阵](upstream_account_matrix.md)、[账号调度](../architecture/account_scheduling_and_cache.md)。
