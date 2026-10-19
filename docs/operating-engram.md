# Operating Engram

Operational guide for Engram installation/update, existing project knowledge,
server configuration, retained readers, Code Workspace, and restart procedures.

---

## Версия, установка и обновление

Для этого source-выпуска серверный daemon и plugin/client/parser manifests
объявляют `7.0.1`.
Это версия исходников, а не доказательство опубликованного релиза, обновлённого
каталога marketplace или установленного комплекта. Установленные версии и байты
проверяются отдельно. Локальная synthetic/no-auth проверка не подтверждает
production, principal, обычную свежую сессию или установку.

R1 — обычный установленный агент и WebUI выбирают один логический проект,
находят и открывают существующее полезное решение с источником, историей и
возвратом. Сохранность данных, Vault и независимых снимков кода остаётся
обязательной. Полный semantic/code/graph/two-worktree путь — следующий R2,
не условие каждого выпуска R1 и не отменённая функция. Применимые проверки
безопасности, миграции, retired writers, платформ и rollback определяет
[release-протокол](RELEASE-PROTOCOL.md); разделение R1/R2 их не отменяет.

1. До обновления зафиксируй фактические версии и пути установленных компонентов,
   серверный origin и режим из `/api/auth/me`. Не меняй auth mode ради установки.
2. Сохрани восстановимую резервную копию полезной БД, документов/версий, истории,
   Vault с его прежним ключом, серверных настроек и client/plugin-data. Не включай
   значения ключей или токенов в отчёт. Destructive cutover требует отдельной
   проверенной процедуры восстановления и полномочия владельца, не этой инструкции.
3. Сервер обновляй по существующему deployment-пути. Для поддерживаемого
   digest-pinned Compose используй [deployment guide](DEPLOYMENT.md): согласованные
   три image digest и retained `publication-result.json`, не source build или `latest`
   как доказательство версии. Сохрани прежний комплект и данные для rollback;
   смена image сама по себе не делает старую схему БД совместимой с rollback.
