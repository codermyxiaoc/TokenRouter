# TokenRouter Docker 镜像

当前标准、本地目录和 standalone Compose 默认从 DockerHub 拉取：

```text
coderxiaoc/tokenrouter:v0.1.278-ct-v2.5
```

在 `.env` 中设置 `SUB2API_IMAGE` 可选择其他已发布标签或镜像摘要。这些 Compose 保留 `pull_policy: always`，部署服务器无需源码。应用依赖 PostgreSQL 和 Redis；Compose 提供运行配置、持久化存储、健康检查和依赖启动顺序。

## Docker Compose

根据部署拓扑选择配置文件：

| 文件 | 服务 | 存储 | 适用场景 |
| --- | --- | --- | --- |
| [`docker-compose.local.yml`](docker-compose.local.yml) | TokenRouter、PostgreSQL、Redis | 本地目录 | 侧重简单备份和迁移的生产部署 |
| [`docker-compose.yml`](docker-compose.yml) | TokenRouter、PostgreSQL、Redis | Docker 命名卷 | 使用 Docker 卷管理的生产部署 |
| [`docker-compose.standalone.yml`](docker-compose.standalone.yml) | 仅 TokenRouter | Docker 命名卷 | 已有外部 PostgreSQL 和 Redis 的环境 |
| [`docker-compose.dev.yml`](docker-compose.dev.yml) | 本地构建的 TokenRouter、PostgreSQL、Redis | 本地目录 | 开发和源码测试 |

完整步骤见 [部署指南](../docs/guides/deployment/index.md)，环境变量示例见 [`.env.example`](.env.example)。

## 从本地源码构建并发布 Ubuntu amd64 镜像

在仓库根目录使用根 `Dockerfile`。它会构建前端并嵌入后端，镜像目标为 `linux/amd64`：

```bash
docker login
docker buildx build --platform linux/amd64 --build-arg VERSION=0.1.278-ct-v2.5 --tag coderxiaoc/tokenrouter:v0.1.278-ct-v2.5 --load --file Dockerfile .
docker run --rm --entrypoint /app/sub2api coderxiaoc/tokenrouter:v0.1.278-ct-v2.5 --version
docker run --rm --entrypoint /usr/local/bin/pg_dump coderxiaoc/tokenrouter:v0.1.278-ct-v2.5 --version
docker push coderxiaoc/tokenrouter:v0.1.278-ct-v2.5
```

版本和 PostgreSQL 客户端检查不挂载现有数据，也不启动应用服务。程序内部版本号不带 `v`，镜像标签保留 `v` 前缀。根 `Dockerfile` 还支持 `COMMIT`、`DATE` 构建参数；发布下一版本时同时替换构建参数、镜像标签与部署端 `SUB2API_IMAGE`。以上命令只发布 `amd64`，不会同时生成 `arm64` 镜像。

### 复用已编译二进制打包镜像

同时交付 Ubuntu 二进制和 Docker 镜像时，可只编译一次前后端，再用 `Dockerfile.goreleaser` 打包同一个 `sub2api`。输入必须来自本次源码的 `linux/amd64`、`GOAMD64=v1`、`CGO_ENABLED=0`、`-tags embed` 构建，已嵌入本次前端、SQL 迁移及版本信息；镜像打包不会重新编译或改写程序版本。前端构建仍需源码中的 `docs/legal/`。定价资源取同次源码的 `backend/resources/`，不要复用旧版本资源。

以下 Bash 命令在仓库根目录执行，`binary` 改为本次已编译程序的实际路径。最小上下文只携带程序、运行时资源、入口脚本和 Dockerfile：

