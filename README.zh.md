[English](README.md) | [Русский](README.ru.md) | **中文**

[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go)](https://go.dev/)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-17-4169E1?logo=postgresql)](https://www.postgresql.org/)
[![Docker](https://img.shields.io/badge/Docker-ready-2496ED?logo=docker)](https://www.docker.com/)
[![CI](https://github.com/thebtf/engram/actions/workflows/docker-publish.yml/badge.svg)](https://github.com/thebtf/engram/actions/workflows/docker-publish.yml)
[![License](https://img.shields.io/github/license/thebtf/engram)](LICENSE)

# Engram

Claude Code 工作站的持久化共享记忆基础设施。

Engram 从编码会话中捕获观察，将其存储在带有 pgvector 扩展的 PostgreSQL 中，并提供 **48 个 MCP 工具** — 包括混合搜索、知识图谱、记忆整合以及跨多个工作站的会话索引。

---

## 架构

单一服务端口（`37777`）提供 HTTP API、MCP 传输和 Web 仪表板占位页面。

```mermaid
graph TB
    subgraph "Workstation A"
        CC_A[Claude Code]
        H_A[Hooks / MCP Plugin]
        CC_A --> H_A
    end

    subgraph "Workstation B"
        CC_B[Claude Code]
        H_B[Hooks / MCP Plugin]
        CC_B --> H_B
    end

    H_A -- "Streamable HTTP / SSE" --> Server
    H_B -- "Streamable HTTP / SSE" --> Server

    subgraph "Engram Server :37777"
        Server[Worker]
        Server --> |HTTP API| API[REST Endpoints]
        Server --> |MCP| MCP_T["SSE + Streamable HTTP"]
        Server --> |Web| Dash["Dashboard *"]
    end

    Server --> PG[(PostgreSQL 17\n+ pgvector)]

    style Dash stroke-dasharray: 5 5
```

\* 仪表板目前为占位页面 — 计划在未来版本中实现。

**服务端**（远程主机 / Unraid / NAS 上的 Docker）：
- PostgreSQL 17 + pgvector 扩展
- Worker — HTTP API、MCP SSE、MCP Streamable HTTP（`POST /mcp`）、仪表板、整合调度器

**客户端**（每个工作站）：
- Hooks — 从 Claude Code 会话中捕获观察
- MCP 插件 — 通过 Streamable HTTP 或 SSE 将 Claude Code 连接到远程服务器

---

## 功能特性

| 功能 | 描述 |
|------|------|
| **PostgreSQL + pgvector** | 支持并发存储，配备 HNSW 余弦向量索引 |
| **混合搜索** | tsvector GIN + 向量相似度 + BM25，RRF 融合 |
| **48 个 MCP 工具** | 搜索、上下文、关系、批量操作、会话、维护、集合 |
| **记忆整合** | 每日衰减、每日关联、季度遗忘 |
| **17 种关系类型** | 知识图谱：causes、fixes、supersedes、contradicts、explains、shares_theme... |
| **会话索引** | JSONL 解析器，支持工作站隔离和增量索引 |
| **集合** | YAML 配置的知识库，支持智能分块（Markdown、Go） |
| **MCP 传输** | SSE + Streamable HTTP（`POST /mcp`），单端口 |
| **嵌入向量** | OpenAI 兼容 REST API |
| **交叉编码器重排序** | API 重排序器，提升搜索结果质量 |
| **Token 认证** | 所有端点的 Bearer 认证 |
| **Instinct 导入** | 将 ECC instinct 导入为指导性观察，支持语义去重 |
| **自学习** | 每会话效用信号检测，实现自适应记忆 |
| **仪表板** | Worker 端口上的 Web 仪表板 *（占位 — 计划中）* |

---

## 快速开始

```bash
git clone https://github.com/thebtf/engram.git
cd engram

# 配置
cp .env.example .env   # 编辑为您的设置

docker compose up -d
```

这将启动 PostgreSQL 17 + pgvector 和 Engram 服务器，地址为 `http://your-server:37777`。

验证：

```bash
curl http://your-server:37777/health
```

**已有 PostgreSQL？** 仅运行服务器容器并设置 `DATABASE_DSN`：

```bash
DATABASE_DSN="postgres://user:pass@your-pg:5432/engram?sslmode=disable" \
  docker compose up -d server
```

然后配置 MCP（见下方[安装](#安装)部分）。

---

## 安装

### 插件安装（推荐）

插件会自动注册 MCP 服务器。设置两个环境变量后安装：

```bash
# 设置环境变量（Claude Code 运行时读取）
# Linux/macOS：添加到 shell 配置文件；Windows：设置为系统环境变量
ENGRAM_URL=http://your-server:37777/mcp
ENGRAM_API_TOKEN=your-api-token
```

```
/plugin marketplace add thebtf/engram-marketplace
/plugin install engram
```

重启 Claude Code。插件提供 hooks、skills 和 MCP 连接 — 全部已配置完成。

### 手动 MCP 配置

如果不使用插件，可直接配置 MCP。Engram 在同一端口暴露三种传输方式：

| 传输方式 | 端点 | 协议 | 最佳用途 |
|----------|------|------|----------|
| **Streamable HTTP** | `POST /mcp` | JSON-RPC over HTTP | 直接连接（推荐） |
| **SSE** | `GET /sse` + `POST /message` | Server-Sent Events | 长连接流式传输 |
| **Stdio 代理** | 本地二进制文件 | stdio 到 SSE 桥接 | 仅支持 stdio 的客户端 |

#### Streamable HTTP（推荐）

添加到 `~/.claude/settings.json`（用户范围）或 `.claude/settings.json`（项目范围）：

```json
{
  "mcpServers": {
    "engram": {
      "type": "url",
      "url": "http://your-server:37777/mcp",
      "headers": {
        "Authorization": "Bearer ${ENGRAM_API_TOKEN}"
      }
    }
  }
}
```

Claude Code 在运行时会展开环境变量中的 `${VAR}` 引用。您也可以使用字面值。

**CLI 快捷方式：**

```bash
claude mcp add-json engram '{"type":"http","url":"http://your-server:37777/mcp","headers":{"Authorization":"Bearer ${ENGRAM_API_TOKEN}"}}' -s user
```

#### SSE 传输

使用 `http://your-server:37777/sse` 作为 URL：

```json
{
  "mcpServers": {
    "engram": {
      "type": "url",
      "url": "http://your-server:37777/sse",
      "headers": {
        "Authorization": "Bearer ${ENGRAM_API_TOKEN}"
      }
    }
  }
}
```

#### Stdio 代理（旧版）

适用于仅支持 stdio 的客户端。需要 `mcp-stdio-proxy` 二进制文件：

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

### 客户端二进制文件（可选）

仅在使用 **stdio 代理**或 **hooks** 时需要。Streamable HTTP / SSE 传输无需任何客户端二进制文件。

**脚本安装（macOS / Linux）：**

```bash
curl -sSL https://raw.githubusercontent.com/thebtf/engram/main/scripts/install.sh | bash
```

**从源码构建（Windows PowerShell）：**

```powershell
git clone https://github.com/thebtf/engram.git && cd engram

$env:CGO_ENABLED = "1"
go build -tags fts5 -ldflags "-s -w" -o bin\mcp-stdio-proxy.exe .\cmd\mcp-stdio-proxy
```

Hooks 是 JavaScript 文件，随插件预配置。无需构建。

---

## 配置

### 服务端

| 变量 | 默认值 | 描述 |
|------|--------|------|
| `DATABASE_DSN` | — | PostgreSQL 连接字符串 **（必需）** |
| `DATABASE_MAX_CONNS` | `10` | 最大数据库连接数 |
| `WORKER_PORT` | `37777` | Worker 端口 |
| `WORKER_HOST` | `0.0.0.0` | Worker 绑定地址 |
| `API_TOKEN` | — | Bearer 令牌（远程访问时推荐） |
| `EMBEDDING_PROVIDER` | `openai` | `openai`（OpenAI 兼容 REST API） |
| `EMBEDDING_BASE_URL` | — | OpenAI 兼容端点 URL |
| `EMBEDDING_API_KEY` | — | OpenAI 提供者的 API 密钥 |
| `EMBEDDING_MODEL_NAME` | — | OpenAI 提供者的模型名称 |
| `EMBEDDING_DIMENSIONS` | `384` | 嵌入向量维度 |
| `RERANKING_ENABLED` | `true` | 启用交叉编码器重排序 |
| `ENGRAM_LLM_URL` | — | 用于观察提取的 OpenAI 兼容 LLM 端点 |
| `ENGRAM_LLM_API_KEY` | — | LLM 端点的 API 密钥 |
| `ENGRAM_LLM_MODEL` | `gpt-4o-mini` | 用于观察提取的模型名称 |

### 客户端（仅 hooks）

这些变量由客户端 hooks 使用，**不**用于 MCP 传输配置。MCP 连接在 `settings.json` 中配置（见[安装](#安装)）。

| 变量 | 默认值 | 描述 |
|------|--------|------|
| `ENGRAM_URL` | — | 插件的完整 MCP 端点 URL |
| `ENGRAM_API_TOKEN` | — | 插件的 API 令牌 |
| `ENGRAM_WORKER_HOST` | `127.0.0.1` | Hooks 的 Worker 地址 |
| `ENGRAM_WORKER_PORT` | `37777` | Hooks 的 Worker 端口 |
| `ENGRAM_SESSIONS_DIR` | `~/.claude/projects/` | 会话 JSONL 目录 |
| `ENGRAM_WORKSTATION_ID` | 自动生成 | 覆盖工作站 ID（8 位十六进制） |
| `ENGRAM_CONTEXT_OBSERVATIONS` | `100` | 每会话最大记忆数 |
| `ENGRAM_CONTEXT_FULL_COUNT` | `25` | 包含完整详情的记忆数 |

---

## MCP 工具（48 个）

44 个始终可用的工具，4 个条件性工具（需要文档存储），另加 `import_instincts`（始终可用，使用嵌入向量进行去重）。

<details>
<summary><strong>搜索与发现（11 个）</strong></summary>

| 工具 | 描述 |
|------|------|
| `search` | 跨所有记忆的混合语义 + 全文搜索 |
| `timeline` | 按时间范围浏览观察 |
| `decisions` | 查找架构和设计决策 |
| `changes` | 查找代码修改和变更 |
| `how_it_works` | 系统理解查询 |
| `find_by_concept` | 按概念标签查找观察 |
| `find_by_file` | 查找与文件相关的观察 |
| `find_by_type` | 按类型查找观察 |
| `find_similar_observations` | 向量相似度搜索 |
| `find_related_observations` | 基于图的关系遍历 |
| `explain_search_ranking` | 调试搜索结果排序 |

</details>

<details>
<summary><strong>上下文检索（4 个）</strong></summary>

| 工具 | 描述 |
|------|------|
| `get_recent_context` | 获取项目的近期观察 |
| `get_context_timeline` | 按时间段组织的上下文 |
| `get_timeline_by_query` | 按查询过滤的时间线 |
| `get_patterns` | 检测到的重复模式 |

</details>

<details>
<summary><strong>观察管理（9 个）</strong></summary>

| 工具 | 描述 |
|------|------|
| `get_observation` | 按 ID 获取观察 |
| `edit_observation` | 修改观察字段 |
| `tag_observation` | 添加/移除概念标签 |
| `get_observations_by_tag` | 按标签查找观察 |
| `merge_observations` | 合并重复项 |
| `bulk_delete_observations` | 批量删除 |
| `bulk_mark_superseded` | 标记为已取代 |
| `bulk_boost_observations` | 提升重要性分数 |
| `export_observations` | 导出为 JSON |

</details>

<details>
<summary><strong>分析与质量（11 个）</strong></summary>

| 工具 | 描述 |
|------|------|
| `get_memory_stats` | 记忆系统统计 |
| `get_observation_quality` | 观察的质量评分 |
| `suggest_consolidations` | 建议合并的观察 |
| `get_temporal_trends` | 时间趋势分析 |
| `get_data_quality_report` | 数据质量指标 |
| `batch_tag_by_pattern` | 按模式匹配自动标记 |
| `analyze_search_patterns` | 搜索使用分析 |
| `get_observation_relationships` | 观察的关系图 |
| `get_observation_scoring_breakdown` | 评分公式分解 |
| `analyze_observation_importance` | 重要性分析 |
| `check_system_health` | 系统健康检查 |

</details>

<details>
<summary><strong>会话（2 个）</strong></summary>

| 工具 | 描述 |
|------|------|
| `search_sessions` | 跨已索引会话的全文搜索 |
| `list_sessions` | 带过滤的会话列表 |

</details>

<details>
<summary><strong>图（2 个）</strong></summary>

| 工具 | 描述 |
|------|------|
| `get_graph_neighbors` | 获取知识图谱中的相邻节点 |
| `get_graph_stats` | 知识图谱统计 |

</details>

<details>
<summary><strong>集合与文档（7 个）</strong></summary>

| 工具 | 描述 |
|------|------|
| `list_collections` | 列出已配置的集合及文档数量 |
| `list_documents` | 列出集合中的文档 |
| `get_document` | 检索完整文档内容 |
| `ingest_document` | 摄取文档：分块、嵌入、存储 |
| `search_collection` | 跨文档块的语义搜索 |
| `remove_document` | 停用文档 |
| `import_instincts` | 将 instinct 文件导入为指导性观察 |

</details>

<details>
<summary><strong>整合与维护（3 个）</strong></summary>

| 工具 | 描述 |
|------|------|
| `run_consolidation` | 触发整合周期 |
| `trigger_maintenance` | 运行维护任务 |
| `get_maintenance_stats` | 维护统计 |

</details>

---

## 记忆整合

### 重要性评分（写入时）

每个观察在创建时获得一个重要性评分：

```
importance = typeWeight * (1 + conceptBonus + feedbackBonus + retrievalBonus + utilityBonus)
```

类型权重：`discovery=0.9`、`decision=0.85`、`pattern=0.8`、`insight=0.75`、`guidance=0.7`、`observation=0.5`、`question=0.4`

### 相关性评分（整合时）

整合调度器定期重新计算相关性：

```
relevance = decay * (0.3 + 0.3*access) * relations * (0.5 + importance) * (0.7 + 0.3*confidence)
```

其中 `decay = exp(-0.01 * daysSinceCreation)`。

### 整合周期

| 周期 | 频率 | 描述 |
|------|------|------|
| **相关性衰减** | 每 24 小时 | 指数时间衰减，配合访问频率提升 |
| **创意关联** | 每 24 小时 | 采样观察，计算嵌入相似度，发现关系（CONTRADICTS、EXPLAINS、SHARES_THEME、PARALLEL_CONTEXT） |
| **遗忘** | 每 90 天 | 归档相关性低于阈值的观察（默认禁用） |

**遗忘保护** — 以下观察永远不会被归档：
- 重要性评分 >= 0.7
- 存在时间 < 90 天
- 类型为 `decision` 或 `discovery`

---

## 会话索引

会话从 Claude Code JSONL 文件索引，支持工作站隔离：

```
workstation_id = sha256(hostname + machine_id)[:8]
project_id     = sha256(cwd_path)[:8]
session_id     = UUID from JSONL filename
composite_key  = workstation_id:project_id:session_id
```

多个工作站共享同一服务器时，会话保持隔离，同时搜索可跨所有工作站进行。

---

## 开发

```bash
make build            # 构建所有二进制文件
make test             # 运行测试（带竞态检测）
make test-coverage    # 覆盖率报告
make dev              # 前台运行 worker
make install          # 安装插件、注册 MCP
make uninstall        # 移除插件
make clean            # 清理构建产物
```

<details>
<summary><strong>项目结构</strong></summary>

```
cmd/
  mcp/                MCP stdio 服务器（本地直接访问）
  mcp-stdio-proxy/    stdio -> SSE 桥接（客户端）
  worker/             HTTP API + MCP SSE + MCP Streamable HTTP + 仪表板
  hooks/              Claude Code 生命周期 hooks（旧版 Go，见 plugin/hooks/）
internal/
  chunking/           AST 感知的文档分块（Markdown、Go）
  collections/        YAML 集合配置 + 上下文路由
  instincts/          Instinct 解析器和导入
  config/             配置管理
  consolidation/      衰减、关联、遗忘
  db/gorm/            PostgreSQL 存储 + 自动迁移
  embedding/          OpenAI 兼容 REST 嵌入提供者
  graph/              内存 CSR 图遍历
  mcp/                MCP 协议（服务器、SSE、Streamable HTTP）
  reranking/          API 交叉编码器重排序器
  scoring/            重要性 + 相关性评分
  search/             混合检索 + RRF 融合
  sessions/           JSONL 解析器 + 索引器
  vector/pgvector/    pgvector 客户端 + 同步
  worker/             HTTP 处理器、中间件、服务
pkg/
  hooks/              Hook 事件客户端
  models/             领域模型 + 关系类型
  strutil/            共享字符串工具
plugin/               Claude Code 插件定义 + 市场
```

</details>

---

## 平台支持

| 平台 | 服务端（Docker） | 客户端插件 | 客户端构建 |
|------|:-:|:-:|:-:|
| macOS Intel | 支持 | 支持 | 支持 |
| macOS Apple Silicon | 支持 | 支持 | 支持 |
| Linux amd64 | 支持 | 支持 | 支持 |
| Linux arm64 | 支持 | 支持 | 支持 |
| Windows amd64 | WSL2/Docker Desktop | 从源码构建 | 支持 |
| Unraid | Docker 模板 | 不适用 | 不适用 |

---

## 卸载

**服务端：**

```bash
docker compose down       # 停止容器
docker compose down -v    # 停止容器并移除数据
```

**客户端（插件）：**

```
/plugin uninstall engram
```

**客户端（脚本安装，macOS/Linux）：**

```bash
curl -sSL https://raw.githubusercontent.com/thebtf/engram/main/scripts/install.sh | bash -s -- --uninstall
```

**客户端（Windows）：**

```powershell
Remove-Item -Recurse -Force "$env:USERPROFILE\.claude\plugins\marketplaces\engram"
```

---

## 许可证

[MIT](LICENSE)

---

最初基于 Lukasz Raczylo 的 [claude-mnemonic](https://github.com/lukaszraczylo/claude-mnemonic)。
