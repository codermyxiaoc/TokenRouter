# 部署与数据库迁移

本文记录 TokenRouter 构建产物、运行拓扑、首次初始化、数据库迁移、升级和恢复之间的工程边界。逐命令安装步骤由既有部署手册维护；修改启动装配、镜像、Compose、setup、SQL 迁移或在线更新前应先读本文。

## 章节导航

- [构建与运行形态](#构建与运行形态)：修改产物或部署拓扑时读取。
- [初始化与启动](#初始化与启动)：修改 setup、配置或健康检查时读取。
- [迁移执行](#migration_execution)：修改 runner 或迁移格式时读取。
- [新增与同步迁移](#新增与同步迁移)：创建本 fork 迁移或同步上游时读取。
- [升级与恢复](#升级与恢复)：修改更新、备份或回退流程时读取。
- [v1.3 升级与缓存切换](#v1_3_upgrade)：从 v1.2 更新 OpenCode、WS 和账号缓存时读取。
- [WS 池与作用域更新](#ws-连接池与执行作用域更新)：更新连接容量和跨实例会话隔离时读取。
- [数据共享功能下线](#数据共享功能下线)：执行迁移 265 或处理遗留导出对象时读取。

## 构建与运行形态

源码镜像由根 `Dockerfile` 多阶段构建：先生成前端静态资源，再嵌入 Go 二进制，最后加入运行时资源和 PostgreSQL 客户端。`buildx --platform` 决定目标架构，`VERSION`、`COMMIT` 和 `DATE` 构建参数写入版本信息。需要让 Ubuntu 二进制包与镜像使用完全相同的程序时，可先完成一次带 `embed` 的静态 `linux/amd64` 构建，再用 `Dockerfile.goreleaser` 打包该程序；最小上下文仍须包含同次源码的 `backend/resources/` 与 `deploy/docker-entrypoint.sh`，并核对镜像内外程序哈希相同。镜像打包阶段不会修正已经编入程序的版本。

二进制归档与多架构发布的另一条入口由 [GoReleaser 配置](../../.goreleaser.yaml) 定义。当前检出没有 `.github/workflows/`，不能依赖 tag 触发自动发布、自动归档或 `VERSION` 回写；发布范围和验证记录由实际执行的流程决定。

仓库支持以下运行形态：

| 形态 | 权威入口 | 持久依赖与边界 |
| --- | --- | --- |
| 单二进制/systemd | `deploy/install.sh` | 外置 PostgreSQL、Redis；安装器管理二进制版本和服务 |
| 完整 Compose | `deploy/docker-compose.yml`、`docker-compose.local.yml` | 应用、PostgreSQL、Redis；分别使用命名卷或本地目录 |
| 独立应用容器 | `deploy/docker-compose.standalone.yml` | PostgreSQL、Redis 由部署环境提供 |
| 源码开发 Compose | `deploy/docker-compose.dev.yml` | 本地构建应用并启动配套依赖 |
| Apple Container | `deploy/apple-container.sh` | 独立脚本管理容器、卷和健康状态 |

应用至少依赖 PostgreSQL 和 Redis。`/app/data` 或等价 `DATA_DIR` 保存配置、安装锁及本地运维产物；数据库、Redis 和对象存储各有独立生命周期，不能只备份应用数据目录就宣称完成系统备份。

手工二进制更新包包含嵌入前端和迁移的 `sub2api`，并携带 `resources/model-pricing/model_prices_and_context_window.json` 作为离线定价回退资源。默认 `pricing.fallback_file` 相对于进程工作目录解析：标准 systemd 的 `WorkingDirectory=/opt/sub2api` 对应 `/opt/sub2api/resources/model-pricing/`；自定义目录或显式定价覆盖继续沿用原配置。Ubuntu 部署的 `pg_dump`、`psql` 是宿主机独立依赖，不能直接复制 Alpine 镜像中的动态链接客户端。

手工归档应保留程序 `0755` 可执行位，并附带校验和及构建来源。带顶层版本目录的更新包用于手工解压替换，不直接交给期待根部 `sub2api` 的安装器。更新默认离线定价文件时，先识别并备份旧官方文件，再安装同版本资源；用户修改过的回退文件和 `pricing.override_file` 保持原有配置，不能只为更新程序覆盖自定义定价。

<a id="dockerhub_deployment"></a>
### DockerHub 镜像与宿主机数据库端口

标准、本地目录和 standalone Compose 使用 `SUB2API_IMAGE` 选择应用镜像，默认 `coderxiaoc/tokenrouter:v0.1.278-ct-v2.5`，保留 `pull_policy: always`。发布端从当前源码构建程序，使用根 `Dockerfile` 或预编译程序的最小上下文生成 `linux/amd64` 镜像，再按本次发布范围推送 DockerHub；部署端只拉取已发布并验证的指定镜像，不依赖源码或现场编译。开发版仍保留本地构建，Apple Container 仍使用其独立镜像变量。构建、校验、推送与服务器更新命令见 [Docker 镜像说明](../../deploy/DOCKER.md)。

标准和本地目录 Compose 把 PostgreSQL 容器的 `5432` 映射到 `${POSTGRES_BIND_HOST:-127.0.0.1}:${POSTGRES_PORT:-5433}`，默认只允许宿主机本地访问。应用仍经内部网络连接 `postgres:5432`，不能为修改宿主机入口而改变应用的 `DATABASE_PORT`。standalone 不创建 PostgreSQL 容器，其 `DATABASE_PORT` 是既有外置数据库的实际连接端口；开发版和 Apple Container 不使用这两个映射变量。

更新时必须沿用既有 Compose 项目名、配置文件和存储方式：标准版的命名卷与本地目录版的挂载不可互换，`postgres_data` 的目标目录和 `PGDATA` 均保持 `/var/lib/postgresql/data`。安装脚本下载的是本地目录版并保存为部署端 `docker-compose.yml`，不能用仓库标准版覆盖后直接启动。原二进制/systemd 部署改用容器时，应先明确如何继续连接原 PostgreSQL、Redis 并保留配置、安装锁及稳定安全密钥；新建三服务 Compose 的空数据库不会自动包含原数据。

只更新应用镜像时，先拉取新镜像并完成升级前备份，再使用 `up -d --no-deps sub2api` 重建应用容器，保持 PostgreSQL、Redis 的镜像和挂载不变。迁移在新应用启动时执行；回退应用镜像不会自动回退数据库，必须先核对本次迁移的兼容性。

逐步操作见 [中文部署指南](../guides/deployment/index.md)、[Docker 镜像说明](../../deploy/DOCKER.md) 和 [Apple Container 指南](../guides/deployment/apple_container.md)。这些是部署者手册，不替代本文的工程约束。

管理后台的数据管理功能还依赖一个通过 Unix Socket 通信的可选 `datamanagementd` 进程。本仓库保留主进程客户端、systemd unit 和安装脚本，但当前检出内容不包含 `datamanagement/` 源码目录，因此根 Makefile 的构建目标和安装脚本的 `--source` 模式不能在本仓库单独完成构建。只有在另行取得兼容二进制或完整源码时才应启用；现成二进制的部署步骤见 [datamanagementd 指南](../guides/deployment/datamanagementd.md)。

## 初始化与启动

进程入口先判断是否需要 setup。未安装时可使用 Web setup、`--setup` CLI 或容器的 `AUTO_SETUP`；setup 测试 PostgreSQL/Redis，执行迁移，创建首个管理员，写入配置，最后创建只读安装锁。安装锁用于阻止重新初始化攻击，不能用删除它的方式修复普通配置问题。

正常启动在依赖注入创建 Ent 客户端时再次运行同一套嵌入迁移，因此每个新版本在监听 HTTP 前完成 schema 对齐。迁移或安全密钥初始化失败会使应用初始化失败，不允许带着部分 schema 提供流量。默认的兼容迁移允许多实例滚动启动，并由迁移锁保证只有一个实例执行 SQL；“升级与恢复”中标记为一次性或破坏性的变更优先于该默认规则，必须按专题停机顺序执行。

`GET /health` 是容器健康检查入口。健康响应只能说明当前进程可服务，不能替代升级后的业务抽样、账本核对或后台任务检查。

<a id="migration_execution"></a>
## 迁移执行

`backend/migrations/*.sql` 通过 `go:embed` 编入二进制，文件名是迁移身份并按字典序决定顺序。执行器使用固定 PostgreSQL advisory lock 串行化多实例迁移；`schema_migrations` 记录文件名、去除首尾空白后的 SHA-256 和应用时间。已有文件校验和不匹配时启动失败，只有 runner 中逐文件列出的历史兼容集合可以放行。

普通 `*.sql` 在单个事务中执行，SQL 与迁移记录一起提交或回滚。包含 `CREATE INDEX CONCURRENTLY` 或 `DROP INDEX CONCURRENTLY` 的文件必须以 `_notx.sql` 结尾；该模式逐语句在事务外执行，只接受带 `IF NOT EXISTS`/`IF EXISTS` 的并发索引语句，也禁止 `BEGIN`、`COMMIT` 和 `ROLLBACK`。非事务迁移可能在 SQL 成功但记录写入前中断，因此每条语句必须可安全重放。

首次检测到旧 `schema_migrations` 且缺少 Atlas 记录时，runner 会用当前最后一个迁移建立 `atlas_schema_revisions` 基线。该兼容记录不改变 SQL 文件仍为 schema 权威来源的事实。

迁移是前向且不可变的：

- 已进入任何环境的文件不得修改、删除或改名；修正必须使用新迁移。
- 文件内不能放可执行的 Down 段；runner 不解析 Goose/Up/Down 标记。
- 普通迁移应尽量幂等并在 SQL 中写中文注释解释变更原因和兼容窗口。
- schema、数据回填、Repository 查询和 Ent schema 变化应在同一兼容序列中设计；若明确无法支持新旧二进制共存，必须在本页记录停机升级、备份与回滚步骤。

## 新增与同步迁移

工单模块使用 `273_support_tickets.sql` 新建工单、消息和附件三张表，不回填或改写已有订单和用户数据。附件二进制随 PostgreSQL 备份保存，不需要新增宿主机附件目录。新程序启动时由上述迁移流程创建表，工单开关与限制通过后台设置热更新。

工单类型调整使用新增的 `274_support_ticket_consultation_type.sql`，不修改已应用的 273 文件及其校验和。274 将售前、售后及其他合并为咨询，正式类型收敛为咨询、财务和技术；不改变工单 ID、正文、消息、附件、时间或请求幂等哈希。数据库触发器将滚动升级期间旧程序写入的三个旧类型转换为咨询。类型合并不保留旧类型的单独分类，旧版本界面可能无法显示咨询标签，因此应更新完整程序及前端；若必须恢复原分类，需要使用升级前的数据库备份，不能仅替换旧二进制恢复分类。

工单优先级收敛使用新增 `275_support_ticket_three_priorities.sql`，将 `urgent` 合并为 `high` 并更新约束，保留 `normal` 作为中优先级的协议值；触发器兼容升级窗口的旧优先级写入，历史消息、附件及幂等哈希不变。移除处理人员功能只调整应用读写和 API，不删除原数据库列。此次总开关语义升级后，原来已保存为关闭的工单配置将关闭整个模块，管理员可在工单设置中重新开启。

工单结单身份使用新增 `276_support_ticket_closure_actor.sql`，增加可空的 `closed_by` 用户外键（删除账号时置空）及默认空字符串的 `closed_by_role` 角色快照，保留所有既有状态、消息和附件。历史完成或撤销记录不回填猜测身份，界面显示“未记录”；新程序完成、撤销或自动过期时写入真实角色。该迁移只追加字段与约束，可重复执行，不修改 273–275 的校验和；回复状态只更改显示标签，仍使用 `pending` / `waiting_user`。旧实例仍可操作原字段，但不会记录结单身份，因此需要完成全部后端及前端升级后再依赖该记录排查操作来源。回退旧程序不会删除新列，但回退期间的新结束操作会缺少身份记录。

新增文件使用 `<递增数字>_<snake_case 描述>.sql`；并发索引使用 `<递增数字>_<描述>_notx.sql`。仓库历史上存在重复编号和字母后缀，不能据此复用编号。每次创建前都要扫描 `backend/migrations/` 的数字前缀，取当前最大值再加一，并确认按字典序排在预期位置。

本 fork 同步上游时有额外硬约束：上游在 `backend/migrations/` 新增的迁移不能原名照搬。应按上游提交顺序逐个把数字前缀改为本 fork 当前最大编号加一；同时更新测试、runner 特例、文档或其它对原文件名的精确引用。已存在于 fork 的迁移保持原名，不为“整理顺序”重编号。

迁移变更至少验证：

- runner 单元测试，包括事务模式、`_notx` 校验、锁和 checksum；
- 受影响 schema/data migration 测试；
- 从空数据库完整应用，以及在已有 schema 上重复执行；
- `schema_migrations` 文件名与预期一致，未修改既有文件 checksum。

## 升级与恢复

当前发布版本为 `v0.1.278-ct-v2.5`。从 v2.4 升级不新增或修改数据库迁移；本次恢复 OpenAI OAuth 手动额度重置入口并适配两种角色的工单手机界面，不重算用户余额、订阅用量、窗口、有效期和已重置次数。手动重置的消费及部分成功边界见[OpenAI 上游额度重置](../interfaces/openai_upstream.md#openai_quota_reset)，手机展示仍遵循[工单生命周期与权限](../domains/support_tickets.md#ticket_lifecycle)。

从 v2.3 或更早跨级升级仍会新增迁移 286–288，最高迁移为 `288_affiliate_ledger_operation_id.sql`，已应用迁移保持不变。启动按已有迁移记录只执行尚未应用的文件；升级前备份并验证数据库与配置，排空在途请求，停止所有旧后端后完整切换新后端及前端资源，保留原数据目录、Redis、对象存储配置和固定安全密钥。先启动一个新实例完成迁移和业务抽样，再启动其余新实例；完成全部实例更新后再启用新增功能。从 v1.9 跨级升级仍会执行迁移 285；从 v1.8 或更早跨级升级还会执行迁移 282–284，异步任务升级边界见下文“当前异步图片任务持久化”。

v2.4 的三个迁移均为追加字段，不重写历史账务、订阅或已有定价：

- `286_content_moderation_engine_meta.sql` 为审核日志增加可空 `engine_meta`，旧记录不回填、旧写入仍允许为空。OpenAI 旧配置继续有效，升级不会自动切换到 TypeSafe；切换前应检查对应端点、Key 池与文本审核结果，TypeSafe 跳过的图片不能视为已审核安全，详见[审核决策流水线](../domains/content_moderation.md#content_moderation_decision_pipeline)。
- `287_channel_reasoning_effort_multipliers.sql` 为渠道价卡增加默认空对象的 `reasoning_effort_multipliers`，保留旧 Max 字段、显式零价、Fable 5.1 默认 Max 倍率及分组价卡 JSON。只有显式配置新增档位倍率才改变相应有效价格；按最终转发档位计算 token 费用，继续使用本 fork 的套餐、分组和账号统计结算规则，详见[路由与结算](../domains/routing_and_billing.md)。
- `288_affiliate_ledger_operation_id.sql` 为返利流水追加可空 `operation_id` 和非空值唯一索引，历史返利金额不变。线下提现登记只扣可用返利，重试必须保留同一 `Idempotency-Key`，不调用外部支付，详见[线下提现登记](../domains/promotions_and_affiliates.md#affiliate_offline_withdrawal)。该索引在普通迁移事务内创建，大型流水表应预留建索引时间，不能把首次启动等待误判为程序卡死。

升级后核对最高迁移 288、审核引擎与历史记录、渠道/分组价卡和实际扣费、返利流水及提现重试，同时确认已有余额、订阅用量、窗口、有效期和重置次数保留。订阅继续沿用下述 v2.1 窗口与到点计数规则，不批量清空用量、不重算升级前历史、不更改套餐倍率；后台正常推进 `reset_counted_at` 属于到点扫描行为。

286–288 的追加 schema 可保留，但旧应用不具备新增审核引擎、档位倍率和线下提现的完整语义，不应在启用这些功能后混跑或直接回退继续处理业务。回退程序不会撤销已经结算的费用、返利扣减或新配置；需要精确恢复升级前状态时，先停止全部新实例、核对升级后账务，再按已验证的数据库与配置备份恢复流程执行，不删除迁移记录来重跑历史迁移。

v2.3 从 v2.2 升级不新增 SQL 迁移，最高迁移保持为 `285_subscription_scheduled_reset_counts.sql`。

v2.3 为渠道探测新增协议选择与“立即测试”，复用已有探测配置 JSON 和历史观测，不新增数据库迁移。缺失协议的旧配置仍按 `auto` 保留原流程；显式选择只检测对应端点，不改变账号保存的协议和业务路由。手动测试只保存本次探测配置并执行一次，成功和失败均写入渠道状态；租约与配置更新在同一事务内，忙碌时不会改写配置。升级需完成所有后端及前端更新后再启用显式协议，避免旧 runner 忽略协议字段；升级后核对选定端点、手动结果及后续定时记录。已有历史红色区间保留，不清空计费、订阅用量或重置次数，详见[渠道探测](../interfaces/model_catalog_and_marketplace.md#group_availability_probe)。

v2.2 新增 `gpt-6-sol`、`gpt-6-luna` 和 `claude-opus-5-5` 的模型目录、客户端配置、价格及协议适配，并修正缓存写入与 Fast 档位的展示和结算倍率一致性。现有显式渠道价格（含零价）继续优先，订阅沿用下述 v2.1 窗口和计数规则，不重算历史、不修改已有用量或有效期。二进制与镜像均需携带同版本官方离线定价资源，保留自定义覆盖；启用新型号前核对上游可用性和账号白名单。升级后抽样核对模型目录、缓存与 Fast 价格展示以及实际扣费，详细定价见[模型目录与展示定价](../interfaces/model_catalog_and_marketplace.md)。

订阅自动重置计数在已发布的 `284_subscription_auto_reset_counts.sql` 增加日、周、月次数；该迁移保持不变。v2.0 新增 `285_subscription_scheduled_reset_counts.sql`，只增加非空 `reset_counted_at` 水位和后台扫描索引，迁移时保留三个已有次数及全部额度、窗口、有效期和账务数据，以迁移事务时间作为历史记录计数基线。新程序启动后自动应用迁移并在启动时、每分钟结算合法重置点，即使没有消费也累计；停机跨周期后从最后水位补记，不回填升级前无法核实的历史。计数细则见[周期额度窗口](../domains/payments_and_entitlements.md#subscription_quota_windows)。

v2.1 对齐 sub2api 的窗口重置规则：日额度仍按项目时区零点，不超过一天的日卡保持一次性日额度；周/月首次激活和手动重置采用实际时刻，自动刷新按原锚点的 7/30 天整数周期推进，只要求重置点严格早于订阅到期。取消完整尾段限制与有限外层额度豁免，并让次数使用相同时间表。本次无需新增迁移，284/285 保持不变；本期已有次数保留，不自动回填或重算历史。新规则上线后，原先被完整尾段限制挡住的有效重置点将可以刷新额度，这是预期的权益变化。升级须同时替换全部后端实例与前端资源，避免新旧规则混跑；不需要批量清空用量、窗口或修改有效期。旧初始周/月零点锚点仅在可明确对应生效当天且早于生效时刻时按生效时刻修正，后续真实锚点保留。回退 v2.0 程序不会撤销已经发放的额度或累计的次数，也不恢复旧规则期间错过的重置点，不能把替换程序视为业务数据回滚。

从 v1.9 跨级升级时同样应先停止旧后端，再整体启动新后端并核对迁移 285；不要将 v1.9 的“窗口实际推进加一”实例与新实例混跑，否则两套累计方式可能重复计次。数据库变更是增量字段，但回退旧程序会恢复旧计数语义；不能把回退期间的次数视为连续、准确的到点统计。升级不重算已有次数，续费新一期仍从零开始，管理员延期保留原记录计数。

本地插件框架新增 `280_plugins.sql` 与 `281_plugin_artifacts.sql`，只扩展插件安装/绑定表和包原件字段，不迁移账号、余额或订阅。插件默认停用且菜单默认隐藏，升级无需先安装插件。启用时需确保插件数据目录可写、可执行并上传匹配服务端系统/架构的包；宿主使用 fork 真实版本参与兼容检查。插件配置、包和 Redis KV 的恢复边界见[本地插件](../interfaces/local_plugins.md)。

Seedance 不新增数据库迁移，默认旧账号不开放视频任务；显式开通前配置账号能力、分组媒体权限及输出 token 定价。任务归属和计费快照依赖 Redis，升级或重启需保留 Redis 数据；客户端仍需查询成功结果触发扣费，见[视频任务计费](../interfaces/seedance_upstream.md#seedance_billing)。

升级前先创建并实际验证 PostgreSQL 备份，同时保存 Redis/对象存储中业务要求恢复的数据。后台备份服务可把数据库 dump 流式写入本地或 S3 兼容存储，并用维护锁串行化备份/恢复；敏感存储配置需要稳定的安全密钥。备份内容策略可能排除大体量历史表，恢复目标必须先核对备份范围。

从 `v0.1.278-ct-v1.4` 更新到 `v0.1.278-ct-v1.5` 不新增 SQL 迁移，当前最高迁移仍为 `277_allow_opencode_user_platform_quotas.sql`；此前 v1.3 到 v1.4 同样没有新增迁移。沿用既有数据库、配置、数据目录和稳定安全密钥，更新应用后检查程序版本、健康状态、登录、网关调用和用量记录；从 v1.2 或更早版本跨级更新时，仍须遵守下文的 v1.3 缓存切换及所有待应用迁移的升级要求。

v1.5 扩大智能路由对已确认上游失败的恢复范围，并在已有错误事件 JSON 中保存最终恢复分组快照。完整更新后，应抽样验证换组、冷却、管理员与用户错误页的恢复目标展示；旧实例不会产生新恢复快照，历史缺失目标的记录不补填猜测值。重放仍受已输出内容、已产生用量及本地业务拒绝等边界约束，详见[智能路由换组与冷却](../domains/smart_routing_api_keys.md#group_failover)和[恢复错误的查询与归属](ops_monitoring_and_alerting.md#recovered_error_visibility)。

<a id="v1_3_upgrade"></a>
### v1.3 升级与缓存切换

从 `v0.1.278-ct-v1.2` 更新到 `v0.1.278-ct-v1.3` 新增 `277_allow_opencode_user_platform_quotas.sql`。迁移扩展用户平台额度 CHECK 约束，保留全部已有平台，仅加入 `opencode_go`；不新增额度记录、不修改余额或订阅，不复用上游迁移编号。上线时沿用原数据库、配置及数据目录，并完成所有实例升级与初始调度快照重建后再启用 OpenCode 账号。旧实例没有新平台处理器，不能处理新平台流量；较早版本跨版本升级还须遵守其间所有迁移的升级要求。

本次缓存切换包括 Antigravity access token 缓存与刷新锁统一改用 `ag:account:<账号ID>`，避免同 project 的账号共享凭据。失效流程同时清理当前账号键和历史 project 键，但旧实例仍可能写入旧键；应完成全部实例升级后再依赖新的隔离规则，不需要清空整个 Redis。账号调度元数据同时补充阈值字段、Anthropic 用量窗口和 OpenCode 查询身份；已有元数据不能仅等待 TTL 过期，应确认新实例完成初始快照重建。具体契约见 [Antigravity 账号与凭据](../interfaces/antigravity_upstream.md#antigravity_account_contract)和[调度快照一致性](../architecture/account_scheduling_and_cache.md#scheduler_snapshot_consistency)。

WS 执行作用域也在本版本切换。升级前排空旧 WS，完成全部实例升级及快照重建，再让客户端重连；不要依赖新旧版本混跑提供一致的 `request_kind` 隔离、抢占或凭据缓存行为。单实例同样需要重连。升级后检查迁移 277、原用户与额度数据、登录、网关调用、用量记录及后台快照状态；验证范围和结果应记录在本次产物说明中。

### WS 连接池与执行作用域更新

WS 池和 Responses 内存优化本身不新增数据库迁移。OAuth/API Key 连接容量系数的缺省值改为 5.0；已有 YAML 或环境变量显式值继续优先，不会被启动时覆盖。连接容量仍受每账号硬上限约束，请求并发准入不变，但活跃会话较多时可能增加 socket、reader 和代理资源占用，容量公式与配置边界见[OpenAI 上游](../interfaces/openai_upstream.md#openai_ws_pool_lifecycle)。

执行状态和抢占由旧 session 键改为 API Key、原始线程/显式会话与 `request_kind` 共同派生的作用域，不双读旧键。`turn`、`prewarm`、`compaction` 和未声明的 kind 共用主通道，`memory` 与其他非空 kind 分别隔离。账号调度粘性仍使用原 session hash，不能把它当作执行状态键。单实例更新后需客户端重连；多实例应排空旧 WS 并完成全部实例更新后再依赖新隔离规则，混跑新旧版本不能保证一致抢占。无需清空 Redis 或重置业务数据；本次规则不替代同一更新包中其它迁移的升级要求，详细边界见[执行作用域](../interfaces/openai_upstream.md#openai_execution_scope)。

### 数据共享功能下线

迁移 `265_remove_data_sharing.sql` 是不支持新旧实例混跑的破坏性迁移。它幂等删除 `data_share_export_artifacts`、`data_share_sessions`，删除 Group、API Key 和复合 Key 映射中的数据共享列与索引，并删除九个数据共享运行设置。对于有效的 `backup_content_config` JSON 对象，迁移只移除 `include_data_share_sessions`；历史上无效的 JSON 或 JSONB 无法表示的值会保留并发出数据库 notice，不阻断迁移。历史迁移 141 至 168 及 226 保持不可变，以支持空库按完整序列初始化和旧版本前向升级。

升级必须按以下顺序执行：

1. 停止接流量并停止全部旧实例，防止旧代码在列删除后继续读写。
2. 创建 PostgreSQL 备份并实际验证其可恢复性。另行记录所有本地导出目录，以及远端存储的 endpoint、bucket、prefix 和对象 key 清单；同时识别可能包含共享会话的旧备份。清单不得记录访问密钥明文。
3. 只启动一个新实例执行迁移。确认两张表、相关列、索引和九个设置均已消失，`backup_content_config` 的其它选项保持不变，并验证原用户端和管理端 `/api/v1/data-sharing/*` 路径返回普通 404。
4. 确认认证快照版本已升至 v37，旧 Redis 快照因版本不匹配自然重建；完成核心网关、计费、备份和管理页面抽样后再扩容其它新实例。

新版本忽略旧配置中的 `gateway.data_sharing_capture`、`gateway.data_sharing_export` 及对应环境变量；部署者应从 YAML、Compose 覆盖和密钥管理中手工删除这些键。迁移只清理 PostgreSQL 元数据，不删除本地目录、S3/R2 对象或旧备份；`.gitignore` 继续保留旧导出目录规则，避免尚未人工处置的敏感文件被提交。外部对象及包含共享会话的旧备份应按部署方的保留和合规策略盘点、保留或清理。

仅回退二进制不能恢复已删除的 schema，也不得通过删除 `schema_migrations` 记录模拟回滚。需要回滚时必须停止全部新实例，恢复升级前已验证的 PostgreSQL 备份，再启动旧版本；外部导出对象和旧备份仍按上述人工策略处理。

### 国产供应商用户平台额度约束

迁移 `248_allow_cn_user_platform_quotas.sql` 由上游迁移 224 按本 fork 当时最大编号 247 递增而来；仓库不保留上游原文件名。它只替换 `user_platform_quotas.platform` 的 CHECK 约束，把 `kimi`、`zhipu`、`deepseek` 加入原有六个平台，并与应用层九平台 allowlist 对齐。

该迁移不回填已有用户的 CN 额度行。已有用户缺失行时继续按领域既有语义视为无限额，管理员显式保存九平台配置后才创建对应记录；新注册用户会在单次批量写入中创建全部九个平台的默认快照。升级后至少验证迁移可重复执行、新用户九行均写入、已有额度记录不变，以及 Kimi/Zhipu/DeepSeek 的日周月限额更新、预检查和结算归属。

### Grok 媒体、搜索与 Voice 定价迁移

迁移 `242_group_video_model_prices.sql` 为分组增加可空 JSONB `video_model_prices`，按 Grok 视频模型族和分辨率保存每秒价格；`243_group_audio_voice_pricing.sql` 增加 Realtime 每分钟、TTS 每百万字符和 STT 每小时价格；`244_group_search_price_per_1k.sql` 增加搜索每千次价格。三类价格均以 `NULL` 表示使用代码默认值，显式 `0` 表示免费。管理端和服务层会规范化模型族、拒绝负价，并保持旧 `video_price_*` 作为视频回退层。

迁移 `245_clear_non_grok_video_generation_config.sql` 清除非 Grok、非 Composite 分组的旧视频价格，避免其它平台误宣称视频能力。清理前会一次性创建 `groups_video_price_backup_245`，保存受影响分组的旧列和 JSONB；`CREATE TABLE IF NOT EXISTS` 保证重放不会覆盖首次快照。Composite 可能最终路由到 Grok，因此保留其配置。确认无需恢复后可手工删除备份表；需要恢复时按 `group_id` 从该表回填价格，不能通过删除 `schema_migrations` 记录触发逆向迁移。

这四个文件由上游迁移 217-220 按 fork 当前最大编号重新编号为 242-245。部署后应验证 Grok 分组的模型级视频价、搜索与三类音频价往返，非 Grok 清理范围和备份表内容，以及异步视频在首次完成轮询时只结算一次。

### 分组逐模型定价迁移

迁移 `246_group_model_pricing.sql` 由上游迁移 221 按 fork 当前最大编号递增而来，为 `groups` 增加默认 `TRUE` 的 `long_context_pricing_enabled` 和可空 JSONB `model_pricing`，并把全部存量分组回填为开启。前者只控制内置模型长上下文阶梯，不会压平渠道显式 token 区间；后者保存分组逐模型价卡，结算和模型市场都按“分组 > 渠道 > 内置”解析。新建管理请求省略开关也按开启处理，避免应用层显式写入布尔零值绕过数据库默认值。

该迁移只新增列，可随新版本正常前向执行；但旧实例不理解分组价卡，混跑期间不能开放或修改 `model_pricing`，否则同一分组可能因命中不同版本实例而出现展示与实扣差异。应先完成全部后端升级并确认认证缓存重建，再开放新管理端。升级后至少验证显式免费价、分组覆盖渠道价、关闭内置长上下文、渠道区间仍保留，以及模型市场单价与实际 `ActualCost` 一致。回退旧二进制不会删除新列，但会忽略新配置；需要继续服务时应先停止写入分组价卡或恢复到不依赖该配置的版本状态。

### 渠道缓存写入 1h 分档迁移

迁移 `261_channel_cache_write_1h_pricing.sql` 为渠道模型价、渠道 token 区间、账号统计模型价和账号统计区间增加可空 `cache_write_1h_price`。NULL 表示兼容旧的 `cache_write_price` 两档同价语义，显式 0 表示 1h 缓存写入免费；迁移只新增列且可重复执行。应用层会在用量带有 5m/1h 明细时分别计算，否则按聚合缓存创建 token 回退，避免历史记录改变金额。

升级后应抽样验证旧渠道配置仍返回相同总价、新配置的 5m/1h API 往返、账号统计成本和模型广场展示，并确认所有实例已运行包含该迁移的版本后再开放 1h 字段写入。

### 分组 OpenAI Fast 强制策略迁移

迁移 `262_group_force_openai_fast.sql` 为 `groups` 增加默认关闭的 `force_openai_fast` 布尔列。管理端只允许 OpenAI/Composite 分组写入；认证快照升级到 v34 后会携带该字段，网关再把它投影到 HTTP、Responses 和 WebSocket 请求的 `service_tier=priority`。组级强制不是绕过策略的旁路：全局 Fast/Flex 过滤或阻断，以及 API Key 的 `force_off`，仍然在最终请求体上生效。

该迁移仅新增列，可重复执行，但旧后端不会读取该策略。发布时应先完成数据库迁移和全部后端实例升级，确认旧 v33 快照被拒绝并重建，再开放管理端开关；回退旧二进制不会删除列，但会忽略新配置，不能在混跑期间依赖组级 Fast 语义。

### 分组推理强度超限动作迁移

迁移 `263_group_reasoning_effort_over_limit.sql` 为 `groups` 增加非空 `max_reasoning_effort_over_limit`，默认 `downgrade`，并记录 `deny` 的拒绝语义。上游同名迁移使用的编号不直接复用；本 fork 按现有最大迁移号递增为 263。管理服务只允许 `downgrade` 或 `deny`，且 `deny` 仅对 OpenAI 分组开放；平台切换到其它类型时会清除上限并恢复默认降档动作。

认证缓存版本由 v34 升至 v35，快照增加该动作。HTTP Responses/Chat、Messages 兼容桥和 Responses WebSocket 都在出站前执行“模型范围映射后再比较上限”的规则；拒绝请求属于本地业务限制，不应进入账号故障转移或 SLA 失败统计。Messages 只对显式 `output_config.effort` 绑定策略，避免改变缺省请求的桥接默认值。部署时先执行迁移并升级全部后端实例，确认旧快照失效、管理 API 往返字段正确，再开放 `deny` 配置。旧二进制会忽略新列，不能在混跑期间依赖拒绝语义；回退时无需删除列，但应停止写入新动作并重新构建缓存。

### 分组 OpenAI Fast Standard 计费迁移

迁移 `264_group_free_openai_fast.sql` 为 `groups` 增加默认关闭的 `free_openai_fast` 布尔列。管理 API、分组复制和认证快照只对 OpenAI/Composite 分组保留该策略；平台切换到其它类型时由服务层清零。上游请求仍使用 Fast/priority，只有用户侧结算在同一模型、渠道和计费时刻重新采用 Standard 价格。

认证缓存版本由 v35 升至 v36，快照新增免费 Fast 字段。Usage Log 的 Fast `total_cost` 继续作为账号统计和账号额度的成本基数，Standard `actual_cost` 与统一结算基础金额用于余额、订阅和 API Key 配额。迁移是幂等新增列，但旧后端不会读取该策略；发布时先执行迁移并升级全部后端实例，确认旧 v35 快照失效、管理 API 往返字段正确，再开放开关。回退旧二进制不会删除列，且不能在混跑期间依赖免费 Fast 价格语义。

### OpenAI 账号级长上下文计费开关下线

迁移 `241_remove_openai_long_context_billing_toggle.sql` 幂等删除迁移 203 创建的两个账号同步触发器和两个函数，并从所有账号 `extra` 中移除 `openai_long_context_billing_enabled`，保留其它 JSONB 数据。新服务仍把该键视为废弃输入：账号创建、更新、批量更新、导入和 CRS 同步即使收到非法类型也会静默丢弃，不再保存或返回旧校验错误。整份替换语义的单账号更新只携带废弃键时等同未提供 `extra`，不会清空其它配置；显式 `extra:{}` 仍表示清空允许清空的字段，废弃键与有效字段并存时只处理有效字段。账号数据导入会在计算幂等指纹前丢弃该键，因此旧键缺失、任意旧值和非法类型均表示同一逻辑请求。

升级后，长上下文用户价格只由分组逐模型基础价、渠道显式区间、模型内置阶梯、分组长上下文开关和分组倍率决定。渠道显式区间优先且不会重复叠加模型内置倍率，也不受分组开关影响；没有显式区间时，开关决定是否按模型广场公开的长上下文档结算。账号统计和账号 `quota_used` 统一使用 `COALESCE(account_stats_cost, total_cost) × account_rate_multiplier`，显式零账号成本不累计额度；这不改变用户余额、订阅或 API Key 配额继续使用 `ActualCost` 的规则。

这是不支持新旧后端混跑的一次性升级。发布前必须停止接流量并排空全部旧实例，验证 PostgreSQL 备份可恢复，再只启动一个新实例执行迁移；确认触发器、函数和旧键已清理，抽样核对模型广场区间价与实扣一致后，才能扩容其它新实例。旧实例不能连接已迁移数据库，否则可能重新写入废弃键或按旧账号开关产生不同用户价格。

发布说明必须明确：此前关闭账号开关的 OpenAI 请求在超过模型阈值后，会开始按模型广场长上下文价格扣费。仅回退二进制不能恢复旧版精确行为；需要回滚时应停止全部新实例，恢复升级前 PostgreSQL 备份，再启动旧版本，不能通过手工补键或删除迁移记录代替数据库恢复。

### 通用高级调度器迁移

迁移 `238_generalize_advanced_scheduler.sql` 为 `groups` 增加受约束的 `scheduler_type`，默认 `basic`，并把旧 OpenAI 实验调度器转换为按分组选择的通用高级调度器。旧 `openai_advanced_scheduler_enabled=true` 时，仅既有 OpenAI 与 Grok 分组回填为 `advanced`；开关为 false 或不存在时，所有存量分组保持基础。其它平台不会被自动升级，新建分组始终为基础。

迁移会把旧粘性、订阅优先、Top-K 和评分权重设置复制到 `advanced_scheduler_*`，随后删除全部 `openai_advanced_scheduler_*` 键，不保留数据库别名或读取回退。部署前必须把配置中的 `gateway.openai_ws.lb_top_k`、`gateway.openai_ws.scheduler_score_weights.*`、`gateway.openai_scheduler.sticky_escape_*` 替换为 `gateway.advanced_scheduler`；新版本会拒绝旧配置，管理设置 API 也会拒绝旧字段。

迁移 `239_add_group_advanced_scheduler_overrides.sql` 为 `groups` 增加非空 JSONB `advanced_scheduler_overrides`，默认 `{}`，并约束顶层必须是对象。它不修改既有分组模式或全局权重；空对象让所有分组继续继承网关通用参数。升级后管理端可仅为高级分组保存需要偏离全局的字段，认证快照版本会再次提升以避免旧缓存缺失覆盖值。

迁移 `240_remove_account_group_priority.sql` 幂等删除 `account_groups.priority` 以及依赖该列的三个索引。该字段没有完整的产品配置入口，真实调度和模型市场统一使用 `accounts.priority`；迁移后 AccountGroup 只表达账号与分组的成员关系。迁移会先按名称删除历史索引再删除列，既支持完整历史 schema，也支持缺少部分索引的兼容数据库。

这是破坏性的一次性升级，不支持新旧二进制或新旧前端混跑。先停止全部旧实例、备份 PostgreSQL 与配置，再启动一个新实例完成迁移，确认认证快照因版本变化而重建、分组模式和通用设置符合预期，并抽样核对账号仍按全局优先级排序后，再扩容其它新实例。仅回退二进制不能恢复已删除的旧设置或 `account_groups.priority`；需要回滚时应停止新实例并恢复升级前的数据库备份和配置。

### MiniMax 平台额度迁移

迁移 `270_allow_minimax_user_platform_quotas.sql` 只扩展 `user_platform_quotas.platform` 的检查约束，允许 `minimax`；不修改既有额度、用户、账号或分组记录。新用户默认额度与管理员额度配置使用扩展后的平台集合，存量用户缺少 MiniMax 配额行时沿用既有无限额语义，直到管理员显式配置。

启动新版本时自动执行此迁移。全部实例升级后再创建 MiniMax 账号、分组和额度；旧版本不识别该平台，因此新增 MiniMax 配置后不能仅凭数据库约束向后兼容就混跑旧实例。

### 智能路由冷却迁移

迁移 `271_api_key_smart_routing_cooldown.sql` 为 `api_keys` 增加非空 `smart_routing_cooldown_seconds`，默认 60，数据库约束为 0–3600。该文件保持首次应用时的原文，校验和为 `c600119f5da453b541b264d51203e425ebe23ac89abe87b6e33d8f44d8d7a7f0`。后续 `272_api_key_smart_routing_cooldown_outbox.sql` 按当前表识别并补齐范围约束，将冷却字段变更纳入既有鉴权缓存 outbox 触发器。

两步迁移不删除既有密钥、分组、候选顺序或用量数据，不覆盖已保存的冷却值。认证快照版本升级为 v39，旧快照重新加载；配置保存后即时失效 Redis/L1，事务 outbox 为失效失败提供重试保障。已应用初版 271 的环境重新构建并启动后只追加执行 272；不得通过删除或修改 `schema_migrations` 中的校验和绕过检查。

升级后已有智能路由 Key 默认采用 60 秒冷却；创建/编辑密钥可设置秒数，0 关闭跨请求冷却。普通与复合前缀 Key 保持原行为。所有实例应更新到支持该字段的版本，以使重试和 Redis 冷却语义一致。抽样验证前组 502/429 后后组成功、全失败返回最后错误、下一请求全冷却返回带 `Retry-After` 的 503，以及冷却到期恢复。详细重放边界见[智能路由 API Key](../domains/smart_routing_api_keys.md#group_failover)。

### 分组客户端协议迁移

迁移 `235_add_group_allowed_client_protocols.sql` 为 Group 增加非空 JSONB `allowed_client_protocols`，按当时六个平台在升级前的实际路由行为回填。OpenAI 是否加入 Messages 取自旧 `allow_messages_dispatch`；其它已有平台按各自迁移矩阵回填。后续新增的 Kimi、Zhipu、DeepSeek 不需要历史回填，新建分组默认启用 Messages、Responses 和 Chat。旧列作为弃用管理 API 字段的数据库镜像保留，不用于支持新旧二进制共存。

数据库默认值是空数组，作为绕过管理服务直接写 Group 时的 fail-closed 默认值。空数组对所有平台都是明确且有效的策略，新代码不会按旧矩阵恢复或自动补协议；管理 API 创建字段缺省时仍使用各平台的新建默认值。

本变更按一次性升级发布，不支持新旧后端或前后端混合运行。认证快照版本随字段增加而升级，部署前遗留的 Redis 快照会因版本不匹配失效并从已回填数据库重建。完成升级后至少抽样验证 OpenAI 旧开关 true/false、任意平台空集合以及 Gemini Responses 的非流和 SSE 请求。

### 上游声明倍率探测下线

迁移 `236_remove_upstream_billing_probe.sql` 幂等删除账号 JSONB 中的 `upstream_billing_probe`、`upstream_billing_probe_enabled`，并删除设置 `upstream_billing_probe_settings`、`openai_low_upstream_rate_priority_enabled`、`openai_oauth_scheduling_rate_multiplier`、`openai_advanced_scheduler_weight_upstream_cost`。迁移不改动其它账号 extra 或设置。旧配置项 `gateway.openai_ws.scheduler_score_weights.upstream_cost` 已失去行为，升级前应从配置文件、Secret 和环境模板中移除。

这是无兼容路由和弃用期的破坏性升级。发布时先停止并确认全部旧实例退出，再备份 PostgreSQL 和旧配置，然后启动一个新实例完成迁移，最后扩容其余新实例；禁止新旧二进制混跑，否则旧进程可能重新写回已删除数据。`GET /v1/sub2api/billing` 和全部 `/api/v1/admin/accounts/*upstream-billing-probe*` 路由在新版本上返回普通 `404`。

不扫描或清理 Redis。遗留探测 leader lock 按原有 2 分钟 TTL 自然过期；这不会恢复任何探测任务。升级后应确认迁移可重复执行、无关账号 extra 和设置保持不变、Ollama Cloud/额度/endpoint capability 探测正常，以及声明倍率不再影响账号排序或评分。

只回退二进制无法恢复已删除的快照与设置。需要回滚时，先停止全部新实例，恢复升级前 PostgreSQL 备份和旧配置，再启动旧版本；不得通过手工删除迁移记录或让旧实例在已迁移数据库上重建历史数据。

### API Key 结算模式与批量图片快照

迁移 `237_add_api_key_billing_modes.sql` 为 `api_keys` 增加非空 `billing_mode`（默认 `auto`）和可空 `preferred_subscription_id`，并在 `batch_image_jobs` 增加同名的提交时结算快照列。它是纯新增列迁移，不需要为存量 Key 回填：旧记录自动保持订阅优先、余额兜底的历史行为；旧批量任务也按 `auto` 兼容结算。

迁移完成后，认证缓存版本会使旧快照失效并重建，避免缓存缺少结算字段。SQL 列本身与旧二进制兼容，但在同一部署中不能让旧实例继续处理用户新配置的指定订阅或仅余额 Key；应先完成全部后端实例升级，再在面板开放该配置。升级后至少抽样验证个人和团队 Key 的订阅选择、套餐受限分组拒绝、指定订阅额度耗尽不扣余额、仅余额不使用订阅，以及批量图片提交后修改 Key 配置仍按提交快照冻结/结算/释放。

<a id="api_key_billing_cache_invalidation"></a>
迁移 `258_extend_api_key_auth_cache_invalidation.sql` 通过 `CREATE OR REPLACE FUNCTION` 扩展既有 API Key 鉴权缓存 outbox 触发器，将 `billing_mode` 和 `preferred_subscription_id` 的变化纳入失效条件。它依赖迁移 237 已存在的列，不修改历史迁移文件；旧实例可继续运行，但完成新版本升级后应确认自动改绑产生的 Key 快照在多实例间及时失效，并抽样检查 outbox 只保存哈希而不保存明文 Key。

### 当前异步图片任务持久化

当前版本恢复显式异步图片接口，并在迁移 `282_media_tasks.sql` 的列表投影之外新增 `283_image_tasks_durable_results.sql`。新表只保存短期紧凑结果、图片访问链接和执行租约，不保存生成请求、密钥或图片 Base64；余额、订阅、渠道定价表不变。完整任务和账务边界见[异步图片与任务记录](../domains/media_tasks.md)。

升级使用正常启动迁移流程，不修改已应用迁移或数据库 checksum。旧 Redis 任务在查询及维护时可回填，未完成旧任务等待原有执行预算后才判定中断；已经在旧进程内丢失、尚未落存储的图片不能凭迁移恢复。应在旧实例正在生成的任务结束后更新，以减少结果不确定的请求。新实例通过执行租约识别失联任务，不能把其它仍活跃实例的任务全部判失败。

上线验证包括：完成结果在 Redis 不可用时可从数据库查询、过期租约变成明确中断状态、终态与列表一致、轮询不扣费。备份与访问控制应覆盖短期结果表中的图片访问链接。该迁移为新增表，应用回退不删除结果或历史列表；旧应用仍不能提供新版本的持久恢复能力。

### 历史版本的自研异步图片任务下线

包含迁移 `234_remove_async_image_storage_setting.sql` 的版本会立即移除自研 OpenAI/Grok 异步图片路由、后台对象存储设置和 `image_storage_config` 数据库记录。这是破坏性升级，不提供任务排空、兼容查询或旧任务恢复。发布 tag notes 必须明确列出这项变化。

升级前先记录旧异步图片设置使用的 bucket 和 prefix，并从配置文件、Secret 管理及部署环境中移除 `image_storage` 和 `IMAGE_STORAGE_*`。旧进程会缓存已解析的对象存储客户端，因此不能与新版本滚动重叠：应先把全部旧实例移出流量并停止，再启动新版本。

历史 Redis `image_task:*` 键按原有最长 24 小时 TTL 自然过期，不执行全库扫描或清空。历史 S3/R2 图片不会自动删除；升级后先列举或 dry-run 旧前缀，确认它不与 `backups/` 或其他业务前缀重叠，再由运维使用对象存储工具定向删除。迁移完成后数据库不再保存旧存储位置，因此记录 bucket/prefix 必须发生在升级前。

旧任务 ID 在新版本上直接返回普通 `404`，在途进程内任务随旧实例停止而终止。回滚旧二进制不会恢复已删除的后台设置；只有显式恢复旧配置才能重新启用旧版本功能。

在线更新和安装脚本可以保留上一版二进制或镜像，但这只是应用回退。数据库迁移不会因镜像回退自动撤销；上线前必须确认新迁移对旧版本是否向后兼容。若 schema 已不兼容，应使用经过演练的数据库备份恢复或新增前向修复迁移，而不是手工删除 `schema_migrations` 记录。

### 创作台 durable 状态与 outbox 迁移

迁移 `257_creative_run_durable_settlement.sql` 为 `creative_runs` 增加 provisioning、provider 成功记录、settlement/release 重试与 reconciler 字段，创建 `creative_run_outbox`，并为每个用户/分组的 active `creative_studio` 托管 Key 增加部分唯一索引。它把既有 queued/running 任务回填为可继续入队的阶段，不改变 Redis 图片 TTL 边界。发布时应先执行迁移，再部署兼容旧状态的应用版本并启动 outbox/transient reconciler；观察 `settlement_pending`、`release_pending`、lease lost、result lost 和 outbox lag 后，再调高恢复告警阈值。

升级完成后至少检查 `/health`、登录/API Key 鉴权、一个非流和流式网关请求、用量结算、关键后台任务及迁移表。保留旧产物和升级前备份，直到这些检查完成。

相关文档：[系统架构](../architecture/system_architecture.md)、[配置边界](../interfaces/configuration.md)、[运维目录](index.md)。
