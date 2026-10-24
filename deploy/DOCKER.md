# TokenRouter Docker 镜像

当前标准、本地目录和 standalone Compose 默认从 DockerHub 拉取：

```text
coderxiaoc/tokenrouter:v0.1.278-ct-v3.29
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
docker buildx build --platform linux/amd64 --build-arg ALPINE_IMAGE=alpine:3.23 --build-arg POSTGRES_IMAGE=postgres:18-alpine3.23 --build-arg VERSION=0.1.278-ct-v3.29 --tag coderxiaoc/tokenrouter:v0.1.278-ct-v3.29 --load --file Dockerfile .
docker run --rm --entrypoint /app/sub2api coderxiaoc/tokenrouter:v0.1.278-ct-v3.29 --version
docker run --rm --entrypoint /usr/local/bin/pg_dump coderxiaoc/tokenrouter:v0.1.278-ct-v3.29 --version
docker push coderxiaoc/tokenrouter:v0.1.278-ct-v3.29
```

版本和 PostgreSQL 客户端检查不挂载现有数据，也不启动应用服务。程序内部版本号不带 `v`，镜像标签保留 `v` 前缀。根 `Dockerfile` 还支持 `COMMIT`、`DATE` 构建参数；发布下一版本时同时替换构建参数、镜像标签与部署端 `SUB2API_IMAGE`。以上命令只发布 `amd64`，不会同时生成 `arm64` 镜像。

### 复用已编译二进制打包镜像

同时交付 Ubuntu 二进制和 Docker 镜像时，可只编译一次前后端，再用 `Dockerfile.goreleaser` 打包同一个 `sub2api`。输入必须来自本次源码的 `linux/amd64`、`GOAMD64=v1`、`CGO_ENABLED=0`、`-tags embed` 构建，已嵌入本次前端、SQL 迁移及版本信息；镜像打包不会重新编译或改写程序版本。前端构建仍需源码中的 `docs/legal/`。定价资源取同次源码的 `backend/resources/`，不要复用旧版本资源。

以下 Bash 命令在仓库根目录执行，`binary` 改为本次已编译程序的实际路径。最小上下文只携带程序、运行时资源、入口脚本和 Dockerfile：

```bash
set -e
binary="$PWD/release/sub2api_v0.1.278-ct-v3.29_linux_amd64/sub2api"
image=coderxiaoc/tokenrouter:v0.1.278-ct-v3.29
build_context="$(mktemp -d)"
test -s "$binary"
mkdir -p "$build_context/backend" "$build_context/deploy"
install -m 0755 "$binary" "$build_context/sub2api"
cp -R backend/resources "$build_context/backend/resources"
install -m 0755 deploy/docker-entrypoint.sh "$build_context/deploy/docker-entrypoint.sh"
cp Dockerfile.goreleaser "$build_context/Dockerfile.goreleaser"

docker buildx build --platform linux/amd64 --build-arg ALPINE_IMAGE=alpine:3.23 --build-arg POSTGRES_IMAGE=postgres:18-alpine3.23 --load --tag "$image" --file "$build_context/Dockerfile.goreleaser" "$build_context"
docker image inspect "$image" --format '{{.Os}}/{{.Architecture}}'
docker run --rm "$image" --version
docker run --rm --user 1000:1000 "$image" --version
docker run --rm --entrypoint /usr/local/bin/pg_dump "$image" --version
docker run --rm --entrypoint /usr/local/bin/psql "$image" --version
binary_hash="$(sha256sum "$binary" | cut -d ' ' -f 1)"
image_binary_hash="$(docker run --rm --entrypoint sha256sum "$image" /app/sub2api | cut -d ' ' -f 1)"
test "$binary_hash" = "$image_binary_hash"
```

核对平台为 `linux/amd64`、程序版本为 `0.1.278-ct-v3.29`、二进制哈希一致，并完成隔离环境的健康、前端及升级验证后再发布：

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
SUB2API_IMAGE=coderxiaoc/tokenrouter:v0.1.278-ct-v3.29
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
backup_dir="backups/pre-v3.28-$(date +%Y%m%d-%H%M%S)"
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

### v3.28 Grok 创作台与画布视频

从 v3.27 升级新增迁移 `316_creative_grok_video.sql`，保存 Grok 画布视频的发送防重标记和原用户、Key、分组、账号及任务关联。创作台可添加有权限和有效价格的 Grok 视频模型及映射别名，接通单张参考图、生成、任务查询、播放、下载和原 Grok 完成后扣费；独立 Video 模型保持原链路。

升级前备份 PostgreSQL、配置、私有素材和稳定安全密钥。完成迁移并统一升级全部应用、后台恢复实例和嵌入前端后，再添加 Grok 视频白名单。不要让旧实例恢复新的 `endpoint=grok` 画布执行。上线检查迁移 316、模型与参数配置、原任务关联及已结算结果下载；真实生成会消耗上游额度，应按需单独验证。

