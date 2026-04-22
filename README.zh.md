<!-- redoc:start:header -->
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

缩减后的 static-first MCP surface 继续服务于 surviving entity model，并保持上下文窗口占用在可控范围内。
<!-- redoc:end:intro -->

---

<!-- redoc:start:whats-new -->
## 最新版本

| 版本 | 亮点 |
|------|------|
| **v5.0.0** | Cleaned Baseline — static-only storage、split observations、session-start gRPC + cache fallback |
| **v4.4.0** | Loom tenant — background task execution 与 daemon-side project event bridge |
| **v4.0.0** | Daemon architecture — muxcore engine、gRPC transport、local persistent daemon、auto-binary plugin |

完整更新日志请查看 [Releases](https://github.com/thebtf/engram/releases)。
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
ENGRAM_URL=http://your-server:37777/mcp
ENGRAM_AUTH_ADMIN_TOKEN=your-admin-token
```

重启 Claude Code。记忆功能现已激活。
<!-- redoc:end:quick-start -->

---

<!-- redoc:start:installation -->
## 安装

### 插件安装（推荐）

插件会自动注册 MCP 服务器、hooks 和斜杠命令。

```bash
# 先设置环境变量
ENGRAM_URL=http://your-server:37777/mcp
ENGRAM_AUTH_ADMIN_TOKEN=your-admin-token
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
        "Authorization": "Bearer ${ENGRAM_AUTH_ADMIN_TOKEN}"
      }
    }
  }
}
```

Claude Code 在运行时会从环境变量中展开 `${VAR}`。

**CLI 快捷方式：**

```bash
claude mcp add-json engram '{"type":"stdio","command":"engram","env":{"ENGRAM_URL":"http://your-server:37777","ENGRAM_AUTH_ADMIN_TOKEN":"${ENGRAM_AUTH_ADMIN_TOKEN}"}}' -s user
```

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
## 升级到 v5.0.0

v5.0.0 是一个 **breaking cleanup release**。

变化要点：
- 主要 runtime 路径现在是 static-only
- session-start inject 基于 issues + behavioral rules + memories
- 旧的 dynamic learning / graph / reranking / extraction 栈已离开主产品路径
- client 与 server 现在会在 session-start path 上显式检查 major-version compatibility

升级步骤：
1. 将插件升级到 `5.0.0`
2. 将 daemon 升级到 `v5.0.0`
3. 重启 Claude Code 和 daemon
4. 验证 plugin update detection 与 session-start cache fallback

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
| `ENGRAM_API_TOKEN` | — | Bearer 认证 token |
| `ENGRAM_AUTH_ADMIN_TOKEN` | — | 管理员 token |
| `ENGRAM_VAULT_KEY` | — | 用于 credentials 加密的标准 vault key |
| `ENGRAM_ENCRYPTION_KEY` | — | 旧版 fallback vault key 环境变量 |
| `ENGRAM_DATA_DIR` | 自动 | daemon 数据目录（也用于 session-start cache） |

### 客户端（hooks）

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `ENGRAM_URL` | — | plugin / hooks 使用的完整 server 或 MCP URL |
| `ENGRAM_AUTH_ADMIN_TOKEN` | — | plugin 使用的 API token |
| `ENGRAM_API_TOKEN` | — | hooks / plugin runtime 使用的 legacy fallback token |
| `ENGRAM_DATA_DIR` | 自动 | cache 与 daemon state 目录 |
| `ENGRAM_WORKSTATION_ID` | 自动 | 覆盖工作站 ID（8 位十六进制） |
<!-- redoc:end:configuration -->

---

<!-- redoc:start:mcp-tools -->
## MCP 工具

Engram 在 v5 中提供缩减后的 static-first MCP surface，围绕 surviving entity model 展开。

主要类别：
- issues / issue comments
- memories / behavioral rules
- documents
- credentials / vault
- loom background tasks

旧的 dynamic search / graph / learning-oriented tool surface 已不再是 primary v5 path。

### `store` — 保存与组织

| 操作 | 说明 |
|------|------|
| `create` | 存储新的观察记录（默认） |
| `edit` | 修改观察记录字段 |
| `merge` | 合并重复的观察记录 |
| `import` | 批量导入观察记录 |
| `extract` | 从原始内容中进行 LLM 驱动的提取 |

### `feedback` — 评价与改进

| 操作 | 说明 |
|------|------|
| `rate` | 评价观察记录是否有用 |
| `suppress` | 抑制低质量观察记录 |
| `outcome` | 记录闭环学习的结果 |

### `vault` — 加密凭据

| 操作 | 说明 |
|------|------|
| `store` | 存储加密凭据 |
| `get` | 检索凭据 |
| `list` | 列出已存储的凭据 |
| `delete` | 删除凭据 |
| `status` | 保险库状态和健康检查 |

### `docs` — 版本化文档

| 操作 | 说明 |
|------|------|
| `create` | 创建文档 |
| `read` | 读取文档内容 |
| `list` | 列出文档 |
| `history` | 版本历史 |
| `comment` | 添加评论 |
| `collections` | 管理文档集合 |
| `ingest` | 分块、嵌入和存储文档 |
| `search_docs` | 跨文档语义搜索 |

### `admin` — 批量操作与分析

包含 21 种操作：`bulk_delete`、`bulk_supersede`、`tag`、`graph`、`stats`、`trends`、`quality`、`export`、`maintenance`、`scoring`、`consolidation` 等。

### `check_system_health` — 系统健康检查

报告所有子系统的状态：数据库、嵌入、重排序器、LLM、保险库、图谱、整合。
<!-- redoc:end:mcp-tools -->

---

<!-- redoc:start:usage -->
## 使用示例

```python
# 验证连接
check_system_health()

# 搜索记忆
recall(query="authentication architecture")

# 预设查询
recall(action="preset", preset="decisions", query="caching strategy")

# 编辑前检查文件历史
recall(action="by_file", files="internal/search/hybrid.go")

# 存储观察记录
store(content="Switched from Redis to in-memory cache for dev environments", title="Cache strategy change", tags=["architecture", "caching"])

# 从原始内容中提取观察记录
store(action="extract", content="<paste raw session notes or code review>")

# 评价一条记忆
feedback(action="rate", id=123, rating="useful")

# 存储凭据
vault(action="store", name="OPENAI_KEY", value="sk-...")

# 检索凭据
vault(action="get", name="OPENAI_KEY")
```
<!-- redoc:end:usage -->

---

<!-- redoc:start:troubleshooting -->
## 故障排除

| 现象 | 解决方法 |
|------|----------|
| `check_system_health` 显示嵌入不健康 | 检查 `ENGRAM_EMBEDDING_BASE_URL` 和 API 密钥。熔断器会在瞬时故障后自动恢复。 |
| 搜索无结果 | 确认观察记录是否存在：`recall(action="preset", preset="decisions")`。检查嵌入是否健康。 |
| MCP 连接被拒绝 | 确认服务器正在运行：`curl http://your-server:37777/health`。检查环境中的 `ENGRAM_URL`。 |
| 保险库返回 "encryption not configured" | 设置 `ENGRAM_ENCRYPTION_KEY`（64 位十六进制字符串 = 32 字节 AES-256）。 |
| 仪表盘无法加载 | 确保使用 `make build` 构建（包含仪表盘）。检查浏览器控制台的错误信息。 |
| 安装后插件未被检测到 | 重启 Claude Code。确认 `ENGRAM_URL` 和 `ENGRAM_AUTH_ADMIN_TOKEN` 已设置为环境变量。 |
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