```bash
set -e
binary="$PWD/release/sub2api_v0.1.278-ct-v2.5_linux_amd64/sub2api"
image=coderxiaoc/tokenrouter:v0.1.278-ct-v2.5
build_context="$(mktemp -d)"
test -s "$binary"
mkdir -p "$build_context/backend" "$build_context/deploy"
install -m 0755 "$binary" "$build_context/sub2api"
cp -R backend/resources "$build_context/backend/resources"
install -m 0755 deploy/docker-entrypoint.sh "$build_context/deploy/docker-entrypoint.sh"
cp Dockerfile.goreleaser "$build_context/Dockerfile.goreleaser"

docker buildx build --platform linux/amd64 --load --tag "$image" --file "$build_context/Dockerfile.goreleaser" "$build_context"
docker image inspect "$image" --format '{{.Os}}/{{.Architecture}}'
docker run --rm "$image" --version
docker run --rm --user 1000:1000 "$image" --version
docker run --rm --entrypoint /usr/local/bin/pg_dump "$image" --version
docker run --rm --entrypoint /usr/local/bin/psql "$image" --version
binary_hash="$(sha256sum "$binary" | cut -d ' ' -f 1)"
image_binary_hash="$(docker run --rm --entrypoint sha256sum "$image" /app/sub2api | cut -d ' ' -f 1)"
test "$binary_hash" = "$image_binary_hash"
```

核对平台为 `linux/amd64`、程序版本为 `0.1.278-ct-v2.5`、二进制哈希一致，并完成隔离环境的健康、前端及升级验证后再发布：

```bash
docker login
docker push "$image"
docker buildx imagetools inspect "$image"
```

记录远端返回的实际摘要、平台、程序哈希及本次检查结果，作为部署包中的镜像信息；这些命令本身不代表镜像已经发布或验证通过。`Dockerfile.goreleaser` 为程序设置 `0755`，并提供 PostgreSQL 客户端及其 Alpine 运行库；Ubuntu 手工二进制包仍由宿主机提供 `pg_dump`、`psql`。当前检出没有 `.github/workflows/`，手工发布不会触发自动归档、其他镜像标签或 `VERSION` 回写。

## 服务器拉取与更新

全新部署时，将所选 Compose 文件与 `.env.example` 放入新的部署目录。下面以命名卷版 `docker-compose.yml` 为例初始化配置；先填写数据库密码和稳定安全密钥，再执行后续拉取、启动命令：

```bash
cp .env.example .env
chmod 600 .env
nano .env
```

如需通过服务器 IP 访问应用，应在 `.env` 设置预期的 `BIND_HOST` 和 `SERVER_PORT`，默认应用端口仅绑定 `127.0.0.1:8080`。全新本地目录部署则选用 `docker-compose.local.yml`，并先创建 `data`、`postgres_data`、`redis_data` 目录。

在现有部署目录修改 `.env`，保留原有密码、JWT/TOTP 密钥以及其他配置：

```dotenv
SUB2API_IMAGE=coderxiaoc/tokenrouter:v0.1.278-ct-v2.5
POSTGRES_BIND_HOST=127.0.0.1
POSTGRES_PORT=5433
```

以下命令以既有配置名为 `docker-compose.yml` 为例；如果此前使用 `docker-compose.local.yml`，每条命令都继续使用该文件和原 Compose 项目名。原配置的 `image` 如果是写死的旧标签，直接修改原文件这一行；只有使用 `${SUB2API_IMAGE:-...}` 的配置才会读取上述变量。

先拉取新应用镜像：

```bash
docker compose -f docker-compose.yml config --quiet
docker compose -f docker-compose.yml pull sub2api
```

升级前验证 PostgreSQL 备份并保留应用配置。以下为内置 PostgreSQL 部署示例，在维护窗口停止应用后执行；standalone 使用原外置数据库的备份流程。若备份失败，先启动旧应用并排查，不继续升级：

```bash
docker compose -f docker-compose.yml stop sub2api
backup_dir="backups/pre-v2.5-$(date +%Y%m%d-%H%M%S)"
mkdir -p "$backup_dir"
chmod 700 "$backup_dir"
cp -p .env docker-compose.yml "$backup_dir/"
docker compose -f docker-compose.yml cp sub2api:/app/data "$backup_dir/app-data"
docker compose -f docker-compose.yml exec -T postgres sh -c 'PGPASSWORD="$POSTGRES_PASSWORD" pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc' > "$backup_dir/database.dump"
docker compose -f docker-compose.yml exec -T postgres pg_restore --list < "$backup_dir/database.dump" > /dev/null
```

