# 上游声明倍率探测

本文定义 API Key 账号的上游倍率声明查询、自动刷新和可选账号倍率同步。它与[上游用量查询](upstream_usage.md)独立，不查询钱包余额，不调用模型生成，也不改变用户价格或账号调度排序。

<a id="upstream_billing_probe_contract"></a>
## 上游协议与账号资格

仅具体平台的 `type=apikey` 账号可使用该能力；OAuth、Setup Token、Bedrock 和 Service Account 不适用。平台支持 API Key 不等于其厂商支持倍率声明：目标站点必须实现 Sub2API 的 `GET /v1/sub2api/billing`，请求使用账号自己的 `Authorization: Bearer <api_key>`。探测基于账号已有 Base URL，保留中继部署路径，不根据响应寻找新地址，也不回退到模型调用或余额接口。

只接收已知版本的账单声明结构，并校验分组倍率、用户覆盖倍率、基准倍率、高峰规则、有效倍率和观测时间的类型与一致性。快照只保存白名单字段及脱敏错误分类，不保存 API Key、原始上游正文或代理凭据。404/405 表示上游不支持，HTTP 错误、非法内容或网络失败不能被当作成功倍率。

传输复用账号的代理、TLS 指纹、受保护 Header Override 与现有 URL 安全策略；配置代理不可用时不静默直连。请求禁止重定向，使用超时及响应体上限，避免将凭据转发到上游返回的另一地址。相关安全边界见[上游传输安全](../operations/upstream_transport_security.md)。

<a id="upstream_billing_probe_settings"></a>
## 开关与展示

新建、编辑账号提供两项独立配置，缺失时均为关闭：

- `extra.upstream_billing_probe_enabled`：自动探测上游声明倍率。只定时维护展示快照，本身不改账号倍率。
- `extra.upstream_billing_rate_sync_enabled`：同步上游声明倍率。启用时同时开启自动探测；关闭自动探测时同步也必须关闭。

账号创建/编辑 API 使用同名顶层布尔字段管理这两个开关；编辑和批量更新只接受顶层显式变更，忽略整份 `extra` 中回传的旧开关，避免旧弹窗重新开启已被关闭的同步。创建/导入兼容 `extra` 中的开关，客户端不能通过伪造 `extra.upstream_billing_probe` 写入可信快照。复制账号可以保留开关，但不能继承源账号的观测结果。凭据、Base URL、代理或相关身份变化使旧快照失效；已发出请求的结果不得覆盖修改后的账号。

全局配置保存在 `settings.upstream_billing_probe_settings`，内容为 `enabled` 和 `interval_minutes`。周期默认 30 分钟，允许 5–1440 分钟；全局启用只扫描显式开启自动探测的账号，不会为历史账号补开关。全局暂停周期扫描不改变账号配置，管理员仍可手动探测。

账号列表增加“上游声明倍率”列，区分未探测、成功、不支持、失败与过期快照，提供手动刷新及安全错误说明。列表刷新只读取已保存状态，不因打开页面、排序或滚动而请求上游。管理员通过账号工具菜单调整全局周期。

<a id="upstream_billing_rate_sync"></a>
## 同步与并发不变量

只有成功声明且账号明确开启同步时，才允许原子更新 `accounts.rate_multiplier`。同步值取 `resolved_rate_multiplier`，不使用探测时混入高峰因子的 `effective_rate_multiplier`。自动同步四舍五入保留四位小数，只接受规范化后 `0 < 倍率 <= 100`；零值、负数、非有限值或超界声明不改原倍率。手动设置账号零倍率的既有能力不因此取消。

探测失败保留最近成功数据并标明当前失败状态，不把旧数据描述为实时成功。同步失败、声明不受支持或数值不符合自动写回范围时，原账号倍率保持不变；快照中的 `synced_rate_multiplier` 仅记录该次实际同步值。

后台探测通过多实例锁控制同轮执行，账号并发合并及有界批量限制避免重复查询；刷新周期带随机抖动并遵守有界 `Retry-After`，不支持的上游采用更长周期。写回使用账号身份和观测版本的原子条件校验，再次检查开关及账号倍率：人工修改、关闭同步、换凭据/代理或更晚的探测完成后，旧结果不能重新打开配置或覆盖新值。最近使用时间等无关热字段不参与探测冲突判断，避免正常转发持续阻断同步。成功写回沿用账号缓存与调度投影失效通道。

`rate_multiplier` 仍只影响账号成本统计和账号额度累计。用户余额、订阅、API Key 配额及渠道/分组定价规则保持原语义，也不重算历史使用记录。声明倍率及高峰信息不进入低倍率优先、候选排序或高级调度评分。详见[路由与结算](../domains/routing_and_billing.md)。

## 管理 API 与兼容边界

以下接口只接受管理员认证：

- `GET /api/v1/admin/accounts/upstream-billing-probe-settings`：读取全局配置。
- `PUT /api/v1/admin/accounts/upstream-billing-probe-settings`：校验并保存全局配置。
- `POST /api/v1/admin/accounts/:id/upstream-billing-probe`：主动探测单个账号，返回 `account_id` 与 `snapshot`。

账号开关随原创建、编辑接口保存。探测和设置操作写管理员审计，但审计内容不得包含凭据或原始上游响应。

本次不恢复对外的 `GET /v1/sub2api/billing`，也不恢复旧的低倍率调度设置或公开账单自省。历史迁移 `236_remove_upstream_billing_probe.sql` 保持原样：它描述当时的删除动作，已升级数据库不重跑。新功能使用现有账号 JSONB 与 settings，无需修改历史迁移；旧进程仍可能清理探测字段，应完成全部实例升级后再启用新开关。

相关文档：[账号维护](../operations/account_maintenance.md)、[账号调度与缓存一致性](../architecture/account_scheduling_and_cache.md)、[配置边界](configuration.md)。
