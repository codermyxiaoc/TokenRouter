# 运维监控与告警

本文描述 Ops 信号采集、实时视图、告警评估、邮件报告和健康诊断。它是[可观测性与数据生命周期](observability_and_data_lifecycle.md)的详细专题，不拥有 Usage 结算、预聚合实现或备份内容策略。

## 章节导航

- [信号流水线](#信号流水线)：修改指标、错误或系统日志采集时读取。
- [实时与历史查询](#实时与历史查询)：修改 dashboard、WebSocket 或查询模式时读取。
- [恢复错误的查询与归属](#recovered_error_visibility)：修改智能路由恢复记录、管理员使用记录错误页或最终状态展示时读取。
- [告警评估](#告警评估)：修改规则、持续时间、静默或通知时读取。
- [计划报告](#计划报告)：修改日报、周报或健康摘要时读取。
- [健康与失效语义](#健康与失效语义)：排查空面板、漏报或后台任务故障时读取。

<a id="ops_signal_pipeline"></a>
## 信号流水线

Ops 面同时接收请求错误、独立上游 attempt 错误、入口准入拒绝、系统日志、并发、账号可用性、实时流量和系统指标。每类信号有独立 repository/队列，不能用一个表的计数代替另一类：最终客户端失败可能包含多个上游错误，一次本地拒绝也可能没有任何上游 attempt。

流式 HTTP 200 只表示响应头已提交，不能据此认定模型完整生成或客户端完整接收。智能路由换组恢复成功的请求可以同时具有最终状态 200 与上游错误 503；排障要结合上游错误事件、请求关联 ID 和流失败标记，不能只筛最终非 2xx 请求。下游断开后继续读取上游时，实际收到的显式失败仍需进入 Ops；客户端取消本身不属于上游 503。

`OpsMetricsCollector` 在 Ops 和 monitoring 开关启用时周期采集数据库、Redis、主机/容器、账号负载和运行时指标。多实例通过 Redis leader lock，必要路径可用 PostgreSQL advisory lock，确保一个周期只持久化一次；运行结果写 job heartbeat、耗时和错误。

系统日志 sink 与 request/error capture 使用有界队列。拥塞时按各自策略丢弃或降级，并累计 dropped/health 计数；它们不得反压网关核心转发。系统日志落库失败会执行 2 秒起、60 秒封顶的指数退避，退避窗口内的批次计入 dropped 而不访问数据库，成功后立即清除失败状态。敏感字段在进入存储前清理，request ID、平台、Group、账号和 endpoint 用于关联。

## 实时与历史查询

管理员 Ops API 提供 concurrency、user concurrency、account availability、realtime traffic、错误/上游错误/请求详情、入口拒绝、系统日志和 dashboard snapshot/trend/histogram/token stats。错误列表与详情弹窗必须共享当前时间范围；自定义范围使用同一组 `start_time` / `end_time` 半开区间，任一边界缺失时统一回退到 `1h`，不能把字面量 `custom` 传给后端。QPS WebSocket 用于短窗口实时展示，仍需管理员鉴权，不能视为长期审计源。

历史 dashboard 查询可按配置使用原始表或预聚合，并在覆盖不足时回退。聚合、水位和回填由[使用记录与运维预聚合](pre_aggregation.md)拥有。页面空数据需区分 monitoring 关闭、过滤条件、采集丢弃、聚合覆盖、查询超时和确实无流量。

<a id="recovered_error_visibility"></a>
## 恢复错误的查询与归属

管理员使用记录的错误页通过 `/api/v1/admin/ops/errors` 显式传入 `include_recovered_upstream=true`，同时展示最终失败与上游重试恢复记录。未筛选阶段时，恢复开关只额外纳入 `upstream/account_auth` 的 2xx 记录，不放宽其它成功阶段；用户、Key、分组、状态码、时间和分页筛选仍共同生效。普通请求错误端点 `/admin/ops/request-errors`、用户 `/usage/errors` 和请求失败/SLA 统计保持原有最终失败口径。

管理员列表和详情的 `status_code` 保持既有上游优先展示值，`client_status_code` 单独返回持久化的最终请求状态；`recovered_upstream` 同时依据最终 2xx 与采集器的固定恢复标记判定。界面可显示“503、已恢复、最终 HTTP 200”，不能仅凭流式响应头 200 将失败流标成恢复。错误页的 503 筛选继续匹配上游状态，因此换组后返回 200 的请求仍可被查到。此查询也适用于此前已经保存的恢复记录，不需要数据迁移。

上游尝试事件在发生时保存实际分组 ID/名称、平台、账号、上游端点和模型快照。错误行的提供方归属取失败事件，不能被后继成功候选覆盖；同一请求的其它失败尝试仍保留在详情的上游事件中，最终用量继续属于实际完成结算的候选。既有错误策略的监控跳过规则仍有效。历史事件缺少分组或端点快照时保留原有值，不依据账号当前绑定猜测历史归属，也不原地改写历史记录。

## 告警评估

告警规则包含 enabled、metric type、operator、threshold、window、sustained minutes、scope/filter 和通知动作。评估器按运行设置周期执行，使用 leader lock 避免多实例重复事件；连续 breach 达到持续要求后创建或更新 active event，恢复后关闭/标记 resolved。

静默记录只抑制匹配范围和时间内的通知/事件动作，不删除原始指标。管理员可以确认、解决事件和配置邮件通知。邮件有全局/运行时限流，发送失败进入 heartbeat/日志，不能把事件标成已经送达。

最终错误透传规则的 `skip_monitoring` 只跳过指定客户端错误的常规监控记录，不能跳过系统健康、访问、计费或安全审计。详见[网关错误响应策略](../interfaces/gateway_error_policy.md)。

## 计划报告

计划报告支持日报、周报、错误摘要和账号健康。Cron 使用分钟级五字段表达式，时区来自应用配置；每分钟调度器只执行已到期报告，并用分布式锁和 last-run key 防止重复发送。

报告收件人、启用项、错误最小计数和账号错误率阈值来自数据库运行设置。生成失败或邮件失败写任务 heartbeat；报告是观测摘要，不应作为扣费、SLA 赔付或账号自动恢复的唯一事实。

## 健康与失效语义

- Ops hard switch 关闭采集/查询和后台评估，但不能关闭网关转发、认证或计费。
- 观察 collector/evaluator/report/aggregation/cleanup 各自 heartbeat、leader lock、最近耗时和错误，不能只检查进程存活。
- 规则与运行设置通过数据库持久化和运行时快照传播；更新后检查多实例版本，不以单个管理 API 成功代表全部实例已刷新。
- Retention/cleanup 只删除观测数据；告警、报告或面板缺历史不影响资金账本，但会降低诊断完整性。

相关文档：[可观测性与数据生命周期](observability_and_data_lifecycle.md)、[使用记录与运维预聚合](pre_aggregation.md)、[账号维护](account_maintenance.md)。
