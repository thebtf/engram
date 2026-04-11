<!-- redoc:start:header -->
[English](README.md) | **Русский** | [中文](README.zh.md)

[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go)](https://go.dev/)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-17-4169E1?logo=postgresql)](https://www.postgresql.org/)
[![Docker](https://img.shields.io/badge/Docker-ready-2496ED?logo=docker)](https://www.docker.com/)
[![CI](https://github.com/thebtf/engram/actions/workflows/docker-publish.yml/badge.svg)](https://github.com/thebtf/engram/actions/workflows/docker-publish.yml)
[![License](https://img.shields.io/github/license/thebtf/engram)](LICENSE)
<!-- redoc:end:header -->

<!-- redoc:start:intro -->
# Engram

**Инфраструктура персистентной общей памяти для AI-агентов программирования.**

AI-агенты программирования забывают всё между сессиями. Каждый новый разговор начинается с нуля — прошлые решения, исправления багов, архитектурные выборы и выученные паттерны теряются. Вы тратите время на повторное объяснение контекста, а агенты повторяют одни и те же ошибки.

Engram решает эту проблему. Он захватывает наблюдения из сессий программирования, хранит их в PostgreSQL с vector embeddings и автоматически внедряет релевантные воспоминания в новые сессии. Один сервер, несколько рабочих станций, ноль потерь контекста.

**7 консолидированных MCP-инструментов** заменяют 61 устаревший, сокращая использование context window более чем на 80%. Гибридный поиск сочетает полнотекстовый поиск, векторное сходство и BM25 с cross-encoder reranking, чтобы находить именно те воспоминания, которые важны.
<!-- redoc:end:intro -->

---

<!-- redoc:start:whats-new -->
## Что нового в v2.4.0

| Версия | Основное изменение |
|--------|-------------------|
| **v2.4.0** | LLM-управляемое извлечение памяти — `store(action="extract")` из сырого контента (ADR-005) |
| **v2.3.1** | Слой устойчивости embeddings — 4-состоянийный circuit breaker с автовосстановлением (ADR-004) |
| **v2.3.0** | Reasoning Traces / System 2 Memory — структурированные цепочки рассуждений с оценками качества (ADR-003) |
| **v2.2.0** | Серверный периодический summarizer — без зависимости от клиента для консолидации |
| **v2.1.6** | UX графа знаний — локальный режим, поиск, визуальное оформление |
| **v2.1.4** | Горячая перезагрузка конфигурации без перезапуска |
| **v2.1.2** | Пользовательские команды — `/retro`, `/stats`, `/cleanup`, `/export` |
| **v2.1.0** | Консолидация MCP-инструментов — с 61 до 7 основных, сокращение context window >80% |

Полный список изменений — в разделе [Releases](https://github.com/thebtf/engram/releases).
<!-- redoc:end:whats-new -->

---

<!-- redoc:start:architecture -->
## Архитектура

Единый сервер на порту `37777` обслуживает HTTP REST API, MCP-транспорты, Vue 3 dashboard и фоновых workers. Несколько рабочих станций подключаются через hooks и MCP-плагин.

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

**Сервер** (Docker на удалённом хосте / Unraid / NAS):
- PostgreSQL 17 с pgvector (HNSW cosine index)
- Worker — HTTP API, MCP SSE, MCP Streamable HTTP (`POST /mcp`), Vue 3 dashboard, планировщик консолидации, периодический summarizer

**Клиент** (каждая рабочая станция):
- Hooks — захватывают наблюдения из сессий Claude Code (11 lifecycle hooks)
- MCP Plugin — подключает Claude Code к удалённому серверу
- Slash-команды — `/retro`, `/stats`, `/cleanup`, `/export`, `/setup`, `/doctor`, `/restart`
<!-- redoc:end:architecture -->

---

<!-- redoc:start:features -->
## Возможности

### Поиск и извлечение
- **Гибридный поиск** — tsvector full-text + pgvector cosine similarity + BM25, объединённые через Reciprocal Rank Fusion
- **Cross-encoder reranking** — API-ранжировщик для повышения точности
- **HyDE query expansion** — hypothetical document embeddings для лучшей полноты
- **Граф знаний** — 17 типов связей, опциональный бэкенд FalkorDB, визуальный обозреватель
- **Предустановленные запросы** — `decisions`, `changes`, `how_it_works` для частых сценариев

### Хранение и организация
- **LLM-управляемое извлечение** — подайте сырой контент, получите структурированные наблюдения (ADR-005)
- **Reasoning traces** — System 2 Memory со структурированными цепочками и оценками качества (ADR-003)
- **Версионные документы** — коллекции с историей, комментариями и семантическим поиском
- **Зашифрованное хранилище** — AES-256-GCM шифрование учётных данных с разграничением доступа
- **Слияние наблюдений** — дедупликация и консолидация связанных воспоминаний

### Консолидация и обслуживание
- **Затухание памяти** — ежедневное экспоненциальное затухание с усилением при частом доступе
- **Креативные ассоциации** — обнаружение связей CONTRADICTS, EXPLAINS, SHARES_THEME
- **Квартальное забывание** — архивация малозначимых наблюдений (защищённые типы не затрагиваются)
- **Периодический summarizer** — серверная генерация паттерн-инсайтов, без зависимости от клиента
- **Оценка важности** — взвешенная оценка по типу с бонусами за концепции, обратную связь и частоту извлечения

### Устойчивость и эксплуатация
- **Устойчивость embeddings** — 4-состоянийный circuit breaker с автовосстановлением (ADR-004)
- **Горячая перезагрузка конфигурации** — изменение настроек без перезапуска
- **Бюджетирование token** — инъекция контекста с учётом настраиваемых лимитов
- **Замкнутый цикл обучения** — A/B стратегии инъекции с отслеживанием результатов
- **Защита перед редактированием** — recall by_file перед модификацией

### Dashboard и UX
- **Vue 3 dashboard** — 15 представлений: наблюдения, поиск, граф, паттерны, сессии, аналитика, хранилище, обучение, здоровье системы
- **7 slash-команд** — `/retro`, `/stats`, `/cleanup`, `/export`, `/setup`, `/doctor`, `/restart`
- **11 lifecycle hooks** — от session-start до stop
- **Изоляция рабочих станций** — разделение по workstation ID с возможностью кросс-поиска
<!-- redoc:end:features -->

---

<!-- redoc:start:use-cases -->
## Сценарии использования

- **Непрерывность контекста** — начните новую сессию и автоматически получите релевантные решения, паттерны и предыдущую работу
- **Архитектурная память** — запросите прошлые дизайн-решения перед принятием новых
- **Осведомлённость перед редактированием** — проверьте, что известно о файле, прежде чем его изменять
- **Обнаружение паттернов** — выявление повторяющихся паттернов между сессиями и рабочими станциями
- **Обмен знаниями в команде** — несколько рабочих станций используют один сервер памяти
- **Управление учётными данными** — хранение и извлечение API-ключей и секретов без .env-файлов
- **Ретроспективы сессий** — анализ прошлых сессий для выявления инсайтов по продуктивности
<!-- redoc:end:use-cases -->

---

<!-- redoc:start:quick-start -->
## Быстрый старт

```bash
git clone https://github.com/thebtf/engram.git
cd engram

# Настройка
cp .env.example .env   # отредактируйте под свои параметры

# Запуск
docker compose up -d
```

Это запускает PostgreSQL 17 + pgvector и сервер Engram по адресу `http://your-server:37777`.

Проверка:

```bash
curl http://your-server:37777/health
```

Затем установите плагин в Claude Code:

```
/plugin marketplace add thebtf/engram-marketplace
/plugin install engram
```

Задайте переменные окружения (считываются Claude Code при запуске):

```bash
# Linux/macOS: добавьте в профиль shell
# Windows: задайте как системные переменные окружения
ENGRAM_URL=http://your-server:37777/mcp
ENGRAM_AUTH_ADMIN_TOKEN=your-admin-token
```

Перезапустите Claude Code. Память активна.
<!-- redoc:end:quick-start -->

---

<!-- redoc:start:installation -->
## Установка

### Установка плагина (рекомендуется)

Плагин автоматически регистрирует MCP-сервер, hooks и slash-команды.

```bash
# Сначала задайте переменные окружения
ENGRAM_URL=http://your-server:37777/mcp
ENGRAM_AUTH_ADMIN_TOKEN=your-admin-token
```

```
/plugin marketplace add thebtf/engram-marketplace
/plugin install engram
```

Перезапустите Claude Code. Всё настроено.

### Docker Compose

```bash
git clone https://github.com/thebtf/engram.git && cd engram
cp .env.example .env   # отредактируйте DATABASE_DSN, токены, конфигурацию embeddings
docker compose up -d
```

**Уже есть PostgreSQL?** Запустите только контейнер сервера:

```bash
DATABASE_DSN="postgres://user:pass@your-pg:5432/engram?sslmode=disable" \
  docker compose up -d server
```

### Ручная настройка MCP

Если вы не используете плагин, настройте MCP напрямую в `~/.claude/settings.json`:

#### Streamable HTTP (рекомендуется)

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

Claude Code подставляет `${VAR}` из переменных окружения при запуске.

**Команда CLI:**

```bash
claude mcp add-json engram '{"type":"http","url":"http://your-server:37777/mcp","headers":{"Authorization":"Bearer ${ENGRAM_AUTH_ADMIN_TOKEN}"}}' -s user
```

#### SSE Transport

Используйте `http://your-server:37777/sse` в качестве URL (та же структура JSON, что и выше).

#### Stdio Proxy (устаревший)

Для клиентов, поддерживающих только stdio:

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

### Сборка из исходников

Требуется Go 1.25+ и Node.js (для dashboard).

```bash
git clone https://github.com/thebtf/engram.git && cd engram
make build    # собирает dashboard + worker + mcp-бинарники
make install  # устанавливает плагин + запускает worker
```
<!-- redoc:end:installation -->

---

<!-- redoc:start:upgrading -->
## Обновление с v1.x до v2.x

**Консолидация инструментов:** 61 устаревший инструмент заменён 7 основными. Существующие вызовы инструментов перестанут работать. Обновите свои рабочие процессы:

| Устаревший инструмент | Эквивалент в v2.x |
|-----------------------|-------------------|
| `search`, `decisions`, `how_it_works`, `find_by_file`, ... | `recall(action="search")`, `recall(action="preset", preset="decisions")` и т.д. |
| `edit_observation`, `merge_observations`, ... | `store(action="edit")`, `store(action="merge")` и т.д. |
| `get_memory_stats`, `bulk_delete_observations`, ... | `admin(action="stats")`, `admin(action="bulk_delete")` и т.д. |

**Новые переменные окружения:**
- `ENGRAM_LLM_URL` / `ENGRAM_LLM_API_KEY` / `ENGRAM_LLM_MODEL` — для LLM-управляемого извлечения
- `ENGRAM_ENCRYPTION_KEY` — шифрование хранилища (AES-256 в hex-формате)
- `ENGRAM_HYDE_ENABLED` — HyDE query expansion
- `ENGRAM_GRAPH_PROVIDER` — `falkordb` или пусто (in-memory)
- `ENGRAM_CONSOLIDATION_ENABLED` / `ENGRAM_SMART_GC_ENABLED` — функции консолидации

**Docker-образ:** Загрузите последнюю версию из `ghcr.io/thebtf/engram:latest`. Миграции базы данных выполняются автоматически при запуске.
<!-- redoc:end:upgrading -->

---

<!-- redoc:start:configuration -->
## Конфигурация

### Сервер

| Переменная | По умолчанию | Описание |
|-----------|-------------|----------|
| `DATABASE_DSN` | — | Строка подключения к PostgreSQL **(обязательно)** |
| `DATABASE_MAX_CONNS` | `10` | Максимум подключений к БД |
| `ENGRAM_WORKER_PORT` | `37777` | Порт сервера |
| `ENGRAM_API_TOKEN` | — | Bearer auth token |
| `ENGRAM_AUTH_ADMIN_TOKEN` | — | Admin token |
| `ENGRAM_EMBEDDING_BASE_URL` | — | Эндпоинт OpenAI-совместимого embedding API |
| `ENGRAM_EMBEDDING_API_KEY` | — | API-ключ для embeddings |
| `ENGRAM_EMBEDDING_MODEL_NAME` | — | Название модели embeddings |
| `ENGRAM_EMBEDDING_DIMENSIONS` | `4096` | Размерность embedding-вектора |
| `ENGRAM_LLM_URL` | — | Эндпоинт LLM для извлечения/суммаризации |
| `ENGRAM_LLM_API_KEY` | — | API-ключ LLM |
| `ENGRAM_LLM_MODEL` | `gpt-4o-mini` | Название модели LLM |
| `ENGRAM_RERANKING_API_URL` | — | Эндпоинт cross-encoder reranker |
| `ENGRAM_ENCRYPTION_KEY` | — | Ключ шифрования хранилища (AES-256 в hex-формате) |
| `ENGRAM_HYDE_ENABLED` | `false` | Включить HyDE query expansion |
| `ENGRAM_CONTEXT_MAX_TOKENS` | `8000` | Бюджет token для инъекции контекста |
| `ENGRAM_GRAPH_PROVIDER` | — | `falkordb` или пусто (in-memory) |
| `ENGRAM_CONSOLIDATION_ENABLED` | `false` | Включить консолидацию памяти |
| `ENGRAM_SMART_GC_ENABLED` | `false` | Включить умную сборку мусора |

### Клиент (hooks)

| Переменная | По умолчанию | Описание |
|-----------|-------------|----------|
| `ENGRAM_URL` | — | Полный URL MCP-эндпоинта для плагина |
| `ENGRAM_AUTH_ADMIN_TOKEN` | — | API token для плагина |
| `ENGRAM_WORKSTATION_ID` | auto | Переопределение workstation ID (8-символьный hex) |
<!-- redoc:end:configuration -->

---

<!-- redoc:start:mcp-tools -->
## MCP-инструменты

Engram предоставляет 7 основных инструментов, объединяющих все операции с памятью. Каждый инструмент поддерживает несколько действий.

### `recall` — Поиск и извлечение

| Действие | Описание |
|----------|----------|
| `search` | Гибридный семантический + полнотекстовый поиск (по умолчанию) |
| `preset` | Предустановленные запросы: `decisions`, `changes`, `how_it_works` |
| `by_file` | Поиск наблюдений, связанных с конкретными файлами |
| `by_concept` | Поиск по тегам концепций |
| `by_type` | Поиск по типу наблюдения |
| `similar` | Поиск по векторному сходству |
| `timeline` | Просмотр по временному диапазону |
| `related` | Обход связей в графе |
| `patterns` | Обнаруженные повторяющиеся паттерны |
| `get` | Получение наблюдения по ID |
| `sessions` | Поиск/список индексированных сессий |
| `explain` | Отладка ранжирования результатов поиска |
| `reasoning` | Извлечение reasoning traces |

### `store` — Сохранение и организация

| Действие | Описание |
|----------|----------|
| `create` | Сохранить новое наблюдение (по умолчанию) |
| `edit` | Изменить поля наблюдения |
| `merge` | Объединить дублирующиеся наблюдения |
| `import` | Массовый импорт наблюдений |
| `extract` | LLM-управляемое извлечение из сырого контента |

### `feedback` — Оценка и улучшение

| Действие | Описание |
|----------|----------|
| `rate` | Оценить наблюдение как полезное или нет |
| `suppress` | Подавить некачественные наблюдения |
| `outcome` | Записать результат для замкнутого цикла обучения |

### `vault` — Зашифрованные учётные данные

| Действие | Описание |
|----------|----------|
| `store` | Сохранить зашифрованные учётные данные |
| `get` | Получить учётные данные |
| `list` | Список сохранённых учётных данных |
| `delete` | Удалить учётные данные |
| `status` | Статус и здоровье хранилища |

### `docs` — Версионные документы

| Действие | Описание |
|----------|----------|
| `create` | Создать документ |
| `read` | Прочитать содержимое документа |
| `list` | Список документов |
| `history` | История версий |
| `comment` | Добавить комментарии |
| `collections` | Управление коллекциями |
| `ingest` | Разбить, векторизовать и сохранить документ |
| `search_docs` | Семантический поиск по документам |

### `admin` — Массовые операции и аналитика

21 действие, включая: `bulk_delete`, `bulk_supersede`, `tag`, `graph`, `stats`, `trends`, `quality`, `export`, `maintenance`, `scoring`, `consolidation` и другие.

### `check_system_health` — Здоровье системы

Отчёт о состоянии всех подсистем: база данных, embeddings, reranker, LLM, хранилище, граф, консолидация.
<!-- redoc:end:mcp-tools -->

---

<!-- redoc:start:usage -->
## Использование

```python
# Проверка подключения
check_system_health()

# Поиск по памяти
recall(query="authentication architecture")

# Предустановленные запросы
recall(action="preset", preset="decisions", query="caching strategy")

# Проверка истории файла перед редактированием
recall(action="by_file", files="internal/search/hybrid.go")

# Сохранение наблюдения
store(content="Switched from Redis to in-memory cache for dev environments", title="Cache strategy change", tags=["architecture", "caching"])

# Извлечение наблюдений из сырого контента
store(action="extract", content="<paste raw session notes or code review>")

# Оценка воспоминания
feedback(action="rate", id=123, rating="useful")

# Сохранение учётных данных
vault(action="store", name="OPENAI_KEY", value="sk-...")

# Получение учётных данных
vault(action="get", name="OPENAI_KEY")
```
<!-- redoc:end:usage -->

---

<!-- redoc:start:troubleshooting -->
## Устранение неполадок

| Симптом | Решение |
|---------|---------|
| `check_system_health` показывает нездоровые embeddings | Проверьте `ENGRAM_EMBEDDING_BASE_URL` и API-ключ. Circuit breaker автоматически восстанавливается после временных сбоев. |
| Поиск не возвращает результатов | Убедитесь, что наблюдения существуют: `recall(action="preset", preset="decisions")`. Проверьте здоровье embeddings. |
| MCP — отказ в подключении | Убедитесь, что сервер запущен: `curl http://your-server:37777/health`. Проверьте `ENGRAM_URL` в переменных окружения. |
| Vault возвращает "encryption not configured" | Задайте `ENGRAM_ENCRYPTION_KEY` (64-символьная hex-строка = 32 байта AES-256). |
| Dashboard не загружается | Убедитесь, что сборка выполнена через `make build` (включает dashboard). Проверьте консоль браузера на ошибки. |
| Плагин не обнаружен после установки | Перезапустите Claude Code. Проверьте, что `ENGRAM_URL` и `ENGRAM_AUTH_ADMIN_TOKEN` заданы как переменные окружения. |
| Высокое потребление памяти | Уменьшите `DATABASE_MAX_CONNS`. Отключите консолидацию, если она не нужна. Проверьте `ENGRAM_EMBEDDING_DIMENSIONS`. |

Логи сервера доступны по адресу `http://your-server:37777/api/logs`.
<!-- redoc:end:troubleshooting -->

---

<!-- redoc:start:development -->
## Разработка

```bash
make build            # Собрать dashboard + все Go-бинарники
make test             # Запустить тесты с race detector
make test-coverage    # Отчёт о покрытии
make dev              # Запустить worker на переднем плане
make install          # Собрать + установить плагин + запустить worker
make uninstall        # Удалить плагин
make clean            # Очистить артефакты сборки
```

### Структура проекта

```
cmd/
  worker/             Точка входа: HTTP API + MCP + dashboard
  mcp/                Автономный MCP-сервер
  mcp-stdio-proxy/    Мост stdio -> SSE
  engram-cli/         CLI-клиент
internal/
  chunking/           AST-aware разбивка документов
  collections/        YAML-конфигурация коллекций
  config/             Конфигурация с горячей перезагрузкой
  consolidation/      Затухание, ассоциации, забывание
  crypto/             AES-256-GCM шифрование хранилища
  db/gorm/            PostgreSQL хранилища + миграции
  embedding/          REST-провайдер embeddings + слой устойчивости
  graph/              In-memory CSR + FalkorDB
  instincts/          Парсер и импорт инстинктов
  learning/           Самообучение, LLM-клиент
  maintenance/        Фоновые задачи (summarizer, паттерн-инсайты)
  mcp/                MCP-протокол, 7 основных обработчиков инструментов
  privacy/            Обнаружение и редактирование секретов
  reranking/          Cross-encoder reranker
  scoring/            Оценка важности + релевантности
  search/             Гибридный поиск + RRF-слияние
  sessions/           JSONL-парсер + индексатор
  vector/pgvector/    pgvector-клиент
  worker/             HTTP-обработчики, middleware, сервис
    sdk/              Извлечение наблюдений, обнаружение reasoning
pkg/
  models/             Доменные модели + типы связей
  strutil/            Общие строковые утилиты
plugin/
  engram/             Плагин Claude Code (hooks, команды)
ui/                   Vue 3 dashboard SPA
```

### CI-процессы

| Процесс | Описание |
|---------|----------|
| `docker-publish.yml` | Сборка и публикация Docker-образа в ghcr.io |
| `plugin-publish.yml` | Публикация OpenClaw-плагина |
| `static.yml` | Деплой сайта на GitHub Pages |
| `sync-marketplace.yml` | Синхронизация плагина с marketplace |
<!-- redoc:end:development -->

---

<!-- redoc:start:platform-support -->
## Поддержка платформ

| Платформа | Сервер (Docker) | Клиентский плагин | Сборка из исходников |
|-----------|:-:|:-:|:-:|
| macOS Intel | Yes | Yes | Yes |
| macOS Apple Silicon | Yes | Yes | Yes |
| Linux amd64 | Yes | Yes | Yes |
| Linux arm64 | Yes | Yes | Yes |
| Windows amd64 | WSL2 / Docker Desktop | Yes | Yes |
| Unraid | Docker template | N/A | N/A |
<!-- redoc:end:platform-support -->

---

<!-- redoc:start:uninstall -->
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
<!-- redoc:end:uninstall -->

---

<!-- redoc:start:license -->
## Лицензия

[MIT](LICENSE)

---

Изначально основан на [claude-mnemonic](https://github.com/lukaszraczylo/claude-mnemonic) от Lukasz Raczylo.
<!-- redoc:end:license -->
