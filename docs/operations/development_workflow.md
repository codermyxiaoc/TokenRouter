# 开发、验证与上游同步

本文记录 TokenRouter 当前工具链、代码生成、测试分层、仓库约束、发布和 fork 同步流程。它替代旧开发指南中的个人机器路径、固定本地凭据和临时故障处置；具体版本以当前 manifest、Makefile 和 Dockerfile 为准。当前检出没有 `.github/workflows/`，不能把未执行的 CI 或自动发布视为验证结果。

## 章节导航

- [工具链与本地运行](#工具链与本地运行)：准备环境或更新依赖时读取。
- [代码边界](#代码边界)：新增后端/前端模块时读取。
- [生成代码与迁移](#生成代码与迁移)：修改 Ent schema、Wire 或数据库时读取。
- [验证策略](#验证策略)：实现和提交前读取。
- [提交与文档](#提交与文档)：形成提交或维护 Project Doc 时读取。
- [同步上游](#同步上游)：引入 upstream PR/commit 时读取。
- [发布](#发布)：创建版本 tag 前读取。

## 工具链与本地运行

| 工具 | 当前来源 | 当前约束 |
| --- | --- | --- |
| Go | `backend/go.mod`、根与部署 Dockerfile | `1.27.0` |
| Node.js | 根与部署 Dockerfile | 镜像构建使用 `node:24-alpine`；本地使用满足前端依赖要求的工具链 |
| pnpm | 根 Makefile 与 Dockerfile | `9`；根命令默认使用 `npx --yes pnpm@9` |
| golangci-lint | `backend/Makefile`、`backend/.golangci.yml` | 后端检查执行 `golangci-lint run ./...`；安装版本由本地工具链提供 |
| PostgreSQL、Redis | Compose 与集成测试 | 生产必需；测试可由 Testcontainers/Compose 提供 |

记录实际使用的 Go、Node、pnpm 和 lint 版本，以便复现构建与验证。升级 Go 时同步 `backend/go.mod`、根与部署 Dockerfile 中的工具链镜像，并核对当前存在的构建入口；不要假设历史 workflow 的版本断言仍在执行。

不要把个人数据库路径、固定密码或某台机器的服务配置写入工程文档。开发配置使用未提交的环境文件或 `backend/config.yaml`；可提交样例在 `deploy/`。前端开发服务器默认通过 `VITE_DEV_PROXY_TARGET` 代理后端，端口由 `VITE_DEV_PORT` 控制。

常用入口：

```bash
# 后端
cd backend
go run ./cmd/server

# 前端
cd frontend
pnpm install --frozen-lockfile
pnpm run dev

# 完整源码 Compose
docker compose -f deploy/docker-compose.dev.yml up --build
```

根 Makefile 中保留 `build-datamanagementd`/`test-datamanagementd` 目标，但当前仓库没有 `datamanagement/` 源树；不要把这些目标纳入默认通过条件，除非该可选源码已随工作范围提供。

## 代码边界

后端依赖方向是 `handler/server -> service -> repository`，repository 实现 service 定义的接口；`.golangci.yml` 阻止普通 handler/service 直接依赖 repository、Redis 或 GORM，只有列出的运维装配例外。新增外部协议时把 wire format 放在 handler/pkg 适配层，领域状态与幂等规则留在 service，存储细节留在 repository。

所有手写代码都要写必要注释，注释使用中文；生成文件不手改。注释应解释约束、失败语义或非显然原因，不复述语句。跨模块不变量应同步到 Project Doc，并在关键手写入口添加唯一 `@project-doc` 锚点。

前端使用 Vue 3、TypeScript、Pinia、Vue Router、Vue I18n 和项目组件。修改界面时：

- 选择项使用 `frontend/src/components/common/Select.vue` 等自研选择框，不使用原生 `<select>`。
- 用户可见文案进入 `src/i18n/locales/`，中英文 key 保持同构。
- API 类型和调用放在 `src/api/`，跨页面状态进入 store/composable，避免在 view 复制协议。
- 修改依赖必须同步 `frontend/pnpm-lock.yaml`，发布安装使用 `--frozen-lockfile`。

## 生成代码与迁移

`backend/ent/` 大部分文件由 Ent 生成，`backend/cmd/server/wire_gen.go` 由 Wire 生成。统一使用：

```bash
make -C backend generate
```

该目标依次执行 `go generate ./ent` 和 `go generate ./cmd/server`。修改 `backend/ent/schema/`、生成 feature 或 Wire provider 后，提交对应生成差异，并检查差异只包含预期 schema/依赖变化。Go 1.27 的 jsonv2 生成代码可能把 Ent 的 JSON 字段表示为 `encoding/json/jsontext.Value`，这是预期的生成结果。

Ent schema 不是生产迁移器。数据库权威变更仍须新增 `backend/migrations/*.sql`，不能依赖 Ent auto-migrate，也不能修改既有迁移。编号、`_notx.sql`、checksum 和 fork 上游重编号规则见 [部署与数据库迁移](deployment_and_migrations.md)。

## 验证策略

验证范围随风险扩大，先运行受影响包/组件，再运行仓库门禁。后端常用命令：

```bash
# 受影响包
(cd backend && go test ./internal/service ./internal/handler)

# 后端 Makefile 提供的测试分层
make -C backend test-unit
make -C backend test-integration

# 普通测试加 lint
make -C backend test
```

集成测试可能启动 PostgreSQL/Redis 容器；环境没有 Docker 时要明确报告未运行，不能用单元测试结果代替。涉及迁移时还要运行 migration runner 和对应 schema/data regression tests。

前端门禁：

```bash
# 根 Makefile 执行 lint、类型检查和关键 Vitest 集
make test-frontend

# 变更涉及其它组件时运行其测试或完整套件
npx --yes pnpm@9 --dir frontend run test:run
npx --yes pnpm@9 --dir frontend run build
```

部署文件变更要运行 `deploy/tests/` 中对应的 shell/Compose 检查，并对所交付的 Compose 变体执行 `docker compose config --quiet`；校验时使用独立的测试变量，避免输出真实配置。依赖或安全边界变更还应运行 `make secret-scan`、`govulncheck` 或相应审计。最终至少执行 `git diff --check`，并确认没有意外生成物、环境文件或秘密。

## 提交与文档

提交信息遵循 Conventional Commits，例如 `feat(gateway): ...`、`fix(billing): ...`、`docs(project): ...`。一次提交应围绕一个可验证目的，生成文件、迁移和契约测试与其源变更一起提交。

`SYNC.md` 是本地同步进度，受 `.gitignore` 保护，永远不要提交。使用 Codex 计划模式时，实施前把计划保存到 `.agents/plans/`。不要覆盖工作区中来源不明的修改；提交前按文件核对 staging 范围。

本地浏览器日志、Python 缓存和 `frontend/audit.json` 原始审计输出不进入版本管理；`.dev/`、`release/`、`diagnostics/` 等本地产物也不进入源码 Docker 构建上下文。清理时区分可再生成的临时产物与备份、运行配置、数据、正式发布包及回归测试，不依据文件未被静态引用就删除功能代码。预编译镜像仍按发布流程把程序复制到独立构建上下文的根目录。

每次代码变更都从 [工程文档目录](../index.md) 路由到相关专题：如果持久架构、领域不变量、外部契约或运维流程变化，同步正文、分类目录和代码锚点；局部实现细节不应无条件扩写成新文档。README 保持项目入口简洁，工程细节放入 `docs/`。

## 同步上游

同步以 upstream PR/commit 为最小可审查单元，逐项理解变更并保留 fork 的产品、计费、安全和部署语义。冲突解决后运行该项涉及的测试，再形成符合 Conventional Commits 的本地提交；`SYNC.md` 只记录本地进度，不进入提交。

两个 fork 专属规则不可省略：

1. 上游新增 `backend/migrations/` 文件时，按上游顺序把前缀重编号为本 fork 当前最大迁移 ID 依次加一，并修复所有精确文件名引用；不能原名照搬。
2. 上游在 `README.md` 新增的工程文档不直接并入 README；把内容归入 `docs/` 的合适 Project Doc 或相关用户手册，没有合适位置时再创建规范命名的新文档。

同步后检查 `git diff upstream/...` 不足以证明行为正确，还要核对本 fork 的迁移顺序、默认配置、i18n、生成文件、部署样例、文档链接和安全边界。已经存在的 fork 修改不能为减少冲突而静默回退。

## 发布

当前检出没有 `.github/workflows/`。创建 tag 不会在本检出自动触发发布，也没有自动创建 Release、构建其他平台或回写 `backend/cmd/server/VERSION` 的流程。发布前按本次范围同步程序版本、Compose 默认镜像和部署说明；程序内部版本号不带 `v`，镜像标签保留 `v` 前缀。

Ubuntu amd64 手工发布可先构建一次前端，再以 `CGO_ENABLED=0`、`GOOS=linux`、`GOARCH=amd64`、`GOAMD64=v1` 和 `-tags embed` 编译后端，把同一个程序交付到二进制归档与 `Dockerfile.goreleaser` 镜像。镜像的最小构建上下文必须携带同次源码的运行时定价资源和入口脚本，镜像内外程序哈希应一致。根 `Dockerfile` 仍可完整构建源码镜像，具体命令与发布核对见 [Docker 镜像说明](../../deploy/DOCKER.md)。

仓库保留 `.goreleaser.yaml`、`.goreleaser.simple.yaml` 和 `tools/goreleaser_prebuilt.sh`。标准配置支持多个二进制平台及 GHCR/DockerHub 镜像标签，不能把它等同于只发布单个 amd64 标签的流程；其前置 hook 会执行 `go mod tidy`，归档文件列表也不等同于手工更新包，使用前需核对本次范围、工作区变化和资源完整性。预编译适配器按 `GORELEASER_PREBUILT_DIR/sub2api_<GOOS>_<GOARCH>` 导入程序，只复制文件，不改写其版本信息。

发布记录应包含源码提交与未提交状态、工具链、程序版本与哈希、目标平台、镜像远端摘要，以及实际执行的验证。二进制包需检查可执行位、嵌入前端/迁移和定价资源；镜像需检查版本、入口权限及 PostgreSQL 客户端，并在隔离环境验证健康、前端和升级。只记录已经完成的检查，不把 tag、构建完成或旧版测试结果写成本次发布成功。

从 v1.2 更新到 v1.3 的迁移 277、WS `request_kind` 作用域、Antigravity 缓存键和调度快照切换要求见 [v1.3 升级与缓存切换](deployment_and_migrations.md#v1_3_upgrade)。部署前核实备份可恢复性及所有待应用迁移的兼容边界，按要求完成全部实例升级；推送镜像或交付更新包与服务器部署是独立步骤。

相关文档：[项目总览](../project_overview.md)、[系统架构](../architecture/system_architecture.md)、[配置边界](../interfaces/configuration.md)、[部署与数据库迁移](deployment_and_migrations.md)、[运维目录](index.md)。
