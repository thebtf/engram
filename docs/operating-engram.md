# Operating Engram

Operational guide for engram server administrators. Covers environment configuration,
redaction rules, startup verification, and restart procedures.

---

## Обычная запись памяти без principal

В режиме `ENGRAM_AUTH_DISABLED=true` обычный вызов `store` не должен передавать
`agent_visibility`. В существующей установке 6.50.3 для памяти проекта используй,
например, `{"action":"create","content":"Решение по проекту","project":"nvmd-devops","privacy_scope":"project"}`.
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

At startup, engram logs the loaded rule file path and its SHA-256 checksum:

```
INFO  redaction: loaded rule file path=/etc/engram/redaction-rules.json sha256=a3f9... rules=2
```

Operators can use this log line to verify the active rule set matches the expected
configuration. If the expected checksum differs from the logged value, a restart with
the correct rule file is required.

If `ENGRAM_REDACTION_RULES_PATH` is set but the file is **absent at startup**,
the server logs a warning and runs with redaction **disabled** (no-op fallback):

```
WARN  redaction: rule file not found — running with redaction disabled path=/etc/engram/redaction-rules.json
```

### Restart Required for Rule Changes (EC-F9)

**Engram does NOT support hot-reload of redaction rules.**

Modifying the rule file on disk while the server is running has **no effect** on the
active rule set. This is intentional: hot-reload without an explicit signal creates
mid-write rule mismatch ambiguity (a write in flight when the rule set changes may
be partially redacted under one rule set and stored under another).

To apply updated rules:

1. Edit the rule file.
2. Restart the server (or send `SIGHUP` if the SIGHUP handler is enabled in your build).
3. Verify the startup log shows the new SHA-256 checksum.

```bash
# Restart example (systemd)
sudo systemctl restart engram-server

# Verify in logs
journalctl -u engram-server | grep "redaction: loaded"
```

> **Note:** SIGHUP-based reload is optional and must be explicitly enabled at build time.
> Consult the build configuration for your deployment. When in doubt, use a full restart.

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
3. Confirm that the installed server, console, and ordinary plugin actually contain this Workspace implementation. The previous installation's memory service and an older client can remain available separately; neither proves that the new client registered code tools. Source-candidate checks do not certify installation.

### Preserve the installed client and Vault

The no-auth Workspace path does not require an owner grant chooser or its Vault-sealed grant references. If an auth-enabled installation uses the chooser, retain its existing persistent Vault key; replicas must share that key. Replacing it also affects existing encrypted credentials. Do not expose key material in diagnostics.

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

A tracked, valid name-only marker retains its existing Git-origin/selected-subdirectory V2 memory scope. Its five-second local identity limit is unchanged: one `git rev-parse` reads the root, selected prefix and Git metadata paths; ordinary `remote get-url origin` still applies Git URL rewriting and credential normalization. Cache reuse checks selected-directory identity, marker bytes, Git environment, config/index/HEAD file fingerprints and nearer scope boundaries. System/global config paths are read authoritatively with separate `git var` calls and fingerprinted even when absent. Only a complete, file-only configuration without includes may omit the second config-origin query after all inputs remain unchanged. Includes and non-file origins still require fresh full derivation; a changed or interrupted inspection never grants scope.

Git environment names are compared case-insensitively only on Windows; Unix keeps its native case-sensitive names. Config-file symlinks are supported only for identified Git config dependencies, not as a general relaxation for index, HEAD or repository markers. Their fingerprints bind each followed link and its target text, the resolved target's file identity, metadata and bounded contents, including an absent target's later creation. Link traversal is limited to 40 followed links and config reads remain limited to 1 MiB. Replacement, retargeting, loops, unsupported targets or changes during inspection refuse stale scope; the selected marker and directory guards are unchanged.

### Identity refusal before onboarding

An existing Git repository with no `.engram-project` can discover the static `project_identity.register_v3` descriptor and any backend-provided unscoped `codebase_*` tools; this is not project-memory access. Registration cannot create the local anchor. For a genuinely new logical project, initialize the selected Git root offline with `engram project init --name <project-name>`, inspect and explicitly track `.engram-project`, then call `project_identity.register_v3` with `{}` through the configured ordinary client. Auth-enabled registration requires a server-authenticated master/admin identity; the supported no-auth first-use path does not authorize historical-memory adoption. Do not register a fresh anchor to recover an existing project's memories: retain the old data and client, establish its historical owner and an approved migration first. Malformed, untracked, unreadable, dangling-symlink and tracked-but-deleted anchors still refuse discovery; restore or repair their existing identity through authorized onboarding rather than minting a replacement.

