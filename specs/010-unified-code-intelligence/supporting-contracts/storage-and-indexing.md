<!--
Adopted from: .agent/intake/engram-code-intelligence-2026-09-05-r1/04-STORAGE-AND-INDEXING.md
Adopted: 2026-09-05
Status: Working supporting contract for feature 010. The intake source remains read-only provenance.
-->

# БД, инкрементальное обновление и публикация индекса

## ADR-ST-01: один PostgreSQL, разные производные

Использовать существующий PostgreSQL 17 и pgvector. Не добавлять отдельный графовый сервер, Qdrant, broker или JSON-file authority. Новые `ci_*` tables описывают исходники и производные. Существующие memories/knowledge_edges не являются местом хранения синтаксического графа.
Локальная SQLite daemon registry хранит source/checkout bindings, persistent dirty-set, watch configuration и resume metadata. Она не становится вторым общим поисковым/knowledge authority. Потеря этой БД допускает controlled re-registration/reconciliation, не потерю пользовательской памяти.
Exports JSON/HTML/Markdown — только пользовательский экспорт pinned view; never ingest them обратно автоматически как текущий индекс. Настройки/ignore files могут оставаться текстовыми, но manifests, graph, vectors, queues и versions — в БД.

## ADR-ST-02: общий разбор, отдельный состав файлов

Для первого релиза выбрать flat effective membership с temporal intervals по checkout, а не рекурсивный граф цепочек overlay. Каждый checkout имеет собственную отображаемую карту path→artifact. Неизменные file bytes/parse products/embeddings переиспользуются между worktree в разрешённом scope.
Первое присоединение worktree требует O(number_of_files) metadata membership, но дорогие parse/embed операции — только для отсутствующих fingerprints. На каждой правке меняются O(changed_files + affected_links) rows, не копируется весь graph.json и не строится заново полный vector index.
Это сознательный обмен небольших повторяющихся ссылок в БД на простые корректные запросы. Persistent shared base manifests/copy-on-write metadata — поздняя оптимизация по реальному объёму, не предшественник UCI-1.

## Нормативная логическая схема

UUID ниже server-assigned, а digest — полный SHA-256 bytes, не усечённый ID. Все связи содержат source/realm scope там, где это исключает cross-scope joins; database composite FK либо соответствующий invariant обязателен.

