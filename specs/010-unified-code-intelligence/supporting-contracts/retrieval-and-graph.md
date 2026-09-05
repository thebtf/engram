<!--
Adopted from: .agent/intake/engram-code-intelligence-2026-09-05-r1/05-RETRIEVAL-AND-GRAPH.md
Adopted: 2026-09-05
Status: Working supporting contract for feature 010. The intake source remains read-only provenance.
-->

# Извлечение, поиск и граф

## Общий вход, разные вопросы

Один file version порождает syntax facts, chunks и reference sites. Эти продукты используются и search, и graph. Нельзя отдельно пройти репозиторий для Graphify, отдельно для SocratiCode, а затем склеить ответы по именам файлов: их версии могут различаться.
Основной путь: `AuthorizedContext → pinned View → exact/FTS/vector candidates → bounded graph expansion → source-grounded result`. Запрос graph не обязан вызывать embedding, а точное чтение по известному file/symbol ref не обязано сначала выполнять semantic search.

## ADR-PARSE-01: локальный ограниченный parser worker

Первый Go tracer использует стандартный Go AST. Для JS/TS/TSX и последующих C#/Python выбран tree-sitter через Go binding с ограниченным набором грамматик. Поставляется подписанный/проверяемый приватный parser worker в комплекте Engram (отдельный local child process допускается), не Python/NetworkX daemon и не новый сетевой сервис.
Parser protocol принимает ограниченные bytes + language/profile, возвращает definitions/reference sites/chunks/diagnostics. Не имеет server/admin key, не исполняет код проекта, не загружает репозиторные plugins и не устанавливает dependencies. Process boundary ограничивает crash impact, но сама по себе не является OS sandbox: time/memory/output caps и очищенное окружение нужны отдельно.
Для tree-sitter Go/CGo поставки проверить Windows amd64, Linux amd64 и поддержанные macOS build targets. Версии runtime/grammar и build toolchain зафиксировать в parser_bundle_digest. Никакого `latest` при startup. Нельзя обещать поддержку языка только потому, что его grammar загружена.
Go type enrichment (`go/types`, разрешённый package loader), TypeScript compiler/Roslyn — поздние read-only adapters. Не запускать `npm install`, build hooks, генераторы или project-controlled MSBuild targets из запроса. Без безопасного type environment возвращать синтаксический уровень и `resolution=partial`, а не выдумывать типовую точность.

## Минимум извлекаемых фактов

File/module/package, namespace, class/interface/type, function/method, declaration/export/import; byte/line spans и signature; call/reference sites; comments и явные ADR/doc/path links. Структурные config/schema adapters выделяют OpenAPI paths/operations, SQL tables/columns/FK, JSON/YAML keys и локальные ссылки.
SQL schema extractor читает разрешённый DDL как текст. Он не подключается к production-БД и не выполняет SQL. OpenAPI external refs не скачиваются автоматически. Framework-aware route extraction — отдельный rule с названием/версией и явным уровнем уверенности.
Markdown headings/links — deterministic facts. Natural-language утверждение о том, что код «реализует требование», не становится verified implements edge только из текстовой близости.

## Символы и источники

`entity_key` строится из source + relative file identity + artifact + extractor-local symbol key. Внутри artifact symbol key учитывает kind, lexical namespace, declaration path и overload/signature shape. Номер строки — locator, не identity.
Имена `Run`, `main`, `Client` не уникальны. Объяснение по неоднозначному имени возвращает доступные кандидаты. Для вложенных функций, anonymous lambdas, partial classes и overloads не подделывать стабильность, которой нет.
`lineage_key` для diff между версиями — отдельная best-effort связь. Exact entity reference всегда проверяется against view membership. Rename matching эвристика не перенаправляет старую цитату на новый произвольный символ.

## Chunking без потери содержимого

