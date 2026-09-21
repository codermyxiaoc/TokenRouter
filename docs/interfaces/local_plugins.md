# 本地插件与宿主服务

本文覆盖管理员安装的本地进程插件、宿主 KV 与账号目录、只读状态和浏览器 UI 桥。插件框架是后端扩展接口，不是最终用户上传任意代码的入口；当前出站传输能力仅面向 OpenAI OAuth HTTP 请求。

<a id="plugin_lifecycle"></a>
## 安装、运行与原有请求边界

管理页面为 `/admin/plugins`；系统设置 → 功能开关 → 插件管理中的“显示插件管理菜单”控制侧栏展示，存储键 `plugin_management_enabled` 默认关闭。它只隐藏菜单，不停用已安装插件，也不替代后端管理员鉴权。

插件包必须通过清单、平台/架构二进制、SHA-256、包大小、解包路径、签名和宿主兼容性校验。默认拒绝未签名包，通过 `plugins.trusted_publishers` 配置受信发布者；内置的上游 OpenAI transport 发布者公钥只允许签署对应固定插件 ID，不可冒用该公钥安装其他 ID。上传安装后默认停用；启用、停用、删除、配置保存、测试及上传继续要求管理员 step-up。配置加密保存，插件包原件保存在 PostgreSQL 以便副本重新校验并恢复本地运行文件。

启动后 `PluginManager` 协调持久安装与本机进程，关闭时停止插件，再释放其依赖。无启用绑定时走原来的上游 HTTP/TLS 客户端；显式启用 OpenAI OAuth 出站绑定后才按账号稳定分桶的灰度百分比介入。命中绑定的 WebSocket 请求使用现有 HTTP Bridge，以免绕过仅支持 HTTP 的插件传输协议。API Key、影子账号和其他平台不因该插件能力改走新传输。命中绑定的插件不可用时不静默绕过已启用的传输策略。

兼容性使用当前 fork 的真实构建版本，不伪装为上游 `0.2.7`。仅声明支持上游 `>=0.2.x` 的包可能不接受当前 `0.1.278-ct-*` 版本，需要插件发布者提供匹配此宿主版本范围的清单，并通过协议与实际功能兼容性校验。

插件是以服务进程权限执行的原生程序，**不是操作系统沙箱**。签名与声明能力用于校验来源及约束宿主接口，不能阻止受信二进制自行访问同一操作系统用户可访问的资源；只应安装可信发布者的包。

<a id="plugin_host_scope"></a>
## 宿主 KV 与账号目录

宿主通过 go-plugin 的 gRPC broker 向具体插件进程反向提供 HostService。每个服务实例由宿主注入插件 ID，插件不能通过请求参数伪造另一个插件的存储域。KV 使用 Redis 前缀 `plugin:kv:v1:<plugin_key>:<namespace>:`，支持 Get、Set、Delete、List；命名空间和键不允许分隔符、通配符或路径穿越字符。

单值上限 256 KiB，List 默认 100 条且最多 1000 条，TTL 为 0 表示不过期，正 TTL 最多 90 天。List 使用 SCAN，可跨实例读取但不提供强一致分页；当前没有插件总存储量配额。跨重启保存依赖部署端 Redis 持久化，TTL 为 0 不代表已写入 PostgreSQL。

只有清单声明对应 OpenAI OAuth 出站能力的插件才接入账号目录。列表和单账号出站身份解析均限定到启用的 OpenAI OAuth 非影子账号；请求其他平台、API Key、禁用账号或影子账号不会返回可用身份。解析可能返回实际访问令牌、代理和出站身份头，因此该能力属于敏感授权，不能因为目录接口是“查询”就向任意插件开放。

<a id="plugin_status_ui"></a>
## 只读状态与 UI 通道

`GET /api/v1/admin/plugins/:id/status` 需要管理员登录，不要求二次验证，适合轮询。它只调用当前运行实例的 Health（超时 10 秒），不会启动插件、保存配置或执行 TestConfig；已安装但未运行的实例返回 HTTP 200 和不健康状态，不存在的有效插件 ID 返回 HTTP 404（`PLUGIN_NOT_FOUND`），真实存储故障仍返回 500。插件应在 Health 响应中提供可公开给管理员查看的状态，不返回密钥。

插件 UI 通过短期签名会话加载到 `sandbox="allow-scripts"` 的 iframe。父页面校验消息来源窗口、空源 iframe 及当前会话，支持 `plugin.status` 获取同一只读状态；页面切换、iframe 导航及延迟返回不会把旧配置或操作结果转发到另一个插件会话。更改配置与测试等敏感 UI 消息仍走 step-up，不能通过状态通道替代。

## 部署与配置

新增迁移为 `280_plugins.sql`、`281_plugin_artifacts.sql`，只创建插件安装/绑定表及插件包持久字段，不复用上游迁移序号。升级仍由正常启动迁移执行，已执行迁移不得改写校验和。

启动配置 `plugins` 的默认值为：`data_dir` 空（优先使用 `DATA_DIR/plugins`，否则 `./data/plugins`）、`allow_unsigned=false`、`trusted_publishers={}`、`max_upload_bytes=134217728`、`max_uncompressed_bytes=268435456`、`start_timeout_seconds=15`。本地目录必须可写且允许执行插件二进制；容器内的插件运行时必须匹配 Linux 和实际 CPU 架构，不得上传开发机 Windows 可执行文件。更改进程配置后重启。

## 相关文档

- [配置边界](configuration.md)
- [系统架构](../architecture/system_architecture.md)
- [部署与数据库迁移](../operations/deployment_and_migrations.md)
- [上游传输安全](../operations/upstream_transport_security.md)
