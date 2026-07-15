<!-- redoc:start:header -->
<p align="center">
  <img src="assets/branding/engram-icon-256.svg" alt="Engram" width="128" height="128">
</p>

[English](README.md) | [Русский](README.ru.md) | **中文**

[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go)](https://go.dev/)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-17-4169E1?logo=postgresql)](https://www.postgresql.org/)
[![Docker](https://img.shields.io/badge/Docker-ready-2496ED?logo=docker)](https://www.docker.com/)
[![CI](https://github.com/thebtf/engram/actions/workflows/docker-publish.yml/badge.svg)](https://github.com/thebtf/engram/actions/workflows/docker-publish.yml)
[![License](https://img.shields.io/github/license/thebtf/engram)](LICENSE)
<!-- redoc:end:header -->

<!-- redoc:start:intro -->
# Engram

**AI 编程代理的持久化共享记忆基础设施。**

AI 编程代理在会话之间会遗忘一切。每次新对话都从零开始——过往的决策、bug 修复、架构选择和已学习的模式全部丢失。你不得不反复解释上下文，而代理则重复犯同样的错误。

Engram 通过保留那些在生产中真正可靠的记忆原语来解决这个问题：显式 issues、documents、memories、behavioral rules、credentials 和 API tokens。一台服务器，多个工作站，零上下文丢失。

在 v5.0.0 中，session-start inject 被简化为静态 composite payload：打开的 issues、always-inject behavioral rules，以及 recent memories。旧的动态 relevance / graph / reranking / extraction 栈已经离开主产品路径。

此后，v6 在这个稳定核心之上重建了 governance：per-workstation keycards、proposal-only rule arbiter、bounded session-start rule router，以及 rule-governance telemetry / rollback controls。热路径依旧保持确定性——session-start 不调用 LLM——但 durable guidance 现在可以被审计和回滚。
<!-- redoc:end:intro -->

---

<!-- redoc:start:whats-new -->
## 最新版本

| 版本 | 亮点 |
|------|------|
| **v6.38.0** | **V7 Meta-memory Discovery (ENG-V7-S2)** —— content-free `know_about` MCP 工具、S2 `CandidateProposer`，以及由 v7 flags 控制的 session-start `meta_summary`。 |
| **v6.37.0** | **V7 State Subsystem (ENG-V7-S1)** —— v7 `StateWriter` adapter 和 bounded native state resume 加固。 |
| **v6.32.0** | **Usefulness / Noise Review Loop (CR-008, MPL-3)** —— packet-centric bounded review queue，具备明确的 empty/gated/error/sparse 状态、独立的 preview/apply、atomic snapshot+audit-backed suppress/preserve，以及诚实的指标。 |
| **v6.31.0** | **Native State Plane + Principal Explorer (CR-006 + CR-007, MPL-1/2)** —— Engram 原生的 session/goal/task/project state plane，带确定性 resume packet；principal/domain/project memory explorer + principal-scoped briefs；CR-005 契约加固。 |
| **v6.30.0** | **Agent Knowledge & Experience Layer foundations (ENG-MPL-1)** —— native state plane、principal briefs、packet-centric review loop、带 applicability gates 的 first-class experience retrieval、forgetting taxonomy 以及 selective temporal truth 契约。 |
| **v6.29.0** | **Rule Governance Telemetry (RG-3)** —— lifecycle health、exception queues、transition controls、rollback-aware snapshots，以及 usefulness telemetry。 |
| **v5.0.0** | Cleaned Baseline — static-only storage、split observations、session-start gRPC + cache fallback |
| **v4.4.0** | Loom tenant — background task execution 与 daemon-side project event bridge |
| **v4.0.0** | Daemon architecture — muxcore engine、gRPC transport、local persistent daemon、auto-binary plugin |

完整更新日志请查看 [Releases](https://github.com/thebtf/engram/releases)。

### Two-Tier Token Model (v6)

Engram v6 将两类凭据严格绑定到不同主机类型：

| Tier | Name | Lives in | Purpose | Issuance |
|---|---|---|---|---|
| **1 — Operator key** | `ENGRAM_AUTH_ADMIN_TOKEN` | 仅 server-host environment（Docker、compose） | 用于 migrations、server-internal RPC 与 dashboard bootstrap 的管理员权限 | 由运维在服务器端设置 |
| **2 — Worker keycard** | `ENGRAM_TOKEN` | Workstation `~/.claude/settings.json` env | Daemon ↔ server gRPC 与常规 MCP tool calls | 通过 admin 登录后的 `/tokens` 页面签发 |

Operator key 绝不能出现在工作站上。Worker keycard 绝不能存放在服务器主机环境中。
<!-- redoc:end:whats-new -->

---

<!-- redoc:start:architecture -->
## 架构

单服务器运行在端口 `37777`，提供 HTTP REST API、gRPC 服务（通过 cmux）、Vue 3 仪表盘，以及静态存储/查询 surface。每个工作站运行本地 daemon，并通过 gRPC 连接到服务器。多个 Claude Code 会话共享一个 daemon。

```mermaid
graph TB
    subgraph "Workstation A"
        CC_A[Claude Code]
        H_A[Hooks + MCP Plugin]
        CC_A --> H_A
    end

    subgraph "Workstation B"
        CC_B[Claude Code]
        H_B[Hooks + MCP Plugin]
        CC_B --> H_B
    end

    H_A -- "stdio / gRPC" --> Server
    H_B -- "stdio / gRPC" --> Server

    subgraph "Engram Server :37777"
        Server[Worker]
        Server --> |HTTP API| API[REST Endpoints]
        Server --> |gRPC| GRPC[Static session-start + tool bridge]
        Server --> |Web| Dash["Vue 3 Dashboard"]
    end

    Server --> PG[(PostgreSQL 17)]
```

**服务器**（Docker 部署于远程主机 / Unraid / NAS）：
- PostgreSQL 17
- Worker — HTTP API、gRPC、Vue 3 仪表盘、static entity stores

**客户端**（每个工作站）：
- Hooks — session-start、session-end 以及相关的 Claude Code lifecycle integrations
- MCP 插件 — 将 Claude Code 连接到本地 daemon / server bridge
- 斜杠命令 — `/setup`、`/doctor`、`/restart` 和 memory-related workflows
<!-- redoc:end:architecture -->

---

<!-- redoc:start:features -->
## 功能

### 搜索与检索
- **Static session-start payload** — issues + behavioral rules + memories，通过 gRPC `GetSessionStartContext`
- **Project-scoped memory recall** — 面向 static memories 的简单 SQL-backed retrieval
- **Document search** — versioned documents 和 collection-backed search 仍然可用

### 存储与组织
- **Memories** — `memories` 表中的显式 project-scoped notes
- **Behavioral rules** — `behavioral_rules` 表中的 always-inject guidance
- **版本化文档** — 支持历史和评论的文档集合
- **加密保险库** — AES-256-GCM 凭据存储，支持作用域访问控制
- **Cross-project issues** — 代理与项目之间显式的 operational coordination

### 弹性与运维
- **Session-start cache fallback** — 当服务器暂时不可用时，使用 `${ENGRAM_DATA_DIR}/cache/session-start-{project-slug}.json`
- **Version negotiation** — 在 session-start path 上显式进行 major-version compatibility 检查
- **配置热重载** — 无需重启即可更改设置
- **Graceful daemon restart** — 保留 binary swap 与 control socket 流程

### 仪表盘与用户体验
- **Vue 3 仪表盘** — 聚焦于 surviving static entity surface
- **Lifecycle hooks** — session-start / session-end 及相关 integrations 仍然保留
- **Multi-workstation support** — 一台服务器、多个本地 daemon、共享 static memory surface
<!-- redoc:end:features -->

---

<!-- redoc:start:use-cases -->
## 使用场景

- **上下文连续性** — 开启新会话时自动回忆相关决策、模式和历史工作
- **架构记忆** — 做新决策前查询过往的设计决策
- **编辑前感知** — 修改文件前检查已知的相关信息
- **模式检测** — 跨会话和工作站发现重复出现的模式
- **团队知识共享** — 多个工作站共享同一个记忆服务器
- **凭据管理** — 无需 .env 文件即可存储和检索 API 密钥和密钥
- **会话回顾** — 分析历史会话，获取生产力洞察
<!-- redoc:end:use-cases -->

---

<!-- redoc:start:quick-start -->
## 快速开始

```bash
git clone https://github.com/thebtf/engram.git
cd engram

# 配置
cp .env.example .env   # 编辑配置

# 启动
docker compose up -d
```

这将启动 PostgreSQL 17 + pgvector 和 Engram 服务器，地址为 `http://your-server:37777`。

验证：

```bash
curl http://your-server:37777/health
```

然后在 Claude Code 中安装插件：

```
/plugin marketplace add thebtf/engram-marketplace
/plugin install engram
```

设置环境变量（Claude Code 在运行时读取）：

```bash
# Linux/macOS: 添加到 shell 配置文件
# Windows: 设置为系统环境变量
ENGRAM_URL=http://your-server:37777
ENGRAM_TOKEN=engram_your_workstation_keycard
```

请先在 `http://your-server:37777/tokens` 生成 worker keycard，然后重启 Claude Code。记忆功能现已激活。
<!-- redoc:end:quick-start -->

---

<!-- redoc:start:installation -->
## 安装

### 插件安装（推荐）

插件会自动注册 MCP 服务器、hooks 和斜杠命令。

```bash
# 先设置环境变量
ENGRAM_URL=http://your-server:37777
ENGRAM_TOKEN=engram_your_workstation_keycard
```

```
/plugin marketplace add thebtf/engram-marketplace
/plugin install engram
```

重启 Claude Code，一切就绪。

### Docker Compose

```bash
git clone https://github.com/thebtf/engram.git && cd engram
cp .env.example .env   # 编辑 DATABASE_DSN、token、嵌入配置
docker compose up -d
```

**已有 PostgreSQL？** 只运行服务器容器：

```bash
DATABASE_DSN="postgres://user:pass@your-pg:5432/engram?sslmode=disable" \
  docker compose up -d server
```

### Binary Installation (v4+)

从 [GitHub Releases](https://github.com/thebtf/engram/releases) 下载 daemon binary：

```bash
# Linux (amd64)
curl -L https://github.com/thebtf/engram/releases/latest/download/engram-linux-amd64 -o engram
chmod +x engram && sudo mv engram /usr/local/bin/

# macOS (Apple Silicon)
curl -L https://github.com/thebtf/engram/releases/latest/download/engram-darwin-arm64 -o engram
chmod +x engram && sudo mv engram /usr/local/bin/

# Windows (amd64) — 下载 engram-windows-amd64.exe 并加入 PATH
```

然后设置：

```bash
export ENGRAM_URL=http://your-server:37777
export ENGRAM_TOKEN=engram_your_workstation_keycard
```

验证：`echo '{"jsonrpc":"2.0","id":1,"method":"ping"}' | engram`

### 手动 MCP 配置

如果不使用插件，可以在 `~/.claude/settings.json` 中直接配置 MCP：

#### Streamable HTTP（推荐）

```json
{
  "mcpServers": {
    "engram": {
      "type": "url",
      "url": "http://your-server:37777/mcp",
      "headers": {
        "Authorization": "Bearer ${ENGRAM_TOKEN}"
      }
    }
  }
}
```

Claude Code 在运行时会从环境变量中展开 `${VAR}`。

**CLI 快捷方式：**

```bash
claude mcp add-json engram '{"type":"stdio","command":"engram","env":{"ENGRAM_URL":"http://your-server:37777","ENGRAM_TOKEN":"${ENGRAM_TOKEN}"}}' -s user
```

`ENGRAM_URL` 可以配置为服务器 origin（`http://host:37777`）或 MCP path（`http://host:37777/mcp`）；hooks 会为 REST 调用自动归一化到服务器 origin。`ENGRAM_TOKEN` 必须始终是 workstation keycard，绝不能使用 operator key。

### 从源码构建

需要 Go 1.25+ 和 Node.js（用于仪表盘）。

```bash
git clone https://github.com/thebtf/engram.git && cd engram
make build    # 构建仪表盘 + daemon + release assets
make install  # 安装插件 + 启动 daemon
```
<!-- redoc:end:installation -->

---

<!-- redoc:start:upgrading -->
## 升级到 v6.x

v6 的关键升级契约是把 workstation token flow 与 static core 之上的 rule-governance milestones 统一起来。

变化要点：
- workstation auth 已从共享 admin token 改为 per-workstation keycards（`ENGRAM_TOKEN`）
- session-start 仍保持确定性，但规则投递现在经过 candidate -> arbiter -> router -> telemetry milestones
- rule-governance snapshots、rollback conflict handling 和 usefulness telemetry 已成为 backend surface 的一部分
- client 与 server 继续在 session-start path 上显式检查 major-version compatibility

升级步骤：
1. 将 plugin 和 daemon 升级到目标 `v6.x` 版本
2. 打开 `<server-url>/tokens`，签发 workstation keycard，并配置 `ENGRAM_TOKEN`
3. 重启 Claude Code 和 daemon
4. 验证 plugin update detection、session-start cache fallback 和当前服务器版本

**Docker 镜像：** 使用 `ghcr.io/thebtf/engram:latest`。数据库迁移会在启动时自动执行。
<!-- redoc:end:upgrading -->

---

<!-- redoc:start:configuration -->
## 配置

### 服务器

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `DATABASE_DSN` | — | PostgreSQL 连接字符串 **（必填）** |
| `DATABASE_MAX_CONNS` | `10` | 最大数据库连接数 |
| `ENGRAM_WORKER_PORT` | `37777` | 服务器端口 |
| `ENGRAM_AUTH_ADMIN_TOKEN` | — | Operator/admin token，仅限 server host |
| `ENGRAM_VAULT_KEY` | — | 用于 credentials 加密的标准 vault key |
| `ENGRAM_ENCRYPTION_KEY` | — | 旧版 fallback vault key 环境变量 |
| `ENGRAM_DATA_DIR` | 自动 | daemon 数据目录（也用于 session-start cache） |

### 客户端（hooks）

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `ENGRAM_URL` | — | plugin / hooks 使用的完整 server 或 MCP URL |
| `ENGRAM_TOKEN` | — | plugin、daemon 与 hooks 使用的 workstation keycard |
| `ENGRAM_SERVER_URL` | — | 某些 launcher 中 `ENGRAM_URL` 的可选别名 |
| `ENGRAM_DATA_DIR` | 自动 | cache 与 daemon state 目录 |
| `ENGRAM_WORKSTATION_ID` | 自动 | 覆盖工作站 ID（8 位十六进制） |
<!-- redoc:end:configuration -->

---

<!-- redoc:start:mcp-tools -->
## MCP 工具

Engram 提供 static-first MCP surface，围绕 surviving entity model 展开，并随 v6 系列 Memory Product Layer milestone 的落地而扩展。

Static core（v5 基线）：
- issues / issue comments
- memories / behavioral rules
- documents
- credentials / vault
- loom background tasks

Memory Product Layer 新增（v6.30–v6.38）：
- native state plane —— session/goal/task/project resume packet（CR-006）
- principal explorer + briefs —— 按 principal/domain 检视 memory + bounded briefs（CR-007）
- review loop —— 基于 candidate/snapshot/audit seams 的 packet-centric candidate/suppress/preserve governance（CR-008）
- v7 state subsystem —— feature-flagged `StateWriter` adapter 和更严格的 resume-packet validation（ENG-V7-S1）
- v7 meta-memory discovery —— feature-flagged `know_about`、S2 `CandidateProposer` 和 session-start `meta_summary`（ENG-V7-S2）

旧的 dynamic search / graph / learning-oriented tool surface 在 v5 demolition 阶段被移除；v6 Memory Product Layer 有意识地重建 durable agent knowledge，而非复活旧栈。

### `store` — 保存与组织

| 操作 | 说明 |
|------|------|
| `create` | 存储新的观察记录（默认） |
| `edit` | 修改观察记录字段 |
| `import` | 批量导入观察记录 |

### `feedback` — 抑制与结果

| 操作 | 说明 |
|------|------|
| `suppress` | 抑制低质量记忆 |
| `outcome` | 记录会话结果 |

### `vault` — 加密凭据

| 操作 | 说明 |
|------|------|
| `store` | 存储加密凭据 |
| `get` | 检索凭据 |
| `list` | 列出已存储的凭据 |
| `delete` | 删除凭据 |
| `status` | 保险库状态和健康检查 |

### `docs` — 版本化文档与集合

| 操作 | 说明 |
|------|------|
| `create` | 创建版本化文档 |
| `read` | 读取文档内容 |
| `list` | 列出版本化文档 |
| `history` | 读取版本历史 |
| `comment` | 添加文档评论 |
| `collections` | 列出已配置的集合 |
| `documents` | 列出集合中的文档 |
| `get_doc` | 读取集合文档 |
| `remove` | 软删除集合文档 |
| `ingest` | 新增或更新集合文档的元数据与内容 |

### `admin` — 管理遥测

`stats` 返回记忆系统遥测。启用 vNext gate 后还可使用 `purge_project`；该操作需要管理员授权，并以项目名称进行确认。

### `check_system_health` — 系统健康检查

报告所有子系统的状态：数据库、嵌入、重排序器、LLM、保险库、图谱、整合。

### V7 条件工具

当 `ENGRAM_V7_PLUG_ENABLED=true` 且对应 slice flag 已启用时：

| 工具 / surface | Flag | 说明 |
|----------------|------|------|
| `get_state` / `set_state` v7 adapter | `ENGRAM_V7_S1_STATE=true` | 通过 v7 S1 subsystem 处理 native state writes，同时保留稳定的 state-plane tools。 |
| `know_about` | `ENGRAM_V7_S2_METAMEM=true` | 按主题返回 content-free discovery packet：`topic`、`project`、`count`、`total_candidates`、`top_tags`、`date_range` 和 `memories`。无匹配时返回空 packet，而不是 memory body text 或工具错误。 |
| session-start `meta_summary` | `ENGRAM_V7_S2_METAMEM=true` | 添加聚合的 project/count/tag/timestamp landscape 数据，帮助代理判断是否需要 detail fetch。 |
<!-- redoc:end:mcp-tools -->

---

<!-- redoc:start:usage -->
## 使用示例

```python
# 验证连接
check_system_health()

# 搜索记忆
recall(action="search", project="engram", query="authentication architecture")

# 存储观察记录
store(action="create", project="engram", content="Switched from Redis to in-memory cache for dev environments", title="Cache strategy change", tags=["architecture", "caching"])

# 抑制低质量记忆
feedback(action="suppress", id=123)

# 存储全局凭据
vault(action="store", name="OPENAI_KEY", value="sk-...", scope="global")

# 检索全局凭据
vault(action="get", name="OPENAI_KEY")
```
<!-- redoc:end:usage -->

---

<!-- redoc:start:troubleshooting -->
## 故障排除

| 现象 | 解决方法 |
|------|----------|
| `check_system_health` 显示嵌入不健康 | 检查 `ENGRAM_EMBEDDING_BASE_URL` 和 API 密钥。熔断器会在瞬时故障后自动恢复。 |
| 搜索无结果 | 确认观察记录是否存在：`recall(action="search", project="engram", query="decisions")`。检查嵌入服务是否健康。 |
| MCP 连接被拒绝 | 确认服务器正在运行：`curl http://your-server:37777/health`。检查环境中的 `ENGRAM_URL`。 |
| 保险库返回 "encryption not configured" | 设置 `ENGRAM_ENCRYPTION_KEY`（64 位十六进制字符串 = 32 字节 AES-256）。 |
| 仪表盘无法加载 | 确保使用 `make build` 构建（包含仪表盘）。检查浏览器控制台的错误信息。 |
| 安装后插件未被检测到 | 重启 Claude Code。确认已设置 `ENGRAM_URL` 和 `ENGRAM_TOKEN`，并且该 token 是 workstation keycard，而不是 operator key。 |
| 内存使用过高 | 减少 `DATABASE_MAX_CONNS`。如不需要可禁用整合功能。检查 `ENGRAM_EMBEDDING_DIMENSIONS`。 |

服务器日志可在 `http://your-server:37777/api/logs` 查看。
<!-- redoc:end:troubleshooting -->

---

<!-- redoc:start:development -->
## 开发

```bash
make build            # 构建仪表盘 + 所有 Go 二进制文件
make test             # 运行带竞态检测的测试
make test-coverage    # 覆盖率报告
make dev              # 在前台运行 worker
make install          # 构建 + 安装插件 + 启动 worker
make uninstall        # 移除插件
make clean            # 清理构建产物
```

### 项目结构

```
cmd/
  worker/             HTTP API + MCP + 仪表盘入口
  mcp/                独立 MCP 服务器
  mcp-stdio-proxy/    stdio -> SSE 桥接
  engram-cli/         CLI 客户端
internal/
  chunking/           AST 感知的文档分块
  collections/        YAML 集合配置
  config/             支持热重载的配置
  consolidation/      衰减、关联、遗忘
  crypto/             AES-256-GCM 保险库加密
  db/gorm/            PostgreSQL 存储 + 迁移
  embedding/          REST 嵌入提供者 + 弹性层
  graph/              内存 CSR + FalkorDB
  instincts/          本能解析器和导入
  learning/           自学习、LLM 客户端
  maintenance/        后台任务（摘要生成器、模式洞察）
  mcp/                MCP 协议，7 个主要工具处理器
  privacy/            密钥检测和脱敏
  reranking/          交叉编码器重排序器
  scoring/            重要性 + 相关性评分
  search/             混合检索 + RRF 融合
  sessions/           JSONL 解析器 + 索引器
  vector/pgvector/    pgvector 客户端
  worker/             HTTP 处理器、中间件、服务
    sdk/              观察记录提取、推理检测
pkg/
  models/             领域模型 + 关系类型
  strutil/            共享字符串工具
plugin/
  engram/             Claude Code 插件（hooks、命令）
ui/                   Vue 3 仪表盘 SPA
```

### CI 工作流

| 工作流 | 说明 |
|--------|------|
| `docker-publish.yml` | 构建并发布 Docker 镜像到 ghcr.io |
| `plugin-publish.yml` | 发布 OpenClaw 插件 |
| `static.yml` | 部署网站到 GitHub Pages |
| `sync-marketplace.yml` | 同步插件到 marketplace |
<!-- redoc:end:development -->

---

<!-- redoc:start:platform-support -->
## 平台支持

| 平台 | 服务器（Docker） | 客户端插件 | 从源码构建 |
|------|:-:|:-:|:-:|
| macOS Intel | Yes | Yes | Yes |
| macOS Apple Silicon | Yes | Yes | Yes |
| Linux amd64 | Yes | Yes | Yes |
| Linux arm64 | Yes | Yes | Yes |
| Windows amd64 | WSL2 / Docker Desktop | Yes | Yes |
| Unraid | Docker template | N/A | N/A |
<!-- redoc:end:platform-support -->

---

<!-- redoc:start:uninstall -->
## 卸载

**服务器：**

```bash
docker compose down       # 停止容器
docker compose down -v    # 停止容器并删除数据
```

**客户端（插件）：**

```
/plugin uninstall engram
```
<!-- redoc:end:uninstall -->

---

<!-- redoc:start:license -->
## 许可证

[MIT](LICENSE)

---

最初基于 Lukasz Raczylo 的 [claude-mnemonic](https://github.com/lukaszraczylo/claude-mnemonic)。
<!-- redoc:end:license -->