```text
spaces(space_id, auth_realm, display_name, state)
sources(source_id, auth_realm, kind=git|directory|document_set, display_name, state)
space_sources(space_id, source_id, display_order)  -- grouping, NOT a grant
source_grants(...) / checkout_grants(...)          -- adapters over existing auth rules
legacy_context_aliases(realm, domain, scheme, value, client_namespace,
                       space_id?, source_id?, mapping_state, revision, provenance)

ci_checkouts(checkout_id, source_id, workstation_id, incarnation_id,
             kind=working_tree|commit_reader,
             owner_principal, locator_ref, current_view_id?, state,
             lease_epoch, owner_instance?, lease_expires_at?)
ci_profiles(profile_id, parser_bundle_digest, resolver_revision,
            chunker_revision, ignore_policy_digest, build_context_json,
            secret_policy_revision)
ci_views(view_id, checkout_id, source_id, incarnation_id, generation,
         profile_id, head_oid?, object_format?, ref_label?, dirty,
         observed_fs_seq, scan_start, scan_end, manifest_digest,
         state=staging|published|superseded|retired, coverage_json, published_at?)

ci_blobs(blob_id, source_id, protection_domain, content_digest,
         byte_length, safe_content?, encoding, storage_state)
ci_parse_artifacts(artifact_id, source_id, blob_id, language,
                   parser_revision, grammar_digest, extraction_profile_digest,
                   status=complete|partial|unsupported|excluded, diagnostics)
ci_definitions(artifact_id, local_symbol_key, kind, name, qualified_local_name,
               signature, byte_start, byte_end, line_start, line_end)
ci_reference_sites(artifact_id, site_key, owner_symbol_key?, raw_target,
                   relation, syntax_span, resolver_hints)
ci_chunks(chunk_id, artifact_id, symbol_key?, chunk_kind, ordinal,
          byte_start, byte_end, content_digest, text_for_search, content_tsv)

ci_memberships(checkout_id, path_key, display_path, artifact_id?,
               file_state=present|excluded|unreadable, mode,
               valid_from_generation, valid_to_generation?)
ci_resolved_edges(checkout_id, edge_key, source_path, source_artifact,
                  source_symbol, target_path?, target_artifact?, target_symbol?,
                  relation, evidence_kind, resolver_revision,
                  evidence_json, resolution_state,
                  valid_from_generation, valid_to_generation?)
ci_embedding_profiles(embedding_profile_id, provider_ref, model,
                      dimension, preprocessing_revision, include_relative_path)
ci_embeddings(embedding_profile_id, embedding_input_digest, vector,
              source_id, protection_domain, completion_seq, status)
ci_chunk_embeddings(chunk_id, relative_path_fingerprint, embedding_profile_id,
                    embedding_input_digest)
ci_jobs(job_id, source_id, checkout_id?, job_kind, input_fingerprint,
        owner_epoch?, target_generation?, state, attempt, retry_after,
        lease_owner?, lease_expiry?, error_code?, counts)
ci_analyses(analysis_id, view_id, kind, algorithm_revision, input_digest,
            artifact_refs, result_json, state)

uci_exposures(exposure_id, exposure_ref, auth_realm, source_id, checkout_id, view_id,
              client_ref, client_session_ref, request_ref,
              operation_kind=code_search|code_graph|versioned_read,
              result_state=ok|empty|partial|stale|unavailable,
              retrieval_mode=exact|lexical|hybrid|graph|unavailable,
              coverage_state=complete|partial|unavailable,
              evidence_source=exact|fts|vector|graph|mixed|none,
              certainty=established|partial|unavailable, idempotency_key,
              idempotency_binding_digest, recorded_at)
uci_completion_evidence(completion_evidence_id, exposure_id, supported_host_ref, callback_ref,
                        outcome=succeeded|partial|failed|abandoned, idempotency_key,
                        idempotency_binding_digest, occurred_at)
```

`ci_blobs.safe_content` необязателен для исключённого файла; query endpoints не разрешают произвольное скачивание blob по hash. Источник с обнаруженным секретом по умолчанию получает metadata-only excluded state, без исходного body в server/index/provider. Coverage отражает исключение; scanner не обещает распознать любые секреты.
`ci_definitions` и `ci_reference_sites` — content-derived facts без привязки к абсолютному пути worktree. Semantic resolution зависит от относительного path/module environment и хранится отдельно в view-scoped edges.
Directory/docs/config nodes могут использовать такую же local definition форму с kind document/section/table/endpoint/config_key. Нельзя превращать хранимый JSONB результата анализа в универсальную изменяемую базу графа.

## Retrieval exposure and completion

`uci_exposures` and `uci_completion_evidence` are durable, append-only UCI evidence, not rebuildable index projections. `ExposureRecorder` is the named UCI evidence owner and the only application writer. The canonical exposure source is the authorized closed search, graph, or versioned-read decision at the shared MCP boundary. The canonical completion source is a verified supported-host callback. Neither table is an access grant, a View-selection mechanism, a publish record, or a product-success receipt. Neither can create authority, change a View, or publish a View.

The exposure stores only opaque `client_ref`, `client_session_ref`, and `request_ref`; the authorized Source/Checkout/View tuple; the closed operation/result/retrieval/coverage/evidence/certainty metadata; timestamp; idempotency key; and canonical non-content binding digest. It stores no source body, query text, absolute path, secret, tool output, or unauthorized ID. The completion child stores only its exposure, verified host/callback refs, closed outcome, timestamp, idempotency key, and canonical non-content binding digest.

