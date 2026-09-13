# 创作台未推送更改审查记录

> 状态：待人工批注。
>
> 本文件记录当前 `main` 相对 `origin/main` 的创作台（图片生成）相关未推送更改审查结果。请以人工批注为最终裁决；“确认问题”不等于必须按建议原样实现，设计取舍和约束请写在批注中。

## 批注方式

请直接在每项末尾填写：

- `你的判定`：`确认问题`、`设计取舍`、`不处理`、`需要讨论` 或其它自定义结论。
- `你的批注`：补充产品语义、部署约束或期望实现方式。
- `最终处理`：修复完成后填写改动、测试和仍保留的风险。

## 审查范围

- 分支：`main`
- 对比基线：`origin/main`，审查时共同祖先 `800cb8f9965baf3a755d610963288e0ddd2e2482`
- 当前 HEAD：`28ad489cef3b04155d879ac8c2a51788d2cc79da`
- 未推送提交：70 个
- 累计差异：156 个文件，`+37,418/-178`
- 工作区：没有已跟踪的额外修改；未跟踪的 `.agents/plans/` 和 `diagnostics/` 目录不属于本次 Git diff

## 优先级说明

- `P1`：会导致图片生成失败、重复调用/扣费、结果或余额丢失、跨用户数据泄露，或使任务不可恢复，建议合并前处理。
- `P2`：在特定规模、配置或交互下会产生错误或明显架构/体验风险，建议发布前处理或明确记录接受风险。

## P1 初步发现

### TR-CS-REV-001 队列缺少租约 fencing，旧 worker 可篡改新 worker 状态

- 位置：`backend/internal/repository/creative_queue.go:198`、`backend/internal/repository/creative_queue.go:302`、`backend/internal/service/creative_worker.go:282`
- 现象：`Ack`、`RequeueAfter` 只按 `runID` 操作；心跳和刷新没有把 lease token/epoch 贯穿到状态变更，刷新 Lua 返回失去所有权时也未被当作错误终止。
- 触发与影响：worker A 在 Redis 分区后失去租约，恢复扫描将任务交给 worker B；A 恢复后仍可 ACK、重排队或写失败状态，造成重复上游请求、结果覆盖、错误计费和状态回退。
- 建议：所有 ACK/重排队/心跳/恢复操作使用 lease token 或 epoch fencing；刷新必须返回明确的 ownership bool，失去租约的 worker 禁止提交结果；增加 Redis 分区测试。
- 你的判定：确认问题
- 你的批注：
- 最终处理：

### TR-CS-REV-002 OpenAI GPT Image 请求无条件发送不支持的 `response_format`

