# 账号维护

本文描述上游账号凭据刷新、健康测试、配额与能力探测、临时不可调度和自动恢复的后台流程。它不定义创建表单字段或请求内调度评分；这些由平台专题与调度架构拥有。

## 章节导航

- [凭据刷新](#凭据刷新)：修改 refresh 候选、并发、重试或状态同步时读取。
- [状态与临时不可调度](#状态与临时不可调度)：修改错误、限流或恢复时间时读取。
- [账号测试与自动恢复](#账号测试与自动恢复)：修改定时测试或恢复条件时读取。
- [额度与能力探测](#额度与能力探测)：修改上游 usage、quota 或 endpoint capability 时读取。
- [运维诊断](#运维诊断)：排查刷新堆积、误封禁或账号抖动时读取。

<a id="account_credential_refresh"></a>
## 凭据刷新

`TokenRefreshService` 分页读取需要维护的账号，按平台 refresher 判断资格，并对每个 provider 应用独立并发/QPS 门槛、单次 attempt 超时、周期总超时和有界退避。OAuth、Setup Token 和 Qoder COSY 的候选规则不同；API Key、Bedrock 和 Service Account 通常由各自请求路径或签名 provider 管理，不应统一假设有 refresh token。

刷新成功后要原子更新凭据/过期时间，清理可恢复错误，并同步账号缓存与调度快照。OpenAI/Antigravity 还可在刷新后确保 privacy 状态。刷新失败按失败阈值记录，不立即把一次瞬时网络错误等同于永久禁用；凭据明确撤销或账号归属失效时才进入需要重新授权的状态。

管理员重新授权合并新认证字段与已有 credentials 后再清理敏感废弃字段，保留模型映射和账号设置。OpenAI 用量查询可能命中缓存，因此查询成功不得清除刷新令牌失效错误；恢复由真实认证或凭据刷新流程确认。

请求路径 token provider 仍会在使用前检查过期偏移，并用账号级锁避免并发刷新。后台刷新降低热路径延迟，但不是唯一正确性来源；两条路径必须使用相同的凭据版本/CAS 保护，避免旧请求覆盖新 token。

## 状态与临时不可调度

账号长期状态、`schedulable`、全账号限流、模型限流和临时不可调度规则是不同层次：

- 凭据/配置错误可以记录 recoverable error 并要求人工重新授权。
- 429、明确 reset time 或短期网络/供应商故障使用恢复时间，在到期前过滤账号或模型。
- 管理员策略和代理过期可能临时移出调度，但不删除账号。
- 账号到期可由维护任务自动暂停；重新启用前仍需验证凭据和关联资源。

状态写入要携带凭据快照或版本条件。较早请求不能在新凭据生效后再次设置旧错误；恢复同样不能清除另一个请求刚确认的永久错误。

## 账号测试与自动恢复

管理端即时测试和 `scheduled-test-plans` 使用平台测试服务调用真实凭据/模型，并保存测试结果。计划使用分钟级 cron 表达式；每个计划可配置自动恢复。成功测试可以清除符合条件的 error、rate limit、temporary unschedulable 和模型限流，但不能绕过管理员禁用、账号过期或类型不匹配。

测试本身应使用受控超时、代理/TLS 路由和脱敏日志。一个模型测试成功只证明该路径当时可用，不证明所有 endpoint capability 或媒体资格。失败结果需区分认证、模型、配额、代理、TLS 和上游容量，以免自动恢复形成启停抖动。

Kimi、Zhipu、DeepSeek 的连接测试按账号 `api_protocol` 选择原生 Anthropic Messages、OpenAI Chat Completions 或 DeepSeek Responses 端点，不得始终假设 Chat 形状。测试复用账号自定义 Base URL、代理、TLS 指纹和受保护 Header Override；Anthropic 协议的自定义中继在模型同步等 OpenAI 格式请求中只移除末尾 `/anthropic`，不能改回官方 host 或丢弃此前的路径前缀。

MiniMax 复用上述连接测试边界，默认测试模型为 `MiniMax-M2.7`；固定协议只测试指定端点，`adaptive` 模式分别测试 Chat、Messages 和 Responses 三个原生端点。缺省测试模型不能回退成 GPT 模型。

管理端连接测试显式选择 `test_type=text|image` 并传入自定义 `prompt`；Grok 还提供视频、搜索、TTS、STT 和 Realtime 模式，素材与结果边界见 [Grok 管理员连接测试](../interfaces/grok_upstream.md#grok_account_tests)。文字测试不因模型名称含图片标记而生图；仅未携带 `test_type` 的历史调用保留旧推断。OpenAI 的 `compact` 与 `legacy_compact` 使用固定载荷，不显示或使用自定义提示词。

文字测试可在账号支持的范围内选择 `test_endpoint`，显式选择只验证当前协议；默认自动仍使用原有账号协议及自适应诊断流程。选项不写入账号配置，不改变用户路由、计费和定时测试。OAuth、Bedrock、Vertex 等凭据只开放其原生协议；不支持的组合直接报错，不能静默改测其他端点。详细参数与协议矩阵见 [管理员账号连接测试](../interfaces/http_api.md#account_connection_tests)。

## 额度与能力探测

平台可维护独立的上游额度快照：OpenAI/Codex 窗口、Gemini tier/model quota、Antigravity credits、Grok 计费/媒体资格、Qoder Credits，以及 Kimi/Zhipu/DeepSeek/MiniMax 的统一用量监控快照等。快照用于调度、容量展示和诊断，不是 TokenRouter 用户余额或订阅账本。

OpenAI 重置次数查询把带到期时间的完整结果保存为账号展示快照；上游只返回正数次数却缺少到期明细时，实时结果仍返回给调用方，但旧快照必须保留。账号用量单元格可在查询次数后手动确认重置，Spark 影子账号需在母账号操作。只有已知成功码且实际重置窗口数为正时，服务才在脱离客户端取消信号的有界上下文中恢复账号 error、限流和临时不可调度状态，再回读额度快照与最新账号投影；恢复不修改人工 `schedulable` 开关。HTTP 200 的无次数或未知结果不进入恢复流程。后续步骤部分失败时响应使用 `cache_refreshed`、`account_state_recovered` 和 `warning_code` 明确区分，调用方不得把已消费的次数当作可重试失败；网络结果不明确时先查询再判断，不自动重复消费。完整交互与结果判断见[管理员手动重置上游额度](../interfaces/openai_upstream.md#openai_quota_reset)。

Codex 积分与重置卡次数分开展示和持久化。手动查询额度后，原始十进制积分余额、是否不限量和观测时间保存在 `extra.codex_credits_snapshot`；重置卡详情失败不丢失积分结果，成功查询缺少积分字段则清除旧积分快照。影子账号查询复用父账号认证，但展示快照保存到实际查询的账号行。积分展示不参与本地账务，也不增加自动扣取或重置操作。

管理员可通过 `POST /api/v1/admin/openai/accounts/:id/referrals/refresh` 查询邀请资格，通过 `POST /api/v1/admin/openai/accounts/:id/referrals/invite` 向单个邮箱发送邀请。两者使用账号现有代理、TLS 路由、OAuth/Agent Identity 与身份请求头；邀请快照只用于显示，不改变调度。发送前服务端重新检查资格、套餐对应活动、发送/奖励名额和必要确认；影子账号只能查询，发送须使用父账号。邀请发送没有幂等凭据，不自动重试或跟随重定向；网络断开导致结果不确定时必须提示先核对 Codex 状态。已确认发送成功后，刷新或持久化失败仍返回发送成功和相应告警，避免重复提交。原有邀请重置入口与只查询重置次数的操作保持独立。

OpenAI API Key 的 Responses 探测只维护 `extra.openai_responses_probe_status`，取值为 `supported`、`unsupported`、`unknown`；管理员路由策略 `extra.openai_text_route_mode` 和 HTTP continuation 能力开关 `extra.openai_responses_continuation_supported` 不属于探测服务，任何探测结果都不得覆盖它们。2xx 响应若仍因 `max_output_tokens` 未完成，或响应状态为 `failed`，应保持 `unknown`；完成但没有 `function_call` 的响应判定为 `unsupported`。网络错误、响应读取失败和其它结论不足的结果保留最近状态。账号默认连接测试以 Responses 为首选协议，路由策略显式强制 Chat 时才使用 Chat 测试路径。HTTP continuation 缺失时按关闭处理；嵌套 Sub2API 账号应显式关闭，直连且确认支持的 API Key 账号才开启。

账号创建、编辑、批量更新和导入会把旧 OpenAI 配置规范化为新字段；复制账号保留 `credentials.openai_workload_capabilities`、`openai_text_route_mode` 与 continuation 能力开关，丢弃已有探测状态并重新探测。调度投影必须同时包含工作负载集合、路由策略、探测状态和 continuation 能力开关，探测状态变化要触发投影失效。Ollama Cloud 等兼容 API Key 上游可以保存其管理会话和用量快照，但只有明确匹配的账号才进入探测，不能把探测协议推广到所有 `apikey`。Grok 计费与媒体资格、各平台额度探测也继续使用各自独立协议。

通用的上游声明倍率探测已移除，不再有定时任务、手动操作、快照或公开账单自省接口。账号创建、编辑、批量更新、复制、CRS 同步和仓储写入都会丢弃历史 `upstream_billing_probe` 与 `upstream_billing_probe_enabled` 键；这项清理不得影响 Ollama Cloud 会话/用量、endpoint capability 或其它额度状态。

实时探测失败时保留最近成功快照并同时暴露当前错误，不把旧数据标为实时。任何配额耗尽或 capability 变化都要触发相关调度投影失效。

<a id="ollama_async_reset"></a>
## Ollama Cloud 异步窗口恢复

仅 OpenAI/Anthropic 平台中明确匹配官方 Ollama Cloud 的 API Key 账号适用。真实上游 429 先建立新的限流代次，恢复时间保留现值、Retry-After 与既有兜底中的有效下限，再异步查询已配置管理会话对应的用量窗口；人工查询本身仍只用于展示。该功能是确认上游窗口自然重置时间，不调用付费重置或充值接口。

探测不阻塞请求内故障转移，使用有界队列、同 Key 去重和失败退避，单次最多 45 秒。只有已耗尽窗口均有有效未来 reset 时，才按其中最晚时间延长冷却；查询失败或字段不足保留原冷却。异步写回再次校验管理会话、账号配置、代理、当前快照和限流代次，使用原子条件更新及调度 outbox。即便连续 429 的 reset 相同，每次也推进独立代次；管理员清空限流或较新 429 后，旧回调不能重新停调账号，也不能缩短更晚的恢复时间。

## API Key 上游用量查询

API Key 上游用量由独立的 `UpstreamUsageService` 提供，和 OAuth/Setup Token 的 `AccountUsageService` 语义分离。它只服务管理员展示，不参与调度、自动暂停、倍率、本地配额或结算；列表加载、滚动和自动刷新都不会产生上游流量。管理员手动查询时，服务按账号和规范化配置指纹合并并发请求，单次约 60 秒超时、512 KiB 响应体上限、禁止重定向，并复用代理、TLS 指纹、Header Override 和既有 `HTTPUpstream`。

配置缺失时，普通 API Key 默认启用 Sub2API；New API 和 Zivv 必须显式选择对应适配器。CN API Key 则按平台/mode 自动选择 Kimi 余额、Kimi Coding、Zhipu Coding 或 DeepSeek 多币种余额适配器，Zhipu payg 明确不支持。Sub2API 的钱包负余额可以展示，`remaining=-1` 只在适配器内部转换为 `unlimited=true`。New API 用 `/api/usage/token/` 读取 Key 配额，再通过固定的钱包端点或受保护的用户访问令牌查询用户钱包；只有钱包结果进入 `balance`，Token 配额进入 `limits`/`subscription`。Zivv 用 `/v1/user/balance` 同时读取钱包、累计用量、Key 限额和套餐，`key_limit=0` 显示为不限量。`unlimited_quota=true` 时忽略上游可能溢出的额度字段，状态接口失败时使用默认单位比例；官方实例未配置用户访问令牌且不提供 API Key 钱包端点时返回 `UPSTREAM_USAGE_WALLET_UNAVAILABLE`，不把 Token quota 当钱包余额。手动查询失败不修改账号状态或运行快照，也不自动回退到另一个协议。

MiniMax Coding 使用固定 `minimax_coding` 适配器查询五小时与每周百分比窗口；MiniMax payg 返回不支持且不发送查询。详情见 [MiniMax 上游](../interfaces/minimax_upstream.md#minimax_usage_and_limits)。

浏览器只缓存成功的归一化结果五分钟，缓存键隔离管理员身份、账号 `updated_at`、代理/Base URL 和配置；失败不缓存，账号凭据、代理或配置变化立即失效。审计仅记录管理员动作和脱敏元数据，不记录 API Key 或上游原始响应。该功能与已移除的 `upstream_billing_probe` 完全不同，不恢复旧的自动倍率探测。

CN 周期监控默认关闭；启用后只把统一快照写入 `extra.cn_usage_monitor_snapshot`，用身份 hash 和账号 `updated_at` CAS 防止旧探测覆盖新凭据。失败保留最近成功结果，余额低于阈值只写带同一身份 hash 的临时不可调度原因，恢复也只清理由该身份创建的状态。多实例同轮由 leader lock 串行化，自定义中继必须命中启用的 URL allowlist。详细字段、适配器和超时语义见[API Key 上游用量查询](../interfaces/upstream_usage.md)。

## 运维诊断

- 观察每 provider 的候选数、刷新成功/失败、节流、超时和最长积压，而不只看总成功率。
- 关联账号测试、刷新、quota probe、代理健康和调度过滤原因，区分凭据故障与出站网络故障。
- 检查账号数据库状态、当前进程投影和跨实例失效是否一致；手工改数据库后等待周期重建不等于即时生效。
- 自动恢复或批量导入后抽查实际协议，避免仅凭 token endpoint 成功误判推理可用。
- OpenAI Chat 排障应同时核对入站协议、`openai_text_route_mode` 和 Usage Log 的 `upstream_endpoint`；默认模式应记录 `/v1/chat/completions`，不能因探测为 `supported` 而变成 `/v1/responses`。

相关文档：[上游账号能力矩阵](../interfaces/upstream_account_matrix.md)、[账号调度与缓存一致性](../architecture/account_scheduling_and_cache.md)、[上游传输安全](upstream_transport_security.md)。