AST-aware chunks используют declaration boundaries; большие функции делятся на bounded subsections с целыми UTF-8 spans. Config/prose делятся структурно, unsupported language — line/text fallback с явным coverage. Длинный хвост функции нельзя тихо отбросить, чтобы попасть в embedding limit.
Source bytes, search text и embedding input различаются. Embedding input может включать language/relative path/symbol heading, но этот префикс не выдаётся за исходную строку. Fingerprint включает preprocessing revision, provider/model/dimension, path-inclusion policy и точные input bytes. Absolute path/worktree name не входит в embedding text.
Для mixed CRLF/LF хранить точные source hashes и span mapping. Допустим reuse embedding нормализованного текста только по явно версионированному preprocessing; нельзя путать это с идентичностью исходного файла. Пробелы/rename могут пересоздать locations без повторного embedding при неизменном нормализованном входе.

## Доказательность каждой связи

Закрытая классификация:
`EXTRACTED` — наблюдаемая конструкция/ссылка в source, например import site;
`RESOLVED` — однозначно связанная цель по определённому static resolver/profile;
`HEURISTIC` — кандидат по неточной эвристике, не доказанная зависимость;
`SEMANTIC` — model-derived утверждение со ссылками на источники и модель/параметры;
`UNRESOLVED` — site существует, но цели нет/она неоднозначна.

Для binding-target call используется relation `calls` только на разрешённой цели; неоднозначная гипотеза — `may_call` с отдельным evidence_kind. В directed multigraph допускаются несколько call sites и relation kinds между одной парой symbols; их нельзя схлопывать в один undirected edge.
Evidence включает source artifact/span, target artifact где известен, extraction/resolver revision, rule key и bounded explanation. Weight/rank не являются вероятностью истины. Default impact не должен скрывать неполноту dynamic dispatch.
Примеры relations: `contains`, `imports`, `exports`, `references`, `calls`, `may_call`, `inherits`, `implements`, `documents`, `mentions`, `configures`, `schema_references`, `tests`, `depends_on`. Тест в похожем файле не получает `tests` без конкретного основания.

## Resolver и инкрементальность

Импорт разрешается в границах selected manifest и build profile. Учитывать aliases/re-exports/index modules/relative paths/package scopes там, где adapter поддерживает их. Не поддержанный resolver сообщает это в coverage.
Изменившиеся exported names, signatures и module locations инвалидируют входящие reference sites. Delete/rename закрывает прежние targets. Вновь добавленная definition может разрешить ранее unresolved site. Graph rebuild не означает re-embed всего репозитория.
Cross-source link разрешается лишь через выбранный source set, явную dependency mapping и target view. Совпадающее имя package в соседнем проекте не доказывает dependency. External package без исходников остаётся external reference с версией из manifest, не фальшивым локальным узлом.

## Search pipeline UCI-1

1. Resolve/authenticate context, pin view + profile. Сформировать authorized effective membership до candidate LIMIT.
2. Выполнить exact name/path/qualified symbol lookup и FTS по code/prose tokens. Сохранять original identifier и его camel/snake составляющие; lexer/version входит в index profile. Русский текст не фильтровать ASCII-only правилами.
3. При разрешённом healthy embedding profile вычислить один query vector; применить bounded timeout. Vector candidates берутся только из embeddings того же profile и видимых chunks выбранного view.
4. Соединить FTS и vector списки через RRF с фиксированным параметром (начально60), exact matches можно выделить отдельно с объяснением. Перекрывающиеся chunks одного symbol/file дедуплицируются с сохранением лучшего source span. Это ranking, не авторитет.
5. Необязательное расширение на один-два graph hops под бюджетом. Возвращать graph-added context отдельно от direct search hits, чтобы не выдавать его score за semantic match.
6. Обрезать только presentation/result list с `truncated=true`, не хранимые исходники. Перед response проверить ACL epoch; при изменении permissions переоценить/отказать, не выпустить устаревший доступ.

PostgreSQL FTS не называется BM25. Поздний BM25 engine получает свой lexical_profile и воспроизводимый relevance test; решение о нём принимается по запросам, где FTS действительно уступает. Наличие обоих источников не обязывает переносить Qdrant.

