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

Engram 解决了这个问题。它从编程会话中捕获观察记录，使用向量嵌入存储到 PostgreSQL 中，并在新会话中自动注入相关记忆。一台服务器，多个工作站，零上下文丢失。

**7 个整合后的 MCP 工具**替代了原来的 61 个遗留工具，上下文窗口占用减少超过 80%。混合搜索结合全文检索、向量相似度和 BM25，并配合交叉编码器重排序，精准呈现最相关的记忆。
<!-- redoc:end:intro -->

---

<!-- redoc:start:whats-new -->
## 最新版本

| 版本 | 亮点 |
|------|------|
| **v2.4.0** | LLM 驱动的记忆提取 — `store(action="extract")` 从原始内容中提取 (ADR-005) |
| **v2.3.1** | 嵌入弹性层 — 四状态熔断器，支持自动恢复 (ADR-004) |
| **v2.3.0** | 推理痕迹 / System 2 记忆 — 结构化推理链与质量评分 (ADR-003) |
| **v2.2.0** | 服务端周期性摘要生成器 — 摘要整合不再依赖客户端 |
| **v2.1.6** | 知识图谱体验优化 — 本地模式、搜索、视觉样式 |
| **v2.1.4** | 配置热重载，无需重启 |
| **v2.1.2** | 用户命令 — `/retro`、`/stats`、`/cleanup`、`/export` |
| **v2.1.0** | MCP 工具整合 — 从 61 个缩减到 7 个主要工具，上下文窗口占用减少超过 80% |