- 位置：`backend/internal/service/creative_executor_openai.go:94`、`backend/internal/service/creative_executor_openai.go:136`、`backend/internal/service/creative_public.go:385`
- 现象：GPT Image 生成和编辑请求都发送 `response_format=b64_json`。
- 触发与影响：默认 OpenAI endpoint 对 GPT Image 模型会返回 400，创作台的直连 OpenAI 路径无法生成图片。当前官方文档说明 `response_format` 仅用于 DALL-E，GPT Image 固定返回 base64。[OpenAI image generation reference](https://developers.openai.com/api/reference/resources/images/methods/generate)
- 建议：按模型能力条件发送该字段，GPT Image 请求省略；用严格的请求体/Multipart contract test 锁定协议。
- 你的判定：确认问题
- 你的批注：
- 最终处理：

### TR-CS-REV-003 inpaint mask 的 alpha 语义反转，且尺寸限制过宽

- 位置：`frontend/src/components/creative/CreativeCanvas.vue:1509`、`frontend/src/components/creative/CreativeCanvas.vue:507`、`backend/internal/service/creative_executor_openai.go:127`、`backend/internal/service/creative_public.go:1091`
- 现象：mask 从透明画布开始，用户涂抹区域被画成不透明白色；OpenAI 约定 alpha 为 0 的透明区域才是要编辑的区域，涂抹区域因此会被保留，未涂抹区域反而被重绘。[OpenAI image-edit types](https://github.com/openai/openai-python/blob/main/src/openai/types/image_edit_params.py)
- 触发与影响：核心 inpaint 交互与用户视觉反馈相反；另外代码允许单个 mask 最大 32 MiB，而上游要求 PNG 小于 4 MiB 且与原图尺寸一致，大 mask 会在上游才失败。
- 建议：以不透明底图开始，使用 destination-out 清除用户涂抹区域；导出前校验尺寸和 4 MiB 限制；增加像素级 mask 测试。
- 你的判定：确认问题
- 你的批注：
- 最终处理：

### TR-CS-REV-004 输出在结算完成前可读取/ACK，ACK 也不是原子的

- 位置：`backend/internal/service/creative_public.go:1613`、`backend/internal/service/creative_public.go:1450`、`backend/internal/service/creative_public.go:1531`、`backend/internal/service/creative_public.go:1661`
- 现象：输出和 output `succeeded` 先写入，余额 capture 与 run 终态在后；读取接口只检查 output 状态，ACK 先删除 Redis 再写数据库。取消场景在 provider 成功后提前返回，跳过 `recordCreativeUsageLog`。
- 触发与影响：capture/终态写入失败时，用户可能拿到未结算结果；ACK 数据库失败会让后续读取变成 result lost；取消成功请求会缺少用量记录。
- 建议：引入 `provider_succeeded/settlement_pending`，只有 capture 和终态提交完成后才开放读取/ACK；删除与数据库 ACK 使用可恢复顺序；取消分支复用统一结算和用量记录流程。
- 你的判定：确认问题
- 你的批注：
- 最终处理：

### TR-CS-REV-005 结算错误会重新执行已成功的付费 provider 请求

- 位置：`backend/internal/service/creative_worker.go:357`、`backend/internal/service/creative_worker.go:402`、`backend/internal/service/creative_worker.go:479`、`backend/internal/service/creative_worker.go:282`
- 现象：每轮重试都会重新执行 `Prepare` 和 `Execute`；`SucceedRun` 失败后继续重试，达到上限时返回 `Terminal=true` 并 ACK 队列，却没有写入失败或 settlement pending 状态。
- 触发与影响：数据库/计费短暂故障可能导致同一图片被上游调用多次、重复扣费，最后 run 仍停留在非终态且没有可恢复队列记录。
- 建议：持久化 provider 结果引用和幂等键；provider 调用只执行一次，后续只重试 capture/落库；达到重试上限进入可扫描的 `settlement_pending`，绝不能静默 ACK 非终态任务。
- 你的判定：确认问题
- 你的批注：
- 最终处理：

### TR-CS-REV-006 临时存储的基础设施错误被永久终结为 `result_lost`

- 位置：`backend/internal/service/creative_worker.go:316`、`backend/internal/service/creative_worker.go:426`、`backend/internal/repository/creative_transient_store.go:68`、`backend/internal/service/creative_public.go:1473`
- 现象：payload/output 读取时，key 不存在、TTL 到期、Redis timeout、连接断开和反序列化错误都走同一条 `result_lost` 路径；标记失败出错时仍继续 ACK。
- 触发与影响：短暂 Redis 故障会释放不了结果、永久丢图并终止任务；用户无法重试，后台也没有补偿依据。
- 建议：区分 not-found/expired 与 infrastructure error；后者重试并返回 503，不得 ACK；`MarkResultLost` 失败时保留队列并由 reconciler 补偿。
- 你的判定：确认问题
- 你的批注：
- 最终处理：

### TR-CS-REV-007 hold/allowance 释放失败没有可靠恢复

- 位置：`backend/internal/service/creative_public.go:1694`、`backend/internal/service/creative_public.go:1762`、`backend/internal/service/creative_worker.go:319`、`backend/internal/service/creative_public.go:812`
- 现象：失败流程先把 run 写成终态，再释放 hold；释放失败只返回错误或被忽略，下一轮看到终态后直接 ACK。创建回滚也显式忽略 release 错误。
- 触发与影响：钱包 hold、订阅额度或 API key quota 可能长期冻结；这类错误没有待处理状态，无法通过现有清理任务恢复。
- 建议：增加 `release_pending`/outbox 和定期 reconciler，只有释放完成或明确记录待补偿后才认为结算完成；回滚错误必须持久化并告警。
- 你的判定：确认问题
- 你的批注：
- 最终处理：

### TR-CS-REV-008 创建回滚使用过期的 allowance 标记，可能泄漏配额

- 位置：`backend/internal/service/creative_billing.go:101`、`backend/internal/repository/usage_billing_repo.go:390`、`backend/internal/repository/usage_billing_repo.go:433`
- 现象：数据库预留后只更新局部 `cmd.AllowanceReserved=true`，回滚仍传入 `AllowanceReserved=false` 的旧 run。
- 触发与影响：Redis 保存或入队失败时钱包 hold 可能释放，但 API key/member allowance 仍被消耗。
- 建议：预留成功后同步更新持久化 run，或释放时根据数据库事实推导，不要依赖调用方的旧内存对象；增加创建失败回滚测试。
- 你的判定：确认问题
- 你的批注：
- 最终处理：

### TR-CS-REV-009 创建流程没有 durable saga/outbox，且队列关闭会假成功

- 位置：`backend/internal/service/creative_public.go:780`、`backend/internal/service/creative_public.go:818`、`backend/internal/service/creative_worker_runtime.go:81`
- 现象：DB run、余额 hold、Redis payload、队列入列是多个独立步骤，没有持久化 provisioning phase 或 outbox；队列为空/关闭时仍可能返回 queued 成功。
- 触发与影响：进程崩溃或 Redis 故障会留下已扣款但永远不执行的任务，payload 到期后无法恢复；这也与文档中“Redis 不可用时创建应失败”的语义不一致。
- 建议：使用 durable provisioning 状态和 outbox/replay scanner；队列不可用时 fail-closed；为每个阶段增加崩溃恢复和部分 Redis key 清理测试。
- 你的判定：确认问题
- 你的批注：
- 最终处理：

### TR-CS-REV-010 managed hidden key 首次创建存在并发竞态

- 位置：`backend/internal/service/creative_public.go:1318`、`backend/internal/repository/api_key_repo.go:73`、`backend/migrations/253_creative_studio_runs.sql:5`
- 现象：先查询再插入，没有可靠唯一约束或 insert-on-conflict；隐藏 key 还会计入 APIKeyLimit，但列表接口隐藏它。
- 触发与影响：两个并发首次请求可能产生多个 managed key，后续 `.Only()` 查询失败；用户看不到占用额度的 key，却可能无法继续创建。
- 建议：增加部分唯一索引，并使用冲突重查或数据库锁；明确隐藏 key 是否计入限额并在 API 中展示占用原因。
- 你的判定：确认问题
- 你的批注：
- 最终处理：

### TR-CS-REV-011 Gemini 自定义 base URL 校验失败会静默回退官方地址

- 位置：`backend/internal/service/creative_executor_gemini.go:85`、`backend/internal/service/creative_executor_gemini.go:137`
- 现象：自定义 URL 校验发生任何错误时返回官方 Google base URL，随后仍携带原 API key/OAuth 和用户图片发出请求。
- 触发与影响：代理配置错误、域名被阻断或恶意配置都可能导致数据发送到错误的计费/隐私边界；同仓库的 Gemini 兼容路径已经采用 fail-closed，当前行为不一致。
- 建议：校验失败直接返回明确错误，不得自动切换目标；增加“无 transport call”的回归测试。
- 你的判定：确认问题
- 你的批注：
- 最终处理：

### TR-CS-REV-012 上游错误可能泄露 prompt/凭据，字节截断还可能制造重试循环

- 位置：`backend/internal/service/creative_executor_openai.go:63`、`backend/internal/service/creative_executor_grok.go:68`、`backend/internal/service/creative_executor_gemini.go:120`、`backend/internal/service/creative_executor.go:43`、`backend/internal/service/creative_public.go:1880`
- 现象：HTTP 4xx/5xx body 经简单截断后直接写入 run error 和 moderation `FailedUnits.Error`，没有敏感信息清理；`message[:500]` 按字节截断可能生成非法 UTF-8。
- 触发与影响：上游回显 prompt、内部 URL 或 token 时会持久化并返回给用户；非法 UTF-8 可能使失败记录写库失败，导致任务不断重试。
- 建议：对外只返回稳定的通用错误码和短消息，详细 body 仅保留脱敏日志；使用 rune-safe 截断并校验 UTF-8；增加回显 secret 和多字节错误测试。
- 你的判定：确认问题
- 你的批注：
- 最终处理：

### TR-CS-REV-013 账号模型映射绕过最终图片模型白名单

- 位置：`backend/internal/service/creative_public.go:681`、`backend/internal/service/batch_image_public.go:1476`、`backend/internal/service/batch_image_public.go:705`
- 现象：映射路径只检查匹配规则，未对映射后的最终模型再次执行 `IsModelSupported` 和图片能力判断。
- 触发与影响：不支持的别名可能被目录展示并进入预留，直到调度阶段才失败；反向映射还可能把文本模型误作为图片模型返回。
- 建议：对 requested、mapped、final 三个模型统一执行最终白名单/图片谓词；目录、价格、预留和调度共用同一 capability helper。
- 你的判定：确认问题
- 你的批注：
- 最终处理：

### TR-CS-REV-014 GPT Image 1 的 2K/比例能力矩阵不正确

- 位置：`backend/internal/service/creative_public.go:460`、`backend/internal/service/creative_executor.go:323`
- 现象：目录暴露 GPT Image 1 的 2K，执行器将其映射到 2304x1536 等尺寸，并把 4:3/16:9 折叠为 3:2。
- 触发与影响：上游严格校验时返回 400，或用户选择的比例没有被尊重。当前 OpenAI 文档/SDK 对不同 GPT Image 版本允许的尺寸不同，不能用一套任意尺寸映射。[OpenAI image generation reference](https://developers.openai.com/api/reference/resources/images/methods/generate)
- 建议：建立 model × operation × tier × ratio 的能力矩阵；不支持的组合在目录和请求校验阶段都拒绝；为每种组合增加 wire test。
- 你的判定：不处理
- 你的批注：
- 最终处理：

### TR-CS-REV-015 IndexedDB 未按 authenticated user 隔离，存在跨账号隐私泄露

- 位置：`frontend/src/utils/creativeLocalStore.ts:11`、`frontend/src/composables/useCreativeStudio.ts:54`、`frontend/src/components/creative/CreativeCanvas.vue:219`、`frontend/src/stores/auth.ts:417`
- 现象：数据库、canvas、settings 和图片 key 都是全局固定值；登出只清理 token/user，不清理或切换本地命名空间。
- 触发与影响：用户 A 登出后用户 B 在同一浏览器登录，可以恢复 A 的 prompt、画布、图片和参数。
- 建议：所有本地 key 以 user ID + workspace ID 命名；账号切换时阻断旧数据恢复并清理/迁移；增加同浏览器双账号测试。
- 你的判定：不处理
- 你的批注：
- 最终处理：

## P2 初步发现

### TR-CS-REV-016 前端只轮询最近 20 条任务，较早 active run 会消失

- 位置：`frontend/src/composables/useCreativeStudio.ts:629`、`frontend/src/api/creative.ts:133`、`backend/internal/handler/creative_handler.go:234`
- 现象：前端固定请求第 1 页 20 条，API 忽略 `_page`，后端固定 offset 0 且丢弃 `has_more`。
- 触发与影响：任务超过 20 条后，较早的 queued/running run 不再展示、轮询或收割，最终可能等到 transient TTL 到期。
- 建议：增加 active-runs 游标接口，或至少正确实现分页并始终保留已知 active ID；输出列表只返回元数据，不要每次加载完整内容。
- 你的判定：确认问题
- 你的批注：
- 最终处理：

### TR-CS-REV-017 清理本地数据会放弃仍在执行且可能已付费的任务

- 位置：`frontend/src/composables/useCreativeStudio.ts:737`、`frontend/src/composables/useCreativeStudio.ts:554`、`frontend/src/i18n/locales/zh/creative.ts:100`
- 现象：清理动作停止轮询、删除 IndexedDB 并旋转 workspace，后端任务仍继续执行和结算；harvest 可能在清理之后写回旧数据。
- 触发与影响：用户看不到仍在运行的任务，任务仍可能扣费；当前“不会影响账户余额”的文案与实际生命周期不符。
- 建议：有 active run 时阻止清理或明确二次确认；实现取消/退休 workspace 语义；清理和 harvest 使用代次或事务化检查。
- 你的判定：不处理
- 你的批注：
- 最终处理：

### TR-CS-REV-018 幂等键会永久绑定 provisioning 失败记录，且前端会污染新表单

- 位置：`backend/internal/service/creative_public.go:749`、`backend/internal/service/creative_public.go:807`、`frontend/src/composables/useCreativeStudio.ts:418`
- 现象：同一个 idempotency key 总是重放已有 run，即使该 run 是 Redis/入队失败产生的 failed row；前端请求失败后保留旧 key，prompt/model 改变也不重置。
- 触发与影响：一次暂时性创建失败会让同 key 永远失败；超时后用户修改表单会持续得到冲突，直到刷新或清理。
- 建议：把 provisioning failure 设计成可修复状态并支持重放；并发插入冲突时回查现有 run；根据表单 fingerprint 在字段变化时生成新 key。
- 你的判定：不处理
- 你的批注：
- 最终处理：

### TR-CS-REV-019 用户费率读取错误会静默回退 group rate

- 位置：`backend/internal/service/creative_public.go:1268`、`backend/internal/service/batch_image_public.go:1142`
- 现象：creative 路径忽略 `UserGroupRateRepo.GetByUserAndGroup` 的所有错误，使用 group rate；batch 路径则 fail-closed，行为不一致。
- 触发与影响：数据库短暂故障时，预估价和 hold 可能低于或高于用户实际费率。
- 建议：除明确的“无用户覆盖”外，其它读取错误直接失败；补充费率缺失、超时和回退边界测试。
- 你的判定：不处理
- 你的批注：
- 最终处理：

### TR-CS-REV-020 CORS 没有允许 `Idempotency-Key`

- 位置：`backend/internal/server/middleware/cors.go:53`、`frontend/src/api/creative.ts:119`
- 现象：前端创建请求始终发送 `Idempotency-Key`，CORS `Access-Control-Allow-Headers` 没有该 header。
- 触发与影响：跨域部署会在浏览器 preflight 阶段被拒绝，创作台无法创建任务；现有 CORS 测试未覆盖该 header。
- 建议：加入允许列表和 OPTIONS 回归测试。
- 你的判定：确认问题
- 你的批注：
- 最终处理：

### TR-CS-REV-021 静态 feature flag 与公开设置不一致，关闭功能会阻断已付费任务读取

- 位置：`backend/internal/service/creative_public.go:133`、`backend/internal/service/setting_public.go:330`、`frontend/src/router/index.ts:965`、`backend/internal/service/setting_service.go:645`
- 现象：前端入口只看 DB public flag，API admission 同时看静态配置；静态关闭时 UI 仍显示但接口 404。run/output/read/ack 也全部受 admission gate 影响，DB 读取错误还可能默认 fail-open。
- 触发与影响：部署关闭新任务后，已经排队或已付费的任务无法取回；运维开关改变了数据可访问性而不只是 admission。
- 建议：分离“接受新任务”和“读取/结算已有任务”的开关；公开设置同时反映静态配置；数据库配置读取失败应有明确 fail-closed 策略并告警。
- 你的判定：不处理
- 你的批注：
- 最终处理：

### TR-CS-REV-022 画布 mask 在旋转、切换锚点和多选时可能错位

- 位置：`frontend/src/components/creative/CreativeCanvas.vue:1508`、`frontend/src/components/creative/CreativeCanvas.vue:881`、`frontend/src/components/creative/CreativeCanvas.vue:768`
- 现象：旋转图片后用轴对齐 bounding box 裁剪并拉伸 mask；切换 inpaint 图片不清理/重绑定旧笔画；Fabric 7 的 `selection:updated` 只提供增删对象，当前逻辑会用增量数组覆盖完整选区。
- 触发与影响：mask 与源图像素不对齐，切换图片时旧笔画被错误复用，选择多个对象时之前选中的图片引用丢失。
- 建议：使用源图逆变换生成 mask 或禁止旋转；按锚点存储笔画并在切换时清理；通过 `canvas.getActiveObjects()` 获取完整选区；增加旋转、切换和多选测试。
- 你的判定：不处理
- 你的批注：
- 最终处理：

### TR-CS-REV-023 Gemini inline 图片请求可能超过官方 20 MB 限制

- 位置：`backend/internal/service/creative_executor_gemini.go:172`、`backend/internal/service/creative_public.go:1091`
- 现象：Gemini executor 始终把图片转成 inline base64；本地允许单图 32 MiB、总计 64 MiB。官方文档要求 inline image 请求总大小小于 20 MB，较大输入应使用 File API。[Gemini image understanding](https://ai.google.dev/gemini-api/docs/generate-content/image-understanding?hl=en)
- 触发与影响：大图在本地校验通过后才被上游拒绝，用户看到 generic provider failure，且 base64 会额外放大内存和请求体。
- 建议：按 provider 限制校验总请求大小；大文件改用 File API 或在界面提前拒绝；配置和目录返回同一套能力上限。
- 你的判定：确认问题
- 你的批注：
- 最终处理：

### TR-CS-REV-024 multipart 读取有硬编码 40 MiB 截断漂移

- 位置：`backend/internal/handler/creative_handler.go:141`、`backend/internal/service/config.go:253`
- 现象：handler 对 multipart part 固定使用 40 MiB `LimitReader`，但服务配置允许 `MaxAssetBytes` 更大，读取后没有检查是否命中上限。
- 触发与影响：配置提升上限后，合法文件会被静默截断，最终以图片解析或 provider 错误失败；全局 HTTP body 上限虽存在，但不能修复这类配置漂移。
- 建议：统一使用配置值并检查 `limit+1` 字节是否存在，超过上限返回明确 413；增加边界文件测试。
- 你的判定：确认问题
- 你的批注：
- 最终处理：

### TR-CS-REV-025 临时存储清理可能用 `KEYS` 阻塞共享 Redis

- 位置：`backend/internal/repository/creative_transient_store.go:184`
- 现象：计数不可用时清理路径回退到全库 `KEYS`，在共享 Redis 上会产生阻塞；终态 run 清理 scanner 也已定义但未被可靠调度使用。
- 触发与影响：任务量增长或 Redis 共享部署时，清理本身可能造成延迟尖峰，进一步触发 worker lease 和读取错误。
- 建议：使用确定的 key 计数、分段 `SCAN` 或独立 keyspace；让 terminal/transient reconciler 由明确的后台任务周期执行并可观测。
- 你的判定：不处理
- 你的批注：
- 最终处理：

### TR-CS-REV-026 run 列表和输出下载的规模/超时策略不足

- 位置：`backend/internal/service/creative_public.go:1413`、`frontend/src/composables/useCreativeStudio.ts:627`、`frontend/src/api/client.ts:21`、`frontend/src/utils/creativeLocalStore.ts:238`
- 现象：列表查询对每个 run 做额外 output 查询，前端每 3 秒刷新并加载完整 blob；下载复用全局 30 秒 Axios timeout，浏览器还会全量写回 IndexedDB。
- 触发与影响：大量历史 run 或慢速网络下，数据库/Redis/浏览器负载显著上升，32 MiB 结果容易在下载阶段失败。
- 建议：active 与 history 分离，列表只返回 metadata；批量读取输出状态；下载使用独立的长超时、断点/重试或服务端流式响应；限制本地缓存并做 LRU。
- 你的判定：确认问题
- 你的批注：
- 最终处理：

### TR-CS-REV-027 前后端 prompt、文件类型和大小限制不一致

- 位置：`backend/internal/service/creative_public.go:946`、`frontend/src/composables/useCreativeStudio.ts:54`、`frontend/src/components/creative/CreativeCanvas.vue:1736`
- 现象：后端按配置和字节数校验 prompt，前端硬编码 8000 并按 JavaScript 字符数限制；文件选择器接受任意 `image/*`，后端只接受 PNG/JPEG/WebP。
- 触发与影响：中文/emoji 用户会遇到前后端限制不一致；GIF/SVG 通过前端选取后，跳过裁剪直接进入生成，最后才在后端失败。
- 建议：公开统一 capability/config endpoint；前端使用服务端限制和 Unicode code point 计数；文件选择器与后端 MIME/魔数白名单一致，并在上传前给出明确错误。
- 你的判定：确认问题
- 你的批注：
- 最终处理：

## 建议的整体改造方向

1. 将 run 设计为可恢复的持久状态机，例如 `queued -> running -> provider_succeeded -> settlement_pending -> succeeded`，另设 `release_pending` 和 `result_lost`；provider 调用、结果持久化、capture、usage log 各自具备幂等键。
2. 将队列 lease token/epoch 贯穿领取、心跳、刷新、ACK、重排队和恢复扫描；失去租约的 worker 必须停止所有状态写入。
3. 抽取 provider capability matrix，按 provider、model、operation、尺寸、比例和 mask 规则统一驱动目录、校验、定价和执行器；用真实 wire contract、像素 fixture 和故障注入测试覆盖协议与恢复。
4. 增加 create outbox 和 settlement/release reconciler，避免“DB 成功、队列失败”或“provider 成功、结算失败”进入不可见状态。
5. 前端提供 active-runs 游标接口，按用户/workspace 隔离本地数据，明确取消/清理语义，并将大输出下载改为可恢复传输。

## 已执行的验证

- 后端 creative 相关包：`go test ./internal/service ./internal/handler ./internal/repository ./internal/server`
- 后端带创作台单元测试：`go test -tags unit ./internal/service ./internal/handler ./internal/repository ./internal/server`
- 上述两组包的 `go vet`（含 `-tags unit`）
- 前端测试：307 个测试文件、2144 个测试通过
- 前端 lint、production build 通过；build 有大 chunk warning
- `git diff --check origin/main..HEAD` 通过

现有测试没有覆盖真实 OpenAI 请求契约、Redis 分区后的 lease fencing、结算失败重试、创建 saga 崩溃恢复和多用户 IndexedDB 隔离，因此通过测试不能排除本文件中的 P1/P2 风险。

## 并行审查说明

按要求尝试派出 `gpt-5.6-sol`、`Max` 思考级别的并行审查智能体；一个完整返回，另一个在中断前返回重点，其余多数因服务端 429 未能完成。所有条目均已用本地代码、仓库文档和测试结果交叉复核。