The exposure digest is SHA-256 over versioned canonical UTF-8 JSON with lexically sorted member names, no insignificant whitespace, and explicit nulls. It binds `auth_realm`, `client_ref`, `client_session_ref`, `request_ref`, `source_id`, `checkout_id`, `view_id`, `operation_kind`, `result_state`, `retrieval_mode`, `coverage_state`, `evidence_source`, `certainty`, and `idempotency_key`. The completion digest binds `exposure_id`, `supported_host_ref`, `callback_ref`, `outcome`, and `idempotency_key`. A retry returns the original record only when its stored digest matches. A mismatch returns non-disclosing `IDEMPOTENCY_MISMATCH`, creates nothing, and returns no stale exposure or completion record.

An initial exposure append failure returns `EXPOSURE_UNAVAILABLE` with `status: unavailable`, `exposure: null`, and no result body or items. A completion append failure returns `COMPLETION_EVIDENCE_UNAVAILABLE` to the callback only; it does not change the original exposure or manufacture completion. Without a qualifying child, completion remains `unknown`; the server never turns a response, timeout, or absent callback into an outcome. This host-completion state is independent of retrieval `result_state` and `coverage_state`.

## Ключи, FK и индексы

Unique checkout incarnation; unique `(checkout_id,generation)` и `view_id`; exactly one current_view pointer. Unique blob на `(source_id,protection_domain,content_digest)`; artifact cache key включает blob/language/parser/grammar/profile. Reindex после parser upgrade обязан получить новый artifact, даже при тех же исходниках.
Membership intervals полузакрытые `[from,to)`, `from>=1`, `to>from` либо null. Unique current row `(checkout_id,path_key) WHERE to IS NULL`; отсутствие overlap обеспечивается единственным fenced publisher transaction и проверяется integration tests. Не полагаться только на partial unique для исторических intervals.
Definition/ref/chunk rows ссылаются на существующий artifact; один chunk span не выходит за byte_length. Edge endpoints разрешаются только в membership выбранного view; relation/provenance enums закрыты. Unresolved site не имеет выдуманного target.
Обязательные B-tree: membership checkout/path/interval; edges checkout/source endpoint/interval и reverse target endpoint/interval; jobs state/retry_after; views checkout/generation. GIN на lexical tsv; exact names и symbol keys — B-tree. `ci_embeddings` dimension1536 по существующему решению Engram; другой model той же размерности всё равно отдельный profile. Новый dimension — отдельная явная миграция, не смешивание векторами.
Широкие secondary JSONB indexes не создавать заранее. Explain Analyze на acceptance corpus определяет нужные дополнительные индексы.

`uci_exposures` requires unique `exposure_ref`, unique `(auth_realm, client_session_ref, idempotency_key)`, and `idempotency_binding_digest`. Its Source/Checkout/View keys must prove one authorized realm/source tuple. `uci_completion_evidence` has an FK to `uci_exposures`, unique `(exposure_id, supported_host_ref, idempotency_key)`, and `idempotency_binding_digest`. B-tree indexes serve idempotency lookups and callback binding. Neither table needs a content, query, path, or tool-output index.

## Что такое опубликованный view

View — coherent recorded file manifest за окно наблюдения, а не обещание атомарного snapshot всего работающего filesystem. Source-of-truth — bytes, прочитанные с known hashes. Watch seq и observed HEAD фиксируются как факты. Изменения после окна относятся к следующему поколению.
Publish делает одновременно видимыми memberships, актуальный lexical index через эти memberships, resolved edges и coverage. Queries никогда не соединяют new-file chunks с old-file graph под одной меткой current.
Embedding и model-derived annotations могут догонять позже. Они строго привязаны к artifact/profile/evidence digest. В ответе указаны vector coverage и enrichment watermark; отсутствие нового embedding не разрешает вернуть старую версию файла. Для воспроизводимой pagination query token фиксирует view, enrichment watermark, query digest и ACL epoch.

## Алгоритм обновления

