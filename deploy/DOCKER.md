# TokenRouter Docker 镜像

当前标准、本地目录和 standalone Compose 默认从 DockerHub 拉取：

```text
coderxiaoc/tokenrouter:v0.1.278-ct-v1.1
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
docker buildx build --platform linux/amd64 --build-arg VERSION=0.1.278-ct-v1.1 --tag coderxiaoc/tokenrouter:v0.1.278-ct-v1.1 --load --file Dockerfile .
docker run --rm --entrypoint /app/sub2api coderxiaoc/tokenrouter:v0.1.278-ct-v1.1 --version
docker run --rm --entrypoint /usr/local/bin/pg_dump coderxiaoc/tokenrouter:v0.1.278-ct-v1.1 --version
docker push coderxiaoc/tokenrouter:v0.1.278-ct-v1.1
```

版本和 PostgreSQL 客户端检查不挂载现有数据，也不启动应用服务。程序内部版本号不带 `v`，镜像标签保留 `v` 前缀。根 `Dockerfile` 还支持 `COMMIT`、`DATE` 构建参数；发布下一版本时同时替换构建参数、镜像标签与部署端 `SUB2API_IMAGE`。以上命令只发布 `amd64`，不会同时生成 `arm64` 镜像。

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
SUB2API_IMAGE=coderxiaoc/tokenrouter:v0.1.278-ct-v1.1
POSTGRES_BIND_HOST=127.0.0.1
POSTGRES_PORT=5433
```

以下命令以既有配置名为 `docker-compose.yml` 为例；如果此前使用 `docker-compose.local.yml`，每条命令都继续使用该文件和原 Compose 项目名：

```bash
docker compose -f docker-compose.yml config --quiet
docker compose -f docker-compose.yml pull sub2api
docker compose -f docker-compose.yml up -d
docker compose -f docker-compose.yml ps
docker compose -f docker-compose.yml logs --tail=100 sub2api
```

私有 DockerHub 仓库需要先在服务器执行 `docker login`。应用镜像更新或端口映射变更应使用 `up -d` 重建相关容器；仅 `restart` 不会应用这些变更。已有部署无需用 `.env.example` 覆盖 `.env`，也不要执行 `down -v`。

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

当前默认固定版本标签 `v0.1.278-ct-v1.1`。后续发布应使用新版本标签，避免同一标签对应不同构建；需要严格固定内容时使用镜像摘要。升级前应验证数据库备份。

## 相关链接

- [GitHub 仓库](https://github.com/TokenFlux/TokenRouter)
- [部署指南](../docs/guides/deployment/index.md)
