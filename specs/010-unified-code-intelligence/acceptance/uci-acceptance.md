<!--
Adopted from: .agent/intake/engram-code-intelligence-2026-09-05-r1/09-ACCEPTANCE.md
Adopted: 2026-09-05
Status: Working supporting contract for feature 010. The intake source remains read-only provenance.
-->

# Приёмка и эксплуатационные критерии

Все проверки ниже — план, не выполненные production tests. `acceptance/scenarios.json` — машиночитаемый перечень; его schema validation доказывает только целостность спецификации.

## Три разных доказательства

**Механика:** real PostgreSQL, настоящий scoped store, leases/transactions, parser contracts, fault injection. Fake embedder допустим для проверки транспорта и shape, но не качества semantic search.
**Установленный путь:** built server/daemon/parser worker, настоящий Windows Git worktree и минимум две одновременные stdio MCP-сессии одного daemon. Package tests и direct service calls этого не заменяют.
**Польза:** заранее выбранные задачи поиска, explain/impact/doc navigation; правильный source/view и проверяемый ответ. Сравнение с обычным поиском файлов на том же corpus/model/budget, без заимствования upstream benchmark numbers.

## Непроходимые обходом gates UCI-1

Две worktree с одинаковым source/path/name/HEAD и разными saved dirty bodies возвращают правильные разные snippets/edges. Update A не меняет B; delete A не удаляет B. Третий клиент, подключённый позже, не меняет контекст первых двух.
Graph и search одного ответа используют согласованный view. Broken syntax/unsupported file видны как partial; старый graph не выдаётся за актуальный. Source ACL применяется на candidate universe и каждом graph hop.
Новый file version не возвращает old embedding hit под current path. Semantic positive query реально использует provider-generated vector; lexical fallback честный и отдельно проверен.

Детерминированный HTTP-stub с заранее запрограммированными vectors считается только mechanical integration proof для транспорта, SQL scope, fusion и degradation. Он не доказывает качество semantic search или FR-07. Semantic acceptance требует отдельно: (1) реальный разрешённый provider без marker-controlled vectors; (2) обычный fresh index полного разрешённого pilot corpus; (3) durable embedding jobs и production `EmbeddingWorker`; (4) current corpus vectors; (5) zero-lexical-overlap MCP query с полезным source result. SKIP, ручной seed target vectors и уменьшенный canary corpus этот gate не закрывают.
Incomplete upload/EOF/crash не переключает current pointer. Old lease epoch не может publish. Lost ACK/replay same build не дублирует изменения. Delete-all отличим от failed scan.
Watcher restart/overflow/offline/reconnect восстанавливают текущее состояние. Неизменённые artifacts не пере-embed-ятся после каждого запуска или нового worktree.
Для exposure completion: без qualifying callback receipt остаётся `unknown`; verified supported host может записать только `succeeded`/`partial`/`failed`/`abandoned`. `partial` completion не является retrieval `result_state` или coverage и не меняет их. Exposure/completion rows — durable append-only non-content UCI evidence, не rebuildable index projection и не authority.

Closed authorized `unavailable` result при healthy recorder остаётся записываемым exposure: query возвращает authorized context/freshness/retrieval/coverage envelope, closed source/index error, пустой `items`, opaque receipt и не меняет recorder health. Если initial exposure append недоступен, query возвращает только отдельный `EXPOSURE_UNAVAILABLE`, `status: unavailable`, `exposure: null` и без contextual envelope/result body/items. Exact retry возвращает original receipt только при равном canonical binding digest; изменение client/session/request/context/result либо host/callback/outcome возвращает non-disclosing `IDEMPOTENCY_MISMATCH`, не добавляет row и не возвращает stale receipt. Completion append failure возвращает `COMPLETION_EVIDENCE_UNAVAILABLE` только callback, не меняет parent exposure и не создаёт completion. `codebase_status` для разрешённого scope показывает только secret-free recorder `healthy`/`degraded`/`unavailable` и closed last failure code. U42 проверяет known/unknown completion, U43 — unavailable exposure recorder, U44 — exposure mismatch, U45 — completion mismatch, U46 — recordable authorized unavailable result.

## Матрица источников и Git

Обычный repository; linked worktree с `.git` файлом; detached и unborn HEAD; одинаковые branch names там, где Git это допускает; branch rename/switch; worktree move; удаление и пересоздание того же пути; временно недоступный mount; отдельный clone/fork с одинаковым origin.
Сохранённый untracked file, staged file с отличающимся working body, rename и case-only rename. Windows paths с пробелами, кириллицей, длинным путём; case-sensitive directory на Windows не приводится безусловно к lower-case. CRLF/LF не ломают spans.
Nested `.gitignore`, negation, ignored generated/vendor, `.agent` separate repo, submodule gitlink, symlink/reparse escape, inaccessible subdirectory, giant/minified/binary/non-UTF8 file. Исключённые файлы перечисляются по разрешённому scope без body.
Unsaved editor buffer не обещается indexed; тест UI/status это не маскирует.

## Матрица графа и извлечения