同时保留原 Compose 覆盖文件、外置配置和原镜像标签。确认备份有效后，仅重建应用：

```bash
docker compose -f docker-compose.yml up -d --no-deps sub2api
docker compose -f docker-compose.yml ps
docker compose -f docker-compose.yml logs --tail=100 sub2api
docker compose -f docker-compose.yml exec -T sub2api /app/sub2api --version
docker compose -f docker-compose.yml exec -T sub2api wget -q -O - http://127.0.0.1:8080/health
```

私有 DockerHub 仓库需要先在服务器执行 `docker login`。应用镜像更新或端口映射变更应使用 `up -d` 重建相关容器；仅 `restart` 不会应用这些变更。已有部署无需用 `.env.example` 覆盖 `.env`，也不要执行 `down -v`。

`--no-deps sub2api` 只更新应用，不替换 PostgreSQL 或 Redis。新应用启动时自动执行待应用迁移。沿用既有数据、配置和稳定安全密钥，完成升级后检查程序版本、健康状态、登录、网关调用和用量记录。

### v2.5 升级检查

从 `v0.1.278-ct-v2.4` 更新到 `v0.1.278-ct-v2.5` 不新增或修改数据库迁移，最高迁移仍为 `288_affiliate_ledger_operation_id.sql`。本次恢复 OpenAI OAuth 账号的手动额度重置入口，并适配用户、管理员工单界面的手机布局。

更新后，账号管理先查询剩余次数，再确认重置；真实重置会消耗上游机会。无次数或无法确认的上游结果不清除账号限流，成功但后续刷新失败会分别提示，不应重复消费。Spark 影子账号仍在母账号操作。工单手机端使用卡片列表、筛选面板及独立对话/回复滚动，权限和处理流程保持原有规则。升级沿用原数据库、配置、数据目录和固定密钥，用户余额、订阅用量、窗口、次数和有效期不因升级重算。跨越 v2.4 升级时仍需遵守下面的历史迁移边界。

### v2.4 历史升级检查

从 `v0.1.278-ct-v2.3` 更新到 `v0.1.278-ct-v2.4` 会执行新增迁移 286–288：追加审核引擎元数据、渠道推理档位倍率配置、提现流水操作号及唯一索引。既有迁移保持不变，旧 Max 倍率、显式零价及现有订阅扣费、窗口和次数规则保留。迁移 288 在普通事务内创建唯一索引，大型流水表需预留维护时间。

更新前验证数据库备份、排空在途请求并停止全部旧应用实例；先启动一个新实例完成迁移，再启动其余新实例，避免新旧审核、倍率与提现语义混跑。升级后确认最高迁移为 `288_affiliate_ledger_operation_id.sql`，抽查余额、订阅用量、重置次数及到期时间。应用回退不会撤销已经结算的扣费或返利，新功能启用后的回退需按数据库备份与兼容性单独处理。

