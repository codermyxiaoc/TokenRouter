# TokenRouter Agent 协作规范

## Project Doc 门禁

本仓库在 `.agents/skills/project-doc/` 内置 `project-doc` 技能。磁盘上存在技能目录不代表当前 Agent 会话已经加载该技能。

- 执行任何仓库任务前，先确认当前 Agent 会话能够调用名为 `project-doc` 的技能。
- 如果不能调用，立即停止；不得读取业务文件、运行仓库命令、执行分析或修改文件。
- 技能不可用时，只回复：“当前环境未安装或未加载 `project-doc` 技能，按仓库规则无法继续。请安装或启用该技能，并在新会话中重试。”
- 如果能够调用，先使用 `project-doc`，再读取 `docs/index.md` 并按目录路由完成任务。

## 通用规范

- 代码必须包含注释，注释统一使用中文。
- Commit message 必须遵循 Conventional Commits 规范。
- 不得提交 `SYNC.md`。
- 除非用户明确要求，否则不得创建或切换 Git 分支；所有任务直接在当前 `main` 分支上完成。

## 计划模式

- 使用 Codex 计划模式时，开始实施前必须先将计划原样保存到 `.agents/plans/`，不允许对撰写好的计划进行修改或简化，确保执行期间可以随时回看；执行期间将任务进度同步到计划文件末尾中。这些计划不需要提交到git仓库

## 前端规范

- 需要选择框时，必须使用项目自研的选择框组件，不得使用原生 `<select>`。

## 上游同步

- 如果上游在 `backend/migrations/` 下新增迁移，不得原样照搬文件名；必须根据当前 fork 的最新迁移 ID 递增后，替换文件名前缀 ID。
- 如果上游在 `README.md` 中新增文档，必须将内容并入 `docs/` 下合适的文档；没有合适文档时新建一篇，不要直接写入 `README.md`，保持其简洁。

## Cursor Cloud specific instructions

Cloud Agent 启动后会拉起本机开发栈。不要改用 `deploy/docker-compose.dev.yml`，那个文件默认把出站流量转到不存在的本机代理。

- Go `1.27.2` 在 `/usr/local/go/bin`，并链接到 `/usr/local/bin/go`。
- Node.js `24.22.1` 和 pnpm `9.15.9` 在 `/usr/local/node/bin`。登录壳里更靠前的 `/exec-daemon/node` 仍是 Node 22，运行前端前把 `/usr/local/node/bin` 放到 `PATH` 前面。
- PostgreSQL 18 监听 `127.0.0.1:5432`。本地角色和数据库都叫 `tokenrouter`，只允许本机 trust 连接。
- Redis 使用 Ubuntu 的 7.0 包，监听 `127.0.0.1:6379`，没有密码。
- 后端是 `http://127.0.0.1:8080`，数据目录是 `/var/lib/tokenrouter-dev`。首次启动会自动初始化；生成的管理员邮箱和一次性密码写在该目录的 `backend.log`。
- 前端 Vite 是 `http://127.0.0.1:3000`，画布开发服务是 `http://127.0.0.1:5174`，并由前端的 `/canvas-app` 反代。
- 两个服务分别在 tmux 会话 `tokenrouter-backend` 和 `tokenrouter-frontend`。端口已经可访问时，启动脚本不会重启它们。