完整更新日志请查看 [Releases](https://github.com/thebtf/engram/releases)。
<!-- redoc:end:whats-new -->

---

<!-- redoc:start:architecture -->
## 架构

单服务器运行在端口 `37777`，提供 HTTP REST API、MCP 传输、Vue 3 仪表盘和后台任务。多个工作站通过 hooks 和 MCP 插件连接。

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

    H_A -- "Streamable HTTP / SSE" --> Server
    H_B -- "Streamable HTTP / SSE" --> Server

    subgraph "Engram Server :37777"
        Server[Worker]
        Server --> |HTTP API| API[REST Endpoints]
        Server --> |MCP| MCP_T["SSE + Streamable HTTP"]
        Server --> |Web| Dash["Vue 3 Dashboard"]
        Server --> |Background| BG["Summarizer + Insights"]
    end

    Server --> PG[(PostgreSQL 17\n+ pgvector)]
    Server -.-> LLM["LLM API\n(extraction/summarization)"]
    Server -.-> EMB["Embedding API"]
    Server -.-> RR["Reranker API"]
```

**服务器**（Docker 部署于远程主机 / Unraid / NAS）：
- PostgreSQL 17 + pgvector（HNSW 余弦索引）
- Worker — HTTP API、MCP SSE、MCP Streamable HTTP（`POST /mcp`）、Vue 3 仪表盘、整合调度器、周期性摘要生成器

**客户端**（每个工作站）：
- Hooks — 从 Claude Code 会话中捕获观察记录（11 个生命周期 hooks）
- MCP 插件 — 将 Claude Code 连接到远程服务器
- 斜杠命令 — `/retro`、`/stats`、`/cleanup`、`/export`、`/setup`、`/doctor`、`/restart`
<!-- redoc:end:architecture -->

---

<!-- redoc:start:features -->
## 功能

### 搜索与检索
- **混合搜索** — tsvector 全文检索 + pgvector 余弦相似度 + BM25，使用 Reciprocal Rank Fusion 融合
- **交叉编码器重排序** — 基于 API 的重排序器，提升精确度
- **HyDE 查询扩展** — 假设文档嵌入，改善召回率
- **知识图谱** — 17 种关系类型，可选 FalkorDB 后端，可视化浏览器
- **预设查询** — `decisions`、`changes`、`how_it_works`，满足常见查询需求

### 存储与组织
- **LLM 驱动提取** — 输入原始内容，获得结构化观察记录 (ADR-005)
- **推理痕迹** — System 2 记忆，包含结构化推理链和质量评分 (ADR-003)
- **版本化文档** — 支持历史记录、评论和语义搜索的文档集合
- **加密保险库** — AES-256-GCM 凭据存储，支持作用域访问控制
- **观察记录合并** — 去重和整合相关记忆

### 整合与维护
- **记忆衰减** — 每日指数衰减，结合访问频率提升
- **创意关联** — 自动发现 CONTRADICTS、EXPLAINS、SHARES_THEME 关系
- **季度遗忘** — 归档低相关性观察记录（受保护类型豁免）
- **周期性摘要生成器** — 服务端模式洞察生成，不依赖客户端
- **重要性评分** — 基于类型的加权评分，结合概念、反馈和检索加成

### 弹性与运维
- **嵌入弹性** — 四状态熔断器，支持自动恢复 (ADR-004)
- **配置热重载** — 无需重启即可更改设置
- **Token 预算** — 上下文注入遵循可配置的 token 限制
- **闭环学习** — A/B 注入策略，带结果追踪
- **编辑前护栏** — 修改文件前按 by_file 回忆相关记忆

### 仪表盘与用户体验
- **Vue 3 仪表盘** — 15 个视图：观察记录、搜索、图谱、模式、会话、分析、保险库、学习、系统健康
- **7 个斜杠命令** — `/retro`、`/stats`、`/cleanup`、`/export`、`/setup`、`/doctor`、`/restart`
- **11 个生命周期 hooks** — 从 session-start 到 stop
- **多工作站隔离** — 工作站 ID 作用域，支持跨工作站搜索
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
claude mcp add-json engram '{"type":"http","url":"http://your-server:37777/mcp","headers":{"Authorization":"Bearer ${ENGRAM_AUTH_ADMIN_TOKEN}"}}' -s user
```

#### SSE 传输

使用 `http://your-server:37777/sse` 作为 URL（JSON 结构同上）。

#### Stdio 代理（旧版）

适用于仅支持 stdio 的客户端：

```json
{
  "mcpServers": {
    "engram": {
      "command": "/path/to/mcp-stdio-proxy",
      "args": ["--url", "http://your-server:37777", "--token", "your-api-token"]
    }
  }
}
```

### 从源码构建

需要 Go 1.25+ 和 Node.js（用于仪表盘）。

```bash
git clone https://github.com/thebtf/engram.git && cd engram
make build    # 构建仪表盘 + worker + mcp 二进制文件
make install  # 安装插件 + 启动 worker
```
<!-- redoc:end:installation -->

---

<!-- redoc:start:upgrading -->
## 从 v1.x 升级到 v2.x

**工具整合：** 61 个遗留工具已被 7 个主要工具替代。现有的工具调用将停止工作。请更新你的工作流：

| 旧工具 | v2.x 对应工具 |
|--------|--------------|
| `search`、`decisions`、`how_it_works`、`find_by_file`、... | `recall(action="search")`、`recall(action="preset", preset="decisions")` 等 |
| `edit_observation`、`merge_observations`、... | `store(action="edit")`、`store(action="merge")` 等 |
| `get_memory_stats`、`bulk_delete_observations`、... | `admin(action="stats")`、`admin(action="bulk_delete")` 等 |

**新环境变量：**
- `ENGRAM_LLM_URL` / `ENGRAM_LLM_API_KEY` / `ENGRAM_LLM_MODEL` — 用于 LLM 驱动的提取
- `ENGRAM_ENCRYPTION_KEY` — 保险库加密（十六进制编码的 AES-256）
- `ENGRAM_HYDE_ENABLED` — HyDE 查询扩展
- `ENGRAM_GRAPH_PROVIDER` — `falkordb` 或留空（内存模式）
- `ENGRAM_CONSOLIDATION_ENABLED` / `ENGRAM_SMART_GC_ENABLED` — 整合功能

**Docker 镜像：** 从 `ghcr.io/thebtf/engram:latest` 拉取最新版本。数据库迁移在启动时自动执行。
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
| `ENGRAM_EMBEDDING_BASE_URL` | — | OpenAI 兼容的嵌入 API 端点 |
| `ENGRAM_EMBEDDING_API_KEY` | — | 嵌入 API 密钥 |
| `ENGRAM_EMBEDDING_MODEL_NAME` | — | 嵌入模型名称 |
| `ENGRAM_EMBEDDING_DIMENSIONS` | `4096` | 嵌入向量维度 |
| `ENGRAM_LLM_URL` | — | 用于提取/摘要的 LLM 端点 |
| `ENGRAM_LLM_API_KEY` | — | LLM API 密钥 |
| `ENGRAM_LLM_MODEL` | `gpt-4o-mini` | LLM 模型名称 |
| `ENGRAM_RERANKING_API_URL` | — | 交叉编码器重排序器端点 |
| `ENGRAM_ENCRYPTION_KEY` | — | 保险库加密密钥（十六进制编码的 AES-256） |
| `ENGRAM_HYDE_ENABLED` | `false` | 启用 HyDE 查询扩展 |
| `ENGRAM_CONTEXT_MAX_TOKENS` | `8000` | 上下文注入的 token 预算 |
| `ENGRAM_GRAPH_PROVIDER` | — | `falkordb` 或留空（内存模式） |
| `ENGRAM_CONSOLIDATION_ENABLED` | `false` | 启用记忆整合 |
| `ENGRAM_SMART_GC_ENABLED` | `false` | 启用智能垃圾回收 |

### 客户端（hooks）

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `ENGRAM_URL` | — | 插件使用的完整 MCP 端点 URL |
| `ENGRAM_AUTH_ADMIN_TOKEN` | — | 插件使用的 API token |
| `ENGRAM_WORKSTATION_ID` | 自动 | 覆盖工作站 ID（8 位十六进制） |
<!-- redoc:end:configuration -->

---

<!-- redoc:start:mcp-tools -->
## MCP 工具

Engram 提供 7 个主要工具，整合了所有记忆操作。每个工具支持多种操作。

### `recall` — 搜索与检索

| 操作 | 说明 |
|------|------|
| `search` | 混合语义 + 全文搜索（默认） |
| `preset` | 预设查询：`decisions`、`changes`、`how_it_works` |
| `by_file` | 查找与特定文件相关的观察记录 |
| `by_concept` | 按概念标签查找 |
| `by_type` | 按观察类型查找 |
| `similar` | 向量相似度搜索 |
| `timeline` | 按时间范围浏览 |
| `related` | 基于图谱的关系遍历 |
| `patterns` | 已检测到的重复模式 |
| `get` | 按 ID 获取观察记录 |
| `sessions` | 搜索/列出已索引的会话 |
| `explain` | 调试搜索结果排序 |
| `reasoning` | 检索推理痕迹 |

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
feedback(action="rate", id=123, useful=true)

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