1. При разрешённом bind checkout восстановить local registry и watch. Native fsnotify события — сигналы возможных изменений, не окончательная истина. Persist dirty paths, local monotonically increasing fs_seq, `rescan_required`; очередное событие пути coalesces прежнее.
2. Debounce 250–500 ms, максимальная задержка batch 2 s. Это настраиваемые начальные параметры; values не являются измеренной гарантией. Новый event во время обработки ставит следующий sequence, не теряется при снятии dirty flag.
3. Worker получает server lease для одного checkout/incarnation с монотонным epoch. Parse выполняется вне DB transaction; другой checkout не ждёт этот lease.
4. Читать файл через безопасный root-relative путь: no symlink escape, size/type/ignore/secret checks, stat-read-stat и hash. При изменении во время чтения retry bounded; иначе `unreadable/changing`, не фиктивный delete. Подтверждённое отсутствие файла даёт deletion intent.
5. Составить candidate manifest из предыдущего published membership плюс delta. Reuse parse artifacts; новые bytes разбираются один раз. Parse error нового файла даёт fresh text и partial graph coverage; старые связи этой области не переименовываются в актуальные.
6. Определить affected dependency closure и пересчитать разрешение ссылок. Публикация не ждёт LLM/embeddings. Если large config change требует source-wide resolution, это один bounded background rebuild, старый view пока доступен как stale.
7. Upload в staging по idempotent build ID, manifest part counts и content digests. Части и EOF сами не меняют current pointer. Server повторно проверяет binding, source access, epoch, bytes/digests/limits; client-provided hash не принимается на веру.
8. Explicit finalize проверяет полноту manifest/delta и готовность обязательных structural products. Короткая transaction блокирует checkout row, сверяет expected previous view и lease epoch, закрывает старые membership/edge intervals, вставляет новые, создаёт published view и переключает pointer. Failure rolls back всё. Big staging writes не удерживают checkout lock.
9. ACK содержит durable view_id/generation. Только после ACK local dirty entries снимаются при условии `entry.seq <= accepted_seq`; более свежие события остаются. Lost ACK → replay same build ID получает прежний result, а не повторный sweep.
10. Async jobs embedding/enrichment берут только актуальный input fingerprint. Запоздалый результат может сохранить reusable artifact, но не переключает checkout назад и не публикует outdated semantic edge.

## Полнота и empty index

Empty manifest — легитимный результат удаления всех файлов, но только explicit finalize complete scan с expected parent и epoch. Stream без сообщений, error при обходе, timeout или missing root не являются пустым corpus.
Full reconciliation также требует census coverage: unreadable directories не переводятся в массовые deletions. Удаляются только достоверно отсутствующие entries; uncertainty отмечается и блокирует destructive replacement этой части.
Публикация через метки index_session_id и `DELETE WHERE session_id<>new` из старого code index запрещена. Она смешивает владельцев и может удалить рабочие данные другой view.

## Потеря событий, restart и offline

Fsnotify overflow, dropped queue capacity, watch-registration error, moved root, startup и Git ref/index transition выставляют rescan_required. Local SQLite сохраняет dirty-set/checkpoint; при недоступной local DB состояние становится unknown и запускается полный безопасный rescan.
Периодический reconcile active roots — независимо от успешных events. Network filesystem/WSL boundary без надёжных events получает polling/degraded status. Отсутствие события никогда не интерпретируется как доказательство currentness.
При server outage основной агент продолжает работу; watch coalesces последние состояния файлов в bounded local DB. Full corpus/полные prompts в offline queue не записываются; при переполнении остаётся rescan_required, не неограниченный event archive. На reconnect индексируется текущее состояние, не последовательно проигрываются все промежуточные saves.
Lease expiry не делает old writer правомочным: finalize с прежним epoch отвергается. Новый process owner сначала узнаёт published view, затем пересобирает delta. Exactly-once filesystem processing не обещается; достигаются idempotent upload и один fenced publish.

## Изменение зависимостей