回退前关闭新 Grok 画布视频创建，由新版核对在途、受理结果不明、待结算和待归档执行。未知受理不得清除防重标记后重发；保留新增关联表、原任务和账务回执，不撤销已完成扣款或用旧数据库覆盖新数据。完整边界见 [v3.28 升级](../docs/operations/deployment_and_migrations.md#creative_grok_video_upgrade)。

### v3.27 失败媒体账务处理与智能路由扩容

从 v3.26 升级新增迁移 `315_media_task_billing_resolutions.sql`，保存媒体任务唯一的永久账务决定。管理员可对已失败且仍有待处理原预留的图片/视频任务，填写原因后释放预留或确认原预留金额扣款；不是对已扣款任务重复退款，也不按当前价格重新收费。

系统设置“功能特性 → 媒体任务待确认处理”增加超时分钟数，默认 **60 分钟**，允许整数 **1–10080**。新版启动后立即处理符合条件的历史失败预留，之后每分钟继续扫描；期限配置与 R2 图片保存期独立。升级前核对预期策略并备份数据库、配置与稳定安全密钥，排空并停止全部旧应用和账务维护实例后再统一升级，不能混跑旧维护器。上线后检查迁移、配置保存、预留状态及账务决定。

智能路由同时从最多 10 个分组调整为 **20 个分组**，包含配置校验、界面提示和实际切组上限；现有顺序与冷却语义不变。全部实例更新后再保存超过 10 组的配置，回退旧版前先恢复旧上限。

回退二进制不会撤销已经提交的释放或扣款，保留新决定表、原账本和审计记录。回退前停止新操作并核对待处理任务，不用旧数据库覆盖升级后资金数据。完整边界见 [v3.27 升级](../docs/operations/deployment_and_migrations.md#v3_27_upgrade)。

### v3.26 媒体订阅倍率优先

本版包含 v3.25 的创作台视频模型参数保存修复，并新增迁移 `314_group_media_subscription_rate_priority.sql`。分组图片和视频独立倍率下各增加“订阅倍率优先”开关，默认关闭；开启后，订阅有最终分组的有效正数覆盖倍率时采用该倍率，否则回退媒体独立倍率，余额计费不变。既有分组、旧任务及已经冻结的资金合同保持原语义。

鉴权缓存版本提升为 v40，普通和复合 Key 的旧快照自动重建，无需清空 Redis。先完成镜像拉取、校验及数据库与配置备份，再短停切换应用；统一升级全部应用、后台恢复实例及前端之后，再启用新开关。升级后检查迁移、默认关闭、开关保存/复制、缓存刷新，以及订阅覆盖、缺省回退和余额计费。创作台的视频参数保存同时按下节检查。

回退前关闭新开关，并核对和处理尚未完成首次报价的 Grok/Seedance 任务；保留新增列、任务、Quote 与预占账本，不用旧数据库覆盖升级后的业务数据。完整边界见 [v3.26 升级](../docs/operations/deployment_and_migrations.md#v3_26_upgrade)。

### v3.25 创作台视频模型参数保存

从 v3.24 升级不新增或修改 SQL 迁移，最高迁移仍为 `313_image_object_cleanup.sql`。创作台的视频模型候选返回实际端点与默认参数，保留账号映射的请求模型名；管理员只填写部分时长、分辨率或比例时，其余空集合按所选端点补齐，保留已填参数和显式零张参考图。切换端点只清理未编辑的旧默认值，非法参数提供具体错误，端点默认值不代表供应商承诺支持。

升级前备份数据库和配置，沿用原 PostgreSQL、Redis、素材目录与稳定安全密钥，统一更新应用及嵌入前端。更新后检查已有视频模型配置、新增和部分填写后的保存、映射别名及切换端点的默认值；原价格、预扣与正式视频 API 校验不变。回退无需撤销迁移，但会恢复旧表单和缺省参数处理；不要删除已有模型配置或任务。完整边界见 [v3.25 升级](../docs/operations/deployment_and_migrations.md#v3_25_upgrade)。

### v3.24 Yezonghui 视频兼容

从 v3.23 升级不新增或修改 SQL 迁移，最高迁移仍为 `313_image_object_cleanup.sql`。Video 账号在 OpenAI Videos 端点下新增显式的“Yezonghui 视频兼容”方案，管理员测试、创作台与画布共用请求适配、任务查询和下载。既有账号默认方案和在途任务保持原配置；本站价格、预扣、结算与失败释放沿用现有视频账本。

先备份数据库、配置和私人素材目录，统一更新全部应用、视频恢复实例与前端，再在对应账号或模型上启用新方案并补齐本站价格。带参考图时须配置上游可访问的本站公网 HTTP(S) 前端基础 URL，确保根路径 `/api/v1/video-references/` 能路由到后端，所有实例共享同一 Redis；短期图片副本在 Redis 保存两小时，不改写原素材或桶权限。链接失效不会自动重发生成。

新任务会冻结 `yezonghui_v1` 方案，旧程序无法接管。回退前停止新增 Yezonghui 请求，由新版处理完在途、待核对及待结算任务，再恢复旧版可识别的账号方案；保留任务、预扣和资金账本，不通过删除记录或重复生成解除异常。完整升级和回退边界见 [v3.24 升级](../docs/operations/deployment_and_migrations.md#v3_24_upgrade)。

### v3.23 异步图片存储与到期清理

从 v3.22 升级新增迁移 `313_image_object_cleanup.sql`，保存新上传异步图片的到期清理记录。后台“数据备份 → 异步图片存储”增加独立的保存时间，默认 24 小时，可设整数 1–8760 小时。每张图片首次上传前固定期限，之后的重试或设置变更不延期；旧版本未登记的桶内图片不追溯删除，创作台素材及备份不受影响。

升级前备份数据库、应用配置和稳定的 `TOTP_ENCRYPTION_KEY`，并将该密钥持久保存在原部署配置中；仅使用 YAML 配置异步存储也不能依赖进程临时密钥。新任务受理前检查清理表与稳定密钥，并对目标目录执行写入、读取及删除探针；后台测试连接必须通过。探针成功缓存最多 30 秒，仍可能发生生成期间的存储故障。图片下载、格式/大小、存储权限、缺桶和超时错误按安全分类展示，原费用与退款规则不变。

服务启动及运行期间清理到期对象，关闭新生成不停止既有清理；更换桶或凭据时保留旧桶的删除权限与原加密密钥。回退到 v3.22 或更早程序会暂停自动清理，重新运行新版后继续；不要删除清理表或密钥，应用回退也不能恢复已经物理删除的图片。完整升级与回退边界见 [v3.23 升级](../docs/operations/deployment_and_migrations.md#v3_23_upgrade)。

### v3.21 云端无限画布

从 v3.20 升级新增迁移 310–312，创建私人画布、云端素材、图片批次和文本/视频/音频执行记录。`/creative` 替换为无限画布，管理员统一配置模型和价格，用户无需录入渠道密钥。旧浏览器画布不迁移，历史任务与资金账本保留。

升级前备份数据库与 `/app/data`，并确保所有实例共享、持久挂载画布素材目录。上传与生成素材可访问 7 天，后台启动补偿并每周清理过期文件；旧画布布局和文字继续保存。统一升级应用、worker 和前端后启用新模型；回退前停止新请求，由新版排空在途、待核对和待结算任务，旧版不能恢复新的音频/文本执行。完整边界见 [v3.21 升级](../docs/operations/deployment_and_migrations.md#v3_21_upgrade)。

本次同时修复图片编辑 multipart 类型、图片批次核对状态、上传替换防重、账号切换会话隔离、后台视频配置及在途容量提示。模型可编辑参数仍受上游支持范围与本站价格校验约束；发布代码不代表所有上游模型均可用。

### v3.20 Midjourney 尺寸与比例

从 v3.19 升级不新增或修改 SQL 迁移，最高迁移仍为 `309_creative_quota_platform.sql`。MJ 工作台开放 `1K`、`2K`、`4K` 及 `1:1`、`4:3`、`3:4`、`16:9`、`9:16` 五种比例，只展示有显式价格的尺寸档位，明确零价仍有效。实际输出像素由上游决定，按用户所选档位计费；界面在尺寸选择处说明此规则，不保证返回像素等于所选标称尺寸。

沿用一次生成四张、预扣四张并按实际有效图片数结算的合同，已创建任务保留原档位价格快照。升级全部应用、工作台 worker 与前端后再使用新档位；回退前停止新增 MJ 任务，让新版完成高档位或非正方形任务的生成、结算、释放与待核对处理，旧版仅支持 `1K`、`1:1`，不能接管这些在途任务。完整边界见 [v3.20 升级](../docs/operations/deployment_and_migrations.md#v3_20_upgrade)。

### v3.19 Midjourney 图片与工作台

从 v3.18 升级不新增或修改 SQL 迁移，最高迁移仍为 309。新增 Midjourney 品牌及忽略大小写的 `mj` 前缀图片识别，工作台通过 OpenAI Images 兼容端点提交一次请求，四张图片分别展示与计费。启用前必须配置明确的图片价格；按最多四张预留，按实际有效图片结算，多余预留释放。

先备份数据库和配置，统一更新应用、工作台 worker 与前端，再启用 MJ。回退前停止新的 MJ 请求并排空全部在途任务、预留和待核对记录；旧 worker 不支持四张输出，不能接管新任务。保留图片任务和资金账本，不通过删除账本释放冻结金额。完整升级与回退边界见 [v3.19 升级](../docs/operations/deployment_and_migrations.md#v3_19_upgrade)。

### v3.18 视频协议适配与预览

从 v3.16 升级不新增或修改 SQL 迁移，最高迁移仍为 309。Video 账号可按端点和最终模型选择版本化官方、可美或原有协议，新增 Vidu 四种原生操作，并修复 Seedance 分辨率、首尾帧比例和帧数计费，以及 Kling/Vidu 暂缺产物的结算恢复。原账号缺少新配置时保留原行为，已结算任务不重算。

升级前备份并停止新视频提交、排空在途创建，统一更新全部应用、视频恢复实例及前端，再启用新方案。显式方案创建版本二任务快照，旧程序无法处理；回退前须完成相关任务的结算或确认释放，核对未知任务并恢复旧版可识别的账号配置，保留任务及资金账本。无需新增环境变量，沿用原数据库、Redis 和稳定安全密钥。

管理员测试支持协议相关参数和独立预览重载，重载不会重新生成。默认 CSP 已增加媒体来源，旧自定义 CSP 需自行核对 `media-src`，否则外部视频仍可能无法播放。完整配置、升级与回退边界见 [v3.18 升级](../docs/operations/deployment_and_migrations.md#v3_18_upgrade)。

### v3.16 MiniMax 任务 ID 兼容

从 v3.15 升级不新增 SQL 迁移。修复 MiniMax H3 创建返回 `TaskId` 时无法接续查询的问题；保存了创建响应的旧未知任务可恢复查询及幂等结算，不重发生成。异常受理字段保留核对，缺少 Token 用量不会按零结算；管理员历史测试未保存的响应不能自动追回。

升级前备份并排空在途创建，统一更新应用和恢复器，沿用账号、端点与计价配置。完整升级与回退边界见 [v3.16 升级](../docs/operations/deployment_and_migrations.md#v3_16_upgrade)。

### v3.15 Sub2API 0.2.17 兼容适配

从 v3.14 升级不新增 SQL 迁移，最高迁移仍为 309。同步 Gemini/Antigravity 模型与思考档位、OpenAI 协议兼容、Codex 工作区隔离、GLM 用量窗口及支付退款恢复，沿用当前项目的路由与计费账本。

升级前备份数据库和配置，暂停新退款并排空提交，记录待确认订单；统一更新全部应用实例。新版本会保留待确认退款的扣减和订阅暂停快照，缺少本次退款号时需要人工核对，不自动重发。回退前须完成新版本退款核对并保留审计账本。模型退役判断、最低输出配置及完整升级边界见 [v3.15 升级](../docs/operations/deployment_and_migrations.md#v3_15_upgrade)。

### v3.14 H3 视频协议与超时预扣恢复

从 `v0.1.278-ct-v3.13` 升级不新增 SQL 迁移，最高迁移仍为 `309_creative_quota_platform.sql`。独立 Video 的 OpenAI Videos 账号新增显式“秘塔 H3”适配：仅在账号选择 `video_openai_profile=metaso_h3` 后，才将旧时长、分辨率、比例和单张参考图转换为 `seconds`、`size`、`input_reference.image_url`；默认保真，不按模型名或域名自动启用。定价支持精确尺寸档位，结算使用有效的实际返回时长；异常终态元数据保留核对，不按旧请求参数猜测扣费，也不重算历史已结算费用。

创建总预算默认 180 秒，可通过 `gateway.video_create_timeout_seconds` 或环境变量 `GATEWAY_VIDEO_CREATE_TIMEOUT_SECONDS` 设置，0 使用默认值，最大 600 秒。租约随预算延长。新任务明确超时，或本版提交进程中断后，只有没有任务 ID 和持久响应证据、且具有可靠起算时点时，才在核对 15 分钟后幂等释放预扣，不自动重发；平台承担上游可能已经受理的成本。已取得响应、正文读取失败、已有或冲突 ID，以及没有故障分类或提交时点的旧未知任务，继续按原流程核对。

升级前验证数据库及配置备份、停止新提交并排空在途创建，统一切换全部应用和后台工作进程，不能混跑旧恢复器。沿用原数据库、Redis、数据目录及稳定密钥；完成升级后再启用 H3 适配或精确尺寸价档，并核对任务状态、实际秒数、扣款与预扣释放。回退前先停流、排空并核对待结算/待释放任务，再将 H3 适配与精确尺寸价格配置恢复为旧版可识别的状态或升级前配置，保留全部任务及资金账本。旧版不识别新配置和超时分类，不能仅替换程序回退。完整边界见 [v3.14 升级](../docs/operations/deployment_and_migrations.md#v3_14_upgrade)。

### v3.13 上游故障切换与并发资金保护

从 `v0.1.278-ct-v3.12` 升级不新增 SQL 迁移，最高迁移仍为 309。优化同组账号切换、智能路由跨组共享账号排除、模型与入口冷却，以及 Gemini 写出响应前的安全恢复。独立 Video 只在明确未受理时切换同组兼容端点账号，复用原任务和预扣，按最终执行账号记录成本；已有任务 ID、输出、用量或受理结果不明时不自动重复生成。

修复首次发送前取消仍请求上游、参考素材 404 被误判为模型缺失、显式冷却规则被默认值覆盖等边界。沿用原数据、配置和稳定密钥，不重算历史账单、不清空 Redis。升级与回退前排空在途提交，统一更新应用和后台工作进程，保留未结算任务及资金账本；从更早版本升级仍须执行下方对应迁移要求。完整边界见 [v3.13 升级](../docs/operations/deployment_and_migrations.md#v3_13_upgrade)。

### v3.12 异步任务恢复与平台额度统计

从 `v0.1.278-ct-v3.11` 升级新增迁移 306–309，保存旧视频任务的恢复账本、异步图片结算依据、创作任务使用记录投递状态与原始平台快照。成功任务可在后台恢复状态、扣费和使用记录；重复恢复以持久去重依据避免重复扣费或累计，未保存生成结果的旧请求不会被自动重新生成。

平台额度与资金结算、请求去重改为同一数据库事务，停止旧 Redis 增量和绝对快照回写。升级前排空在途请求，停止全部旧应用及工作进程，保存数据库、配置和原平台缓存状态，再启动一个新实例完成迁移，最后启动其余新实例。不能混跑旧版；不清空 Redis、不重置用户额度，也不自动猜测补加历史漏记。

回退前须处理新任务的待结算、待释放与使用记录投递，核对平台额度基线和旧缓存；旧程序不能维护新恢复账本，不应直接替换程序继续运行。完整升级与恢复边界见[平台额度事务化升级](../docs/operations/deployment_and_migrations.md#transactional_platform_quotas)。

### v3.11 自动结算订阅顺序

从 `v0.1.278-ct-v3.10` 升级不新增或修改 SQL 迁移，最高迁移仍为 `305_gemini_cached_contents_mutation_lease.sql`。自动结算优先使用下一次有效额度重置或到期时间最近的可用订阅，预检、倍率解析和事务分配共用排序规则；不足时继续按既有规则使用其他合格订阅和余额。

沿用原数据库、Redis、数据目录与稳定安全密钥，无需重建 API Key。全部实例统一升级，避免同一请求的预检与结算混用排序规则。指定订阅、仅余额模式以及已有固定预占合同的资金快照保持原规则，不重算历史账单；Token 视频终态重分配继续使用当时有效的订阅顺序。升级核对与回退边界见 [v3.11 升级](../docs/operations/deployment_and_migrations.md#v3_11_upgrade)。

### v3.9 每日活动资格与并发资金修复

从 `v0.1.278-ct-v3.8` 升级新增 `301_daily_activity_credit_events.sql`，记录兑换码余额和管理员加款的活动资格事件，不改已有余额、不回填历史兑换或调账。两个可选来源开关默认关闭，只记录各自开启后的新增到账；先完成全部实例及前端升级，再按需开启。

三类活动可独立启停，旧规则缺失独立开关时兼容开启；停用只隐藏卡片并阻止新领取，保留进度、历史及未领次数。外部支付购买订阅按原订单实付金额的数值直接累计，站内余额支付排除；在线余额充值仍只累计本金。退款不扣减活动累计或资格，首次开启后已完成的历史退款订单也按原完成时间计入，因此升级后可领取次数可能增加，不会自动领取或修改余额。

修复兑换事务查询额外占用连接导致并发超时，以及领取成功响应不完整时前端提前清除请求号的问题。升级需同时更新后端和嵌入前端。活动事件及领取账本必须随数据库备份，回退前先关闭两个来源和活动并排空在途入账及领取；不能删除账本重置资格。完整规则与升级核对见 [v3.9 升级](../docs/operations/deployment_and_migrations.md#v3_9_upgrade)。

### v3.8 视频账号测试与每日活动

从 `v0.1.278-ct-v3.7` 升级新增 `300_daily_activities.sql`，建立每日活动领取、幂等批次、消费事件和累计消费账本。新增 Video 账号手动测试，可按账号已配置模型及端点选择参数，查看异步进度和生成结果。

每日活动默认关闭。先备份数据库与配置，完成全部实例及前端升级，再到“系统设置 → 每日活动”配置并首次开启；首次开启后才累计充值与实际消费，不回填历史金额。每日充值礼包按北京时间零点刷新，充值和消费奖励的累计进度与未领次数跨日保留。临时关闭活动不会重置起点及累计进度。

活动账本属于资金记录，须随数据库备份，不得当作日志清理。旧程序不会记录活动消费事件，混用旧实例或回退会缺少该期间的累计资格；只回退程序不会撤销已发放奖励或余额，也不应删除账本恢复状态。完整规则与升级检查见 [v3.8 升级](../docs/operations/deployment_and_migrations.md#v3_8_upgrade)。

### v3.7 Gemini 第三方模型同步修复

从 `v0.1.278-ct-v3.6` 升级到 `v0.1.278-ct-v3.7` 无新增数据库迁移。新增账号同步模型时保留第三方接入来源；原生列表返回端点不支持或明确的 Cloudflare 拦截时，受控尝试同站点兼容列表，并修复同步失败提示丢失具体原因、重复显示的问题。实际生成仍使用 Gemini 原生协议，列表成功不代表上游生成端点已放行。完整条件见 [Gemini 模型同步](../docs/interfaces/gemini_upstream.md#gemini_model_list_sync)。

更新应用镜像及嵌入前端后生效，沿用既有数据库和配置；从更早版本升级仍须遵守下方对应版本的迁移与回退要求。

### v3.6 视频模型详情

从 `v0.1.278-ct-v3.5` 升级到 `v0.1.278-ct-v3.6` 新增 `299_channel_model_details.sql`，为渠道价卡添加详情 JSONB 列；分组沿用既有配置。视频模型支持逐模型开关和纯文本说明，模型广场按分组覆盖、渠道继承展示。旧价卡默认不显示说明，纯说明条目不参与新版本计费匹配。

先备份数据库与配置，完成所有后端、前端升级后再编辑详情。旧版无法识别纯说明条目，直接回退可能留下空收费价卡，遮住渠道价格或改变模型准入。回退前应清除分组和渠道中仅含说明、没有任何收费设置的条目，或恢复升级前的配置备份；含显式价格（包括零价）、倍率、预扣、图片附加费或收费层级的价卡应保留。不要混用旧版编辑或直接回退。完整边界见[v3.6 升级](../docs/operations/deployment_and_migrations.md#v3_6_upgrade)。

### v3.5 上游兼容同步与安全、计费修复

从 `v0.1.278-ct-v3.4` 升级到 `v0.1.278-ct-v3.5` 新增迁移 296–298，分别增加充值赠金快照、扩展新平台额度约束、将订单七项金额快照扩为 `decimal(21,3)`。保留 Video/Qoder、订阅倍率、异步媒体任务和图片资金保护等既有能力，不重算历史余额、价卡或用量。

升级前备份数据库和配置，在支付低峰排空在途请求及 WebSocket 连接，先升级一个实例完成迁移，再完成其余实例与前端更新。迁移 298 会取得订单表 DDL 锁，应观察锁等待；升级前发出的找回密码链接需重新申请。新平台和充值优惠应在全部实例更新后启用，旧程序不能保证新平台配置、赠金及三位小数币种处理一致。

交付 Ubuntu `linux/amd64` 安装包与同程序 Docker 镜像。升级后核对迁移 298、版本、健康、登录、充值报价与到账、网关及媒体任务扣费。仅回退程序不会撤销迁移或已结算权益；已被旧版本舍入的金额也不会自动还原。完整边界见[上游兼容同步升级](../docs/operations/deployment_and_migrations.md#v3_5_upgrade)。

### v3.4 异步图片扣费与资金预占修复

从 `v0.1.278-ct-v3.3` 升级到 `v0.1.278-ct-v3.4` 新增 `295_image_billing_reservations.sql`，创建独立图片资金合同表及索引，不修改既有迁移、价卡、历史用量或用户余额。本次修复自动扣费从订阅切换余额时误用订阅倍率，并为按固定图片价格结算的 OpenAI/Grok 异步图片请求增加生成前资金预占与同步幂等结算；渠道按 Token 计费的图片继续沿用原后扣流程。

升级前停止旧实例接收异步图片请求，等待在途任务完成，备份数据库和配置后再替换全部应用实例。不可混跑旧版后扣与新版预占。明确失败释放预占；无法确认上游执行结果的请求保留资金并进入待对账，不自动重放。此次升级不自动纠正历史负余额或退还历史误扣；回退前必须排空或核对释放新版本预占，旧版本没有新账本维护能力。

交付 Ubuntu `linux/amd64` 安装包及同程序 Docker 镜像，沿用原 PostgreSQL、Redis、数据目录和固定安全密钥。更新后核对迁移 295、版本、健康、资金冻结与结算记录。完整边界见[异步图片资金保护升级](../docs/operations/deployment_and_migrations.md#async_image_billing_upgrade)。

### v3.3 Z-Pay 查单与取消订单修复

从 `v0.1.278-ct-v3.2` 更新到 `v0.1.278-ct-v3.3` 不新增或修改 SQL 迁移，最高迁移仍为 `294_intelligence_tests.sql`。本次交付 Ubuntu `linux/amd64` 二进制和同平台 DockerHub 镜像，修复 Z-Pay 官方域名直接使用 GET 查单；其他 EasyPay 平台保留 POST，并仅在成功但空白的响应后单次回退 GET。后台支付配置的随机订单探测可识别明确的“订单不存在”响应。

沿用原数据库、Redis、数据目录、Compose 项目和固定安全密钥，只更新应用版本。升级不会批量改写旧订单状态或余额；此前取消失败的订单可重新点击取消，后台后续按正常查单、退避与对账流程处理。状态仍无法确认时保留 `PENDING`；确认未付款后才能本地取消，已付款继续按验签、金额校验和幂等履约规则处理。

更新后核对版本、健康、支付配置测试和订单查询结果，检查未付款取消、已付款入账及重复通知没有重复发放权益。回退 v3.2 无需撤销迁移，但会恢复原查单兼容问题。完整边界见[部署与数据库迁移](../docs/operations/deployment_and_migrations.md#v3_3_upgrade)及[EasyPay 查单兼容](../docs/domains/payments_and_entitlements.md#easypay_order_query)。

### v3.2 画图缩略图滑动修复

从 `v0.1.278-ct-v3.1` 更新到 `v0.1.278-ct-v3.2` 不新增或修改 SQL 迁移，最高迁移仍为 `294_intelligence_tests.sql`。本次交付 Ubuntu `linux/amd64` 二进制和同平台 DockerHub 镜像，修复降智检测画廊的鼠标拖动、滚轮、键盘与触摸滑动，并隔离缩略图内部滚动和焦点；作品详情保留原滚动、交互及沙箱权限。

沿用原数据库、Redis、数据目录、Compose 项目和固定安全密钥，更新后核对版本、健康、画廊滑动与作品详情。检测配置、历史和计费规则保持不变；回退 v3.1 无需撤销迁移，但会恢复原缩略图操作问题。完整边界见[部署与数据库迁移](../docs/operations/deployment_and_migrations.md#v3_2_upgrade)。

### v3.1 降智检测错误与未知评判修复

从 `v0.1.278-ct-v3.0` 更新到 `v0.1.278-ct-v3.1` 不新增或修改 SQL 迁移，最高迁移仍为 `294_intelligence_tests.sql`。本次交付 Ubuntu `linux/amd64` 二进制和同平台 DockerHub 镜像，修正糖果、画图检测的错误分类、空错误字段误判和未知评判显示。

新收到的失败结果只保留安全的错误分类、说明与 HTTP 状态码，区分额度不足、限流、鉴权、超时等原因；检测服务未给出明确判定时显示“未知”，不能据此判定模型降智。提交是否受理不明确时仍不自动重复提交，避免重复产生模型请求及费用。已有记录中被旧版本丢弃的错误详情不会自动补回，原有路由、计费及检测配置保持不变。

沿用原数据库、Redis、数据目录、Compose 项目和固定安全密钥，完成后端和前端更新后核对版本、健康，以及检测详情中的失败原因与未知状态。回退 v3.0 无需撤销迁移，但新检测会恢复旧错误处理方式；跨版本升级继续遵守下述历史迁移要求。完整边界见[部署与数据库迁移](../docs/operations/deployment_and_migrations.md#v3_1_upgrade)及[降智检测](../docs/domains/intelligence_tests.md)。

### v3.0 降智检测普通 Key 修复

从 `v0.1.278-ct-v2.9` 更新到 `v0.1.278-ct-v3.0` 不新增或修改 SQL 迁移，最高迁移仍为 `294_intelligence_tests.sql`。本次修复普通 API Key 默认开启“不可用时自动降级”时，在降智检测中无法选择或保存的问题；交付 Ubuntu `linux/amd64` 二进制和同平台 DockerHub 镜像。

普通 Key 的自动降级开关不再影响检测资格，也不会被检测配置修改。仍要求 Key 绑定所选且启用的分组，并满足原有归属、状态、有效期和额度检查；复合、智能路由与系统管理 Key 仍不能用于检测。远端模型请求继续使用 Key 原有网关路由和计费规则；检测过程中若原分组停用，自动降级可能使结果来自其他分组，需要固定分组时在密钥管理中关闭该选项。

沿用原数据库、Redis、数据目录、Compose 项目和固定安全密钥，完成全部后端及前端更新后，核对默认开启自动降级的普通 Key 能被选择、保存和正常执行检测，并确认原有权限与账务规则保持不变。回退 v2.9 无需撤销迁移，但会恢复该 Key 资格问题；跨越 v2.9 或更早版本升级时继续遵守下述历史迁移要求。完整边界见[部署与数据库迁移](../docs/operations/deployment_and_migrations.md#v3_0_upgrade)及[降智检测](../docs/domains/intelligence_tests.md#intelligence_access)。

### v2.9 降智检测升级检查

从 `v0.1.278-ct-v2.8` 更新到 `v0.1.278-ct-v2.9` 新增 `294_intelligence_tests.sql`，只创建独立的检测配置、执行记录表及索引，不回填或修改既有余额、订阅、额度、账本、用量记录或计费规则。本次交付 Ubuntu `linux/amd64` 二进制和同平台 DockerHub 镜像；Apple Container 的 `linux/arm64` 环境须选择实际已发布的兼容镜像。

降智检测总开关与定时检测默认关闭，升级不会自动创建检测任务。继续使用原数据库、Redis、数据目录和稳定的 `JWT_SECRET`，多实例必须使用相同密钥；检测 Base URL 必须是 Manxue 可访问的本站公网 HTTPS 网关地址。启用前配置只绑定目标分组的专用普通站内 API Key，其模型请求仍通过既有网关正常计费。

升级后核对迁移 294、版本与健康、原有账务数据、开关关闭行为，以及启用后的管理员配置和用户分组权限。关闭总开关只阻止新检测；已获得远端 ID 的任务继续 GET 轮询并保存结果，不能因为重启、关开功能或 Key 停用而重新 POST。回退前先关闭新检测并等待已提交任务结束，旧版本不具备检测恢复器；缺少远端 ID 的 `submitting` 记录保持未知并先核对使用记录，不通过删除记录重试。完整升级与恢复边界见[部署与数据库迁移](../docs/operations/deployment_and_migrations.md#v2_9_upgrade)及[降智检测](../docs/domains/intelligence_tests.md)。从 v2.7 或更早版本跨级升级时，还须遵守下述 v2.8 视频平台升级要求。

### v2.8 历史升级检查

从 `v0.1.278-ct-v2.7` 更新到 `v0.1.278-ct-v2.8` 新增迁移 289–293，最高迁移为 `293_video_fallback_and_token_prepay.sql`。本次交付 Ubuntu `linux/amd64` 二进制和同平台 DockerHub 镜像；Apple Container 的 `linux/arm64` 环境须选择实际已发布的兼容镜像，本次标签不提供该架构。

独立 Video 平台新增持久任务和恢复器，支持按秒、输出 Token、按次计费，每分辨率使用一个单价，可配置参考图片附加费、缺省分辨率兜底价及 Token 固定秒价预扣。未设置独立视频倍率时沿用套餐对分组的倍率，独立倍率优先；已受理任务保留其价格快照。迁移只追加视频价卡、任务表和内部重置代次，不迁移旧 Grok/Seedance 任务，不改写已有余额、订阅有效期、额度窗口、自然重置次数及旧价卡。新增价卡配置默认为空或关闭，不会自动开启视频或 Token 预扣。

升级前验证 PostgreSQL 与配置备份，排空在途请求，停止全部旧后端；先启动一个 v2.8 实例完成迁移和检查，再启动其余 v2.8 实例并更新前端。不要混跑旧实例：旧程序的订阅/平台额度手动重置不会推进新代次，会破坏 Video 旧预留退款与新用量的隔离。沿用原数据库、Redis、数据目录、Compose 项目和稳定安全密钥，不执行 `down -v`。

全部实例升级后再配置并开放 Video 账号、分组、端点和价格；升级后核对迁移 289–293、版本及健康、既有余额和订阅数据、日/周/月重置与延期、任务归属和幂等、预扣退补差/失败释放及用量记录。Token 用量缺失或不可信的任务保持待对账，不能当作零价成功。新版同时修复 MiniMax 嵌套任务响应解析、视频任务身份校验/创建重定向重放，以及订阅手动重置与批量延期的锁等待；协议和资金边界见[独立视频任务](../docs/domains/video_tasks.md)。

OpenCode Zen、MiniMax 按量及 GLM/Zhipu 按量账号可显式配置通用上游用量查询，原生 Coding/GO 查询和周期监控规则保持原样；手动通用查询结果不会参与周期停用或调度。已有账号配置继续保留，详见[上游用量](../docs/interfaces/upstream_usage.md)。

回退旧应用前，先停止受理新 Video 任务，保留 v2.8 恢复器直到已有任务完成结算或确认释放，并核对不存在未处置的 Video 冻结额度。受理不明和待对账任务须按真实结果核对，不能删除任务行、迁移记录或直接改余额代替处理；旧程序没有新恢复器，回退不会自动退款。数据库迁移不会随镜像回退撤销，精确恢复升级前状态仍须按已验证的备份流程处理，且应先核对升级后的账务。详细迁移说明见[部署与数据库迁移](../docs/operations/deployment_and_migrations.md)。

### v2.7 历史升级检查

从 `v0.1.278-ct-v2.6` 更新到 `v0.1.278-ct-v2.7` 不新增或修改 SQL 迁移，最高迁移仍为 `288_affiliate_ledger_operation_id.sql`。本次新增用户与管理员任务记录的图片/视频预览，用户筛选改为用户名或邮箱搜索选择，模型改为可搜索的精确选择，并补齐仪表盘“实际消费”中英文文案。

预览只读取已有结果，不重新生成、不查询上游任务或执行扣费；原有余额、订阅、路由和生成规则保持不变。图片沿用短期结果与对象存储链接，视频结果使用 Redis 短期缓存和十分钟播放票据。历史未缓存、未取得完成结果或已过期的产物不能补预览；后续合法完成查询可产生新的可用快照。播放文件需要服务器访问公网媒体站点，保留现有 Redis 和固定安全密钥；外部反向代理应对 `/api/v1/media-tasks/preview-content/` 隐藏票据路径，详见[任务结果预览](../docs/domains/media_tasks.md#media_task_preview)。

升级后核对版本、健康、用户/管理员任务筛选与归属隔离、已有图片及新视频结果预览。交付为 `linux/amd64`，使用原 Compose 项目及数据卷，只重建应用容器；二进制部署沿用原配置与数据库。

### v2.6 历史升级检查

从 `v0.1.278-ct-v2.5` 更新到 `v0.1.278-ct-v2.6` 不新增或修改 SQL 迁移，最高迁移仍为 `288_affiliate_ledger_operation_id.sql`。本次交付 `linux/amd64` 二进制和同平台 DockerHub 镜像，包含已同步的上游 v0.2.10 / v0.2.11 适配、用户与管理员任务记录的手机卡片及折叠筛选，并将版本检查、在线更新和安装脚本统一到 `codermyxiaoc/TokenRouter` 的 GitHub Releases。

上游适配覆盖 Sonnet 5.5、GPT-6.1 Sol、Astra Ultrafast、Claude 原生重置查询与兑换、风控白名单、仪表盘指标、Key 每小时创建限制及兼容协议修正。现有订阅与余额结算、订阅窗口、次数和有效期规则保留；升级不重算历史，不清空用户用量，也未引入上游的余额在途预占。Claude 兑换会消耗上游重置机会，应先查询再按需确认，未知结果不重复兑换；原有本地账号限制仍通过既有恢复入口处理。

沿用原数据库、配置、数据目录和固定安全密钥，完成全部后端与前端更新后，核对新模型目录与定价、常用网关请求和用量记录、任务手机布局及 GitHub 更新来源。二进制部署同步本版本官方离线定价资源并保留自定义覆盖；Docker 镜像已携带同版本资源。更新缓存按来源隔离，无需清空 Redis。Apple Container 需要已发布的 `linux/arm64` 镜像，本次 amd64 标签不提供该架构。跨版本升级继续遵守下方历史迁移边界，详细约束见[部署与数据库迁移](../docs/operations/deployment_and_migrations.md)。

### v2.5 历史升级检查

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

当前默认固定版本标签 `v0.1.278-ct-v3.29`。后续发布应使用新版本标签，避免同一标签对应不同构建；需要严格固定内容时使用镜像摘要。升级前应验证数据库备份。

## 相关链接

- [GitHub 仓库](https://github.com/codermyxiaoc/TokenRouter)
- [部署指南](../docs/guides/deployment/index.md)
