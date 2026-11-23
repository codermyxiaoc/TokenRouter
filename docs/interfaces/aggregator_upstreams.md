# TypeSafe、Cline 与 Command Code 上游

本文描述三个独立 API Key 平台的账号、协议、模型目录和上游钱包边界。它们复用现有分组、渠道、订阅、用户平台额度、代理和错误策略，不改变独立 Video/Qoder、前缀复合 Key 或智能路由的结构，也不引入上游 Composite 分组与利润控制。

<a id="provider_protocols"></a>
## 账号与协议

三平台仅接受 `type=apikey`、`account_mode=payg`，创建和编辑共用 `AdditionalProviderFields.vue`，API Key、模型白名单/映射、Header Override、代理、本地配额与优先级继续使用原有组件。账号平台必须与分组匹配；用户日/周/月额度按 `typesafe`、`cline`、`command_code` 分开结算。平台选择不会赋予图片、视频、WebSocket、远程 Compact 或其它媒体能力。

| 平台 | 默认 Base URL | 上游协议 | 默认测试模型 |
| --- | --- | --- | --- |
| TypeSafe | `https://api.typesafe.ai` | 仅同步 `POST /v1/systemone` | `jev-latest` |
| Cline | `https://api.cline.bot/api/v1` | Chat Completions；本站 Messages/Responses 使用现有转换链 | `deepseek/deepseek-v4-flash`，有效 ClinePass 快照优先其模型 |
| Command Code | `https://api.commandcode.ai/provider/v1` | 自适应或固定 Chat、Responses、Messages | `deepseek/deepseek-v4-flash` |

TypeSafe 模型广场、套餐预览和 `/v1/models` 按分组内可调度账号的配置生成公开目录。仅配置最终白名单时，其中的具体型号可分别作为 `POST /v1/systemone` 的 `model`。例如，将 `jev-1.13`、`jev-1.14`、`gemini-decision-1` 和 `gpt-6-luna-decision` 加入白名单后，客户端可分别使用这四个 ID。未配置型号的账号继续提供 `jev-latest`。

使用模型映射时，客户端提交公开名，网关按渠道和账号规则解析实际部署，仅改写请求体顶层的 `model`，保留部署名的大小写。具体型号只要显式列入最终白名单，即使同时是另一条映射的目标，也可以直接选择。只出现在映射目标中、没有独立公开声明的部署名不自动列出。管理员仍可探测账号配置的别名和部署。广场报价与用户结算均查该公开型号的价目，Ops 和用量映射链记录实际投递目标。要对自定义型号收费，可按公开 ID 配置分组或渠道价格；系统找不到可用定价时，广场显示未定价，并依现有缺价策略记录零成本用量。

TypeSafe 分组不提供文本入站协议开关或 OAuth 配置。Jev 请求校验拒绝重复键、保留字段大小写混淆、非法问题结构和流式参数；`state` 与完整问题对象均进入内容审核，响应强制安全 JSON Content-Type；必须含合法非负整数输入/输出计量，缺失计量不能作为零费用成功。响应中的 `usage` 和计量字段不得重复（包括大小写与转义同名键），数值按十进制精确验证，不经浮点舍入；数字字符串和数学上为整数的指数写法继续兼容，单项上限保持 `2^40`。OpenCode Zen 的 `jev-1.13` 和 `jev-1.13-free` 保留原有准入与价格规则；TypeSafe 的 `jev-latest` 即使映射到同名免费部署，仍按公开的 `jev-latest` 计价。内容审核客户端同时兼容未带 `/v1` 和已带 `/v1` 的配置，以及数字/数字字符串用量。

Command Code 的 `api_protocol` 默认 `adaptive`。`api_base_urls` 可分别覆盖三协议；空覆盖继承当前 Base URL，不能将中继 Key 自动发送到官方。Messages 在未单独覆盖时去掉根地址末尾 `/v1` 后拼接原生路径。显式固定协议优先；存在 `protocol_rules` 时按管理员规则选择，显式空数组表示 Chat 兜底；未配置规则才查询协议目录与内置模型族规则。Command Code 不启用 OpenAI 专属历史裁剪或续接缓存。Cline 非流式 `{success,data}` 封装在三类入站统一解包，每个 choice 必须有可解析的消息结构；只有 null、索引或空对象的 choices、重复结构字段以及失败封装不能返回空答案并计费。合法工具调用、拒绝、思考和显式空完成继续保留。模型映射后再选择协议，Claude 等带供应商前缀的模型在计费与转发之间保持一致。

<a id="model_catalog"></a>
## 模型目录与测试

Command Code 从账号 Chat Base URL 的 `/models` 读取 `supported_endpoints`。目录仅决定协议，不扩大独立白名单、渠道定价或分组权限。缓存按账号、地址及凭据/组织请求头/代理/TLS 身份摘要隔离，成功缓存 30 分钟、错误退避 5 分钟；首次刷新共享最多 2 秒等待并响应取消，刷新期间保留旧目录。后台请求限时 15 秒、响应限 2 MiB，禁止重定向，继续执行既有 URL、代理和受保护请求头规则。刷新日志只保留账号、来源主机、模型数或错误类型，不记录 URL 的用户信息、路径、查询、片段及可能含凭据的传输错误文本。

账号连接测试、渠道探测和模型下拉使用平台能力：TypeSafe 自动/SystemOne，Cline 自动/Chat，Command Code 自动或三种文本协议。测试采用映射后模型，错误日志以 `test_id` 关联，不打印原始上游响应或凭据。模型同步失败保持原配置；Antigravity 显式模型限制优先于默认目录。

<a id="provider_wallets"></a>
## 上游用量与冷却

管理员手动查询只展示结果；后台监控仍须显式开启，并以现有统一快照、查询身份摘要和数据库 CAS 处理。平台钱包不是用户本地余额，不修改用户倍率、订阅或本站结算。原生查询只在转发根地址为对应官方 HTTPS 主机时启用；自定义中继使用通用查询适配器，TypeSafe 也使用通用查询配置。

- Cline 查询用户、积分余额与 ClinePass 窗口；微美元转 USD，窗口单独标为百分比。普通积分模型使用 `cline:credits` 冷却，`cline-pass/*` 使用 `cline:pass`，`cline-free/*` 不借用两者。缺失订阅、积分耗尽与窗口耗尽各自作用于对应钱包；已耗尽但缺重置时间时使用有限复查冷却。组织费用上限、推理并发和管理员停调不能被健康余额误清除。
- Command Code 必须先查询 `whoami` 确定组织身份，再查询三类积分、5 小时/周窗口及订阅周期用量。购买积分、月度积分、免费积分分别展示；订阅总量以本周期使用量加剩余还原。购买积分大于零时套餐窗口不参与冷却或可配置阈值；组织/成员费用上限仍有效。周期查询失败保留完整的上次成功快照，不把缺失样本解释成零用量，也不主动清除旧冷却。
- 上游结构化余额、费用上限和窗口错误由平台专用分类处理；管理员自定义错误码策略仍先执行。Cline 只读取错误主体，不从 metadata 猜错误码。恢复只清除自己负责的原因，不覆盖其它模型、钱包或管理员状态。

OpenCode GO 的 `/usage` 403 只表示该接口不可用，不判定推理 Key 失效；429 缺有效窗口时可用不超过 24 小时的 `Retry-After`。更完整的展示、身份与缓存约束见[上游用量查询](upstream_usage.md)，本站额度见[用户平台额度](../domains/platform_quotas.md)。