## Vector filtering и recall

Первый correctness path — точный vector distance по authorized current candidate universe в PostgreSQL. Это baseline для сравнения ANN, не обещание latency на40m LOC.
ANN включается после теста на worst-case selectivity: source/view membership может исключать большинство векторов общей таблицы. Недопустимо взять global top10 и затем выбросить чужие worktree, объявив «нет результатов». Использовать scoped indexes/partitions либо iterative search с достаточным budget; при недоборе — exact scoped fallback или honest partial/degraded result.
Hidden content никогда не материализуется/не попадает в scores/counts ответа. Внутренний физический index scan не заменяет логическую authorization перед ranking/limit над выдаваемым universe. Качество ANN измеряется относительно scoped exact baseline.
Provider switch той же dimension не совместим по смыслу векторов: создавать новый profile, backfill и атомарно выбирать healthy profile для запросов. До его готовности старый profile применим только если это явно разрешённый current profile, иначе lexical-only. Смешивать scores разных embedding spaces нельзя.

## Графовые операции

Explain возвращает один symbol/file, его источник, incoming/outgoing references и уровни доказательности. Neighbors поддерживает direction и relation filter. Path выполняет bounded directed traversal; undirected exploration — отдельный explicit режим.
Impact — reverse traversal по выбранным dependency/call relations, с раздельным представлением proved/static и may/heuristic путей. Flow — forward graph из entrypoint; не runtime execution trace. Cycles — SCC/import-cycle анализ; recursion внутри функции не смешивается с module cycles.
Начальные бюджеты: depth≤4 по умолчанию, максимум8; ≤5000 посещённых узлов, ≤200 узлов/400 рёбер в ответе, явный stop_reason при cap. Реализация — batched indexed adjacency queries с visited set или bounded recursive SQL, не загрузка всего корпуса в память на каждый вызов. Deadline и node budgets проверяются во время обхода, не только в финальном LIMIT.
`no_path` означает отсутствие пути в выбранном покрытом подграфе и разрешённых relations. При partial coverage/cap возвращать `unknown_or_truncated`, не доказательство отсутствия зависимости. Отсутствие callers не даёт права удалять код без source/test проверки.

## Обзор, communities и UI

UCI-1: module/directory grouping, fan-in/out, SCC и focused subgraph. UCI-2: analytics jobs на pinned view, центральные узлы, сообщества, связи между подсистемами и короткий generated overview. Algorithm/seed/revision фиксируются; результирующие labels — display data, не новая source authority.
Leiden или другой community algorithm не запускается после каждого keystroke. Он пересчитывается после structural quiet period/по запросу, с `analysis_view_id` и staleness. Новая версия графа не требует синхронно генерировать весь wiki.
Nuxt console получает отдельный read-only Code экран с Source/Checkout/View selector, поиском, focus-node neighbors/path/impact, индикатором stale/partial и source citations. Не путать его с existing memory Graph route. Большой graph загружается частями; full HTML export — опциональный снимок, не основной storage.

## Semantic enrichment документов

Это более медленный, опциональный слой. Worker получает только разрешённые изменённые excerpts/declared facts, возвращает bounded entities/relations с evidence spans. Server проверяет schema, существование источников/целей, ACL, parser/profile/view dependency digest и currentness.
Модель не задаёт source_id, scope, permissions, resolver truth или lifecycle исходников. Её edges имеют `SEMANTIC`, не EXTRACTED/RESOLVED. Prompt injection из документа не получает права вызывать инструменты или менять config.
При изменении evidence старое утверждение становится stale и исключается из default current query до recompute; его можно видеть в историческом view. Нельзя безусловно сохранять старую semantic связь ради неизменности topology и нельзя удалять все semantic данные из-за code-only обновления.
Server уже имеет provider/settings/vault основу. Новые endpoints/model API keys не пишутся в репозиторий, полный source corpus не отправляется по умолчанию. Rate/token budget и optional availability не блокируют structural index.
