[English](README.md) | **Русский**

[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go)](https://go.dev/)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-17-4169E1?logo=postgresql)](https://www.postgresql.org/)
[![Docker](https://img.shields.io/badge/Docker-ready-2496ED?logo=docker)](https://www.docker.com/)
[![CI](https://github.com/thebtf/engram/actions/workflows/docker-publish.yml/badge.svg)](https://github.com/thebtf/engram/actions/workflows/docker-publish.yml)
[![License](https://img.shields.io/github/license/thebtf/engram)](LICENSE)

# Engram

Инфраструктура постоянной разделяемой памяти для рабочих станций Claude Code.

Engram фиксирует наблюдения из сессий программирования, хранит их в PostgreSQL с pgvector и предоставляет **48 MCP-инструментов** — гибридный поиск, граф знаний, консолидация памяти и индексация сессий между несколькими рабочими станциями.

---

## Архитектура

Единый серверный порт (`37777`) обслуживает HTTP API, MCP-транспорты и заглушку веб-панели.

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

\* Панель управления — заглушка, запланирована на будущий релиз.

**Сервер** (Docker на удалённом хосте / Unraid / NAS):
- PostgreSQL 17 с расширением pgvector
- Worker — HTTP API, MCP SSE, MCP Streamable HTTP (`POST /mcp`), панель управления, планировщик консолидации

**Клиент** (каждая рабочая станция):
- Hooks — захват наблюдений из сессий Claude Code
- MCP Plugin — подключает Claude Code к удалённому серверу через Streamable HTTP или SSE

---

## Возможности

| Возможность | Описание |
|-------------|----------|
| **PostgreSQL + pgvector** | Конкурентное хранилище с векторным индексом HNSW (косинусное расстояние) |
| **Гибридный поиск** | tsvector GIN + векторное сходство + BM25, слияние через RRF |
| **48 MCP-инструментов** | Поиск, контекст, связи, массовые операции, сессии, обслуживание, коллекции |
| **Консолидация памяти** | Ежедневное затухание, ежедневные ассоциации, ежеквартальное забывание |
| **17 типов связей** | Граф знаний: causes, fixes, supersedes, contradicts, explains, shares_theme... |
| **Индексация сессий** | JSONL-парсер с изоляцией по рабочим станциям, инкрементальная индексация |
| **Коллекции** | Базы знаний с YAML-конфигурацией и интеллектуальным разбиением (Markdown, Go, Python, TypeScript через tree-sitter) |
| **MCP-транспорты** | SSE + Streamable HTTP (`POST /mcp`) на одном порту |
| **Embedding** | Локальный ONNX BGE (384D) или OpenAI-совместимый REST API |
| **Кросс-энкодерное переранжирование** | ONNX-реранкер для повышения качества поиска |
| **Token-аутентификация** | Bearer-аутентификация для всех эндпоинтов |
| **Импорт инстинктов** | Импорт ECC-инстинктов как наблюдений-руководств с семантической дедупликацией |
| **Самообучение** | Обнаружение сигналов полезности в рамках сессии для адаптивной памяти |
| **Панель управления** | Веб-панель на порту worker *(заглушка — запланирована)* |

---

## Быстрый старт

```bash
git clone https://github.com/thebtf/engram.git
cd engram

# Настройка
cp .env.example .env   # отредактируйте под свои параметры

docker compose up -d
```

Это запускает PostgreSQL 17 + pgvector и сервер Engram по адресу `http://your-server:37777`.

Проверка:

```bash
curl http://your-server:37777/health
```

**Уже есть PostgreSQL?** Запустите только серверный контейнер и укажите `DATABASE_DSN`:

```bash
DATABASE_DSN="postgres://user:pass@your-pg:5432/engram?sslmode=disable" \
  docker compose up -d server
```

Затем настройте MCP (см. [Установка](#установка) ниже).

---

## Установка

### Установка через плагин (рекомендуется)

Плагин автоматически регистрирует MCP-сервер. Задайте две переменные окружения и установите:

```bash
# Задайте переменные окружения (используются Claude Code при запуске)
# Linux/macOS: добавьте в профиль оболочки; Windows: задайте как системные переменные окружения
ENGRAM_URL=http://your-server:37777/mcp
ENGRAM_API_TOKEN=your-api-token
```

```
/plugin marketplace add thebtf/engram-marketplace
/plugin install engram
```

Перезапустите Claude Code. Плагин предоставляет hooks, skills и MCP-подключение — всё уже настроено.

### Ручная настройка MCP

Если вы не используете плагин, настройте MCP напрямую. Engram предоставляет три транспорта на одном порту:

| Транспорт | Эндпоинт | Протокол | Лучше всего для |
|-----------|----------|----------|-----------------|
| **Streamable HTTP** | `POST /mcp` | JSON-RPC over HTTP | Прямое подключение (рекомендуется) |
| **SSE** | `GET /sse` + `POST /message` | Server-Sent Events | Долгоживущие потоковые соединения |
| **Stdio Proxy** | локальный бинарник | stdio → SSE мост | Клиенты, поддерживающие только stdio |

#### Streamable HTTP (рекомендуется)

Добавьте в `~/.claude/settings.json` (область пользователя) или `.claude/settings.json` (область проекта):

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

Claude Code подставляет `${VAR}` из переменных окружения при запуске. Можно также использовать значения напрямую.

**Через CLI:**

```bash
claude mcp add-json engram '{"type":"http","url":"http://your-server:37777/mcp","headers":{"Authorization":"Bearer ${ENGRAM_API_TOKEN}"}}' -s user
```

#### SSE-транспорт

Используйте `http://your-server:37777/sse` в качестве URL:

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

#### Stdio Proxy (устаревший)

Для клиентов, поддерживающих только stdio. Требуется бинарник `mcp-stdio-proxy`:

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

### Клиентские бинарники (опционально)

Нужны только для **stdio proxy** или **hooks**. Транспорты Streamable HTTP / SSE работают без каких-либо клиентских бинарников.

**Установка скриптом (macOS / Linux):**

```bash
curl -sSL https://raw.githubusercontent.com/thebtf/engram/main/scripts/install.sh | bash
```

**Сборка из исходников (Windows PowerShell):**

```powershell
git clone https://github.com/thebtf/engram.git && cd engram

$env:CGO_ENABLED = "1"
go build -tags fts5 -ldflags "-s -w" -o bin\mcp-stdio-proxy.exe .\cmd\mcp-stdio-proxy
```

Hooks написаны на JavaScript и поставляются вместе с плагином. Сборка не требуется.

---

## Конфигурация

### Сервер

| Переменная | По умолчанию | Описание |
|------------|-------------|----------|
| `DATABASE_DSN` | — | Строка подключения к PostgreSQL **(обязательно)** |
| `DATABASE_MAX_CONNS` | `10` | Максимальное количество подключений к базе |
| `WORKER_PORT` | `37777` | Порт worker |
| `WORKER_HOST` | `0.0.0.0` | Адрес привязки worker |
| `API_TOKEN` | — | Bearer-токен (рекомендуется для удалённого доступа) |
| `EMBEDDING_PROVIDER` | `onnx` | `onnx` (локальный BGE) или `openai` (REST API) |
| `EMBEDDING_BASE_URL` | — | URL OpenAI-совместимого эндпоинта |
| `EMBEDDING_API_KEY` | — | API-ключ для OpenAI-провайдера |
| `EMBEDDING_MODEL_NAME` | — | Имя модели для OpenAI-провайдера |
| `EMBEDDING_DIMENSIONS` | `384` | Размерность вектора embedding |
| `RERANKING_ENABLED` | `true` | Включить кросс-энкодерное переранжирование |
| `ENGRAM_LLM_URL` | — | OpenAI-совместимый LLM endpoint для извлечения наблюдений |
| `ENGRAM_LLM_API_KEY` | — | API-ключ для LLM endpoint |
| `ENGRAM_LLM_MODEL` | `gpt-4o-mini` | Модель для извлечения наблюдений |

### Клиент (только hooks)

Эти переменные используются клиентскими hooks, а **не** для настройки MCP-транспорта. MCP-подключение настраивается в `settings.json` (см. [Установка](#установка)).

| Переменная | По умолчанию | Описание |
|------------|-------------|----------|
| `ENGRAM_URL` | — | Полный URL MCP-эндпоинта для плагина |
| `ENGRAM_API_TOKEN` | — | API-токен для плагина |
| `ENGRAM_WORKER_HOST` | `127.0.0.1` | Адрес worker для hooks |
| `ENGRAM_WORKER_PORT` | `37777` | Порт worker для hooks |
| `ENGRAM_SESSIONS_DIR` | `~/.claude/projects/` | Директория с JSONL-файлами сессий |
| `ENGRAM_WORKSTATION_ID` | авто | Переопределение ID рабочей станции (8-символьный hex) |
| `ENGRAM_CONTEXT_OBSERVATIONS` | `100` | Максимум наблюдений на сессию |
| `ENGRAM_CONTEXT_FULL_COUNT` | `25` | Наблюдения с полной детализацией |

---

## MCP-инструменты (48)

44 всегда доступных инструмента, 4 условных (требуют хранилище документов), а также `import_instincts` (всегда доступен, использует embedding для дедупликации).

<details>
<summary><strong>Поиск и обнаружение (11)</strong></summary>

| Инструмент | Описание |
|------------|----------|
| `search` | Гибридный семантический + полнотекстовый поиск по всей памяти |
| `timeline` | Просмотр наблюдений по временному диапазону |
| `decisions` | Поиск архитектурных и проектных решений |
| `changes` | Поиск модификаций и изменений кода |
| `how_it_works` | Запросы на понимание системы |
| `find_by_concept` | Поиск наблюдений по тегам концепций |
| `find_by_file` | Поиск наблюдений, связанных с файлом |
| `find_by_type` | Поиск наблюдений по типу |
| `find_similar_observations` | Поиск по векторному сходству |
| `find_related_observations` | Обход связей в графе знаний |
| `explain_search_ranking` | Отладка ранжирования результатов поиска |

</details>

<details>
<summary><strong>Получение контекста (4)</strong></summary>

| Инструмент | Описание |
|------------|----------|
| `get_recent_context` | Недавние наблюдения для проекта |
| `get_context_timeline` | Контекст, организованный по временным периодам |
| `get_timeline_by_query` | Хронология, отфильтрованная по запросу |
| `get_patterns` | Обнаруженные повторяющиеся паттерны |

</details>

<details>
<summary><strong>Управление наблюдениями (9)</strong></summary>

| Инструмент | Описание |
|------------|----------|
| `get_observation` | Получить наблюдение по ID |
| `edit_observation` | Изменить поля наблюдения |
| `tag_observation` | Добавить/удалить теги концепций |
| `get_observations_by_tag` | Найти наблюдения по тегу |
| `merge_observations` | Объединить дубликаты |
| `bulk_delete_observations` | Массовое удаление |
| `bulk_mark_superseded` | Пометить как устаревшие |
| `bulk_boost_observations` | Повысить оценки важности |
| `export_observations` | Экспорт в JSON |

</details>

<details>
<summary><strong>Анализ и качество (11)</strong></summary>

| Инструмент | Описание |
|------------|----------|
| `get_memory_stats` | Статистика системы памяти |
| `get_observation_quality` | Оценка качества наблюдения |
| `suggest_consolidations` | Предложения по объединению наблюдений |
| `get_temporal_trends` | Анализ трендов по времени |
| `get_data_quality_report` | Метрики качества данных |
| `batch_tag_by_pattern` | Автоматическая разметка по паттернам |
| `analyze_search_patterns` | Аналитика использования поиска |
| `get_observation_relationships` | Граф связей наблюдения |
| `get_observation_scoring_breakdown` | Разбор формулы оценки |
| `analyze_observation_importance` | Анализ важности |
| `check_system_health` | Проверка состояния системы |

</details>

<details>
<summary><strong>Сессии (2)</strong></summary>

| Инструмент | Описание |
|------------|----------|
| `search_sessions` | Полнотекстовый поиск по индексированным сессиям |
| `list_sessions` | Список сессий с фильтрацией |

</details>

<details>
<summary><strong>Граф (2)</strong></summary>

| Инструмент | Описание |
|------------|----------|
| `get_graph_neighbors` | Получить соседние узлы в графе знаний |
| `get_graph_stats` | Статистика графа знаний |

</details>

<details>
<summary><strong>Коллекции и документы (7)</strong></summary>

| Инструмент | Описание |
|------------|----------|
| `list_collections` | Список настроенных коллекций с количеством документов |
| `list_documents` | Список документов в коллекции |
| `get_document` | Получить полное содержимое документа |
| `ingest_document` | Загрузить документ: разбиение, embedding, сохранение |
| `search_collection` | Семантический поиск по фрагментам документов |
| `remove_document` | Деактивировать документ |
| `import_instincts` | Импорт файлов инстинктов как наблюдений-руководств |

</details>

<details>
<summary><strong>Консолидация и обслуживание (3)</strong></summary>

| Инструмент | Описание |
|------------|----------|
| `run_consolidation` | Запустить цикл консолидации |
| `trigger_maintenance` | Запустить задачи обслуживания |
| `get_maintenance_stats` | Статистика обслуживания |

</details>

---

## Консолидация памяти

### Оценка важности (при записи)

Каждое наблюдение получает оценку важности при создании:

```
importance = typeWeight * (1 + conceptBonus + feedbackBonus + retrievalBonus + utilityBonus)
```

Веса типов: `discovery=0.9`, `decision=0.85`, `pattern=0.8`, `insight=0.75`, `guidance=0.7`, `observation=0.5`, `question=0.4`

### Оценка релевантности (консолидация)

Планировщик консолидации периодически пересчитывает релевантность:

```
relevance = decay * (0.3 + 0.3*access) * relations * (0.5 + importance) * (0.7 + 0.3*confidence)
```

Где `decay = exp(-0.01 * daysSinceCreation)`.

### Циклы консолидации

| Цикл | Частота | Описание |
|------|---------|----------|
| **Затухание релевантности** | Каждые 24ч | Экспоненциальное временное затухание с учётом частоты обращений |
| **Творческие ассоциации** | Каждые 24ч | Выборка наблюдений, вычисление сходства embedding, обнаружение связей (CONTRADICTS, EXPLAINS, SHARES_THEME, PARALLEL_CONTEXT) |
| **Забывание** | Каждые 90 дней | Архивация наблюдений ниже порога релевантности (отключено по умолчанию) |

**Защита от забывания** — наблюдения никогда не архивируются, если:
- Оценка важности >= 0.7
- Возраст < 90 дней
- Тип — `decision` или `discovery`

---

## Индексация сессий

Сессии индексируются из JSONL-файлов Claude Code с изоляцией по рабочим станциям:

```
workstation_id = sha256(hostname + machine_id)[:8]
project_id     = sha256(cwd_path)[:8]
session_id     = UUID from JSONL filename
composite_key  = workstation_id:project_id:session_id
```

Несколько рабочих станций, использующих один сервер, сохраняют изоляцию сессий, при этом поиск работает по всем станциям.

---

## Разработка

```bash
make build            # Собрать все бинарники
make test             # Запустить тесты с детектором гонок
make test-coverage    # Отчёт о покрытии
make dev              # Запустить worker в режиме переднего плана
make install          # Установить плагин, зарегистрировать MCP
make uninstall        # Удалить плагин
make clean            # Очистить артефакты сборки
```

<details>
<summary><strong>Структура проекта</strong></summary>

```
cmd/
  mcp/                MCP stdio-сервер (локальный прямой доступ)
  mcp-sse/            MCP SSE HTTP-сервер (автономный)
  mcp-stdio-proxy/    stdio → SSE мост (клиентская сторона)
  worker/             HTTP API + MCP SSE + MCP Streamable HTTP + панель управления
  hooks/              Hooks жизненного цикла Claude Code (устаревшие Go, см. plugin/hooks/)
internal/
  chunking/           AST-aware разбиение документов (md, Go, Python, TS)
  collections/        YAML-конфигурация коллекций + маршрутизация контекста
  instincts/          Парсер инстинктов и импорт
  config/             Управление конфигурацией
  consolidation/      Затухание, ассоциации, забывание
  db/gorm/            PostgreSQL-хранилища + автоматические миграции
  embedding/          ONNX BGE + OpenAI REST провайдеры
  graph/              Обход графа в памяти (CSR)
  mcp/                MCP-протокол (сервер, SSE, Streamable HTTP)
  reranking/          ONNX кросс-энкодерный реранкер
  scoring/            Оценка важности + релевантности
  search/             Гибридный поиск + RRF-слияние
  sessions/           JSONL-парсер + индексатор
  vector/pgvector/    pgvector-клиент + синхронизация
  worker/             HTTP-обработчики, middleware, сервис
pkg/
  hooks/              Клиент событий hooks
  models/             Доменные модели + типы связей
  strutil/            Общие строковые утилиты
plugin/               Определение плагина Claude Code + marketplace
```

</details>

---

## Поддержка платформ

| Платформа | Сервер (Docker) | Клиентский плагин | Сборка клиента |
|-----------|:-:|:-:|:-:|
| macOS Intel | Да | Да | Да |
| macOS Apple Silicon | Да | Да | Да |
| Linux amd64 | Да | Да | Да |
| Linux arm64 | Да | Да | Да |
| Windows amd64 | WSL2/Docker Desktop | Сборка из исходников | Да |
| Unraid | Docker-шаблон | Н/Д | Н/Д |

---

## Удаление

**Сервер:**

```bash
docker compose down       # остановить контейнеры
docker compose down -v    # остановить контейнеры и удалить данные
```

**Клиент (плагин):**

```
/plugin uninstall engram
```

**Клиент (установка скриптом, macOS/Linux):**

```bash
curl -sSL https://raw.githubusercontent.com/thebtf/engram/main/scripts/install.sh | bash -s -- --uninstall
```

**Клиент (Windows):**

```powershell
Remove-Item -Recurse -Force "$env:USERPROFILE\.claude\plugins\marketplaces\engram"
```

---

## Лицензия

[MIT](LICENSE)

---

Изначально основан на [claude-mnemonic](https://github.com/lukaszraczylo/claude-mnemonic) авторства Lukasz Raczylo.