A staged Git deletion of a committed `.engram-project` is not an unonboarded repository. Discovery and `engram project init` refuse a replacement even when the index no longer lists the file; restore the existing accepted anchor through an authorized repair rather than generating a new UUID. An unavailable Git index or HEAD inspection also refuses initialization.

An unresolvable HEAD is not proof of a new repository. Malformed refs and a missing current-branch ref with prior HEAD/reference history refuse onboarding and offline initialization; repair the existing Git state before retrying. A genuinely unborn repository remains supported.

If another visible ref tip, linked/detached worktree HEAD or listed worktree index in the same Git repository contains `.engram-project`, a pre-anchor checkout cannot initialize a replacement identity. A valid repository anchor is accepted after explicit `git add`; a commit is not required for V3 discovery, so an uncommitted index-tracked sibling anchor must also be preserved. An untracked-only sibling file does not establish accepted identity. Select or restore the existing accepted anchor through an authorized repository repair; Engram does not copy it or infer its server binding. Absence inspection checks at most 128 visible refs, 128 worktree records and 128 distinct tree tips, with a 128 KiB NUL-delimited worktree-list limit and one read-only index check per non-bare worktree. Excess, unsupported or unreadable evidence refuses onboarding instead of guessing. This is not a full historical scan: deleted refs and anchors absent from all visible tips and indexes require an explicit owner decision when older identity is known.

V3 repository checks ignore inherited `GIT_DIR`, `GIT_WORK_TREE`, `GIT_INDEX_FILE`, `GIT_OBJECT_DIRECTORY`, `GIT_ALTERNATE_OBJECT_DIRECTORIES`, `GIT_COMMON_DIR` and `GIT_NAMESPACE` for their own Git child commands. The selected repository's native `.git`/linked-worktree layout supplies that metadata instead. Ordinary Git configuration and the calling process environment remain unchanged; this is not credential filtering or a change to legacy identity adapters.

The existing 30-second `tools/list` discovery limit includes V3 identity and Git inspection, not just the backend handshake. A shorter caller deadline or request cancellation stops those Git commands and refuses discovery; interrupted inspection is never evidence that an anchor is absent. Identity hashes, visible-tip validation and the evidence limits above remain unchanged.

### Две независимые dirty-копии

Используй собственный тестовый репозиторий и два настоящих Git worktree. Изменения можно сохранять без коммита: индекс отражает рабочую копию, а не только Git HEAD. Не выполняй rename/delete на полезных пользовательских файлах ради проверки.

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
- Новая UCI-регистрация не мигрирует canonical project, память или Issues. `.engram-project` не нужно создавать или переписывать для этой задачи. Отдельный `engram project init` относится только к явно новой project identity; существующий name-only/invalid marker нельзя заменять ради кода.
- Сохраняй установленный plugin-data и его `ENGRAM_CLIENT_INSTANCE_ID`, Vault key и полезную историю. Не делись ключами между независимыми установками и не выводи их в отчёт. Ограничение opaque handles: native daemon удерживает не более 1024 транспортных владельцев с LRU eviction; после вытеснения получи свежую серверную привязку.

Рабочий продукт проверяет независимый инженер по этой инструкции на установленных компонентах. Source fixture, старые donor receipts, успешный memory recall и HTTP readiness не заменяют installed-путь: Home → подключение → два worktree → реальные векторы → типизированная связь → исходник того же View. Fresh-host automatic context — отдельная приёмка; инструкция не выдаёт её за выполненную.

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

Use an agreed disposable repository. Set expected answers before querying, and record installed component identities without secrets or private source in the record. A second operator should be able to follow this guide without private hints.

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

Observe API and PostgreSQL readiness, daemon liveness, selected checkout and View, file coverage, embedding progress, job state, retrieval mode, and evidence limitations. `codebase_status` and the selected Workspace view are the relevant sources; memory aggregates are not code-index health. Apply updates through the supported installer or deployment path, preserve the prior components for rollback, reconnect the selected daemon, and repeat F1–F7 on the actual installed artifacts before claiming a release complete.