4. Для первой установки plugin нужны Node.js 18+ и обычный marketplace выбранного
   host. В OMP: `omp plugin marketplace add thebtf/engram-marketplace`, затем
   `omp plugin install engram@engram`. В Claude Code: `/plugin marketplace add
   thebtf/engram-marketplace`, затем `/plugin install engram`. Это host-specific
   пути из [Installation](../README.md#installation). Standalone
   [Bash](../scripts/install.sh) и [PowerShell](../scripts/install.ps1) installers
   регистрируют Claude Code, не OMP; без заданной версии выбирают GitHub latest.
   Не используй uninstall/reinstall как обычное обновление: удаление может затронуть
   plugin-data или `~/.engram`.
5. При обновлении существующего OMP plugin используй
   `omp plugin upgrade engram@engram`. Обновление marketplace меняет только каталог.
   Затем выполни поддерживаемый host reload `/reload-plugins` или открой свежую
   сессию по [setup](../plugin/engram/commands/setup.md#updating-omp). Это не обещание
   перезапуска daemon и не повод перезапускать действующий root без его владельца.
6. Настрой установленный launcher на тот же bare server origin, без `/mcp` и `/sse`.
   Для уже подтверждённого no-auth режима достаточно `server_url` в
   `~/.engram/config.json`; auth-enabled режим требует выданный workstation keycard
   в `api_token`, не серверный `ENGRAM_AUTH_ADMIN_TOKEN`. Сохрани прежние значения
   при обновлении. Непустые environment/plugin options имеют приоритет над файлом;
   путь файла выбирается как `ENGRAM_CONFIG_FILE` → существующий plugin-data
   `config.json` → `~/.engram/config.json`. Защити файл с keycard правами пользователя.
   Не печатай его содержимое. Для Codex используй его отдельный cwd-safe порядок
   в [setup](../plugin/engram/commands/setup.md#codex-mcp-launcher-and-project-root).
7. В обычном host проверь свежее обнаружение MCP и фактическую identity проекта,
   затем выполни сценарий R1 ниже. Доступность `/api/ready`, версия manifest и
   успешная установка plugin не заменяют чтение полезной памяти и fresh-session proof.

## Открыть существующую полезную память

1. Открой обычный установленный агент из настоящего каталога проекта на том же
   сервере, что и WebUI. Сохрани существующий `.engram-project` и Git common-dir
   `engram-project.json`. Имя, путь и remote URL не являются project ID.
2. Сверь подтверждённый `project_id` агента с каноническим проектом в селекторе
   Home или **Проекты**. При одинаковых названиях используй выданный qualifier и ID,
   не угадывай связь по имени. Ошибка каталога не означает успешный пустой проект.
3. Открой **Память**, выполни буквальный поиск известного решения в выбранном
   проекте и при необходимости перейди на следующую страницу. Не создавай новую
   тестовую память вместо проверки существующей полезной записи.
4. Открой найденную запись. Проверь полный доступный текст, источник и поля
   актуальности. Открой аудит; он не является полным текстом прежних версий.
   Используй **Назад** или Escape: проект, запрос, фильтры и страница сохраняются.
5. Для документа открой нужный path, требуемую версию, историю и сравнение.
   Недоступная версия не заменяется другой. Историю за пределами страницы читай
   продолжением; отказ чтения не означает пустую историю.
6. Если старая ссылка ведёт в **Неразрешённые проекты**, используй отдельный
   retained review. Проверяй reason, provenance и только предложенные `reader_kinds`.
   Metadata-only группа остаётся метаинформацией без content/history запроса;
   это не доказательство отсутствия исходных данных. Unsupported или недоступный
   reader остаётся явным отказом, без подстановки другого вида или проекта.
7. После штатного обновления проверь то же решение в свежей обычной сессии агента
   и WebUI. При неверной identity останови scoped-чтение и сохрани данные:
   не угадывай UUID/label, не удаляй marker/cache/client-data, не создавай новую БД
   или Vault key и не отключай auth. Диагностика identity приведена ниже.

Подробные маршруты, version/history и return-контракты:
[console README](../apps/operator-console/README.md#project-knowledge-journey-2026-10-11).


## Сессионный stdio для памяти

Явный запуск `engram --session-memory` обслуживает только память через уже
настроенный gRPC backend. Без этого аргумента остаётся полный mux-backed путь.
Режим не заменяет Workspace, Loom и автоматическую доставку контекста
через host hooks. Он не запускает общий daemon, reconciliation, индексаторы
или watchers и не меняет fencing существующего daemon.

Доступны только объявленные backend инструменты из списка `store`, `recall`,
`recall_memory`, `feedback`, `store_rule`, `list_rules`, `get_memory_brief`,
`query_principal_memory`, `continuity_slot`. Остальные вызовы отклоняются,
даже если клиент знает их имена. Авторизация и серверные флаги не меняются.

Запускай процесс из каталога выбранного проекта с переносимым UUID
в `.engram-project` либо Git common-dir cache. Используются штатные `ENGRAM_URL` / `ENGRAM_SERVER_URL`,
`ENGRAM_TOKEN`, `CLAUDE_SESSION_ID` и installation identity: явный
`ENGRAM_CLIENT_INSTANCE_ID` либо `client-instance-id` в `ENGRAM_DATA_DIR`
(по умолчанию `~/.engram`). Identity не выводится и не заменяется сессионным ID.
Для нового проекта используй `engram project init`; UUID не даёт прав доступа.
Локальный `initialize` не доказывает доступ к памяти: проверь `tools/list`
и настоящий `recall`. Неверная identity или авторизация остаётся отказом.

В обычном SQL `recall` (`action: "search"`) отсутствующий или пустой `project`
берётся из уже разрешённого контекста вызова. Явный `project` имеет приоритет
в handler; прежняя transport-проверка и канонизация сохраняются. Этот fallback
не переписывает аргументы и не меняет другие инструменты, поиск или privacy checks.
Если нет ни аргумента, ни проекта в контексте, сохраняется ответ
`{"memories":[],"count":0,"note":"project parameter is required for memory search"}`.
Он сообщает об отсутствии области вызова, а не доказывает отсутствие памяти.

Framing — один JSON-RPC 2.0 объект на строку, до 1 MiB на запрос и ответ.
ID — JSON string или number (поле обязательно для запроса), не `null`,
до 16 KiB; исходные байты ID сохраняются в ответе. Эквивалентные числовые
и экранированные строковые ID относятся к одному активному запросу.
Одновременно допускаются 8 запросов; новые запросы сверх лимита отклоняются.
`notifications/cancelled` отменяет только указанный запрос, кроме `initialize`.
Отмена, обработанная до принятия ответа на отправку, подавляет этот ответ.
После принятия ответа отмена уже не отзывает его. ID освобождается до появления
байтов ответа у клиента, поэтому после чтения ответа ID можно сразу использовать снова.
Заблокированный stdout не удерживает блокировку активных запросов.
EOF, SIGINT, SIGTERM и ошибка stdout отменяют запросы этого процесса и закрывают
его gRPC-соединения. Переподключение — новый процесс; другие клиенты не затрагиваются.
На Unix завершение по сигналу не ждёт EOF от клиента. Блокирующий ввод или вывод
унаследованных stdio может сохраняться до выхода собственного CLI-процесса;
не считай закрытие Go-обёртки доказательством прерывания syscall.Read/Write.
После отмены на завершение workers у process-owned stdout отводится не более
одной секунды; затем выход процесса прекращает оставшийся системный I/O.
gRPC-соединения закрываются перед выходом. Переданные вызывающим кодом streams
должны прерывать Read/Write при Close; для них транспорт ждёт завершения readers/workers.

Для установленной приёмки нужны поставленный бинарник, отдельное явное изменение
аргументов только выбранного MCP клиента и проверка двух разрешённых scopes.
Сохрани прежние аргументы для возврата к full mux. Успешный локальный fixture
не доказывает установленный путь, PostgreSQL concurrency или production history.
Первое разрешение Git locator может создать запись каталога и локально закрепить UUID;
его проверка требует разрешения на соответствующий backend и локальные записи.

## Обычная запись памяти без principal

В режиме `ENGRAM_AUTH_DISABLED=true` обычный вызов `store` не должен передавать
`agent_visibility`. Для уже подтверждённого канонического проекта используй,
например, `{"action":"create","content":"Решение по проекту","project":"<подтверждённый project_id>","privacy_scope":"project"}`.
`privacy_scope` управляет видимостью проекта/рабочей станции, когда включён
`ENGRAM_VNEXT_F_ENABLED`; его существующие ограничения сохраняются.

Оба явных значения `agent_visibility`, `private` и `shared`, требуют principal
из аутентифицированной серверной identity. `shared` — значение по умолчанию только
для записи с владельцем. Аргументы `principal`, `owner_principal` и `session_id`
не задают владельца. Без principal поле нужно опустить, а не подставлять identity
сессии. Если нужна именно principal-private память, отсутствие аутентифицированного
владельца остаётся отказом: не заменяй её обычной записью проекта. Отдельный
`privacy_scope="private"` требует непустой workstation identity от SourceClient
keycard; no-auth режим её не создаёт.


## Redaction Rules (EC-F5, EC-F9)

### Overview

Engram supports server-side PII and secret scrubbing before durable memory storage.
Redaction is configured via a server-side rule file and runs before the write-lint
phase. The original content is NOT preserved — redaction is destructive.

### Configuration

Set `ENGRAM_REDACTION_RULES_PATH` to the absolute path of the rule file:

```bash
export ENGRAM_REDACTION_RULES_PATH=/etc/engram/redaction-rules.json
```

If `ENGRAM_REDACTION_RULES_PATH` is **unset**, redaction is a **no-op** and the
system behaves exactly as v6.2.x. No configuration change is required to disable
redaction.

### Rule File Format

Rules are JSON objects with `id` (unique rule identifier), `pattern` (regex), and `replacement` fields:

```json
[
  {
    "id": "aws-access-key",
    "pattern": "AKIA[0-9A-Z]{16}",
    "replacement": "[REDACTED:aws-access-key]"
  },
  {
    "id": "github-pat",
    "pattern": "gh[pousr]_[A-Za-z0-9_]{36,}",
    "replacement": "[REDACTED:github-pat]"
  }
]
```

Rules are applied in order. Each match is replaced; unmatched content is preserved.

### Startup Verification

For a nonempty successfully loaded rule set, startup logs report its path and count:

```text
INFO redaction: rules loaded path=/etc/engram/redaction-rules.json rules=2
```

The current implementation does **not** log a SHA-256 checksum. Its admin-only
`redaction_rules_status`, when advertised, reports active/inactive, `rule_count`
and `restart_required`, not a load checksum or source path. Do not treat a file hash
computed after startup as proof of the loaded bytes. Preserve the intended rule file
and its checksum as deployment evidence, and verify startup with that unchanged file.

If the configured file is missing, unreadable, invalid JSON or has an invalid regex,
startup logs a warning and leaves the layer disabled; this is not active protection:

```text
WARN redaction: failed to load rules, layer disabled path=/etc/engram/redaction-rules.json error=...
```

### Restart Required for Rule Changes (EC-F9)

**Engram does NOT support hot-reload of redaction rules.**

Modifying the rule file on disk while the server is running has **no effect** on the
active rule set. This is intentional: hot-reload without an explicit signal creates
mid-write rule mismatch ambiguity (a write in flight when the rule set changes may
be partially redacted under one rule set and stored under another).

To apply updated rules:

1. Preserve the intended rule file and its checksum, then edit the rule file.
2. Restart the exact server through its existing deployment/service owner; preserve
   the database volume, configuration and Vault key. See [deployment](DEPLOYMENT.md).
3. Verify readiness and the startup `redaction: rules loaded` path/count with the
   intended file unchanged. If the admin status tool is available, confirm its loaded
   rule count; neither that count nor HTTP readiness proves the exact rule bytes.

The server entry point handles `SIGINT` and `SIGTERM` for shutdown. This source
does not implement a redaction `SIGHUP` reload or a build switch enabling one.
Do not send `SIGHUP` as a substitute for a supported server restart. A client
reconnect, browser reload or daemon restart does not reload server redaction rules.

### Full Redaction Rejection (EC-F5)

If a redaction rule matches and the resulting content is **empty** (the rule matches
the entire content), the write is **rejected** rather than storing an empty memory.
The error code is `content_fully_redacted`.

Example MCP tool response when full redaction occurs:

```json
{
  "error": "content_fully_redacted",
  "rule_id": "aws-access-key",
  "note": "The memory content was fully redacted by the configured rule. Revise the content or update the redaction rules, then retry."
}
```

The redaction attempt is logged to `audit_log` with:
- `action = 'redacted'`
- `rule_id` of the matched rule

The memory row is **not written** to the database.

### Audit Log

All redaction events (successful scrub or full-content rejection) are written to
`audit_log`:

| `action`    | Meaning                                                                                      |
|-------------|----------------------------------------------------------------------------------------------|
| `redacted`  | Content matched a rule. Write succeeds with scrubbed content (partial match), or is rejected with MCP error code `content_fully_redacted` when the entire content was stripped. |

---

## Snapshot Retention (T049)

Bulk operation snapshots (`bulk_op_snapshots` table) are auto-pruned during the
sleep cycle. Default retention: **30 days**.

Override via environment variable:

```bash
export ENGRAM_SNAPSHOT_RETENTION_DAYS=7   # 7-day retention
```

**Pinned snapshots** (rows with `pinned=true`) are exempt from pruning regardless
of age. Pin a snapshot via the `pin_snapshot` MCP tool or the admin dashboard.

---

## Rollback Conflicts (EC-F3)

If a memory or crystallization-candidate row no longer matches the immutable
post-operation state captured by the snapshot, rollback is **refused** atomically.
No rows are restored on conflict.

The conflict is reported as:

```json
{
  "error": "rollback_conflict",
  "conflict_ids": [42, 99],
  "conflict_refs": [{"entity":"memory","id":42}, {"entity":"candidate","id":99}],
  "snapshot_id": "snap-abc123"
}
```

`conflict_ids` preserves the legacy numeric list. `conflict_refs` identifies each
affected `memory` or `candidate` row, including mixed operations with equal IDs. To
resolve:

1. Review the conflicting rows.
2. If safe to proceed, the operator must manually reset the modified rows or create
   a new snapshot capturing the current state.
3. Retry the rollback with the new snapshot.

The audit log records `action='rollback_attempted_with_conflict'` for all refused
rollback attempts.

## Bring up and verify Code Workspace

This guide covers the current single-user `ENGRAM_AUTH_DISABLED=true` installation on its configured HTTP LAN origin. Do not use a hardcoded test address, login, browser read grant, HTTPS, or reverse proxy to make Workspace work. Auth-enabled browser access retains its separate identity and grant checks; that path is deferred here, not removed. A source-built fixture or HTTP selfcheck is not installed acceptance.

### Identify the installation before changing it

1. Record the configured server URL, server version and image digest or source commit, browser bundle, installed plugin and daemon path and version, parser executable and bundle digest, and embedding model and dimensions. Inspect installed components, not a different `engram` binary on `PATH`. Do not record secrets, database DSNs, or private source bodies.
2. At the configured server origin, check `/api/version`, `/api/selfcheck`, `/api/flags`, `/api/auth/me`, and `/api/ready`. Confirm that the effective auth mode is `auth_disabled=true`; do not infer it from a local environment file. A healthy HTTP process does not prove an indexed View or MCP connection.
3. Confirm that the installed server, console, and ordinary plugin actually contain this Workspace implementation. Before cutover, preserve useful old data and rollback inputs; after a verified v7 cutover, an old client is not the normal fallback for a wrong identity or missing tool. Source-candidate checks do not certify the new installation or its code-tool discovery.

### Preserve the installed client and Vault

The no-auth Workspace path needs no browser read grant, but the server still uses its Vault key to seal chooser references. Preserve the existing key in either auth mode, including for encrypted credentials and settings. Configuration prefers `ENGRAM_VAULT_KEY`, then its `ENGRAM_ENCRYPTION_KEY` alias; absent an inline key, an explicit `ENGRAM_ENCRYPTION_KEY_FILE` is used, otherwise the persistent default key file (`/data/vault.key` when `/data` exists, else the server data directory). Never remove it to trigger generation of a replacement. Replicas serving the same encrypted data and references need the same key; independent installations must not share credentials merely for convenience. Do not expose key material in diagnostics.

The current client selects a muxcore daemon namespace from a hash of its persistent `ENGRAM_CLIENT_INSTANCE_ID`. The plugin launcher stores an installation ID in its own plugin-data directory unless an explicit ID is configured. Preserve that directory and keep the ID stable across launches; separate installs must not share an explicit ID. A pre-upgrade v6.49.3 daemon may still run in the old global `engram` namespace while the new installation's daemon runs in its own namespace. The old marker does not identify the installation that owns it. Do not assume the new launcher adopted or stopped that daemon.

For a legacy daemon that needs retirement, first identify its live control status, owner, process image, installed client path, and connected clients against the old marker. If ownership cannot be proven, leave it running and report the coexistence; do not claim exclusive installation. Only with operator authority for that exact daemon, coordinate old client disconnection and use its existing supported graceful control to shut it down. Confirm its status is no longer live and reconnect the intended new installation. Never kill a PID, delete a marker or control socket, or use an unverified process match as a substitute for ownership proof. If that platform or installation has no supported graceful control, stop and escalate retirement to its owner rather than forcing it.

### Первый запуск: от меню до исходника

Эта инструкция относится к существующей установке с `ENGRAM_AUTH_DISABLED=true` на её настроенном HTTP LAN-адресе. Не создавай логин, browser grant, HTTPS-прокси или новую конфигурацию ради этого пути. Браузер не видит локальный Git. Регистрацию и индексирование выполняет установленный агент на компьютере с кодом; сервер остаётся владельцем Source, Checkout и View.

1. Открой главную страницу установленной консоли.
2. Выбери **Рабочее место** на Home или в основном меню.
3. Открой **Добавить / подключить репозиторий**. При пустом каталоге этот раздел открыт сразу. Сообщение «Вкладка подключена» означает только связь вкладки с сервером, не готовый индекс.
4. На компьютере с кодом открой обычный агент с установленным Engram из корня нужной Git-копии. Проверь, что агент использует тот же сервер, что и консоль. Не подменяй установленный клиент случайным `engram` из `PATH`.
5. В консоли выбери **Новый репозиторий** и введи читаемое название. Путь и UUID не нужны.
6. Нажми **Копировать задачу** и передай текст агенту. Если браузер на HTTP не разрешает буфер обмена, нажми **Выделить задачу**, затем скопируй текст обычной командой клавиатуры.
7. Дождись ответа агента. Задача использует существующий native MCP: `codebase_context register` с `source_label`, без `locator`. Native host определяет настоящий Git-корень текущего каталога. Сервер выдаёт отдельный checkout-bound `context_handle`; до первого опубликованного View поле `context` остаётся `null`.
8. В той же сессии агент вызывает `codebase_status`, `codebase_index` и `codebase_status` с `after_barrier.token` из `run_id` и `wait_ms:60000`. `wait_ms` задаёт максимальную готовность ждать, но один вызов daemon ждёт не более 250 мс. `running`/`timed_out` означает ожидание, не ошибку индексирования и не готовый снимок. Для обновления состояния используй тот же `context_handle` и token, не запускай новый индекс и не повторяй запрос бесконечно. `started`, `already_running` и принятый index intent тоже означают работу, не готовый снимок. Если работа ещё не завершилась, агент сообщает состояние; при ошибке сообщает ошибку, а не объявляет успех.
9. Вернись в консоль и нажми **Подключение выполнено — обновить каталог**. Проверь реальные подписи **Репозиторий** и **Рабочая копия** в серверном каталоге. Копирование задачи само по себе не создаёт регистрацию.
10. Если копия уже подключена, но не имеет View, выбери её. При свежем доступном daemon target появится **Индексировать рабочую копию**. Если кнопки нет, повтори задачу в локальном агенте этой копии, затем обнови каталог. Не создавай разрешение браузера и не отправляй файловый путь в HTTP API.
11. После публикации выбери **Снимок индекса**, проверь ревизию и время, затем нажми **Закрепить выбранный View**. Новый снимок не подменяет прежний автоматически.
12. Проверь свежесть, число эмбеддингов кода, покрытие, состояние задания и причину ошибки. Нулевое или частичное покрытие не означает готовый смысловой поиск.
13. Введи запрос о поведении без известного имени функции. Например, для подходящего корпуса: «Как выбирается рабочая копия для поиска кода?». Нажми **Искать**.
14. Проверь **Режим ответа**, **Покрытие векторами**, структурное покрытие, неподдерживаемые файлы, неразрешённые ссылки и причины деградации. `lexical` — полезный лексический ответ, но не доказательство смыслового поиска. Для концептуальной проверки нужен `hybrid` с реальным вкладом `vector` в найденный результат. Режим и полный индекс сами по себе не подтверждают качество ответа.
15. У результата нажми **Исследовать граф**. Выбери входящие или исходящие связи. Чтобы пройти связи с клавиатуры без графического представления, выбери список.
16. Выбери автоматически извлечённую связь. Проверь её тип, вид доказательства и precision. Открой исходник ссылки, если он выдан. Entity-level, heuristic и ambiguous — ограничения, не точное доказательство вызова.
17. Нажми **Прочитать исходник** у результата или действие чтения в панели связи. Файл, строки, байты и digest относятся к тому же закреплённому View. Сегодняшний файл на диске не заменяет сохранённый фрагмент этого View.

Каталог вкладки не является всей базой данных. Пустой ответ не доказывает отсутствие кода или исторических записей у других владельцев. При ошибке обновления подтверждённый View и уже выданные результаты сохраняются; повтори обновление. При отказе доступа или несовпадении контекста сервер может снять закрепление. Auth-enabled установка сохраняет свои проверки пользователя и grant; это отдельный путь, не способ обойти текущую авторизацию.

### Исторический hotfix v6.52.1

The following describes only the v6.52.1 hotfix before the portable-UUID cutover, not the current setup or project-identity authority. Use the portable UUID flow below for this candidate; preserve old markers and anchors as migration and provenance inputs, not instructions to restore retired V2/V3 runtime fallbacks.

An older regular `.engram-project` containing exactly `id` (six lowercase hexadecimal characters) and a valid `name` also uses the existing V2 adapter in a Git workspace, even when the marker is untracked. Field order does not matter. The historical ID is validated as a format marker, not used as project authority. A configured origin retains its existing remote/selected-path scope. Without an origin, the selected directory must already contain a regular, valid `.engram-project-v2.json`: its existing anchor and explicit sharing flag are forwarded with the established absolute-path-derived selector and legacy ID. A local `shared:false` anchor retains the server's path-derived clone isolation. Missing, malformed, symlinked or changed anchors refuse; this route never calls the anchor creator. Marker/index/anchor bytes are not rewritten or staged, and no V3 registration or historical-memory migration is performed. Non-Git directories, unknown/duplicate historical-marker fields and conflicting nearer anchors still refuse. After the normal plugin update, reconnect `engram:engram` and read an existing project memory before claiming recovery; do not replace either marker to clear a connection error.

The exact Git index is streamed through a fixed 32 KiB buffer under the existing five-second identity deadline. The open file, final metadata and current pathname must identify the same regular file; replacement, mutation, cancellation and symlinks refuse stale scope. JSON markers, V2 anchors and config files retain their bounded reads; the index is not loaded as one large allocation.

Git environment names are compared case-insensitively only on Windows; Unix keeps its native case-sensitive names. Config-file symlinks are supported only for identified Git config dependencies, not as a general relaxation for index, HEAD or repository markers. Their fingerprints bind each followed link and its target text, the resolved target's file identity, metadata and bounded contents, including an absent target's later creation. Link traversal is limited to 40 followed links and config reads remain limited to 1 MiB. Replacement, retargeting, loops, unsupported targets or changes during inspection refuse stale scope; the selected marker and directory guards are unchanged.

### Переносимый UUID проекта

Обычные MCP-вызовы и hooks используют Go resolver и один `project_id` UUID.
Имя, каталог и хеш remote не являются идентификатором проекта. Для явно нового
проекта выполни из выбранного каталога:

```bash
engram project init --name "Example Workspace"
engram project resolve --root <actual-directory> --json
```

`init` работает локально, в том числе без Git, и выводит JSON с `project_id`
и необязательным `name`. Он не добавляет файл в Git и не создаёт серверную
регистрацию. Если UUID уже закреплён, команда возвращает его, а не создаёт другой.
Для переноса проекта в clone сохрани `.engram-project`; добавление и commit
этого файла выполняй явно.

`resolve` читает фактический каталог вызова. Вложенная `.engram-project` задаёт
явную границу; подкаталог сам по себе не создаёт новый проект. Git-копии одного
репозитория используют общий UUID cache `engram-project.json` в Git common-dir.
Если UUID ещё нет, настроенный сервер может разрешить нормализованный primary
remote через `/api/projects/bootstrap`; результат закрепляется в cache и marker.
Это не перенос исторической памяти. Неверный marker или конфликт UUID остаётся
ошибкой, а не поводом переписать существующую identity.

For a diagnostic matching the launcher's configuration, use the **installed plugin
path reported by the host**, not an arbitrary PATH binary:

```text
node <installed-plugin-path>/scripts/run-engram.js project resolve --root <actual-project-directory> --json
```

This is not guaranteed read-only: without a pinned ID, resolution can ask
`/api/projects/bootstrap` for the primary-remote binding and persist the returned ID.
An ambiguous or unmigrated binding needs its owner; do not invent a UUID or call
retired `project_identity.register_v3`. The host's actual callback cwd matters;
a shell `cd` alone does not change a running host's project context.

`tools/list` не требует проекта. Отсутствующий или конфликтующий контекст
отклоняет только операцию, которой нужен проект; используй JSON-код ошибки
`resolve` для диагностики. `project_identity.register_v3` больше не является
шагом обычной настройки.

`issues list/get` не требуют текущего проекта: `project` и `source_project`
задают фильтры чтения. Изменения Issues сохраняют требования проекта и actor.
`vault status` также не требует проекта. Для `store` проект нужен по умолчанию;
явный `scope="global"` отменяет его выбор. Для `get/delete` пустой selector
выбирает global, непустой `project` или `scope="project"` требует текущего
проекта, а явный `scope="global"` очищает project. `vault list` выбирает namespace
только по `project`: без него читает global, с ним — проект и global; один
`scope="global"` непустой selector не отменяет. Некорректные action, scope,
типы проектных selectors или JSON не получают исключение из project guard.
Авторизация и проверка canonical UUID остаются обязательными.

Старый memory relay gateway, listener/runtime и test-only advisor prewarm удалены.
Не включай их через `hap_01b` или `ENGRAM_HAP_01B_*`: launcher игнорирует старую
конфигурацию и удаляет такие переменные из окружения child. Обычные server URL
и keycard остаются отдельными credentials доступа. Исторические записи,
миграционные readers и проверки авторизации не отменяются этим cutover.

#### Комментарии к сохранённым Issues

Миграция `189_issue_project_write_scope` сохраняет project guard на `INSERT issues`
и на явных `UPDATE source_project/target_project`. Обновление только метаданных,
включая `updated_at` при комментарии, не разрешает старые selectors заново и
не меняет историческую project-привязку. Поэтому автор с canonical UUID может
добавить обычный комментарий к сохранённому issue без ручного adoption его
старых source/target selectors.

Guard `issue_comments.author_project`, авторизация и проверки остальных
project-ролей не меняются. Новые или явно записываемые project selectors без
подтверждённого membership, а также применимые conflicting redirects получают
прежний отказ. Ошибка вставки комментария или обновления родителя откатывает
обе записи в одной транзакции.

Миграция меняет один trigger, а не строки или memberships. Она выполняется
обычной цепочкой запуска после `188`; production rollout требует отдельного
допуска. DDL требует блокировку таблицы `issues`. Rollback миграции запрещён,
чтобы не вернуть отказ комментариев; для последующего ремонта нужен forward
путь. Старые sealed consolidation plans привязаны к прежней схеме: после
изменения trigger получи свежий Census/Preflight, не переиспользуй старый plan.

### Первая запись документа

Первая запись versioned document с canonical UUID регистрирует тот же проект в каталоге внутри транзакции документа. Ошибка вставки откатывает и регистрацию; обычное чтение проект не создаёт. Удалённый или merged проект не восстанавливается автоматически. Global input не создаёт project UUID, а historical selectors остаются под существующим SQL guard: доказанный redirect сохраняется, unresolved input получает отказ. Сохранённые HTTP/store regressions в `.agent/runs/pr568-review/document-first-write/green-result.json` дали 12 PASS без fail/skip с обычной авторизацией и включённым SQL guard. Это focused source proof, не browser/installed acceptance.

### Проверка миграции проектов на восстановленной копии

Эти операции не выполняют production cutover. `ProjectConsolidationStore` меняет только явно настроенную отдельную восстановленную БД. Авторизация чтения данных, приватность записей и правила Vault остаются вне определения Project ID. Census/Preflight ниже общие; для native restore используй maintenance CLI, а не custom HTTP backup/VerifyBackup.

1. Сохрани точную версию исходников и используй уже существующий Vault key в приватном хранилище. `OpenExistingVault` не создаёт заменяющий ключ. Не выводи ключ, credentials или тела записей в отчёт.
2. Получи `GET /api/projects/consolidation/census?source_version=<точная версия исходников>`. Ответ содержит фактические таблицы, counts, непрозрачные ссылки строк, schema/snapshot identity и незакрытые роли. `source_version` — привязка вызывающего; сам HTTP запрос не доказывает Git HEAD или установку.
3. Передай `POST /api/projects/consolidation/preflight` с `census_reference`, `census_fingerprint` и `evidence`. Каждый элемент evidence задаёт `reference`, `kind`, `group`, необязательный retained `project_id` и точные `rows`. Допустимые доказательства: `portable_marker`, `common_dir_binding`, `established_alias`, `session_provenance`, `source_provenance`. Ссылки ведут к сохранённому структурному доказательству. Это не разрешение объединять по имени, basename, hash-prefix, похожему содержимому или upstream. API не обследует чужую файловую систему вместо автора доказательства. Группа без UUID получает один детерминированно закреплённый UUID; конфликт сохраняется как unresolved membership.
   Sealed plan содержит один Census и исходные Evidence, без второго массива Mapping. После authentication и bounded gzip decode сервер восстанавливает Mapping тем же deterministic `buildMapping` и проверяет прежний fingerprint до effects. Structural metadata ограничена 56 MiB как при JSON encoding перед gzip, так и после распаковки; превышение получает `PROJECT_ADOPTION_METADATA_CAPACITY_EXCEEDED`, и producer не выдаёт непригодный reference. Не добавляй server registry или другую identity ради уменьшения token.

#### Native maintenance CLI

`engram-server consolidate-projects` — подкоманда существующего server binary, не `engram` и не отдельный сервис. Она выходит до обычного service startup: не запускает listener, фоновые jobs или автоматический migration chain. Сначала восстанови разрешённый backup в отдельную изолированную target БД существующим recovery-путём `scripts/recovery/verify-postgres-backup-restore.ps1`; CLI не выполняет `pg_dump`/`pg_restore` и не создаёт БД. Source для rehearsal также должна быть разрешённой изолированной копией. После отдельно разрешённого schema DDL получи свежий Census/Preflight; data-only rebase не заменяет sealed plan.

| Аргумент | Фактическое поведение |
| --- | --- |
| `--source-dsn-file` | Обязательный файл с DSN source. Соединение использует `default_transaction_read_only=on`. |
| `--target-dsn-file` | Обязательный файл с DSN отдельной восстановленной target. Разные строки DSN не доказывают разные БД: domain проверяет их identity. |
| `--plan-file` | Обязательный JSON-файл обычного Preflight с непустыми `plan_reference` и `plan_fingerprint`; не plaintext mapping и не receipt о прежнем rebase. |
| `--plan-id` | Ожидаемый настоящий `plan_id` из успешного dry-run. При `--commit` обязателен; если передан при dry-run, тоже должен совпасть. Не создавай новый UUID. |
| `--commit` | По умолчанию `false`: target-транзакция откатывается. Только явный `--commit` выбирает Apply. Отдельного `--dry-run` нет. |

Каждый DSN-файл содержит только строку подключения; крайние пробелы и перевод строки удаляются. Предел файла — 64 KiB. Plan-файл содержит один JSON-объект, без второго JSON или хвоста; предел — 128 MiB, `plan_reference` — до 64 MiB, fingerprint — до 256 байт. Это пределы локального CLI, не HTTP. Все три файла храни вне Git с доступом только у исполнителя; CLI проверяет regular file и размер, но не настраивает и не подтверждает ACL. Не передавай DSN или секреты в аргументах, отчётах и логах.

Физическая identity БД включает имя из `current_database()`, `system_identifier` кластера из `pg_control_system()` и фактический положительный OID БД из `pg_database`. Domain читает эти значения по позициям SQL-результата; пустое имя, отсутствующий system identifier, неположительный OID или ошибка чтения дают `PROJECT_ADOPTION_SOURCE_IDENTITY_UNAVAILABLE`, а не запасную identity. Разные строки DSN могут указывать на одну физическую БД; её identity от написания DSN не зависит. План, запечатанный с прежним ошибочным OID=0, получает `PROJECT_ADOPTION_STALE_PLAN`: получи свежий Census/Preflight. Старые tokens остаются входами миграции и истории; не перепривязывай вручную token, identity hash или fingerprint ради принятия старого плана.

Сохрани тот же существующий Vault key. `OpenExistingVault` использует настроенный `ENGRAM_ENCRYPTION_KEY`, затем `ENGRAM_ENCRYPTION_KEY_FILE`, иначе существующий default key file; новый ключ не создаётся. Предпочитай уже настроенный приватный файл, не подставляй новый ключ ради запуска. CLI использует прямой доступ к БД и Vault, а не HTTP token; локальный доступ не отменяет разрешение на выбранные данные и target effects.

1. На точном собранном binary проверь имя подкоманды через `engram-server consolidate-projects --help`. Его фактическая строка справки:

   ```text
   engram-server consolidate-projects --source-dsn-file S --target-dsn-file T --plan-file P [--plan-id ID] [--commit]
   ```

2. Только на допущенных изолированных source/target выполни default dry-run, подставив пути к существующим приватным файлам:

   ```bash
   engram-server consolidate-projects --source-dsn-file "/private/source.dsn" --target-dsn-file "/private/target.dsn" --plan-file "/private/preflight.json"
   ```

   CLI открывает обычный sealed plan, восстанавливает Mapping из Census/Evidence и проверяет fingerprint и настоящий state ID. Общий executor проверяет свежесть source и отдельную identity target. Native-путь сравнивает восстановленные строки по native keys и digest, schema/objects и sequences, удерживает эксклюзивные блокировки всех таблиц target, проверяет ожидаемый результат и неизменность source. Это не сравнение одних counts. Dry-run выполняет domain transaction с откатом; не считай его бесплатным read-only запросом к target.
3. Сохрани возвращённые `plan_id` и `target_identity`; проверь `dry_run:true`. Только после успешного dry-run и отдельного допуска Apply на эту изолированную target повтори ту же команду с `--plan-id "<plan_id из dry-run>" --commit`. JSON успеха в stdout также содержит `replay`, `operation_id`, `receipt_reference`, `after_snapshot_identity` и `occurrences_before`/`occurrences_after`. Последние считают occurrences ролей, не все строки owner corpus. Выход 0 сам по себе не доказывает полную сохранность данных.
4. В пределах того же допуска проверь повтор с теми же plan, `--plan-id` и `--commit`. Replay использует существующий state ID как idempotency key, проверяет receipt, target и свежесть source; ожидается `replay:true`, а не второй Apply. После новых записей получи свежий Census/Preflight и снова проверь restored-target equivalence; не исправляй вручную membership или receipt ради повтора.

`--help` возвращает 0 без доступа к БД. Ошибки возвращают 1 и JSON `{"code":"…"}` в stderr; Vault logging может идти отдельно. Основные публичные коды:

| Код | Причина / действие |
| --- | --- |
| `PROJECT_ADOPTION_CLI_ARGUMENTS_INVALID` / `PROJECT_ADOPTION_PLAN_ID_REQUIRED` | Неверные аргументы или `--commit` без ожидаемого ID. |
| `PROJECT_ADOPTION_PLAN_FILE_INVALID` / `PROJECT_ADOPTION_PRIVATE_INPUT_UNAVAILABLE` | Plan-файл либо DSN-файл недоступен или не проходит проверку формата/размера. |
| `PROJECT_ADOPTION_EXISTING_VAULT_REQUIRED` / `PROJECT_ADOPTION_CONNECTION_INVALID` | Существующий ключ не открыт либо DSN не разобран; последующая ошибка соединения может получить общий `PROJECT_ADOPTION_NATIVE_OPERATION_FAILED`. |
| `PROJECT_ADOPTION_SOURCE_IDENTITY_UNAVAILABLE` | Физическая identity БД не прочитана полностью; операция не продолжается с пустыми или нулевыми значениями. |
| `PROJECT_ADOPTION_TARGET_IS_SOURCE` | Target — та же физическая БД, что и source, даже при другом тексте DSN. Выбери отдельную разрешённую восстановленную БД. |
| `PROJECT_ADOPTION_STALE_PLAN` | Неверен ожидаемый ID либо устарели план/данные, в том числе identity старого плана с OID=0. Сверь `--plan-id` с dry-run; для устаревшего плана получи свежий Census/Preflight. Не обходи проверку. |
| `PROJECT_ADOPTION_CONSERVATION_FAILED` / `PROJECT_ADOPTION_REPLAY_CONFLICT` / `PROJECT_ADOPTION_SECRET_UNREADABLE` | Сохранность, receipt повтора или чтение секрета не подтверждены; Apply не принимается. |
| `PROJECT_ADOPTION_CANCELLED` / `PROJECT_ADOPTION_NATIVE_OPERATION_FAILED` | Отмена или иная ошибка. Ошибки до commit откатывают target-транзакцию; ошибка вывода `PROJECT_ADOPTION_OUTPUT_FAILED` после commit не доказывает откат. |

Для CLI, однократно собранного из исходников `597a296e`, постоянный real-PG тест физической identity прошёл: 1 PASS, 0 skips, 0 FAIL. Реальный CLI отверг старый план с OID=0 как `PROJECT_ADOPTION_STALE_PLAN` и другой текст DSN той же source как `PROJECT_ADOPTION_TARGET_IS_SOURCE`. Каждый отказ вернул exit 1, один JSON-код ошибки и отдельную информационную запись Vault. Полная сверка до/после всех 90 таблиц каждой БД (89 793 строки source, 181 350 строк target) не выявила различий PK, содержимого по SHA-256 и xmin; все 48 ciphertext-строк credentials на каждой стороне сохранены. Это проверка неизменности при двух отказах, не новый Apply или replay. Ранее также подтверждены `--help`, отказ без аргументов, 4 native и 3 CLI pure-теста без skips.

Для schema/objects, 40 sequences, replay и чтения 48 секретов через Go `CredentialStore` используются результаты прежнего native rehearsal, а не повторное выполнение на `597a296e`: 44 unresolved project credentials прочитаны по точному native ID и проверенной unresolved membership, четыре global — обычным global reader. Эти результаты не доказывают API/UI или новый полный restore → dry-run → Apply → replay на исправленной identity. Сохрани owner rows, native IDs/версии, provenance/references, constraints/sequences и независимые Checkout/View; basename или похожее имя не дают права назначить проект.

**Откат после ошибки, возникшей уже после записи в target, API/UI на мигрированных данных, установка и release ещё не подтверждены.** Успех сборки, тестов и двух отказов не означает приёмку всего продукта или production cutover.

Production переключение требует актуальной инвентаризации записей после snapshot и контролируемой остановки writers (quiescence), проверенного restorable backup, изолированного rehearsal и явного разрешения владельца на точный cutover effect. Прежние 9 добавлений/128 изменений/0 удалений относятся к двум историческим снимкам, не к текущему production delta. Здесь нет разрешения на live SQL, destructive cutover или установку. При подтверждённом cutover native-путь заменяет custom transport writers/entrypoints после проверки consumers; это не две постоянные системы миграции. Старый HTTP путь пока не объявлен удалённым; нужные migration/rollback readers и owner data сохраняются.

#### Существующий HTTP transport до проверенного cutover

Следующие шаги описывают ещё существующий custom transport и его ограничения, а не обязательный backup/VerifyBackup перед native CLI. Не выполняй оба пути как две последовательные миграции.

4. Получи `POST /api/projects/consolidation/backup` с `plan_reference` и `plan_fingerprint`. Ответ — `application/octet-stream`, не JSON/base64. Формат `engram.owner-stream/2`: четырёхбайтовая big-endian длина перед каждым Vault AES-GCM ciphertext. Перед encryption frame plaintext сжимается gzip; reader сначала проверяет authentication, затем распаковывает не более 524 260 байт. Plaintext содержит ordinal, digest предыдущего ciphertext, привязку manifest, native table/PK, исходный xmin и не более 256 KiB байтов строки. xmin обозначает исходную транзакцию, не бизнес-версию записи; бизнес-версии сохраняются в полном native row. Ciphertext frame не превышает 512 KiB. Строка может занимать несколько frames, включая разрыв UTF-8 между chunks. Начальный и конечный аутентифицированные manifest связывают source/schema/snapshot, классификацию, counts и sequences. Старый unpublished frame codec не принимается. Census хранит structural metadata, не тела корпуса. До сохранения измерь поток в счётчик: memory bound не доказывает disk peak. При прерывании HTTP после начала ответа сервер выставляет trailer `X-Engram-Backup-Error`; такой экспорт нельзя принимать как backup.
   Новый Census и export объявляют все persistent tables текущей схемы, включая пустые `project_consolidation_memberships`, `project_consolidation_redirects`, `project_consolidation_operations`, `project_consolidation_backups` и `project_consolidation_frames`. Это retained migration state, а не новые project occurrences или global authority. Свежесть учитывает PK/xmin каждой строки и все sequences; изменение recovery metadata тоже делает прежний plan устаревшим.
5. Подготовь отдельную пустую БД с той же полной схемой либо используй точную непустую восстановленную копию, прошедшую полное сравнение с этим потоком. Задай приватный DSN через `ENGRAM_CONSOLIDATION_RESTORE_DSN`; адрес не принимается из HTTP body. `POST /api/projects/consolidation/verify-backup` принимает строго ordered multipart: `plan_reference`, `plan_fingerprint`, затем бинарный `sealed_export`, без дополнительных частей. Поток можно передать напрямую без второго файла. Проверка сохраняет encrypted frames в `project_consolidation_frames`, проверяет chain/final manifest, уникальные native keys и исходные xmin, восстанавливает только пустую отдельную БД либо сравнивает полный native row с непустой копией. Любое отклонение откатывает импорт. Restore/apply временно отключают только собственный consolidation trigger; затем drain deferred constraints и восстанавливают точные O/R/A/D modes до conservation и receipt. Независимые triggers остаются включены. Sequence RESTART создаёт транзакционное хранилище до setval, сохраняя value/is_called при откате. Обычный `CredentialStore.Get` и Vault decrypt проверяют каждый активный секрет в его точном namespace, без выдачи plaintext. Повтор не дублирует frames и не перезаписывает valuable inputs. Резерв должен отдельно покрывать BYTEA/TOAST, indexes, WAL/temp и metadata: encrypted original остаётся обязательным входом, stream не заменяет его автоматически.
   Общий для native и stream schema fingerprint связывает columns, constraints, indexes, routines, relation owners и отсортированные ACL, RLS enable/force, определения и O/R/A/D modes triggers, policy roles/command/permissive/USING/WITH CHECK, extension/enum/domain attributes. Сравниваются имена объектов и ролей, не raw OID. Проверка выполняется до envelope/import и Apply, включая replay, и после восстановления временного DDL. `row_security=off` требует полного чтения или получает отказ; права не обходятся. Не обходи несовпадение fingerprint: migration 186 должна быть отдельно разрешена на обеих изолированных БД до нового Census/plan. Сохрани original/map/key и докажи owner-row conservation до и после DDL с явным migration-metadata delta. `VerifyBackup` не вставляет таблицу `migrations`: её точные строки должны быть заранее подготовлены на target. Не запускай source `NewStore` или весь migration chain автоматически.
   Под target lock транзакционный `pg_temp` ledger связывает точные keys/digests прежних local recovery rows, аутентифицированные исторические строки, текущий backup ID с ordered frame chain и только запланированные additions. Совпадающий historical key требует полного совпадения строки. Исключение целой таблицы, совпадение plan prefix или произвольное число новых projects не заменяют conservation. Ciphertext остаётся в PostgreSQL; в Go не копируется весь архив.
   Genuine старый `engram.owner-stream/2`, который ещё не объявлял пять recovery tables, читается только вместе с исходным аутентифицированным plan и доступной неизменной исходной БД. Рецепт выбирается по точному Census/manifest inventory до проверок, не как fallback после ошибки. Для declared baseline rows сохраняются прежние Columns+Definitions, revision и fingerprint; текущие effective objects source/target всё равно должны совпасть до и после restore. Проверяются все объявленные rows/sequences/secrets. Это не восстанавливает историю, которой в старом потоке не было, и не доказывает старое состояние policies, отсутствующее в архиве. Такой partial proof никогда не разрешает новый Preflight/Backup/DryRun/Apply или replay Apply: нужен свежий полный Census/plan/export. Исходные bytes, plan и key не переписываются. Без исходной БД или при её изменении этот reader не является самостоятельным recovery path.
   Только точный `POST /api/projects/consolidation/verify-backup` принимает `multipart/form-data` с boundary вместо обычного JSON. Предел всего HTTP body, включая части и заголовки multipart, — 2 GiB (2 147 483 648 байт). `http.MaxBytesReader` ограничивает чтение, включая chunked requests без `Content-Length`; заявленный больший размер получает 413 без чтения тела. Переполнение во время чтения также получает 413. Поток закрывается при успехе, ошибке, отказе auth/rate и cancellation. Пределы `plan_reference` (32 MiB), `plan_fingerprint` (256 байт), ciphertext frame (512 KiB) и обычного JSON body (10 MiB) не увеличены. Backup, VerifyBackup, DryRun и Apply получают server-controlled deadline не более 10 min, сокращённый более ранним caller deadline. Upload read deadline и stream write deadline задаются через `ResponseController`; cancellation прерывает I/O и DB transaction. Census, Preflight и остальные routes сохраняют 30 s. Глобальные server timeouts, authentication, CORS, rate и trace policy не ослаблены.
   Для Backup, VerifyBackup, DryRun и Apply сервер проверяет data-admin до чтения тела и назначения расширенных context/socket deadlines. Клиент без admin-прав получает отказ без чтения тела и без продления обычного срока. Более ранний caller deadline остаётся обязательным.
   2 GiB — transport ceiling, не разрешение выделить память, файл или место в БД. Read-only measurement текущего owner baseline подтвердило compact plan 8 629 646 байт, compressed stream 701 411 892 байт и ordered multipart 710 042 002 байта; authentication, chain, final manifest и counts проверены без сохранения frames. Это measurement-only overlay, не доказательство production codec, HTTP duration или target restore. Перед effects отдельно допусти BYTEA/TOAST, indexes, WAL/temp и metadata. HTTP regressions используют зарегистрированный router и bounded reader; verifier seam не заменяет data-admin guard и не доказывает restore/crypto.
6. Вызови `POST /api/projects/consolidation/dry-run`, затем `/apply` с plan, `backup_reference` и UUID `idempotency_key`. Оба используют один настоящий executor; dry-run откатывает изменения. Executor держит короткую блокировку таблиц, проверяет свежесть, сохраняет все неидентификационные поля и отказывает при FK/unique collision. Нет `ON CONFLICT DO NOTHING`, удаления или смысловой дедупликации.
   Совместимая retained membership определяется по текущему native object/field occurrence, а не только по новому HMAC адреса. Сохраняются её исходные Reference, SourceRowKey, NativeVersion, SourceSnapshot, fingerprint и reasons; меняется только разрешённый target/текущий RowKey. При изменении адресующего PK обновляются также прежние memberships этого объекта. Для keyless rows сохраняются digest/multiplicity и точный восстановленный physical key; изменение такого addressing не разрешено. Реальное назначение другому проекту получает `CONFLICTING_MEMBERSHIP`, не overwrite.
   Verify записывает текущую backup metadata внутри той же транзакции. Apply вставляет operation до расчёта полного final revision и завершает receipt той же транзакцией; receipt-only UPDATE сохраняет transaction xmin. После этого нет других persistent effects. Обычный повтор сверяет полный revision; новое изменение строк, recovery metadata, sequences или effective schema получает отказ.
7. Проверь повтор того же apply. После новых записей прежняя карта получает `STALE_PLAN`; новые данные не объявляются мигрированными. Для новой карты нужен свежий census/backup. В смешанном legacy bucket redirect допустим только для доказанной конкретной сессии: `engram_consolidation_scope` и `ResolveLegacy` возвращают её сохранённый UUID. Пустая или неизвестная сессия получает `PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP`, без возврата старого selector и legacy registration. Trigger использует сессии записываемой строки для scalar и каждого ordered array occurrence. Пустая global scope и canonical UUID сохраняются; неизвестный historical selector без доказанного redirect получает отказ. Если global и exact session redirects конфликтуют, SQL и `ResolveLegacy` отказывают одинаково. После перевода consumers удали временный bridge в отдельном согласованном cutover; историческую принадлежность сохрани.
8. Для unresolved content используй только `ConsolidationMembership.RowKey` из `UnresolvedMembership(reference, kind)`, затем обычный reader с его privacy checks. `SourceRowKey`, `NativeVersion` и `SourceSnapshot` сохраняют исходное происхождение, даже когда адресующий PK меняется; native whole-row путь — `[""]`. `kind=native` возвращает точную retained table/PK metadata для отдельно авторизованных credential/document readers, не секрет или новую auth capability. Не передавай opaque reference как `project` и не используй старый selector как fallback. `CanonicalMembership(project_id, table)` связывает retained history и Sources с тем же UUID, не меняя Checkout/View/embedding IDs. История объекта читается только после подтверждения его native ID в этой membership.
   Страница retained content имеет DTO `{ reference, kind, items, total, next_cursor }`; обязательного поля `provenance` на уровне страницы нет. UI сохраняет происхождение из самих записей и истории, исходные ID, причину группы, reference и исходную ссылку. Проверки reference/kind, выбранного history ID, readable content, размера страницы и continuation остаются обязательными. Известная unresolved группа не становится canonical project или разрешением на запись; неизвестный selector либо ошибка каталога/resolver по-прежнему блокирует обычные project-запросы.
   Unresolved credential читается через существующий authenticated `GET /api/vault/credentials/{name}?id=<native-ID>&unresolved_reference=<opaque-reference>`. Не добавляй `project` или `scope` к этому запросу. Reader проверяет `UnresolvedMembership(reference,"credential")` и active native ID в одной read-only snapshot, сверяет имя и сохраняет обычные Vault authorization, key-fingerprint и decrypt checks. Reference не даёт разрешение чтения. Ответ содержит `project_id:null` и unresolved reference; старый namespace не становится canonical ID. Четыре global credentials используют прежний `scope=global` reader. Metadata/errors/logs не содержат secret plaintext.
   Оба варианта списка Vault сохраняют native ID и проверенную retained reference, даже если исходный namespace похож на UUID. В трёх интерфейсах Vault/Secrets такие строки помечены `Retained #<ID> · read-only`: Reveal использует только ID/reference, Delete недоступен. Строки без доказанной принадлежности или с конфликтом остаются видимыми как `Unavailable #<ID>` с исходным namespace в `Origin`; Reveal и Delete для них недоступны. Исходный namespace не становится canonical selector или global scope. Недоступная строка не блокирует чтение корректных соседних строк.
   Native FK memberships для `content_chunks`, `knowledge_edges`, `citation_log`, `promotion_log` и `rule_arbiter_evaluations` входят в census. Их IDs, embeddings и исторические поля не переписываются. Принадлежность наследуется только от согласованных FK parents; отсутствие доказательства остаётся unresolved.
   `ListUnresolved(ctx)` возвращает полный набор сохранённых `reference`, `name` и `reason`, проверяя форму membership envelope и его opaque-привязку. Каталог не выполняет полный аудит native targets и не утверждает, что каждый payload существует, доступен или проверен. Отсутствующий либо изменившийся native target не скрывает его unresolved metadata и остальные корректные проекты. Повреждённый envelope или подменённый opaque reference по-прежнему отклоняет каталог, а не превращается в успешный неполный список.
   Обязательный `reader_kinds` содержит только виды существующего обзора, подтверждённые таблицами retained membership: `memory` для `memories`, `document` для `versioned_documents`, `issue` для `issues`, `code` для `sources`. В смешанной группе сохраняются все такие виды без повторов. Это metadata, не проверка payload, доступности или прав. Пустой массив `[]` означает, что для memberships этой группы нет reader в обзоре; в частности, группа только из `projects` остаётся видимой как метаинформация. UI не отправляет для неё запрос памяти или истории и не объявляет успешный пустой content. Это не доказательство отсутствия исходных данных и не разрешение на native/Vault чтение. При переходе к другой группе UI выбирает только объявленный вид, предпочитая память, если она указана. Явно запрошенный, но не объявленный вид остаётся видимо недоступным; подстановка другого вида не скрывает исходную ссылку.
   При выбранном обращении `UnresolvedMembership(reference, kind)` сохраняет полную проверку native key, единственного совпадения и selector перед обычными privacy/access checks. Отсутствующий либо изменившийся target получает отказ, не успешный пустой content. `LegacyUnresolved(ctx, selector)` также сохраняет полную native-проверку и переводит только сохранённую unresolved принадлежность; он не заменяет session-aware `ResolveLegacy`. Для `kind=code` reader возвращает только точный native `sources.source_id`. Неизвестная, чужая или устаревшая выбранная ссылка получает отказ. Полная migration/conservation проверка остаётся на существующем boundary миграции.
   Изолированный real-PostgreSQL `TestProjectConsolidationUnresolvedMembershipBatch` прошёл без skip: полный metadata catalog при missing/changed native target, отказ выбранного reader, metadata/opaque tampering, foreign Vault и прежние native-key cases. Это не доказательство полного HTTP каталога на owner corpus за 10 секунд, установленного клиента или завершённой owner streaming миграции.
   Полные native строки `documents`, `content` и `project_identity_comparisons` входят в census и unresolved membership. Для unresolved fixture `SourceRowKey` задаётся точным исходным ключом, включая encoded-link fixture; пустой metadata default не заменяет доказательство происхождения и не используется как fallback. Пустые `{}` в `ci_profiles.build_context_json` и `versioned_documents.metadata` не создают выдуманной неизвестной роли. Непустой object сохраняется с selective hold; ключи `scope`, `topic` и `readonly_review` сами по себе не доказывают Project ID. Обычные состояния, digests, audit labels и legacy alias namespaces сохраняются без переписывания.
9. Перед восстановлением после cutover останови writers, сохрани новый export и инвентаризируй записи после миграции. Используй forward compensation либо ещё одну пустую восстановленную БД. Не перезаписывай новые записи старым backup.

Сохранённый composite `.agent/runs/pr568-review/stream-integrity/stage5-http-native/result.json` основного coordination checkout содержит 15 top-level cases / 61 PASS без fail/skip из отдельных успешных команд. Настоящий зарегистрированный router/store/AES-GCM прошёл Census → Preflight → Backup → Verify → DryRun → Apply → replay и обычное retained Vault чтение без verifier seam. Первоначальные неуспешные harness receipts сохранены; это не новый all-suite PASS. Stage3 отдельно подтверждает два real-PostgreSQL oracle, stage4 — восемь групп invariants с точечной коррекцией fixture ACL.

`.agent/runs/pr568-review/stream-integrity/stage6-historical/result.json` сохраняет 2/2 PASS: direct-store и зарегистрированный HTTP reader прочитали genuine старый архив с его исходным plan/key. Проверены все объявленные 85 таблиц и 40 sequences, обычное чтение credentials и неизменность source rows/xmin/sequences/effective objects. Это не восстановление отсутствовавших пяти recovery tables и не полный 90-table proof. Старый partial plan по-прежнему не разрешает новый Preflight/Backup/DryRun/Apply или replay Apply. Исходный capture и принятые RED не повторялись; original archive/plan/key и полезные fixture outputs сохранены. Эти focused результаты не заменяют full-owner native proof, browser/installed acceptance или release.

### Две независимые dirty-копии

Используй собственный тестовый репозиторий и два настоящих Git worktree. Изменения можно сохранять без коммита: индекс отражает рабочую копию, а не только Git HEAD. Не выполняй rename/delete на полезных пользовательских файлах ради проверки.

Для проверки изоляции используй одинаковые relative path, symbol и записанную
ревизию, но разные сохранённые dirty-тексты и вызываемые функции. Два клиента
одного daemon и две вкладки должны одновременно читать свои source/graph без
cross-worktree результатов. Разные HEAD сами по себе не доказывают этот случай.

1. Подключи и проиндексируй копию A по инструкции выше.
2. Открой вторую вкладку консоли через Home → **Рабочее место**.
3. Открой установленный агент из Git-корня копии B.
4. В **Добавить / подключить репозиторий** выбери **Другую копию существующего репозитория** и нужный репозиторий по читаемой подписи.
5. Передай агенту показанную задачу. Он получает свежий `codebase_context list`, выбирает единственный соответствующий Source по серверному ответу и вызывает `select`. Затем `register` берёт Source из возвращённого handle/серверной привязки, а Git-корень — из текущего native host. Оператор не вводит `source_id`. При одинаковых названиях агент должен показать неоднозначность, а не объединять Source по имени или remote URL.
6. Дождись отдельного View B. Обнови каталог и закрепи B во второй вкладке. Проверь отличающиеся подписи рабочих копий; они не являются полномочием, но помогают не перепутать копии.
7. Сохрани разные версии одного тестового механизма в A и B без коммита. Дождись публикации новых View через watcher; если нужна сверка, используй **Запросить сверку** в выбранной копии или native `codebase_index` с её handle.
8. Обнови разрешённые варианты в A и явно переключись на новый снимок. Проверь новый текст A. B и старый закреплённый View A должны сохранить прежний текст.
9. Переименуй только созданный для проверки файл в A. Дождись публикации и явно выбери новый View A. Поиск и исходник нового снимка должны показывать новый путь; B и старый View A — прежний путь.
10. Удали только этот тестовый файл из A. После публикации выбери новый View A. В нём файл отсутствует; B и старый View A остаются независимыми и читаемыми.
11. Для проверки перезапуска сначала подтверди владельца, установленный executable, PID и `daemon_generation` **точно этого** daemon через его поддерживаемый muxcore control. Уже существующий протокол: `{ "cmd":"status" }`, затем после совпадения с удержанной identity `{ "cmd":"shutdown", "drain_timeout_ms":2000 }`. Дождись недоступности этого control и выхода прежней generation, затем открой обычный установленный plugin заново. Это control-протокол, не команда `engram shutdown`: такой CLI здесь не заявлен. Не убивай PID, не удаляй marker/socket и не используй случайный тестовый controller на общем daemon. Если штатный host не предоставляет доступ к этому управлению, перезапуск выполняет владелец установки; отметь его отдельно от обычного переподключения.
12. После подтверждённого перезапуска снова открой обычный клиент. Старый session handle больше не используй. Получи свежий серверный `list`/`select`, проверь каждую рабочую копию через `codebase_status`, затем обнови каталоги обеих вкладок. Подписи, разные тексты A/B, удаление/переименование в A и исторические View должны остаться различимыми.

### Поддерживаемая область и восстановление

- Go, JavaScript, TypeScript и TSX проверяй отдельными реальными примерами, включая приватный helper и входящую/исходящую связь. Для JS/TS/TSX установленный daemon должен иметь совместимый parser executable и bundle digest. Общий embedding coverage не сертифицирует язык; языки показанных фрагментов не являются полным списком поддерживаемых файлов.
- C# и динамические вызовы не считаются проверенными по результату Go или TS. Неподдерживаемый файл, неоднозначная ссылка, ограниченный обход и частичная экстракция должны остаться видимыми ограничениями.
- Если провайдер не работает, сохрани последний хороший View. Проверь существующую конфигурацию модели, размерность и причину задания; не меняй профиль, сеть или провайдер из задачи подключения. Code embeddings и memory embeddings — разные доказательства.
- Когда daemon офлайн, опубликованный View может оставаться читаемым. Новая регистрация и запрос индекса требуют живого разрешённого владельца. Обновление каталога не создаёт Source само по себе.
- Новая UCI-регистрация не мигрирует память или Issues. Не создавай другой UUID ради индексации: сохрани существующую `.engram-project` и cache. `engram project init` предназначен для явно нового проекта; переход со старого name-only marker не доказывает миграцию исторических данных.
- Сохраняй установленный plugin-data и его `ENGRAM_CLIENT_INSTANCE_ID`, Vault key и полезную историю. Не делись ключами между независимыми установками и не выводи их в отчёт. Ограничение opaque handles: native daemon удерживает не более 1024 транспортных владельцев с LRU eviction; после вытеснения получи свежую серверную привязку.

Полный R2-путь проверяет независимый инженер на установленных компонентах: Home → подключение → два worktree → реальные векторы → типизированная связь → исходник того же View. Source fixture, старые donor receipts, memory recall и HTTP readiness его не заменяют. Это не условие обычного R1-чтения полезной памяти. Fresh-host automatic context — отдельная приёмка; инструкция не выдаёт её за выполненную.

### Index and inspect an authorized View

For first indexing, use the no-View `context_handle` returned by `codebase_context` `register` in the **same client session**, then call daemon-side `codebase_status` with that handle to prepare the registered local checkout. `codebase_context` `list` only returns authorized published Views, not fresh checkouts. Do not substitute a repository path, a made-up UUID, or a handle from another client. The following `codebase_index` input uses that server-issued checkout handle:

```json
{"context_handle":"<selected checkout handle>"}
```

Index admission returns `run_id`, not a completed View. On the **daemon-side** `codebase_status`, pass that handle and the returned run ID to wait for a bounded read-your-save barrier:

```json
{"context_handle":"<selected checkout handle>","after_barrier":{"token":"<returned run_id>","wait_ms":5000}}
```

`wait_ms` accepts 1–60000 ms as the caller's maximum willingness to wait. Each daemon-side call waits at most 250 ms. `running` or a `timed_out` barrier means pending, not an index failure or completed read-your-save proof. Refresh status with the same `context_handle` and token; do not start another index, substitute another View, or retry indefinitely. Report pending when the run is still active. A completed matching run and published View are required before claiming success.

Plain `codebase_status` for an authorized published View reads that exact snapshot through the server's normal read authorization. It does not require ownership of the indexing workstation and does not register another checkout or adopt memory. Its daemon `status` is `null`: that means local index-run liveness is unavailable, not healthy, idle, or completed. Use the original owner's issued `after_barrier` token for local run progress; foreign or stale tokens and denied index bindings remain refused. A valid registered checkout without a published View retains the owned `never_indexed`/local-liveness preparation path only after its stronger index binding succeeds. Permission, context-mismatch, malformed-response and service failures never take that path.

The top-level `total_chunks` and `embedded_chunks` count unique chunks in the selected View. A chunk contributes to `embedded_chunks` only when every required selected-View membership/path input has a compatible ready embedding for the exact profile, protection domain and dimension. The structured `embedding.total_candidates` and `embedding.ready_candidates` count membership candidates instead; reused chunks can make that denominator larger. A path-independent cached vector can satisfy both memberships before the job cursor visits both, but semantic completion still requires the exact job/candidate proof. These counts are not interchangeable, and vector-cache row count is a third unit.

The daemon-side `codebase_status` requires a handle; follow the schema advertised by the installed plugin. Wait for a completed run and a published View, then inspect coverage, omissions, supported languages, and **code-embedding** progress until ready. Configure the shared provider with `ENGRAM_EMBEDDING_URL` as its base URL, optionally ending in `/v1`; the client calls `/v1/embeddings`. Set `ENGRAM_EMBEDDING_MODEL` to a model that returns 1536-dimensional vectors, matching the UCI vector schema. Supply `ENGRAM_EMBEDDING_API_KEY` privately only if the provider requires one. A reachable provider or memory embeddings alone do not prove code embeddings. Preserve the last good View if a job fails.

The code-job worker caps each request's estimated padded UTF-8 cost (`longest canonical input bytes × input count`) at 64 KiB while retaining the 128-input ceiling. A larger single input is sent alone, unchanged, within the existing input-capacity guard. This is a workload proxy, not a provider token limit or success guarantee; model/profile/cache identity and provider deadlines, retries, and concurrency stay unchanged.

On the published View, call `codebase_search` with a conceptual query that does **not** contain the known function name, then follow a returned reference. For example, replace the query with one chosen for your corpus:

```json
{"context_handle":"<published View handle>","query":"How does this repository choose a working copy for code search?","limit":10}
```

Confirm that the conceptual `codebase_search` result reports vector or hybrid retrieval in the selected published View, with its source citation. Lexical fallback is degraded, not semantic proof. The indexer derives graph edges automatically; do not create them manually. `codebase_graph` resolves a function by name, not by the citation's chunk `entity_key`. Use the citation's `source_id` and `view_id`, and copy the function name from the cited source excerpt; if the excerpt does not show the function name, first read the cited span from the same View with `codebase_read`.

```json
{"action":"neighbors","context_handle":"<published View handle>","target":{"source_id":"<returned source_id>","view_id":"<returned view_id>","name":"<function name from the source excerpt>"},"direction":"both"}
```

Follow a direct and a reverse relation, including one neighbor not on the search page. For `codebase_read`, copy the exact `ref`, server-issued canonical UUID `membership_id`, `span` (byte and line start and end), and bare 64-hex-character `content_digest` from a returned citation, along with the same View handle. All four are required; old citations without membership_id must be searched again. The UUID selects the file's temporal membership, even when another path has identical bytes/entity/span/digest. A caller path is not a selector, and an ambiguous graph location has no source-read descriptor. Do not read today's file from disk as a substitute for the stored View span. Evidence labeled ambiguous or heuristic does not prove a resolved call; a missing dynamic edge does not prove no dependency exists.

The JS, TS, and TSX facts/v9 parser keeps resolver-only lexical metadata separate from published references. Source references retain their 8192-site bound, and lexical facts have an independent 16384-site bound. Local declaration identities include their actual lexical scope, so repeated names in sibling functions or blocks do not falsely make the whole file partial and hide an otherwise supported direct call. Module and exported identities remain unchanged where unambiguous; genuine collisions, shadowing, parse errors and exhaustion still retain conservative partial or unresolved results. Legal accessors, overloads, and merged declarations retain separate navigable occurrences without turning an ambiguous name into a resolved graph target.

Class static blocks retain their own `var` scope. Declaration keys include their full lexical scope and any method/interface/namespace occurrence suffix before the final key bound is applied. A full-key digest is used only when that composed identity would exceed the existing key bound; distinct source sites remain distinct, and identifier names and source bytes remain intact. Resolver reference identities have a separate bound for fixed prefixes and bounded source offsets; identifier, payload and fact-count limits are unchanged.

Direct `eval` is classified from the parsed callee, not text inside its arguments. Parentheses such as `(eval)(code)` preserve direct eval; optional calls such as `eval?.(code)` and sequence calls such as `(0, eval)(code)` are indirect. A parameter or alias may hold intrinsic eval, so a shadowed name alone does not prove safety. In sloppy-script JavaScript, passing intrinsic eval to a parameter named `eval` still permits direct eval; strict-mode modules reject parameter bindings named `eval`. Only a proven unchanged non-intrinsic function binding avoids that conservative direct-eval treatment.

### Open and accept Workspace

Open the configured HTTP LAN console from **Home → Workspace** without logging in. Select **Repository → Working copy → Indexed snapshot**, then **Pin selected View**. If several registrations share a human-readable repository or working-copy name, use the displayed number of indexed working copies and snapshots to distinguish the published one; labels alone do not establish identity. A checkout without a published View may offer **Index checkout** only when a single fresh daemon target is available. Request indexing, refresh authorized choices, and explicitly pin the published snapshot; request acknowledgement is not completion. The server checks the no-auth Source, Checkout, and View. Keep another working copy and browser tab on their own selected Views.

A published snapshot is an immutable View. When its daemon goes offline, the authorized catalog entry, historical pin, and released structure, search, graph, and source remain readable. The server still checks durable Source-and-Checkout registration, ownership, and any required browser grant. A fresh daemon-target observation is required for bootstrap and index actions, not for published-View reads. Only an authorized owner-side indexing or local-barrier operation can renew that observation; a plain selected-View status read does not advertise ownership of a daemon target. Expiry or daemon restart requires a fresh owned target before those actions can proceed.

Check freshness, embedding coverage and count, embedding job state, and any error reason in the selected Workspace status. **Index ready** with complete code embeddings means semantic search can be checked, but the search result's `vector` or `hybrid` retrieval mode is the proof for that query. **Semantic search not ready** means lexical search or source reading may still work; check the provider, model, and job reason before requesting reindex or reconciliation. **Updating** keeps the pinned snapshot visible; **Newer snapshot available** requires an explicit switch; a failed job does not replace the last good snapshot. Refresh status after action. Search conceptually, follow an automatically derived direct or reverse relation, inspect its evidence precision, and read the source in the **same selected View**. A relation marked ambiguous or entity-level does not prove an exact call site, and a missing unsupported relation does not prove absence. In a second tab, pin the other worktree and recheck each tab's displayed copy and snapshot after reload. If the old document lease is still active, close the old document or wait for expiry before using **Retry tab binding**; an immediate retry can still report a conflict. Never transfer a tab's binding, UUID, or handle between tabs.

An unavailable or malformed status response does not replace an already confirmed View or its released search and source results; use **Refresh status** to retry. Failed chooser refreshes release their loading state and leave **Refresh authorized choices** available. Only the latest request for the current tab binding may update choices or status, and an obsolete response cannot clear a newer request's pending state. Returning to Workspace from another console section resumes the same server tab binding and restores its historical View only after a successful status check, even if the chooser now lists only a newer snapshot. If this restoration is unavailable, contextual controls stay hidden: use **Refresh authorized choices** to retry rather than pinning the newer snapshot as a recovery step. Access denial or a context mismatch clears the pin and its saved recovery state; it cannot be restored by navigating away and back.

No-auth HTTP LAN access assumes a trusted single-user deployment; it is not an authenticated multi-user access policy. Login, read-grant issuance, HTTPS, and a proxy are not prerequisites for this configured path. An auth-enabled deployment retains its principal, grant, and tab-bound authorization requirements; do not use the no-auth path to bypass them.

### Verify F1–F7 on installed components

F1–F5 and the full code journey are R2 acceptance, not a blanket R1 release gate. F6 retirement and retention checks remain independently applicable to any cutover shipping those changes; F7 applies to the journey being delivered. The owning [release protocol](RELEASE-PROTOCOL.md) still governs security, data/migration, platform, artifact and rollback checks. For the R2 checks below, use an agreed disposable repository, set expected answers before querying, and record installed identities without secrets or private source. Another operator must be able to follow the guide without private hints.

The complete R2 obligations remain in [Feature 010](../specs/010-unified-code-intelligence/spec.md)
and [Feature 011](../specs/011-operator-code-console/spec.md): promised language/corpus
coverage, the twelve-task value/baseline set, same-View evidence, recovery, privacy and
non-content exposure integrity, plus the populated responsive/zoom/keyboard/task-language
journey. Earlier S2/S4 receipts retain their own candidates and are not current installed
acceptance. The summary below does not replace or narrow those contracts.

1. **F1: first use.** Follow **Первый запуск: от меню до исходника** from Home with an empty browser catalog. Use its visible task in the ordinary installed native host; do not pre-register the source as hidden fixture setup, initialize project identity, write SQL, or supply UUIDs.
2. **F2: two working copies.** Follow **Две независимые dirty-копии**, including saved divergent code, rename, delete, explicit View switches, and the confirmed supported daemon restart. Record A/B isolation and historical View retention. A client reconnect alone is not daemon restart evidence. Preserve ordinary memory independently before and after rollout.
3. **F3: semantic search.** Index more than 50 eligible candidates. Wait for code embeddings to become ready. Run exact-symbol, non-lexical conceptual, and negative queries. Inspect vector or hybrid retrieval, profile, source span, totals, and continuation; lexical fallback is not semantic success.
4. **F4: graph and evidence.** Follow a derived direct and reverse relation to an off-page neighbor. Read its released evidence and exact span through the same View. If evidence is only partial or entity-level, report that limit. Do not write graph edges manually.
5. **F5: recovery.** In an owned test environment, observe queued or unavailable indexing when the daemon is offline, then restore it and verify publication. Interrupt the test embedding provider and confirm an honest error or degraded state with the last good View intact. Authentication-enabled grant revocation is a separate deferred scenario.
6. **F6: retired writers.** Check the [retired-writer matrix](#f6-retired-writer-matrix) against installed server and plugin components. A missing menu item does not prove writer retirement; historical records and permitted readers must remain available.
7. **F7: instructions.** Have another operator repeat the path from this guide on the installed components. Record dead ends, F1–F6 outcomes, and component identities before claiming installed acceptance.

### F6 retired-writer matrix

The source-candidate denominator is **5 HTTP methods + 3 MCP actions + 2 old bookmarks = 10 negative checks per start**. Compare the matrix with the actual installed server, daemon, and console before testing; source-only fixture evidence does not certify an installed release.

| Former writer or bookmark | Expected refusal |
| --- | --- |
| `POST /api/graph/nodes` | HTTP 405; no node created. |
| `POST /api/graph/edges` | HTTP 405; no edge created. |
| `DELETE /api/graph/nodes/{id}` | HTTP 405; no node deleted. |
| `DELETE /api/graph/edges/{id}` | HTTP 405; no edge deleted. |
| `POST /api/books` | HTTP 405; no Book job admitted. |
| MCP `graph` action `add_edge` | Absent from the advertised `graph.action` enum; call returns `IsError`, `unknown graph action: add_edge`. |
| MCP `graph` action `remove_edge` | Absent from the action enum; call returns `IsError`, `unknown graph action: remove_edge`. |
| MCP `graph` action `add_node` | Absent from the action enum; call returns `IsError`, `unknown graph action: add_node`. |
| Old `/graph` bookmark | No manual graph editor. |
| Old `/books` bookmark | No plaintext Book uploader. |

Repeat all 10 checks with `ENGRAM_GRAPH_ENABLED=true` and `ENGRAM_BOOKS_ENABLED=true` after restart. Record the component identities and each refusal. Retained reads include `GET /api/graph/nodes`, `/api/graph/edges`, `/api/graph/traverse`, `/api/graph/find-path`, `GET /api/books/{id}/status`, authorized `/api/documents`, `/api/documents/history`, `/api/documents/comments`, `/api/rules`, `/api/issues`, `/api/context/search`, MCP `graph` actions `get_edges`, `traverse`, `find_path`, `synonyms`, Tier2 `Traverse` in `internal/retrieval/hybrid.go`, and the separate UCI `codebase_graph`. Verify their existing ACLs, not just availability. Before transitioning residual Book jobs to `failed: interrupted by retirement`, ensure no old Book writer is live; preserve document versions, `source_book_job_id`, and partial documents. The source matrix follows `internal/worker/service.go`, `internal/mcp/tools_graph.go`, `internal/worker/handlers_graph_test.go`, `internal/worker/handlers_books_retirement_test.go`, `internal/mcp/tools_graph_t014_test.go`, and the [DA03 source receipt](../specs/011-operator-code-console/acceptance/da03-retirement-receipt.json). It is not installed acceptance evidence.

The linked DA03 source receipt records **NOT_PROVEN** for full source acceptance because its bookmark browser test selectors failed. Repeat the two bookmark checks with scoped selectors on the exact installed UI; do not convert its HTTP/MCP fixture results into a release PASS.

The Windows amd64 (`win32-x64`) parser has been built and smoke-tested as a source candidate, but its release asset is not published and the ordinary installed Windows parser is not yet certified. Linux amd64 and macOS arm64 have no parser targets in the generated package policy, even though launcher clients exist for those platforms; do not infer JS, TS, or TSX facts or graph coverage from a source build. On Windows, Go and structured text remain in the first scope; JS, TS, and TSX require a published parser bundle installed by the supported plugin path. At release, check the published asset against the `win32-x64` `asset`, `size`, and executable `sha256` in the matching package's generated `plugin/engram/parser-targets.json`, then check the installed parser's `--bundle-digest` output and daemon diagnostics for the separate compatible bundle digest. A candidate policy entry alone is not installation evidence. Full C#, Python, Vue SFC, communities, and the old manual graph or Book workflows are not promised. The source evidence manifest `tools/uci-parser/manifest.json` still marks Windows and Linux amd64 `not_claimed` and `not_reverified` for the v2 facts contract, with macOS targets blocked; do not turn candidate smoke evidence into a manifest or installed-release PASS.

### Diagnose by symptom

| Symptom | Check first |
| --- | --- |
| Workspace cannot open on HTTP LAN | Check the configured browser origin, installed console bundle, `/api/auth/me` auth mode, and browser errors. Do not substitute localhost, HTTPS, login, or a proxy. |
| No working copy appears | From Home → Workspace open Add / connect repository; run its task in the ordinary installed agent at the correct Git root, then refresh the browser catalog. Check the agent's server and Source/Checkout response; no browser grant is required in the configured no-auth realm. |
| `LOCAL_CODE_CATALOG_FULL` while registering a no-auth checkout | The shared Workspace catalog supports 128 active checkouts. Existing checkout registration replays still work; take a no-longer-needed checkout offline before registering another. Do not hide catalog entries or use another Source label to bypass the limit. |
| API healthy but no code tools | Compare the server's `/api/flags` with the actual daemon path, plugin version, inherited `ENGRAM_CODE_INTEL_ENABLED`, and fresh `tools/list`. Current source is on unless exactly `false`; an older installed daemon may differ. |
| No JS, TS, or TSX facts | Inspect the installed parser executable, bundle digest, and extraction diagnostics. Do not use a source path as a binary. |
| Search is lexical only | Inspect the chosen View's code-embedding jobs, model profile, and provider. |
| Saved changes do not appear | Inspect the owning checkout watcher, ignore rules, index job, and View publication; keep older Views intact. |
| A result contains another working copy's code | Stop relying on the result and investigate authorization and context isolation before retrying. |

Observe API and PostgreSQL readiness, daemon liveness, selected checkout and View, file coverage, embedding progress, job state, retrieval mode, and evidence limitations. `codebase_status` and the selected Workspace view are the relevant sources; memory aggregates are not code-index health. Apply updates through the supported installation/deployment path and preserve rollback inputs. Close R1 only with the ordinary installed agent/WebUI same-project/useful-decision and fresh-session oracle above. Close R2 only with its complete installed F1–F7/code contract; neither source checks nor a successful R1 silently closes it. Independently applicable release gates remain in force.
