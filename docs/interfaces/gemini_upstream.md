# Gemini 上游

创作台 Gemini 图片请求统一使用 `generateContent` 的 inlineData。自定义 base URL 校验失败会直接 fail-closed，不会回退到 Google 官方地址；本地按 base64 编码后的 JSON 请求总大小估算并限制在 20 MiB 以内，超限应在提交前返回输入过大错误。本轮不使用 File API。

本文描述 Gemini OAuth、API Key 和 Service Account 账号，Gemini v1beta 原生入口，以及 Anthropic/OpenAI 兼容转换的当前边界。它不固化上游动态模型清单，也不把 Antigravity 混合账号等同于 Gemini 原生账号。

## 章节导航

- [账号与认证](#账号与认证)：修改 OAuth 变体、API Key 或 Vertex 凭据时读取。
- [协议分派](#协议分派)：修改 Gemini 原生、Messages、Responses 或 Chat 转换时读取。
- [模型与会话](#模型与会话)：修改模型解析、thinking、signature 或缓存连续性时读取。
- [管理员模型同步](#gemini_model_list_sync)：修改第三方模型目录兼容、创建预览或同步错误提示时读取。
- [显式上下文缓存](#gemini_explicit_cache)：修改缓存资源、有效期、归属或存储费用时读取。
- [配额与调度](#配额与调度)：修改 tier、模型限流、混合调度或粘性时读取。
- [错误与诊断](#错误与诊断)：修改 Google 错误、刷新或 failover 时读取。

## 账号与认证

Gemini 正式支持：

| 类型 | 当前契约 |
| --- | --- |
| `oauth` | 支持 `code_assist`、`google_one` 和 `ai_studio` 变体；前两者使用内置 Gemini CLI 客户端，AI Studio 需要配置的 OAuth client |
| `apikey` | 使用 Base URL 和 API Key 直连；`credentials.provider_type=third_party` 表示 Gemini 兼容第三方提供商，缺失或 `official` 表示默认来源；实际官方能力还校验 Base URL |
| `service_account` | Agent Platform (Vertex)：支持 Service Account JSON 换取 token，或 `auth_mode=apikey` 使用 Vertex API Key；project/location 决定区域或 Express 路径 |

AI Studio API Key 同时接受传统 `AIza` 和 `AQ.Ab` 格式，使用后者时按相应认证方式发出请求，日志与安全错误同时脱敏。Vertex API Key 不换取 OAuth token；模型同步只对 Service Account JSON 开放，API Key 认证缺少可靠列模型接口时明确返回不支持。第三方地址和 Vertex 均不套用 AI Studio 等级或本地模拟配额，即使历史来源标签仍写作 `official` 也以实际地址判定。

Code Assist/Google One 需要有效 project；AI Studio 的 project 可选并使用选择的 tier。第三方 API Key 保持 `type=apikey` 和 Gemini 兼容请求形状，但必须配置非 Google 官方域名的 Base URL；它没有 Google 官方账号等级，因此不写 `tier_id`，也不参与本地模拟 RPD/RPM 预检或用量窗口。第三方上游实际返回 `429` 时始终使用通用冷却，不解析 Google 日配额的重置语义。OAuth refresh 会重试并兼容历史 client 元数据；token provider 使用过期前偏移和并发锁，避免同账号重复刷新。其它导入类型没有 Gemini 正式转发契约，见[上游账号能力矩阵](upstream_account_matrix.md)。

<a id="gemini_protocol_dispatch"></a>
## 协议分派

Gemini、Antigravity 及混合分组的原生与 Messages/Chat/Responses 兼容入口统一执行 `allow_image_generation`，识别请求的图片响应模态及映射后的图片型号，关闭时返回客户端协议形状的 400 并在上游调用前拒绝。已绑定缓存或智能/复合 Key 仍按实际选组检查，不能借模型别名绕过。同步保留 fork 现有分组默认值及管理员已保存值，不采用上游 246 将所有 Gemini/Antigravity 分组重新开启的回填；需要图片能力时由管理员显式开启。

Gemini SDK/CLI 使用 `/v1beta/models`、`/v1beta/models/{model}` 和 `{model}:{action}` 形状，保持 Google 请求、流和错误语义。Anthropic Messages、Count Tokens、OpenAI Responses 与 Chat Completions 入口则先归一化，再由 Gemini 兼容服务转换为上游请求，响应恢复为原客户端协议。

Gemini 分组支持 Messages、Responses、Chat 和 Gemini GenerateContent，新建时默认只启用 GenerateContent；四项都可关闭，迁移前已有分组启用四项。GenerateContent、StreamGenerateContent 和 CountTokens 的 POST 动作受 Gemini 协议开关控制，模型列表 GET 不受影响。

Responses 转换是正式分支：非流和 SSE 共用 Gemini 上游执行、重试与响应适配，保留模型、reasoning、工具调用、usage、结束原因和首次 Token 指标。上游失败只在客户端收到首个字节前允许切换账号；流开始后按 Responses SSE 语义结束，不能回写普通 JSON 或换账号。

API Key、OAuth 和 Vertex Service Account 在认证头、base URL、project/location 和错误结构上不同；协议转换层必须保留这些传输差异。每次 failover attempt 都重新选择账号并重建 payload，流式输出开始后不再切换。

普通 `/v1beta/models`、单模型查询及生成入口也允许复合或智能 Key 选择 Antigravity 分组。目录按渠道和账号映射后的最终上游型号过滤，仅保留 Gemini；自定义别名不会因名称缺少 `gemini-` 被隐藏，指向 Claude 的伪 Gemini 别名不会通过。生成入口在共享选号前声明原生协议资格，基础与高级候选、黏性和等待计划都先排除最终映射到 Claude 的账号，再申请并发槽；实际派发前再次校验最终型号，智能选组使用相同协议资格。显式缓存绑定不会因协议不匹配而切到其它账号；分组协议开关、Key 权限、订阅与余额准入保持不变。

Antigravity 专用 `/antigravity/v1beta/*` 强制选择 Antigravity 账号，其契约由[Antigravity 上游](antigravity_upstream.md)拥有，不属于 Gemini 原生账号支持范围。

## 模型与会话

可请求模型由分组、渠道和账号能力共同解析。客户端模型依次经过 Key 重定向、渠道映射和账号映射；Vertex 或 AI Studio 的最终模型标识与计费模型可以不同。模型列表不能仅回显默认常量，也不能展示没有可调度账号支持的目标。

兼容层维护 thinking/推理字段、tool/schema、图片输入、usage、finish reason 和 Gemini thought signature。工具 schema 会递归移除 Gemini 不支持的字段；`const` 与标准注解转换/清理遵循原工具参数结构，不把元数据作为额外必填参数；INTEGER 的整数 `exclusiveMinimum` 转换为加一后的包含式 `minimum`，且不覆盖更严格的既有下界，无法等价转换的独占下界只清理不伪造。需要跨轮次的 signature、session 和 cache 连续性时，粘性会话优先复用账号；切换账号必须重新评估可继续性，不能把另一个账号的内部状态当作通用上下文。

Messages/Chat 兼容请求按模型代际转发思考意图：Gemini 2.5 使用 `thinkingBudget`，3 及后续适用模型使用 `thinkingLevel`；显式模型档位不被兼容字段覆盖，图片、音频等不支持思考参数的型号不强加配置。开启思考的流请求可返回摘要：Messages 使用独立 thinking block，Chat/Responses 沿用原有 reasoning 输出。历史 thinking/redacted_thinking 与 Chat reasoning 内容从历史剔除，不作为普通提示词重新发送；未知字段和工具参数整数精度保留。原生显式 thinkingLevel 和兼容入口实际转发的档位进入计费记录，预算值本身不猜测收费档位。

Gemini 4 Argon 默认基础价为输入 2、输出 10、缓存读取 0.1 USD/M Token，Cyber 对应 4、20、0.2；精确档位自定义价优先，未配置时档位别名回退基础型号。Argon 使用隐式缓存，不累加显式缓存存储费用。既有渠道/分组自定义价与倍率保持优先，不依据日期自动改变模型价格。

原生与 Claude 兼容生图响应按上游实际返回的 `inlineData`/`inline_data` 图片 part 数量计费，自定义模型别名也适用。流式响应按单个 payload 中观测到的最大图片数记录，避免累积式 SSE 重复计费；未观测到内联图片时才回退到请求模型名或映射后模型名的生图启发式。

<a id="gemini_model_list_sync"></a>
## 管理员模型同步

账号管理的“同步上游模型”首先请求配置地址的 `/v1beta/models`，API Key 使用 `x-goog-api-key`。显式选择第三方提供商（`provider_type=third_party`）且配置非 Google 官方地址的 API Key 账号，在原生列表返回 404/405 或明确的 Cloudflare 403 HTML 拦截页时，才额外尝试同站点、同部署路径前缀的 `/v1/models`，该次请求使用 Bearer Key；一次同步最多两次列表请求，复用原代理、TLS、请求取消和响应大小限制。官方来源、OAuth、401、普通 JSON 403、429、5xx、网络错误、超限、无效 JSON 或空模型列表不触发兼容尝试。不会读取上游错误正文提供的新地址，也不会用默认模型清单伪造成功。

新增账号的同步预览需携带当前 Gemini 接入来源，与已保存账号保持相同策略。编辑账号的同步仍读取已保存配置，修改 Base URL 或 Key 后须先保存。Base URL 使用服务根地址（允许部署子路径）；模型列表兼容不改变实际生成的 Gemini 原生端点、鉴权、模型权限、调度和计费，列表成功不代表生成端点可用。

模型同步在令牌刷新及列表请求前检查账号显式配置的代理。代理缺失、停用、过期或关联身份不符时明确失败，不静默直连；未配置代理时保持直接请求。共享模型列表传输禁止 HTTP 重定向，包含 Gemini 原生列表、同站兼容列表和 Vertex 分页请求，避免凭据被带到重定向地址；其它使用共享传输的平台遵守相同规则。

同步失败仅向管理员返回安全诊断消息，包含上游 HTTP 状态或 Cloudflare 拦截分类；两种列表都失败时分别保留端点和失败分类，不返回原始 HTML、凭据或内部网络错误。前端读取 API 客户端归一化后的错误消息，避免重复拼接“同步上游模型失败”。

<a id="gemini_explicit_cache"></a>
## 显式上下文缓存

固定 Gemini 分组的 API Key 可通过 `/v1beta/cachedContents` 创建或分页列举缓存，通过 `/{cache_id}` 查询、修改有效期或删除，再在原生生成请求中以 `cachedContent` 引用。缓存绑定创建时的用户、API Key、分组和实际上游账号；查询和修改只允许同一密钥及分组，生成请求固定回原账号，不跨账号故障转移。复合和智能路由 Key 不支持此资源接口或缓存引用，避免无模型参数的管理操作无法选择原分组；无缓存的已有请求不变。

列表、查询和删除是非消费操作，余额或 Key 消费额度耗尽后仍可管理已付费缓存，但身份有效性、停用状态、IP、分组权限和资源归属仍须通过。创建及 PATCH 保留原资金准入。PATCH 与 DELETE 使用同一修改租约，不能通过并发删除破坏续期结算。

AI Studio 官方 API Key 与 Vertex Service Account JSON 账号按各自认证和资源路径执行；OAuth 和 Vertex API Key 账号不支持显式缓存；兼容网关必须显式声明支持缓存，并可设置上游最大 TTL。站点 `gateway.gemini_cached_content_max_ttl_seconds` 默认 86400 秒，未指定请求有效期默认 3600 秒，实际受站点与账号上限共同限制。创建内容仍经过现有模型准入、协议门禁、并发和内容审核。出站缓存操作禁止 HTTP 重定向，不能将正文或 `x-goog-api-key` 转交其它地址。已经发出的创建操作与客户端断连脱钩，并发槽持有至实际上游操作结束；创建成功后的预留、保存和结算使用有界独立上下文，客户端离线不能跳过扣费。

创建收取输入费和缓存存储费，输入费继续遵守现有渠道计费模式；延长只收新增 Token-hour 存储费；缩短和删除不退回已付存储费。创建按上游确认且不超过请求上限的实际有效期收存储费，已过期的创建响应会拒绝并清理；续期按调用前固定的目标到期时间封顶，慢响应不能把缩短变成未预留资金的延长。存储单价优先取定价目录或覆盖文件的 `cache_storage_cost_per_token_per_hour`，否则使用内置已知模型或 Pro/Flash 系列价；完全缺少价格时拒绝创建。存储费用不视为额外模型调用，延长沿用创建时记录的模型与渠道计费口径。

资金操作使用本站持久预留、捕获和释放机制，保留自动选择订阅/余额、套餐分组倍率、团队付款及个人额度边界。费用已知后才按数据库中的最新资金状态建立独立 `gemini_cache` 合同；创建后资金不足会尝试删除上游缓存，不回退为可透支的普通后付费。结算同步执行且幂等；明确失败释放预留，上游结果不明或数据库捕获失败则保留待核对合同，不能推测已退款。创建已经生效但本地保存失败时，只有确认上游清理成功或资源已经不存在才释放预留；保存与清理都失败则保留待核对合同。待核对标记暂时也无法写入时，原始预留及过期扫描时间仍保留，不执行退款。此时本地缓存记录可能缺失，管理员须结合资金合同 ID 与错误日志中的 `upstream_name`、账号 ID 人工核对上游资源及费用，不能视作资源已自动恢复。

缓存修改使用数据库租约串行化，未提交的有效期保持旧值；进程重启后的恢复只在确认上游状态后回滚未完成修改。账号暂时不支持缓存与账号已删除分别处理，不把临时不可用当作可删除记录。原缓存已确定过期时可作废修改意图。低概率上游状态不明或数据库结算故障的资金合同需要按账本核对，不能仅因租约到期自动退款。缓存绑定表和租约由迁移 303–305 创建，不修改旧图片/视频任务表。

## 配额与调度

Vertex `service_account`（包含 Service Account JSON 与 Vertex API Key 认证）支持账号总额、日额、周额上限，与 API Key/Bedrock 共用账号成本口径、超限停调和通知。它不因此加入仅针对 API Key/Bedrock 的池模式，也不改变 AI Studio 本地模拟配额或用户资金计费。

Gemini tier、上游配额和按模型 reset 信息作为账号资格与容量信号。普通账号的 429 响应可以更新账号或模型的 `rate_limited_until`，后续调度在恢复时间前过滤该候选；公共池账号由请求级同账号重试或切号消化 429，不写默认本地账号限流，但管理员显式配置的自定义错误策略仍然优先。实时配额查询失败不应伪造剩余额度。

<a id="gemini_same_account_retries"></a>
原生 v1beta、Claude 兼容与 Chat/Responses 兼容三条转发路径对 429/5xx 等可重试错误先在同一账号上重发，次数由 `gateway.max_same_account_retries_gemini` 控制（默认 4 次，即首次加重试共 5 次请求，范围 0–10，修改后需重启），用尽后才交给切号或返回上游错误。Claude 兼容路径因思考签名错误触发的降级重发不占用这个次数。429 冷却仍在每次失败时按下述规则写入，与重试次数无关。

显式配置 `provider_type=third_party` 的账号保留本站原有第三方冷却规则（默认 5 分钟）；旧账号仅凭非官方 Base URL 被识别为第三方时，优先使用响应明确给出的重试时间，没有重试时间则遵守全局 429 秒级兜底设置（默认 5 秒，可关闭）。两类第三方都不从 Google 每日配额文案推断午夜恢复，官方 AI Studio 与 Vertex 各自规则不变。

显式开启 mixed scheduling 的 Antigravity 账号可以加入 Gemini 分组候选，但仍需满足目标分组、模型、endpoint、额度、并发和凭据约束。普通 Gemini 请求保持 Gemini 协议和计费归属；专用 Antigravity 路由不混入 Gemini 账号。

## 错误与诊断

流式读取因客户端断开而中止时，原生及兼容入口按已经观测到的累计 token/图片用量结算，不以缺少最终分片抹掉已发生的用量；未观测到用量不虚构消费。后续空 `usageMetadata`、非对象或只有 `totalTokenCount` 的心跳不会覆盖先前已收到的分项累计值；显式零值仍有效，累计快照取最新值而不逐帧叠加。明确取消且没有上游结果的请求静默按 499 收尾，不记为账号失败。缓存命中高于总输入的被拦截响应将非缓存输入限制为零，避免负 token 和负费用；Antigravity 同样保留下限。

原生路径会解析 HTTP 2xx 的错误信封、流内错误和空响应，将真实的 429/5xx 等语义状态登记到运维。模型正常 finishReason 不等同于上游故障；promptFeedback.blockReason 和内容过滤类 finishReason 按请求级 400 分类，不计入账号 SLA，Google error 信封则保留自身上游语义状态。已有失败 attempt 仍单独保留恢复记录，不因最终请求级拒绝覆盖；非流正文/聚合结果使用 NonStream 标记，不能误记为流式。诊断采集不改变原生响应字节、HTTP 状态或用量，也不为已经输出的流新增重放动作。

Gemini 原生、Messages、Chat/Responses 兼容路径的网络传输错误统一记录为 status=0/request_error，并在未写客户端响应时立即交给故障转移。持久的 DNS/代理凭据/连接拒绝问题临时移出账号，瞬时 EOF/断连不停调；客户端取消不换号、不停调，countTokens 的真实上游传输失败继续本地估算。退避等待可由请求取消及时中断。

Vertex 429 支持 `google.rpc.RetryInfo.retryDelay`，小数秒向上取整、最长 15 分钟；没有可解析时间时只冷却 1 分钟。AI Studio 的既有每日兜底、公共池与管理员错误策略保持不变。

OAuth refresh、Service Account token、project/tier 发现和上游请求错误分别记录。401/403 需要区分凭据、project/region、API 未启用或策略拒绝；429 解析 reset 并更新限流；网络/5xx 只在响应未开始时允许换账号。

Gemini 原生入口返回 Google 形状，Anthropic/OpenAI 入口返回对应客户端形状。最终错误可应用[网关错误响应策略](gateway_error_policy.md)，但 project、service account JSON、token、API key 和内部上游响应不能无条件透传。排障应核对 OAuth variant、project/location/tier、最终模型、thought/session 状态、quota reset 和 attempt 链。

相关文档：[网关请求生命周期](../architecture/gateway_request_lifecycle.md)、[账号调度与缓存一致性](../architecture/account_scheduling_and_cache.md)、[账号维护](../operations/account_maintenance.md)。

Vertex 共享容量型 429（RESOURCE_EXHAUSTED、无配额详情且提示稍后重试）仅沿用有界同号退避，不写配额耗尽冷却；带 QuotaFailure/RetryInfo、配额或 provisioned throughput 信息的错误保留原处理。每个实际 HTTP 错误响应的显式错误策略检查结果在重试循环和最终处理间复用，避免首次 401 暂停因重复检查误升级为永久 error。Vertex 服务账号换 token 明确返回 `invalid_grant` 且账号不存在时，停止该账号调度并记录 account_auth 事件，再请求换号；其它 OAuth、网络或取消错误不能按账号不存在处理。


Gemini/Antigravity 生图请求在账号映射完成、确定最终图片模型或 IMAGE 输出模态后挂载[图片执行策略](openai_upstream.md#image_attempt_routing)。同组至多两个不同账号，每账号一次生成发送，共享首次截止时间；普通文本、countTokens 与管理员指定账号探测保留原行为。带参考图使用 edit 健康范围，纯文生图使用 generate。已有图片策略的请求不进入文本同账号退避、容量循环或跨组重放；已获得 HTTP 成功后即使正文读取失败也不能换号重新生成。冷却候选只影响选择，不计真实生成发送次数。

策略生效时只按实际观测到的图片产物计张数，包含有效图片类型的 inlineData 和 fileData；没有图片不能仅凭模型名称按一张扣费。已有 token 用量仍按原有规则结算。读取超过执行截止时间会记录模型短冷却，主动取消不会处罚账号。
