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

Engram решает эту проблему, оставляя только те примитивы памяти, которые реально работали в продакшене: явные issues, documents, memories, behavioral rules, credentials и API tokens. Один сервер, несколько рабочих станций, ноль потерь контекста.

В v5.0.0 session-start inject упрощён до статического composite payload: открытые issues, always-inject behavioral rules и recent memories. Старый динамический relevance / graph / reranking / extraction stack больше не находится на основном продуктном пути.

Сокращённая static-first MCP surface остаётся для surviving entity model и удерживает использование context window на разумном уровне.
<!-- redoc:end:intro -->

---

<!-- redoc:start:whats-new -->
## Что нового в v5.0.0

| Версия | Основное изменение |
|--------|-------------------|
| **v5.0.0** | Cleaned Baseline — static-only storage, split observations, session-start gRPC + cache fallback |
| **v4.4.0** | Loom tenant — background task execution и daemon-side project event bridge |
| **v4.0.0** | Daemon architecture — muxcore engine, gRPC transport, local persistent daemon, auto-binary plugin |

Полный список изменений — в разделе [Releases](https://github.com/thebtf/engram/releases).
<!-- redoc:end:whats-new -->

---

<!-- redoc:start:architecture -->
## Архитектура

Единый сервер на порту `37777` обслуживает HTTP REST API, gRPC-сервис (через cmux), Vue 3 dashboard и статическую surface хранения/чтения. Каждая рабочая станция запускает локальный daemon, который подключается к серверу по gRPC. Несколько сессий Claude Code делят один daemon.

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

**Сервер** (Docker на удалённом хосте / Unraid / NAS):
- PostgreSQL 17
- Worker — HTTP API, gRPC, Vue 3 dashboard, static entity stores

**Клиент** (каждая рабочая станция):
- Hooks — session-start, session-end и связанные Claude Code lifecycle integrations
- MCP Plugin — подключает Claude Code к локальному daemon / server bridge
- Slash-команды — `/setup`, `/doctor`, `/restart` и memory-related workflows
<!-- redoc:end:architecture -->

---

<!-- redoc:start:features -->
## Возможности

### Поиск и извлечение
- **Static session-start payload** — issues + behavioral rules + memories через gRPC `GetSessionStartContext`
- **Project-scoped memory recall** — простой SQL-backed retrieval для static memories
- **Document search** — versioned documents и collection-backed search остаются доступны

### Хранение и организация
- **Memories** — явные project-scoped notes в таблице `memories`
- **Behavioral rules** — always-inject guidance в таблице `behavioral_rules`
- **Версионные документы** — коллекции с историей и комментариями
- **Зашифрованное хранилище** — AES-256-GCM шифрование учётных данных с разграничением доступа
- **Cross-project issues** — явная operational coordination между агентами и проектами

### Устойчивость и эксплуатация
- **Session-start cache fallback** — `${ENGRAM_DATA_DIR}/cache/session-start-{project-slug}.json` используется при временной недоступности сервера
- **Version negotiation** — явная проверка major-version compatibility на session-start path
- **Горячая перезагрузка конфигурации** — изменение настроек без перезапуска
- **Graceful daemon restart** — сохраняется binary swap и control socket flow

### Dashboard и UX
- **Vue 3 dashboard** — сфокусирован на surviving static entity surface
- **Lifecycle hooks** — session-start / session-end и связанные integrations остаются установленными
- **Multi-workstation support** — один сервер, несколько локальных daemon’ов, общая static memory surface
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
claude mcp add-json engram '{"type":"stdio","command":"engram","env":{"ENGRAM_URL":"http://your-server:37777","ENGRAM_AUTH_ADMIN_TOKEN":"${ENGRAM_AUTH_ADMIN_TOKEN}"}}' -s user
```

### Сборка из исходников

Требуется Go 1.25+ и Node.js (для dashboard).

```bash
git clone https://github.com/thebtf/engram.git && cd engram
make build    # собирает dashboard + daemon + release assets
make install  # устанавливает плагин + запускает daemon
```
<!-- redoc:end:installation -->

---

<!-- redoc:start:upgrading -->
## Обновление до v5.0.0

v5.0.0 — это **breaking cleanup release**.

Что изменилось:
- основной runtime путь теперь static-only
- session-start inject основан на issues + behavioral rules + memories
- старый dynamic learning / graph / reranking / extraction stack ушёл из главного product path
- client и server теперь явно проверяют major-version compatibility на session-start path

Шаги обновления:
1. обновить plugin до `5.0.0`
2. обновить daemon до `v5.0.0`
3. перезапустить Claude Code и daemon
4. проверить plugin update detection и session-start cache fallback

**Docker-образ:** Используйте последнюю версию из `ghcr.io/thebtf/engram:latest`. Миграции БД выполняются автоматически при старте.
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
| `ENGRAM_VAULT_KEY` | — | Канонический vault key для шифрования credentials |
| `ENGRAM_ENCRYPTION_KEY` | — | Legacy fallback env var для vault key |
| `ENGRAM_DATA_DIR` | auto | Каталог данных daemon’а (включая session-start cache) |

### Клиент (hooks)

| Переменная | По умолчанию | Описание |
|-----------|-------------|----------|
| `ENGRAM_URL` | — | Полный URL сервера / MCP для plugin и hooks |
| `ENGRAM_AUTH_ADMIN_TOKEN` | — | API token для plugin |
| `ENGRAM_API_TOKEN` | — | Legacy fallback token env var для hooks / plugin runtime |
| `ENGRAM_DATA_DIR` | auto | Каталог cache и daemon state |
| `ENGRAM_WORKSTATION_ID` | auto | Переопределение workstation ID (8-символьный hex) |
<!-- redoc:end:configuration -->

---

<!-- redoc:start:mcp-tools -->
## MCP-инструменты

Engram предоставляет сокращённую static-first MCP surface для surviving entity model.

Основные категории в v5:
- issues / issue comments
- memories / behavioral rules
- documents
- credentials / vault
- loom background tasks

Старая dynamic search / graph / learning-oriented tool surface больше не является primary v5 path.

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