本次同步上游功能与优化，并补充 Grok 七种账号测试模式、其他平台的手动测试端点选择。默认自动测试保留原流程，手动选择只影响本次测试，不写入账号协议配置；图片、视频、语音等真实测试仍可能消耗上游额度。详细边界见[部署与迁移](../docs/operations/deployment_and_migrations.md)、[管理员账号连接测试](../docs/interfaces/http_api.md#account_connection_tests)。

### v2.3 历史升级检查

从 `v0.1.278-ct-v2.2` 更新到 `v0.1.278-ct-v2.3` 不新增 SQL 迁移，最高迁移仍为 `285_subscription_scheduled_reset_counts.sql`。渠道探测新增协议选择与“立即测试”，旧配置缺少协议时继续按 `auto` 执行，不会自动改成某个单一协议。对于只支持 Chat Completions、不允许 `/v1/messages` 的上游，可在分组编辑页选择 Chat Completions 后立即测试。

“立即测试”只保存本次探测配置并执行一次；不会提交其它分组草稿。成功、失败均写入原渠道状态历史，切换协议不会清空历史红色区间。手动与定时探测共用租约，忙碌时不保存本次配置；已开始的探测在浏览器断开后仍按超时结束并保存结果。升级后核对协议选项、单次结果、渠道状态更新与后续定时探测；先完成全部后端实例及前端更新，再使用显式协议，避免旧实例忽略新增协议设置。正常业务的账号协议与路由规则保持原样，订阅额度、重置次数及有效期也不因本次升级改变。完整边界见[渠道探测](../docs/interfaces/model_catalog_and_marketplace.md#group_availability_probe)。

### v2.2 历史升级检查

从 `v0.1.278-ct-v2.1` 更新到 `v0.1.278-ct-v2.2` 不新增 SQL 迁移，最高迁移仍为 `285_subscription_scheduled_reset_counts.sql`，已应用迁移保持不变。本次增加 `gpt-6-sol`、`gpt-6-luna`、`claude-opus-5-5` 的模型目录、账号选择、客户端配置、定价和协议适配，并修正缓存写入与 Fast 档位的展示、结算倍率一致性；显式渠道价格和零价仍保持原有优先级。

沿用 v2.1 的订阅窗口和到点累计重置次数规则，不重算已有次数、不清空用量或修改订阅有效期。升级后核对新模型的可见目录、价格和正常请求扣费；已配置模型白名单的账号需显式加入对应模型，上游也需提供这些模型。二进制部署应同步安装本版本的官方离线定价资源，保留用户自定义定价覆盖；Docker 镜像已包含同版本资源。模型价格与协议边界见[模型目录与展示定价](../docs/interfaces/model_catalog_and_marketplace.md)。

### v2.1 历史升级检查

从 `v0.1.278-ct-v2.0` 更新到 `v0.1.278-ct-v2.1` 不新增 SQL 迁移，最高迁移仍为 `285_subscription_scheduled_reset_counts.sql`。本次对齐订阅额度窗口与重置次数的时间规则：

- 日额度按项目配置时区零点刷新，不超过一天的日卡仍是一次性日额度；周、月额度首次激活或手动重置以实际操作时刻为锚点，自动刷新按锚点的 7 天、30 天整数周期推进。
- 只要重置点严格早于订阅到期就可刷新，不再要求尾段剩余一个完整周期，也不依赖额外月额度保护。原先被该限制挡住的有效重置点会恢复刷新，属于预期的额度权益变化。
- 到点计数采用相同时间表，即使未消费或用量为零也累计；保留本期已有次数，不自动回填或重算历史。首次激活与手动重置本身不加次数，管理员延期保留计数，续费新一期从零开始。
- 仅将明确对应订阅生效当天、且早于生效时刻的旧初始周/月零点锚点按生效时刻识别；后续手动重置等真实锚点保留。无需批量清空用量、窗口或修改有效期。

升级前备份并验证数据库与配置，排空在途请求，停止所有旧后端后完整切换新后端及前端资源。多实例不能混跑新旧额度规则。沿用原数据目录、PostgreSQL、Redis、对象存储及固定 JWT/TOTP 密钥，不执行 `down -v`。升级后抽样核对日/周/月重置时间、临期订阅与管理员延期、用户和管理员计数，以及正常请求的扣费。回退应用不会撤销已经发放的额度或累计的次数，也不会恢复旧规则期间错过的重置点，不能把回退视为业务数据回滚。详细规则见[订阅额度窗口](../docs/domains/payments_and_entitlements.md#subscription_quota_windows)。

### v2.0 历史升级检查

从 v1.9 更新到 `v0.1.278-ct-v2.0` 新增 `285_subscription_scheduled_reset_counts.sql`。它只增加计数水位与索引，保留已有日、周、月次数，以及原额度、窗口、有效期和账务数据；升级前无法可靠恢复的历史次数不自动补算。

- 重置次数按符合额度规则的到点时间累计，即使未使用或用量为零也累计；后台在启动时及每分钟处理，列表查询即时展示。
- 管理员订阅列表显示日、周、月次数。首次激活或手动重置本身不加次数，管理员延期保留次数，续费新一期从零开始。
- 当时实际额度刷新、临期完整窗口与有限外层保护规则保持原样；v2.1 已按上文调整窗口规则。计数任务本身不清零额度、不改变扣费。
- 先停止所有旧后端，再启动新版本，避免旧版实际推进计次与新版到点计次混跑导致重复。沿用原数据库、Redis、对象存储配置和固定安全密钥。

升级后核对迁移 285、版本、健康、登录、任务记录、管理员计数展示及临期延期后的重置时间。迁移不会自动回滚；回退 v1.9 会恢复旧计数语义，不能把回退期间次数视为连续准确的到点统计。详细规则见[订阅额度窗口](../docs/domains/payments_and_entitlements.md#subscription_quota_windows)。

从 v1.8 或更早版本跨级升级还会执行此前未应用的迁移 282–284：图片/视频任务记录、异步图片持久结果及三项计数字段。升级前等待在途生成完成，保留 Redis、S3 配置与固定 TOTP_ENCRYPTION_KEY；不能恢复已经丢失的旧进程内图片。详见[异步图片与任务记录](../docs/domains/media_tasks.md)。

### 历史版本升级要求

从 `v0.1.278-ct-v1.4` 升级到 `v0.1.278-ct-v1.5` 不新增 SQL 迁移，当时最高迁移仍为 `277_allow_opencode_user_platform_quotas.sql`；此前 v1.3 到 v1.4 同样没有新增迁移。

v1.5 的智能路由扩展真实上游错误的换组恢复，并在管理员和用户错误页显示最终恢复分组。恢复目标复用已有错误事件 JSON，不改写历史缺失的快照。升级后抽样检查候选顺序、冷却及恢复分组展示；已输出内容或已产生用量的请求仍不重放。详细边界见[智能路由换组与冷却](../docs/domains/smart_routing_api_keys.md#group_failover)和[恢复错误的查询与归属](../docs/operations/ops_monitoring_and_alerting.md#recovered_error_visibility)。

从 `v0.1.278-ct-v1.2` 升级到 `v0.1.278-ct-v1.3` 新增 `277_allow_opencode_user_platform_quotas.sql`，仅扩展用户平台额度约束以允许 `opencode_go`，不回填用户额度、不修改余额或订阅。所有实例完成升级并重建初始调度快照后，再启用 OpenCode 账号。从 v1.2 或更早版本跨级更新到 v1.5 时，仍须执行以下 v1.3 升级要求。

v1.3 的 WS 执行状态按 API Key、原始线程/会话和 `request_kind` 隔离，Antigravity token 缓存与刷新锁改为按账号隔离。多实例部署应先排空旧 WS 连接，完成全部实例升级和调度快照重建，再让客户端重连；混跑旧实例不能保证新作用域、凭据缓存和账号阈值规则一致。无需清空 Redis。WS 连接容量的缺省系数改为 5.0，已有显式配置继续优先，应观察连接数及代理资源占用。完整边界见[v1.3 升级与缓存切换](../docs/operations/deployment_and_migrations.md#v1_3_upgrade)。

从 v1.1 或更早版本跨版本升级，还会执行此前尚未应用的工单表、类型、优先级和结单身份迁移（273–276），工单附件随数据库保存。迁移不会因为换回旧镜像自动撤销，工单类型和优先级合并也无法仅靠旧镜像恢复；需要精确回滚时必须恢复升级前数据库与配置备份，备份后的新写入也会随之回退。不要删除迁移记录或改写已应用文件的校验和。

**沿用现有存储方式。** 标准 Compose 使用命名卷，本地目录版使用 `./data`、`./postgres_data`、`./redis_data`；两种文件不能互换后直接启动。安装脚本下载本地目录版并把它保存为 `docker-compose.yml`，这种部署仍应更新本地目录版。PostgreSQL 的镜像版本、数据挂载和 `PGDATA` 不随本次应用镜像更新改变；升级前按现有备份流程验证数据库备份。

**原二进制/systemd 部署需要继续连接原数据。** 新建三服务 Compose 会创建自己的数据库，不会自动迁移旧用户、密钥或用量。应先确认原 PostgreSQL、Redis 的地址与容器可达性，使用 standalone 配置连接原依赖，并保留原配置、安装锁及 JWT/TOTP 密钥；若要迁移进容器数据库，则需单独完成数据库备份恢复。容器中的 `127.0.0.1` 指向容器自身，不能直接照搬原宿主机回环地址。

## PostgreSQL 宿主机端口映射

标准和本地目录 Compose 支持以下变量：

| 变量 | 默认值 | 含义 |
| --- | --- | --- |
| `POSTGRES_BIND_HOST` | `127.0.0.1` | PostgreSQL 映射到宿主机的绑定地址 |
| `POSTGRES_PORT` | `5433` | PostgreSQL 映射到宿主机的端口，可改为未占用端口 |

默认可从宿主机通过 `127.0.0.1:5433` 连接。需要从其他机器连接时，将绑定地址改为预期的宿主机接口，并限制允许访问的来源。应用容器仍连接 `postgres:5432`，端口映射不会修改数据库内部端口、账号或数据目录。

`DATABASE_PORT` 仅在 standalone 中控制外置数据库实际连接端口；不要通过它修改上述宿主机映射。standalone、开发版与 Apple Container 不使用 `POSTGRES_BIND_HOST`、`POSTGRES_PORT`。

## 独立容器

直接使用 `docker run` 时，必须提供与 `docker-compose.standalone.yml` 中 `sub2api` 服务相同的设置。至少需要配置：

| 变量 | 用途 |
| --- | --- |
| `AUTO_SETUP=true` | 启用无人值守容器初始化 |
| `SERVER_HOST=0.0.0.0` | 让应用监听容器内网卡 |
| `DATABASE_HOST`、`DATABASE_PORT` | PostgreSQL 地址 |
| `DATABASE_USER`、`DATABASE_PASSWORD`、`DATABASE_DBNAME` | PostgreSQL 凭据和数据库名 |
| `DATABASE_SSLMODE` | PostgreSQL TLS 模式 |
| `REDIS_HOST`、`REDIS_PORT` | Redis 地址 |
| `REDIS_USERNAME`、`REDIS_PASSWORD`、`REDIS_DB` | Redis 凭据和数据库编号 |
| `JWT_SECRET` | 登录会话的稳定签名密钥 |
| `TOTP_ENCRYPTION_KEY` | 双因素认证的稳定加密密钥 |

## 数据库启动恢复

应用启动执行数据库迁移时，会对 PostgreSQL 暂时不可用和连接类错误进行有限次数的指数退避重试；凭据错误、迁移校验失败及其他永久错误会立即返回。标准 Compose 的 PostgreSQL 健康检查同时执行 `pg_isready` 和简单 SQL 查询，应用级重试仍用于宿主机重启后数据库恢复场景。

必须持久化 `/app/data`，把公开端口绑定到预期的宿主机接口，并应用独立 Compose 文件中的安全与资源限制。应用不使用旧的 `DATABASE_URL` 或 `REDIS_URL` 变量。

## 构建支持架构

- `linux/amd64`
- `linux/arm64`

根 `Dockerfile` 支持以上目标；某个 DockerHub 标签实际包含的架构以该次发布结果为准。

## 镜像标签

当前默认固定版本标签 `v0.1.278-ct-v2.5`。后续发布应使用新版本标签，避免同一标签对应不同构建；需要严格固定内容时使用镜像摘要。升级前应验证数据库备份。

## 相关链接

- [GitHub 仓库](https://github.com/TokenFlux/TokenRouter)
- [部署指南](../docs/guides/deployment/index.md)
