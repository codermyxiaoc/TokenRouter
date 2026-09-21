# OpenAI 上游

本文描述 OpenAI OAuth/API Key 账号，以及 Responses、Chat、Messages、Embeddings、Images、Realtime 和 Codex 兼容能力的当前契约。它不枚举会随上游变化的完整模型列表，也不把所有 OpenAI 形状的入口都解释为任意平台可用。

## 章节导航

- [账号与凭据](#账号与凭据)：修改 OAuth、API Key、隐私或客户端限制时读取。
- [协议与传输](#协议与传输)：修改 Responses、WebSocket、Realtime 或兼容转换时读取。
- [远程压缩协议](#远程压缩协议)：区分原生 `remote_compaction_v2` 与旧版 `/responses/compact` 时读取。
- [WS 连接池生命周期](#openai_ws_pool_lifecycle)：修改连接容量、排队、保活或关闭行为时读取。
- [执行作用域](#openai_execution_scope)：修改线程、请求类型、抢占、续接和状态隔离时读取。
- [大请求内存与语义](#responses_large_body)：修改 Responses 原始 JSON 片段和读体分配时读取。
- [模型与能力](#模型与能力)：修改模型别名、endpoint capability 或推理参数时读取。
- [额度与调度](#额度与调度)：修改窗口配额、评分、粘性或自动暂停时读取。
- [失败与诊断](#失败与诊断)：修改错误分类、刷新、CAS 状态或 failover 时读取。
- [流式首输出与恢复观测](#openai_stream_failover_observation)：修改 SSE 前导、心跳、重放资格或失败诊断时读取。

## 账号与凭据

OpenAI 正式支持 `oauth` 与 `apikey`。OAuth 账号保存 access/refresh token、账号/组织上下文和 Codex 能力元数据，后台与请求路径都可触发刷新；API Key 账号保存 key、base URL、工作负载能力、文本协议路由和 Responses 探测事实。其它通用导入类型不构成 OpenAI 转发支持，详见[上游账号能力矩阵](upstream_account_matrix.md)。

OAuth 补全账号元数据时，ID token 中的个人 `chatgpt_plan_type` 是个人套餐的权威来源。`accounts/check` 可能按 access token 的 `poid` 命中另一个 workspace；仅当该记录的账号 ID 与个人 `chatgpt_account_id` 一致时，才能把它的 `entitlement.expires_at` 与个人套餐组合。账号不一致时，到期时间必须改从个人 `/backend-api/subscriptions` 的 `active_until` 获取；若套餐本身来自 `accounts/check`，套餐和到期时间仍保持来自同一条记录。

OAuth 账号可受 Codex CLI-only、允许客户端、agent identity、privacy status 和 OAuth passthrough 策略限制。OAuth 出站的 `originator` 必须与最终 User-Agent 首段配对；客户端未提供可识别官方身份或身份修复失败时统一回退 `codex-tui`，PAT、模型/额度探测、Alpha Search、HTTP 与 WebSocket 走同一默认身份。客户端或 TLS 路由显式提供且可配对的官方身份继续保留，历史 `codex_cli_rs` 仍只作为兼容识别值。API Key 账号不应借用 OAuth-only 的内部端点或身份元数据。Header override、代理、base URL 和 TLS 配置属于出站安全边界，不能覆盖受保护认证头或绕过目标校验。

身份解析在修剪空白前验证原始 User-Agent 的 HTTP Header 合法性；含 CR/LF 或非法控制字节的候选、规范身份和 override 均不能参与配对，继续使用已有合法身份或默认 Codex 身份。后台显示的 Pro、Business 套餐分级只是对原始 `plan_type` 的展示映射，不改写凭据或账号调度资格。

OpenAI OAuth 账号的 `extra.codex_fingerprint_mode` 控制 Codex Responses 的设备指纹收敛，未配置、空值或无效值都默认 `off`，只有 `device`、`session`、`full` 是显式 opt-in：`device` 只统一 installation ID，`session` 进一步统一 session ID 并按客户端原始 session 稳定派生 thread ID，`full` 再把所有客户端收敛到同一 thread。session/full 的 turn ID 每个请求重新生成，但同一次请求的 HTTP 头、`client_metadata` 和内嵌 turn metadata 必须共用同一组 ID；HTTP 内部重试也不得重新派生。普通转换与 OAuth passthrough 都遵守该配置，透传大 body 只局部改写 `client_metadata`，不做整包解码；旧版 `/responses/compact` 保持既有协议且不应用额外收敛。管理员配置的真实 OpenAI device ID 优先于账号 ID 派生值。Spark 影子账号继承父账号模式、device ID 和稳定种子，不允许以影子 ID 分裂同一 OAuth 凭据的上游设备身份。

OpenAI 兼容请求的显式粘性会话头按 `session-id`、`session_id`、`conversation_id`、OpenCode 会话头和 CodeBuddy 会话头依次读取；其中 `session-id` 是 Codex 客户端使用的连字符形式，优先于旧下划线形式。WebSocket 会话日志采用相同优先级，缺少显式会话头时才回退到 `prompt_cache_key`，避免重连时因头名差异漂移到其它账号。

<a id="openai_protocol_dispatch"></a>
## 协议与传输

OpenAI 平台拥有以下正式协议族：

| 协议 | 处理边界 |
| --- | --- |
| Responses HTTP/SSE | 原生 OAuth/API Key 转发；支持允许的 `/responses/*` 子路径 |
| Responses WebSocket | 根据账号 transport capability 选择 WS 或兼容传输；连接建立后遵守流式不可换账号边界 |
| Chat Completions | API Key 默认保留 Chat 协议原生转发；显式强制时才转换到 Responses；每次 attempt 重建协议状态 |
| Anthropic Messages | 转换到 OpenAI 请求并把事件、工具、thinking/usage 恢复为 Anthropic 形状 |
| Embeddings | 仅 OpenAI 分组，账号工作负载能力必须包含 `embeddings` |
| Images | OpenAI 图片生成/编辑；当前网关保留同步生命周期，批量图片由 Gemini/Vertex 专题定义 |
| Realtime/Live/sideband、Alpha Search | 仅 OpenAI 分组，并受分组开关、账号类型和 transport capability 限制 |

<a id="codex_native_images"></a>
### OAuth 原生 Codex Images

OAuth/Codex 凭据的 Images 请求在最终账号模型映射之后选择通道。精确登记的 `gpt-image-1.5`、`gpt-image-2`、`gpt-image-2.5-flare`、`gpt-image-2.5-sunburst` 以及后两者的 `2026-09-08` 快照使用 `/backend-api/codex/images/generations|edits`；其它已有图片型号保留 Responses 兼容通道，不根据前缀放行未来未知型号。API Key 的 Images 路径保持既有行为。生成/编辑 JSON、multipart 图片与 mask 统一经现有输入校验构造原生载荷，复用 Codex 认证、代理和当前 TLS 路由。

只有原生端点明确返回 404/405 才允许同账号一次 Responses 回退。429/5xx 继续既有故障转移策略，不切到另一种协议重复生成；网络结果不确定、HTTP 200 后读取中断、非法结果或未产出完整图片时记录错误，不盲目重放。JSON/SSE 的实际图片与部分用量继续计入原结算路径，图片尺寸优先按真实输出识别，客户端断连仍按既有媒体排水规则处理。

图片输入缓存是总缓存输入 token 的子集，独立图片缓存价受渠道显式价格、区间缓存倍率和整体价格倍率约束；显式零价不能被兜底价覆盖。日期型号在缺少精确目录价时先匹配基名目录，最后才用内置静态价。统计存储复用已有图片用量字段，无新增迁移，按张渠道计费不切换成 token 计费。

<a id="images_url_backfill"></a>
### 图片结果回填

OpenAI API Key 账号可通过 `extra.images_url_to_b64_json=true` 启用图片回填，默认关闭。非流式 `/images/generations` 与 `/images/edits` 响应中，只有缺少非空 `b64_json` 且含 URL 的图片项会被补全；已有 Base64、显式 `response_format=url` 和流式请求保持原行为。回填保留原 URL、修订提示词和所有上游元数据，下载失败只跳过该项；用量、图片数量和计费尺寸始终从回填前的上游响应读取。下载复用账号代理，不携带账号认证或客户端 Cookie；每张最多 20 MiB、60 秒，只接受字节嗅探确认的 PNG/JPEG/WebP/GIF，data URI 也执行内容与大小检查。目标检查见[上游传输安全](../operations/upstream_transport_security.md)。

### 创作台 Images 契约

创作台异步执行器的 `generate` 使用 `/v1/images/generations` JSON，`edit`/`inpaint` 使用 `/v1/images/edits` multipart；固定发送 PNG、单张 `n=1`，并按最终模型能力透传尺寸、质量和背景。GPT Image 模型不发送 `response_format`（其响应固定包含 base64），只有 DALL-E 模型保留 `response_format=b64_json`。inpaint 的 mask 必须是与源图同尺寸、4 MiB 以内的 PNG，透明像素表示需要重绘区域。

创作台 OAuth 账号同样使用上述 Codex 原生通道及明确的 404/405 回退；API Key 仍走原 Images 端点。generate/edit/inpaint 保留异步预占、最终结算和附件校验。网络或成功响应后的结果不确定错误不能让 worker 再次生成，避免重复扣取上游成本。

Responses 回退已经收到并成功解码的完整图片不会因随后 `response.failed`、`response.incomplete` 或读流中断被丢弃；继续交付单张结果并执行原保存/结算，记录 `creative.openai_oauth_error_recovered` 后台事件。日志只含任务、账号、端点和错误分类，不写提示词、图片或原始响应。预览片段不等于完整图片，没有完整产出时保留原失败与安全重试策略。

OpenAI 分组支持 Messages、Responses 和 Chat，新建时默认启用 Responses 与 Chat；三项都可关闭。已有分组迁移时仅在旧 `allow_messages_dispatch` 开启时加入 Messages。该旧字段只作为 Messages 的弃用兼容镜像，专用 `messages_dispatch_model_config` 仍只负责 Claude 到 GPT 模型映射；系列和精确映射都只在目标值非空时生效，全部留空时不执行分组层模型映射。Responses WebSocket 是 OpenAI/Grok 的原生传输能力，不因其它平台启用兼容 Responses 而开放。

管理员可在 OpenAI/Composite 分组上设置 `force_openai_fast`。网关在 HTTP Responses、Chat/Messages 转换、passthrough 和 Responses WebSocket 的 `response.create` 中统一把组级强制意图规范化为 `service_tier=priority`，再执行全局 Fast/Flex 策略；全局 `filter`/`block` 以及 API Key `force_off` 不会被绕过。该字段随 API Key 认证快照传递，快照版本变更后旧缓存必须重建；其它平台的值由管理服务清零。

`free_openai_fast` 是同一分组的用户计费策略，不会改变出站 `service_tier`。只有 OpenAI 账号实际按 `priority`/`fast` 计费时才生效；网关使用同一模型映射、渠道价卡、峰值和长上下文时刻重新取得 Standard 价格，将其写入用户侧 `ActualCost` 和统一结算的基础金额，同时保留 Fast `TotalCost` 给 Usage Log、账号统计和账号额度。Standard 定价缺失时沿用零成本缺价记录，不能借此绕过原有定价错误边界；非 OpenAI 账号、普通 tier 和不可信认证快照均不适用。该字段也随 API Key 认证快照传递，因此快照版本为 v36，旧 v35 快照必须失效并重建。

OpenAI 分组的 `max_reasoning_effort` 是显式推理强度上限，`max_reasoning_effort_over_limit` 取 `downgrade`（默认）或 `deny`。网关只对客户端真正发送的 `reasoning.effort`、`reasoning_effort` 和 Messages `output_config.effort` 执行策略，不会因为兼容桥为缺省 Messages 请求生成的默认 `medium` 而改变行为；模型范围映射先于上限比较。`downgrade` 把超限值改写为上限，`deny` 在 HTTP 上返回 403 `permission_error`，Messages 返回 Anthropic `forbidden_error`，Responses WebSocket 以 policy-violation 关闭。复合 Key 已在鉴权中间件解析到具体 OpenAI 分组，因而使用该分组的策略；本 fork 的管理端不开放 Composite 分组推理配置，也不恢复已移除的旧复合平台处理器。该动作和上限随认证快照传递，快照版本为 v35，旧 v34 快照必须失效并从数据库重建。

<a id="openai_missing_tier_policy"></a>
### 未指定服务层级的可选策略

全局 Fast/Flex 规则新增 `service_tier=missing` 条件；只有管理员显式选择该条件并设置 `force_priority`，才把未提供 tier（含 null）的 OpenAI 请求作为 Priority 候选。旧 `all` 规则仍只匹配显式有效 tier；显式 `default`、`flex` 和其它平台不因新条件而升级。候选继续进入原系统规则、分组/Key 策略，HTTP 与 WS 使用同一裁决；系统 filter/block、Key force_off 和 free Fast 的既有适用条件不变。原有系统 force_priority 的优先级也保留，不能把 Key force_off 理解为高于所有系统强制规则。

### API Key 文本配置

OpenAI API Key 的普通文本配置把四个概念分开持久化：

- `credentials.openai_workload_capabilities` 是工作负载集合，允许 `text_generation`、`embeddings` 与显式开启的 `seedance`。缺失时仍只写入前两项默认值，显式空数组表示不承接这些工作负载。Seedance 还要求 API Key 与自定义 Base URL，其原生异步任务及计费见 [Seedance 上游](seedance_upstream.md)。
- `extra.openai_text_route_mode` 是管理员拥有的路由策略，只允许 `preserve_client_protocol`、`force_responses`、`force_chat_completions`。
- `extra.openai_responses_probe_status` 是探测服务拥有的只读事实，只允许 `supported`、`unsupported`、`unknown`。探测更新不得改写管理员路由策略。
- `extra.openai_responses_continuation_supported` 是管理员拥有的 HTTP continuation 能力开关，取值为布尔值，缺失时按 `false` 处理。只有显式为 `true` 时，Messages 转 Responses 的兼容桥才会发送和缓存 `previous_response_id`；它不改变协议路由，也不覆盖探测状态。

普通文本协议按下表解析：

| 路由模式 | Chat 入站 | Responses 入站 | Messages 入站 |
| --- | --- | --- | --- |
| `preserve_client_protocol` | Chat | Responses；探测为 `unsupported` 时转 Chat | Responses；探测为 `unsupported` 时转 Chat |
| `force_responses` | Responses | Responses | Responses |
| `force_chat_completions` | Chat | Chat | Chat |

因此 `preserve_client_protocol` 下的 Chat 请求只访问上游 `/v1/chat/completions`，请求体保持 Chat 形状，不再先尝试 `/v1/responses` 后按 404 回退。Responses 与 Messages 没有同形 Chat 首选路径，只有探测明确不支持时才在默认模式下降级。显式强制模式始终优先于探测事实。OAuth、Grok、Images、Compact 和 WebSocket 使用各自专用路由，不套用这张普通文本矩阵。

运行时与调度缓存只读取上述新键。账号创建、更新、批量更新和导入仍可接收旧 `openai_capabilities`、`openai_responses_mode`、`openai_responses_supported`，但必须在持久化前规范化并删除旧键；复制账号保留工作负载、路由策略和 continuation 能力开关，将探测状态重置为 `unknown` 后重新探测。嵌套 Sub2API 等可能把请求转给 OAuth 上游的 API Key 账号应保持 continuation 关闭；确认直连 API Key 上游支持 HTTP continuation 后再开启。

OpenAI 兼容非流式响应的 usage 按 `usage`、`response.usage`、`data.usage`、`data.response.usage` 的顺序解析；前两条原生路径优先于 Cline 等兼容上游使用的 `data` envelope。同层的 hosted image usage 必须随对应路径读取，不能把不同 envelope 的 token 与图片用量混合。

`/backend-api/codex` 和无 `/v1` 别名服务特定客户端兼容，但仍经过 TokenRouter Key 鉴权、分组准入、调度和结算。Responses WebSocket 不支持 Qoder；其它平台是否可进入 OpenAI 兼容处理器由路由和平台专题共同决定，不能仅凭 URL 推断。

<a id="responses_bridge_compatibility"></a>
### Responses 桥接保真

网关生成的 Responses 事件写出 `sequence_number`；compact 合成流从 0 递增，无法取得上游序号的合成错误/终止帧写 0。原生透传不重新排序或重编号。

Responses 转 Messages 按输出项和内容索引记录已发文本；done 只补已发前缀之后的尾文，终态正文仅在此前没有正文时恢复，未知索引不猜配，message_stop 后不追加。服务层仍先识别失败：恢复正文不能把 failed 变成成功，也不能解除已输出后的禁止重放；失败日志、部分用量和智能路由的恢复记录仍需保存。

仅 Responses 转 Chat 合并开头的 system/developer 指令，并把会话中途的 system/developer 转为原位置的 user，以适配 Chat 上游；保留其图片等多模态内容和工具顺序。这会改变该兼容路径的指令权重与缓存前缀，原生 Chat 请求不执行此规范化。Codex `agent_message` 的 input_text 和客户端携带的 encrypted_content 字符串作为正文保留，不执行解密或记录密文；任务边界清理旧 reasoning，避免跨轮附着。

### 远程压缩协议

TokenRouter 同时兼容原生 Remote Compaction V2 和旧版 Compact 端点。两者共享 compaction 输出语义，但请求路径、传输方式、账号能力设置和模型改写边界不同：

| 边界 | 原生 `remote_compaction_v2` | 旧版 `/responses/compact` |
| --- | --- | --- |
| HTTP 识别 | 裸 `/responses` 请求同时携带 `stream=true` 且 `input` 含 `compaction_trigger`；`x-codex-beta-features` 不是识别门槛，但原生 V2 出站必保证包含 `remote_compaction_v2` | 客户端显式请求 `/responses/compact`，或带 `compaction_trigger` 但不满足原生 V2 条件的裸 `/responses` 请求被网关提升 |
| 上游传输 | 保持普通 Responses 流式链路，由上游直接返回包含 `compaction` item 的 SSE | 走独立 Compact 子路径；body-signal 流式客户端由网关把 unary JSON 结果合成为 Responses SSE，并在长时间等待时发送注释心跳 |
| 模型处理 | 沿用普通 Responses 的模型处理，不应用 `compact_model_mapping`，也不会因此追加 `-openai-compact` | 仅此路径在常规模型处理基础上应用账号 `credentials.compact_model_mapping` |
| 账号设置 | `extra.openai_native_compaction_v2_mode`、`openai_native_compaction_v2_supported` 和对应 `openai_native_compaction_v2_*` 探测信息只控制此路径；不读取旧端点状态 | `extra.openai_compact_mode`、`openai_compact_supported`、`openai_compact_*` 探测信息和 Compact 专属模型映射都只控制此路径 |

账号设置页中的“原生 V2 压缩”和“旧版 Compact 端点”都是各自协议的能力覆盖，不是协议开关。两者均提供 `auto`、`force_on`、`force_off`：自动模式跟随各自独立的探测结果，未探测账号保持可选以兼容历史配置，明确不支持时排除；强制开启始终允许，强制关闭始终排除。V2 模式只筛选原生 V2 请求，旧版模式只筛选 `/responses/compact`；两者都不会把普通 Responses 或另一条压缩协议改写成自己的路径。原生 V2 即使强制开启仍须满足普通 Responses 端点能力，不能把不支持 Responses 的 API Key 上游纳入候选。旧端点的 OpenAI OAuth GPT-5.6 请求还会把 `reasoning.effort=max` 降为 `xhigh`，原生 V2 则保留常规 Responses 推理强度语义。

管理端连接测试的 `compact` 模式是原生 V2 健康检查，使用普通账号模型映射并要求响应实际出现 compaction item；`legacy_compact` 是旧端点兼容性测试，才使用 `compact_model_mapping`。两种测试的可用状态、最后状态、错误和时间戳完全隔离，旧端点 404 不得改变 V2 能力判定。

官方 Codex WebSocket v2 会先发送 `generate=false` 的预热 `response.create`，再以预热响应 ID 作为业务请求的 `previous_response_id`。严格续接比较会忽略逐请求变化的 `client_metadata`、仅用于传输的 `stream_options`，并把 `generate=false` 与后续省略该字段视为等价；`generate=true` 以及 model、instructions、tools、reasoning、store 等上下文字段仍必须保持一致，避免把无关请求错误串接。

OpenAI OAuth 的 HTTP、passthrough、旧版 Compact 与 WebSocket 出站会在模型映射和本地 fast 策略处理完成后，由网关生成 `x-codex-routing-hint`。提示至少包含最终上游模型；只有有效的 `priority` 或 `flex` 才附带 tier，`fast` 先规范化为 `priority`，`default`、未知值和空值均保持 model-only。旧版 Compact 规范化必须保留 `service_tier`，否则提示会丢失已经生效的路由层级。该头由网关独占控制：所有账号类型都会先删除调用方及账号覆盖提供的任意大小写变体，只有 OpenAI OAuth 路径会重新生成；API Key 路径不得透传伪造提示。OAuth HTTP 也不再自动注入或透传旧版 `responses=experimental` beta 标记，但同一头中的其它独立 beta 项仍保留。

`x-codex-beta-features` 是 Codex 的会话级协商头：OAuth 普通 Responses HTTP 与 WebSocket 握手在客户端未声明时补入 `remote_compaction_v2`，客户端给出的非空值保持原样；原生 V2 请求无论账号类型都保证该 feature 存在。上游响应中的 `x-codex-turn-state` 会在 HTTP/SSE、SSE 转 JSON 与 passthrough 路径显式回传。网关优先按客户端原始[执行作用域](#openai_execution_scope)记录最近签发账号，缺少作用域时保留 API Key 与显式 session 的兼容回退；故障转移后，已知由其它账号签发的客户端回带值会被剥离，未知或同账号的值保持透传。

WebSocket 连接池把 routing hint 视为拨号和普通复用的软亲和：优先复用相同提示建立的连接，池满时仍可在硬兼容连接上排队，显式 continuation 也不会仅因提示变化而断链。握手 beta feature 与本 fork 的 TLS fingerprint profile 仍是硬兼容键，任一变化都禁止复用，并会使尚未完成的旧目标预热拨号失效。路由诊断只记录网关推导的最终模型、规范化 tier、传输类型、账号 ID、是否生成提示和 WS 亲和决策，不记录提示头值、token 或凭据。

Responses WebSocket 的 TTFT 只从实际 token delta 计算；若上游没有 delta，则携带完整文本或工具参数的 `response.output_text.done`、`response.function_call_arguments.done` 可作为语义输出兜底。`response.completed`、`response.done` 以及 content part/output item 等结构终态不产生 TTFT，纯终态响应保持未观测状态，避免把总耗时误记为首 token 延迟。

Responses HTTP/SSE 同样区分结构进度与可见输出：`response.created`、空 reasoning item 等进度可以提交当前 attempt、解除首输出超时并关闭 pre-output failover 窗口，但不记录 TTFT；非空文本/工具 delta、完整文本或工具参数、图片结果以及终态内实际 output 才开始 TTFT。只携带 usage 的终态必须保持 TTFT 未观测。

OAuth passthrough 的 Codex 请求可以省略 `instructions`，网关会按请求模型补入内置 Codex 基础指令；显式提供的非空字符串保持不变，空白或非字符串值仍在本地拒绝。该规则同时适用于 Responses SSE 与旧版 Compact 请求。

Responses Lite 通道由 HTTP `X-OpenAI-Internal-Codex-Responses-Lite: true` 或 WebSocket `client_metadata` 中的对应标记识别，不根据模型名称推断。任何向 OpenAI 上游转发该标记的 HTTP、passthrough、旧版 Compact 或 WebSocket 请求都必须强制顶层 `parallel_tool_calls=false`。OAuth 账号还会统一设置 `reasoning.context=all_turns`，并把私有 namespace 工具声明迁入 `input.additional_tools`；API Key 账号保留除此之外的标准 Responses 请求语义。未携带 Lite 标记的普通 Responses、Grok 和专用 Images 请求不应用这些约束。

Codex 自动化引导的 `<heartbeat>` 信封兼容旧的合法 `automation_id`，以及 `automation_id` 加成对 `current_time_iso` / `instructions` 的形式；时间要求 RFC3339Nano，说明不得为空。重复、未知字段、嵌套标签、属性和不完整字段对继续拒绝，不把任意客户端正文识别为可信引导。

OpenAI OAuth 账号承接 Anthropic `count_tokens` 时会调用 Responses `input_tokens` 端点；缺少 scope、端点不存在，或上游代理在 API 前返回 HTML 格式的 `403` 时，网关改用本地 token 估算并返回成功结果。这类端点级失败不会冷却、临时踢出或标错账号；其它结构化鉴权与上游错误仍进入正常健康策略。

OpenAI OAuth 的普通 Responses 请求默认原样保留 Codex namespace 工具声明，并保留 `function_call`、`tool_call`、`custom_tool_call`、`mcp_tool_call` 历史项上的 `namespace`；普通消息等非调用项上的残留字段仍会清理。旧版 Compact 请求始终摊平 namespace 并移除输入项字段。API Key 出口通常按标准 Responses schema 清理，但请求在顶层 `tools` 或 `type=additional_tools` 的输入项中声明 namespace 工具时，必须保留调用项的 namespace，避免 Lite 历史重放失配；普通消息正文中的同名 `tools` 字段不构成声明。

API Key Responses 回放还会校验输入项 ID 前缀：message 使用 `msg`、工具调用使用 `fc`、reasoning 使用 `rs`；不符合类型约束的 ID 直接删除而不改写，避免伪造标识指向另一上游对象。兼容层把 function-only 上游返回的 `fc_` 工具调用还原为客户端 `custom_tool_call`/`tool_search_call` 时，会分别重typed 为 `ctc_`/`tsc_` 并保留后缀；再次降级到 function 时恢复原 `fc_`，输出项没有对应的 function ID 则继续删除。流式恢复用上游 ID 匹配后续事件，只向客户端发送重typed ID，保证历史重放和 SSE 生命周期都有效。仅当 OAuth 账号的兼容中转不接受 namespace 时，才应启用账号 `extra.openai_responses_flatten_namespaces=true` 恢复平名行为。每次 failover attempt 都会清空上一账号登记的平名映射，避免响应还原状态串到下一账号。

Responses 工具定义在进入 OAuth passthrough、Codex transform、Grok 或 API Key Chat 分流前统一修正显式为 `null` 的 `parameters.type`，将其归一为 `object`；处理范围包括顶层 `tools[]` 和多轮历史 `input[].tools[]` 中的嵌套工具。缺失 `type` 的合法宽松 Schema 保持原样，不能为了兼容而补写并收窄客户端语义。

Responses 请求降级到 Chat Completions 时，工具结果中的 `input_image`、`image_url` 和完整图片 data URL 不能留在只接受文本的 `tool` message。转换器会按 `call_id` 从工具结果中提取图片，把原位置替换为稳定标记，并在对应的一组工具回复后追加用户多模态消息；并行调用按工具声明顺序归属图片，孤儿或未回答调用不携带媒体。没有可识别图片的工具结果必须保留原始字节，避免无关 JSON 重编码改变提示缓存前缀。

国产供应商的原生 Responses 路径沿用无状态请求约束（`store=false`、移除 `previous_response_id`），并把工具输出中的合法图片提升到后续 user 多模态消息，工具输出本身保留文本及图片归属标记。并行工具回复保持连续，期间插入的 system/developer 通知在整批工具回复及提升图片之后恢复；无图片时不因这一步重编码正文。该处理不扩展到普通 OpenAI/Grok 原生 Responses，也不调整计费模型或账号调度。

发往 DeepSeek 平台或官方 DeepSeek API host 的 Chat 请求，在桥接器完成真实推理内容回注后，仅给缺少或为空的 assistant `reasoning_content` 补单个空格占位，以兼容历史推理内容缺失的多轮工具会话。真实推理缓存命中或客户端已提供非空内容时保持原值；其它兼容 Chat 上游不使用该占位规则。

OpenAI API Key 账号以 `force_chat_completions` 承接 `/v1/messages` 时，Chat 流中的并行 `tool_calls` 必须按 `tool_calls[].index` 聚合 ID、名称和全部参数分片，在流收尾时再按 index 顺序生成各自连续闭合的 `content_block_start`、`input_json_delta`、`content_block_stop`；参数分片暂存后一次拼接，聚合期间通过 Anthropic `ping` 维持下游活动，文本与 thinking 仍即时流式输出。空工具参数归一为 `{}`，call ID 保持原样，以便下一轮 `tool_result.tool_use_id` 配对。Anthropic `tool_choice.disable_parallel_tool_use=true` 映射为 Chat 顶层 `parallel_tool_calls=false`，字段缺失或为 `false` 时保持默认 `true`；`auto`、`any`、`none` 和具名工具的选择语义不变。

<a id="openai_ws_pool_lifecycle"></a>
## WS 连接池生命周期

旧版池与新版 `ctx_pool` 共享按账号计算的连接容量。启用动态容量时，上限为 `min(max_conns_per_account, ceil(account.concurrency × factor))`；关闭动态容量时取每账号硬上限。新版模式的账号并发小于等于 0 仍不可调度，旧版无限并发场景继续由硬上限约束。OAuth/API Key 的缺省系数为 5.0，显式配置仍优先；默认硬上限 128 是每个账号的上限，不是全服务连接总量。扩大的是保留会话的连接容量，每轮仍通过原有用户/账号并发槽，不放宽实际执行并发或结算规则。

普通满池请求同时等待目标租约、账号池变化和取消；其它连接释放、拨号完成或连接移除可触发同账号内重新选择。等待保持原截止时间、硬兼容条件和队列上限，广播重选不代表严格 FIFO。`ForcePreferredConn` 续接仍只等待指定连接，不因其它连接空闲而漂移。TLS profile/key、beta 及按指纹模式启用的稳定设备/会话身份是硬边界；routing hint 仍只是软亲和。

需要持续消费控制帧的池连接只有一个常驻上游 reader，通过有界队列向当前租约交付业务帧；空闲时也能处理 ping/close。借出前发现 reader 已登记或缓存的残留业务帧时，将连接标为脏并退出复用；帧登记与租约检查互斥，覆盖已登记但尚未投递的交接窗口。该检查不推断尚未登记的迟到帧属于哪次请求。关闭时先交付已收到的队列内容，再返回真实关闭错误。超时可立即终止底层连接，避免无谓等待优雅关闭。后台探活失败只在连接已死/已脏或能取得空闲租约时移除，不能误关刚借出的连接。

常驻 reader 的连接不在普通借出路径重复执行同步 ping；后台和轮间预检使用独立 10 秒探活上限，保留正常请求读写超时。ping 不占用业务写锁等待 pong。`passthrough` 有自己的直拨与 relay reader，HTTP bridge 使用 HTTP/SSE，两者均不额外启动池 reader。重试使用新连接时仍受原可重试错误、预算和尚未输出的限制。

<a id="openai_execution_scope"></a>
## 执行作用域

账号调度粘性与执行状态使用不同的键。账号亲和继续使用既有 session hash；执行作用域由可信认证上下文的 API Key ID、客户端原始线程或显式会话身份，以及 `request_kind` 通道共同派生。线程标识优先于共享 session；头和 body 元数据按既有客户端优先级解析。作用域必须在 namespace、模型改写和 `full` 指纹收敛之前计算，HTTP 入站借用 WS 池时也传入同一个原始作用域。

`turn`、`prewarm`、`compaction` 和未声明的 kind 共用主通道；`memory` 与其他非空 kind 分别隔离。无线程元数据的子智能体请求可按其明确的子智能体标识分道。只有 session 的旧客户端保留同 session 主通道；所有明确身份都缺失时不登记抢占，不能把类似提示词推导的粘性哈希当可靠身份，此时原会话状态的兼容回退继续保留。新作用域查不到状态时不统一回读旧 session 键，以免重新引入跨线程状态共享。

启用抢占的 OAuth WS 模式中，同作用域新连接取代旧连接时，先尝试发送 1013 关闭帧，再在有界时间内取消旧上下文；旧请求清理不得删除新 owner。不同线程、任务类型、API Key 或分组不互相抢占。明确配置为 HTTP bridge（包括全局默认模式）或 passthrough 时不登记该抢占；HTTP bridge 不继承或发布原生 WS socket/turn-state。`ctx_pool` 根据最终归一化报文大小自动转 HTTP bridge 时，外层抢占登记仍沿用原条件，不使用原始帧大小提前猜测最终传输方式。

turn-state、会话连接和失效密文 lineage 的读写/清理统一使用本轮执行作用域；response ID 对应账号、连接、HTTP 所有者及跨分组归属仍保持原维度。纯 HTTP 的签发账号来源追踪也优先使用原始作用域，防止同 session 不同任务互相覆盖来源；无作用域时保留旧显式会话回退。该追踪仍只剥离已知来自其它账号的客户端回带值，不主动注入 WS 状态。

会话状态主要在有容量和 TTL 限制的进程内存中，Redis 只共享必要的抢占 owner 等原有记录；不需要数据库迁移或全库清理。新旧版本的抢占键不同，多实例应统一更新并让旧 WS 连接排空后重连，不能依赖混合版本实现一致抢占。已输出内容或观测用量后禁止重放、错误事实留痕与幂等结算规则仍适用。

<a id="responses_large_body"></a>
## 大请求内存与语义

Responses 的普通无改动路径优先检查必要顶层字段、复用原始请求；只需修改 input 元数据时按项处理，未变的图片、工具结果和大数字保留原始 JSON 片段，最后按实际长度组装新请求。legacy 字段转换、重复键、异常编码等需要旧解码语义的场景仍回退原流程。公共读体按实际到达数据分块接收，不按不可信的 Content-Length 一次分配整包，也不扩大原有解压和请求大小限制。

原始 byte slice 在整个请求、重试和异步使用期保持不可变。无修改返回原 slice，有修改构造新 slice；智能路由的原请求副本、OpenCode 原始会话 body、审核和必要日志快照不得原地覆盖或提前归还内存池。namespace/Lite、工具调用、账号一次映射、旧/原生压缩及 Images 端点和计费不因减少分配而改写语义。分块组装期间仍可能同时持有分块和最终结果，这不是零拷贝或固定比例的进程内存保证。

## 模型与能力

客户端模型先经过 Key、渠道和账号层映射。OpenAI 内置别名、reasoning effort 归一化、旧版 Compact 端点支持、图像/embedding 能力和传输能力会影响候选账号；模型列表只公开当前分组可请求的结果。

GPT-5.6 的内置产品仅为 `gpt-5.6-sol`、`gpt-5.6-terra`、`gpt-5.6-luna`。裸 `gpt-5.6` 不作为预设型号或 Sol 别名，OAuth 归一化与用量计费候选也不再自动将它改为 Sol 或旧 GPT；未知名称沿用兼容上游的既有透传边界，不因此保证上游支持。管理员显式 Key、渠道和账号映射仍然有效，历史配置和用量记录不回写。模型目录查询与能力来源见[模型目录与市场](model_catalog_and_marketplace.md#model_catalog_metadata_lookup)。

`gpt-6-astra` 的最终上游请求只接受 `low` 至 `max` 推理档位。网关不为 Astra 硬编码推理档位改写；需要兼容遗留 `minimal` 或 `none` 的分组，应在“推理强度映射”中按请求模型配置目标值（例如分别映射到 `low`）。`none` 仅可用于映射，不能作为最大推理强度，因为它没有可比较的强度排名；未配置映射时，普通转发层不得自行把一个推理档位改成另一个档位，客户端显式值由上游或对应兼容层决定是否接受。GPT-5.6 仍支持 `none`。本地价格目录和代码回退均保留 Astra 的官方标准价，远端价卡尚未同步时也不得退回到其他 GPT 型号计费。

Usage Log 将客户端显式档位和最终上游档位分别记录：`requested_reasoning_effort` 保留策略改写前的值（包括 `none`），`reasoning_effort` 以最终上游请求体为准；分组映射发生时，界面可据此展示改写箭头。协议转换或兼容策略未实际转发的字段不记录最终档位。请求未显式提供 effort 时，才允许从模型名后缀推导，并继续受模型能力门槛约束，避免把第三方模型名中的普通 `-max` 后缀误记为推理档位。最终为 `high`、`xhigh`、`max` 的请求在使用首输出超时策略的链路中均选择高 effort 档；协议桥仍可按真实上游能力调整实际转发值，例如 Anthropic 兼容转换可把不支持的 `max` 降为 `xhigh`。

API Key 的普通调度能力只表达 `text_generation` 与 `embeddings` 工作负载，不再用 `chat_completions` 同时代表工作负载和协议。Responses 生图等必须使用原生 Responses 的路径仍有独立能力门禁：以 Responses 为首选协议解析后若落到 Chat，该账号不能承接此类请求。OAuth/Codex 账号还可能包含 Realtime、WebSocket、旧版 Compact 端点状态和客户端身份限制。未知模型可以在管理员明确配置的兼容上游中透传，但没有定价或能力证据时不能虚构价格与功能。

Images API 的流式与非流式上游请求都脱离客户端请求取消信号继续执行，并由上游响应超时控制最终回收。生图属于长耗时且上游可能已经产生实际成本的媒体任务；客户端中途断开不能取消上游并丢失已完成图片的计费结果。下游写失败不改变图片产出和结算事实。

## 额度与调度

Codex 额度评分先读取规范化的 5h/7d 窗口；只有规范字段缺失时，才按上游窗口长度把 primary/secondary 映射到对应窗口。用量和 reset 来自同一窗口，相对 reset 必须以已记录的采样时间为基准，不能每次查询都从当前时刻重新顺延。额度评分及诊断使用规范窗口及原始兜底；自动暂停保留既有规范窗口用量读取，双方复用相对 reset 的采样锚定规则。调度优先采用未来的规范 5h reset，再回退已有 SessionWindowEnd，其它平台窗口不受影响。

OpenAI 是通用高级调度器的能力适配者之一，而不是该调度器的全局所有者。只有最终目标 Group 的 `scheduler_type=advanced` 时，OpenAI 路径才在共同 active/schedulable、分组、模型、限流和并发硬过滤后使用通用 Top-K 评分；`basic` 保留原有默认选择路径。高级分组可用稀疏 `advanced_scheduler_overrides` 覆盖全局 Top-K、评分权重和粘性开关，未设置字段继续继承网关设置。高级分组还会考虑所需 transport/capability、账号优先级、负载、排队、错误率、近期延迟、配额余量和粘性上下文。previous response、WebSocket 会话和显式 session 可约束账号复用；只有策略允许时才能迁移。

OpenAI 专属能力只在账号和请求具备对应条件时加入候选或分数：Responses transport、WebSocket、旧版 Compact、previous response、订阅优先和 Codex 额度余量都不会排除缺失这类可选信号的普通账号。OAuth 5 小时、7 天等上游窗口和自动暂停仍由 OpenAI 设置及账号运行状态控制，不随高级调度器通用化而迁移到其它平台。

HTTP Responses 返回响应 ID 后，账号粘性绑定与用户/API Key 所有权绑定共用独立的三秒写入预算，保留请求上下文值但不继承客户端取消。这样客户端在收到终态后立即断开仍可完成后续续接所需的绑定；写入失败仍记录诊断，不延长原有绑定 TTL，也不绕过后续所有权与账号资格校验。

OAuth 账号的 5 小时、7 天等上游窗口和重置时间保存在账号运行状态中，可触发临时限流或自动暂停；API Key 的 Responses 探测事实继续独立于工作负载能力和管理员路由策略。OpenAI 不再采集上游站点声明倍率，也不按该值进行低倍率优先或高级评分。账户本地 `rate_multiplier` 和渠道上游计费模型来源继续用于 TokenRouter 结算，但都不是用户余额、订阅、Key 限额或用户平台额度。

管理 API 的 `GET /admin/openai/accounts/:id/quota` 保持只读；账号列表使用 `POST /admin/openai/accounts/:id/quota/refresh` 查询上游并把重置次数写入 `account.extra.codex_reset_credit_snapshot`。正数次数只有同时取得到期明细时才覆盖快照，前端水合时过滤已过期明细并把次数收敛到仍有效的卡片数量。该 extra 键只用于展示缓存，不触发调度 outbox；Spark 影子账号的查询可解析母账号额度，但快照仍写在被查询的行上，且列表继续只提供查询入口，不提供真实重置按钮。

## 失败与诊断

账号状态更新使用凭据快照/CAS，避免较早请求在 token 已刷新后再次封禁账号。401/403、429、endpoint 不支持、内容策略、网络错误和上游 5xx 分别分类；只有可切换且客户端响应未开始的失败才进入下一账号。OpenAI 上游代理或 CDN 返回的 HTML 403 只证明当前链路或端点被阻断：请求仍可按既有规则 failover，但不得递增连续 403 计数、临时停调或永久禁用账号；结构化 JSON 与纯文本 403 继续按账号级策略处理。API Key passthrough 池模式会把 `pool_mode_retry_status_codes` 命中的 HTTP 错误先转换为未提交响应的 failover，在同账号预算耗尽后才换号；未配置时默认覆盖 401、403、429，显式空列表可关闭这类按状态码重试。原生 Responses 上游返回的确定性 `400` 在现有账号策略、池模式重试和错误透传规则均未要求改写或故障转移时，按真实 400 回写，并保留脱敏后的 `message` 与诊断所需 `type`、`code`、`param`；瞬时处理错误和容量类 400 仍保持可重试或通用网关错误语义。图片模型被 Codex 文本端点以 plan-gated `400` 拒绝时属于端点错配：当前尝试仍切号，但不写模型冷却，避免影响同账号后续通过 `/v1/images/*` 正常生图；专用 Images 端点上的同类拒绝仍按真实账号能力缺失冷却，图片模型的 `404 model_not_found` 也不豁免。Responses HTTP 与 WebSocket v2 首次发送时保留加密 reasoning/compaction；若上游明确返回 `invalid_encrypted_content`，同账号恢复最多重试一次，清理账号绑定的加密状态但保留未加密 compaction。

账号与模型组合的瞬时失败按连续结果累计：首次失败只记录，第二次短冷却，第三次及以后长冷却。请求间隔较长不能把持续故障误当成恢复，只要未超过状态回收 TTL，稀疏流量中的失败仍继续累计；任一成功结果立即清零该组合。TTL 只负责回收长期不再使用的条目，不能兼作短窗口的连续失败重置条件。

流式错误要保持 SSE/WebSocket 协议完整；Responses 可产生 `response.failed`，非流接口返回相应 OpenAI envelope。入站 WebSocket 的下行写不得继承独立的 ingress 租约取消信号：旧 ingress 路径绑定客户端请求生命周期并叠加 write timeout，v2 relay 只受 write timeout 限制，退出路径再通过显式 Close/CloseNow 回收连接；这样租约丢失不会在终态事件写入期间抢先硬关 TCP，客户端可先收到终态事件，再收到 1013 关闭帧。上行写继续继承控制面取消，以便快速回收上游连接。HTTP 200 SSE 中的 `rate_limit_exceeded` 按语义状态 429 进入故障转移与池模式重试，但不使用该 200 响应的正常配额快照头写入默认账号冷却。上游容量降载通常先发 `error`、再以 `response.failed` 收尾；`server_is_overloaded` / `slow_down` 的前置错误帧在尚无业务输出时继续留在 attempt 缓冲中，触发有界同账号重试和 pre-output failover，并按请求级瞬时故障处理，不冷却当前账号。已有真实输出或重试耗尽后不能重放请求，SSE 与 WS HTTP bridge 会仅在客户端副本中把这两个致命码改为可重试的 `server_error`，原始事件仍用于账号策略与观测。客户端尚未收到业务输出时，池模式账号的其它瞬态流内处理错误也可在请求级预算内重试同一账号；旧版 Compact 桥接心跳注释不算业务输出，即使已提交 200 响应头，只要没有语义 SSE 载荷，最终失败仍必须追加 `response.failed`。OpenAI Responses 标准流与 passthrough 流若只收到前导事件和完全不含 output、usage、error 的 `response.completed` / `response.done`，会在尚未写出客户端业务内容时按静默拒绝切换账号，而不是记录 0/0 成功；终态含 usage、error、任一输出项，或此前已出现语义输出时均不触发该规则。一旦真实输出开始，网关不得重放请求或切换账号。最终错误还可命中[网关错误响应策略](gateway_error_policy.md)，但规则不会把失败结算成成功。排障应同时检查账号类型、required transport/capability、客户端限制、privacy status、模型映射、quota reset、代理/TLS 和 attempt 记录。

<a id="openai_stream_failover_observation"></a>
### 流式首输出与恢复观测

原生 Responses 与透传流在首个需保留的输出前暂存前导，已知纯 `ping`/`heartbeat` 和没有内容的文本、推理或转写 delta 可继续缓冲。判空采用字段和类型白名单；带用量、工具、加密状态、未知字段或未知事件时保守处理，不能将“没有可见文字”直接等同于可重放。显式流错误已经观测到非零上游用量时不再切换账号，仍执行原有账号限流/健康处理；已有正文、工具和状态输出的请求同样保留原禁止重放边界。缺少终态的 EOF、读取错误和首输出超时继续走各自原有处理分支，不因显式流错误的规则调整而改变。

网关生成的排队与保活心跳统一排除在业务输出计数之外，保持真实 HTTP 提交状态。同一响应多次启动保活时保留历史计数，新分组的独立写入器重置计数，详见[智能路由换组边界](../domains/smart_routing_api_keys.md#group_failover)。

最终流失败或同一上游流内由成功终态恢复的前置错误产生 `openai.stream_attempt_diagnostic`，记录当前尝试的流路径、首个提交事件及原因、阻止重试的原因、结果和客户端断连状态。诊断不保存正文、工具参数或推理密文；它用于区分真实输出、保守提交的结构事件、已有用量和进入转发前已写出的内容，不能把未记录可见内容时间直接解释为没有业务输出。输出前切换账号仍使用已有的尝试错误记录。实际上游失败与恢复记录的归属见[运维信号流水线](../operations/ops_monitoring_and_alerting.md#ops_signal_pipeline)。

<a id="chat_stream_failure"></a>
### Chat 兼容桥的流错误

Responses/Messages 转 Chat Completions 共用的扫描器先识别流内 `error`，再解析普通 chunk；HTTP 200 携带 JSON 错误也按错误处理。多行 `data:` 先组成完整事件，`event: error` 携带的扁平错误对象也具有失败语义，不能依靠后续 `[DONE]` 转成成功。多行事件有总字节数和行数上限；完整单行 JSON 继续及时交付，兼容省略事件间空行的历史上游。空流、只有角色前导或 `[DONE]` 的空流，以及没有完成标记的 EOF 不能合成成功终态。上游提供有效 `finish_reason` 时允许省略 `[DONE]`；已有业务数据并以 `[DONE]` 结束的兼容格式继续有效。单个格式不合法的普通 chunk 保持原有跳过策略，但不能靠它把空响应变成成功。

桥接器延迟发送结构前导，直到首次业务输出或确认正常结束。输出前且没有上游用量时，可重试错误携原始语义状态、响应头和 Ops 事件返回 handler，继续原有账号故障转移及智能路由换组。已经输出文本、推理或工具数据，或已经观测用量后不再重放；连接仍可写时 Responses 追加 `response.failed`，Messages 追加原生错误事件，部分用量交给既有结算路径，不再追加成功终态。客户端取消本身不伪造成上游故障或继续重试；取消后排水实际收到的显式上游错误仍保存为 Ops 失败事实，不能依赖下游成功接收错误帧才记录。

智能路由 Chat 桥每轮记录 `smart routing chat stream finished`，带请求关联上下文、分组/账号、终态类别、标准 `finish_reason`、是否输出/观测用量及客户端断连/取消标志。这些安全元数据用于区分 HTTP 200 下的正常完成与中途失败，不包含模型正文或工具参数；`finish_reason` 和 `[DONE]` 仅反映上游所报终态，不保证客户端已收到全部内容。

相关文档：[网关请求生命周期](../architecture/gateway_request_lifecycle.md)、[账号调度与缓存一致性](../architecture/account_scheduling_and_cache.md)、[模型目录与市场](model_catalog_and_marketplace.md)。