Изменение файла B способно изменить смысл неизменённого A. Поэтому checksum-only parser cache не заменяет resolver invalidation. Использовать reverse import/reference sites, module/export fingerprints и conservative resolution fallback.
Изменение export/signature/module path пересчитывает входящие ссылки; rename/remove закрывает прежние endpoints. Вновь появившийся module требует повторной попытки ранее unresolved sites подходящего namespace. Lockfile/go.mod/go.work/tsconfig/solution/build tags изменяют profile и затрагиваемую closure. Для неизвестной зависимости — широкий resolve, но без повторного embedding неизменённых chunks.
Relation к удалённому symbol не сохраняется как current просто потому, что исходный caller файл не изменился. В тестах обязательно покрыть importer unchanged / callee changed.

## Планирование ресурсов

Справедливость по checkout: interactive/current-source work выше idle history/enrichment. Default ограничить тяжёлые parse/embedding workers и их DB concurrency; не занимать весь существующий pool из10 connections. Один активный source с тысячами файлов не должен вытеснять другие сессии.
Все разрешённые checkout обнаруживаются автоматически, но stale/offline/неиспользуемые worktrees не получают бесконечные watchers. Active bindings держат watch; idle retention/polling policy видима через status. Query к idle tree запускает catch-up, не выдаёт old state как fresh.
Лимиты file bytes/chunks/symbols/edges, pending jobs и per-source disk quota обязательны. Limit reached сообщает partial coverage и путь/причину; не тихое усечение хвоста функции.

## Retention и удаление

Начальные параметры: current view всегда сохраняется; последние32 поколения плюс published views за24h, временные query pins до30min. Это configurable policy, а не вечный event journal. Большая pinned история требует квоты и явного решения.
GC сохраняет artifacts, достижимые из retained view/pin/valid analysis. Temporal history не удаляется, пока нужный pinned view опирается на interval. Jobs с obsolete input отменяются либо оставляют только reuse cache в лимите. GC batch small, не full-table lock.
UCI evidence имеет отдельный configured bounded retention period. Пока запись retained, `uci_exposures` и `uci_completion_evidence` append-only; retention transaction удаляет completion child перед parent без update retained record. Evidence не становится authorization record или source-content archive.
Обычный PostgreSQL backup/restore включает evidence tables как данные, а не пересоздаёт их из code corpus. Restore verifier пересчитывает каждый canonical binding digest, проверяет FK completion→exposure, Source/Checkout/View realm invariant и closed enums. Любая ошибка integrity оставляет recorder `unavailable`; она не даёт fabricated record или stale receipt.
Unregister checkout не удаляет Source и предметные данные. Explicit source erase проверяет полномочия и удаляет index-derived bodies/embeddings/annotations согласно policy. Privacy revocation применяется к старым view/pins немедленно в query boundary; pinned history не является обходом ACL.

## Уточнения cache keys и capture policy

Общий view profile разбивается на независимые fingerprints: syntax-extraction, link-resolution, admission/ignore и embedding. Parser artifact key включает только действительные входы синтаксического разбора; изменение ignore policy не должно пере-parse-ить и пере-embed-ить оставшиеся неизменённые файлы. Изменение build/module environment инвалидирует link resolution, но не автоматически embedding input. Все входы, реально влияющие на результат, обязаны входить в соответствующий ключ.
Для Git перечислять tracked + разрешённые untracked через NUL-safe Git plumbing. Tracked файл не исчезает только от нового правила .gitignore; явные Engram exclusions и неизменяемые secret/root ограничения применяются отдельно. Nested .gitignore/negation для untracked проверяются Git-compatible implementation. Репозиторный ignore/config не может расширить разрешённый root или снять protected secrets exclusion.
Commit views используют отдельную read-only `commit_reader` checkout identity, связанную с разрешённым Git object source. Она не является рабочим каталогом, не имеет file watcher и не меняет current working-tree pointer. В первой поставке обязательны working_tree views; commit_reader включается вместе с историческим diff.
Staging rows не доступны обычным queries. Логическое содержание published view неизменно; его operational state/retention metadata может перейти в superseded/retired без переписывания manifest или source facts.