Definition/reference/call/import с точным span; несколько calls между одной парой symbols; same-name symbols и overloads; TS alias/re-export; Go module/export change; changed callee при неизменном caller; раньше unresolved module появился; удалённая definition; recursive function vs module cycle.
Для dynamic/DI reflection missing edge не выдаёт `complete runtime call graph`. Path cap возвращает truncated/unknown, а не `no dependency`. Heuristic/model edge не становится RESOLVED без другой evidence.
Markdown → exact file/symbol link, SQL foreign key, OpenAPI endpoint, config key use, broken doc link; doc claim «реализовано» не становится фактом тестового исполнения. Поздний semantic result для старых source hashes не применяется к current view.

## Матрица миграции

Legacy slug и canonical UUID с одинаковым строковым значением/label; V3 repository и directory anchors; один старый project на несколько sources; один source в нескольких spaces; product-space без Git; ambiguous alias; source moved/forked; старый клиент без cwd и несколько available checkouts.
Данные notes/issues/rules/docs/state до/после читаются с теми же ID, version и правами. Исторические HAP/audit receipts проходят прежнюю verification без переписывания cryptographic inputs. Private/shared/global/domain/empty-principal cases сохраняют deny/allow.
Legacy code_chunks не имеют придуманного checkout provenance. Новые shared caches не раскрывают существование приватного blob. Source/checkout unregister не удаляет предметную память.

## Продуктовые задачи

Зафиксировать 12 задач до настройки: четыре conceptual searches без exact-name подсказки (включая русский→английский код), две exact symbol/path, две multi-hop impact/path, две code+doc/schema связи, одну сравнения worktree и одну после increment/restart.
Для каждой записать правильные source spans/graph paths, допустимые alternative answers, corpus commit+dirty manifests, model/provider/profile и budget. Target для первого пилота: relevant source в top5 минимум в10/12 задач; все isolation/security случаи100%; не менее9/12 задач решаются с меньшим объёмом прочитанных исходников, чем зафиксированный baseline, без ухудшения правильности. Это acceptance target, не обещание общей точности83% и не статистическая оценка рынка.
Если baseline уже находит exact symbol одним grep, не штрафовать этот путь и не навязывать semantic tool. Польза измеряется на реальных трудных запросах, а не запрете простого чтения.

## Начальные SLO и их границы

Проверочный профиль: разрешённый repository порядка10k text files/до1M LOC; минимум5 активных worktree, ещё inactive registrations; LAN RTT≤50ms; изменение≤20 files суммарно≤1MiB; provider healthy. Зафиксировать фактическое железо, DB/data sizes и cold/warm state. Для большего корпуса отдельно измерить, не переносить обещание линейно.
Warm structural/FTS update после последнего save в batch: целевой p95≤2s; embedding readiness новых chunks: p95≤10s при provider availability и отсутствии превышения квоты. Превышение меняет observable lag, а не скрывает старые rows как fresh.
Warm search p95≤800ms для server-side retrieval без внешнего query embedding; bounded query с embedding — целевой≤2.5s. Small explain/neighbors/impact p95≤1s при depth≤4 и заданных caps. Каждый показатель требует минимум100 samples; cold index отдельно, не в одном percentile.
Initial full index измеряется по files/s, bytes/s, parse/embed counts и peak memory, без обещания «любой репозиторий за N минут». Второй чистый worktree того же source/profile не вызывает повторный parse/embed неизменённых input fingerprints; metadata enumeration допустима.
Критерий storage роста: серия100 saves одного файла не создаёт100 полных копий source graph. Рост объясняется изменёнными artifacts/edge intervals и bounded retention; показать counts до/после GC. Test retention сохраняет pinned views и удаляет недостижимые derived artifacts без потери current.

## Безопасность

Threat boundaries: untrusted source files, local paths/Git metadata, parser child, daemon↔server, query principal, external embedding/model, browser graph rendering. В каждом boundary проверить oversized/malformed inputs и неверное source binding.
Не выполнять source scripts/hooks; parser без credentials. HTML/Markdown labels экранируются, graph node не является командой. Provider URL задаёт оператор, не fetched source; разрешённые self-hosted private endpoints поддержаны без arbitrary request-controlled SSRF.
Secret scanner — дополнительная защита, не формальное доказательство отсутствия секретов. `.env`/keys/credentials/transcripts excluded по умолчанию; обнаруженный secret-bearing файл metadata-only. Никаких keys в logs/errors/status/exports.
Revoked ACL проверяется для pinned historical views, query cache, pagination и graph traversal. После уже выданного ответа нельзя обещать удаление локальной копии клиента; revocation действует на последующие/ещё не авторизованные выдачи.

## Один финальный отчёт

На candidate: точные commit/tree и hashes установленных artifacts; актуальные source/view IDs и manifests synthetic fixtures; migration/restart/fault/isolation results; UCI evidence backup/restore and integrity-verifier result; recorder health/failure and exact/mismatch idempotency results; 12 task results со всеми failures; SLO sample counts/p95/max и resource counters; versions/provider profiles без ключей; independent correctness/security findings; exact SonarQube/regression; known limitations и rollback.
Не собирать новый approval packet на каждую строку docs. Но после изменения выпускаемых байтов нельзя выдавать старые результаты за свежие. Реальная приёмка делает один работающий продукт, а не имитирует его десятком отчётов.
