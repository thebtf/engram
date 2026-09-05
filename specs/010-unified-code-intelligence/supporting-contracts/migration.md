<!--
Adopted from: .agent/intake/engram-code-intelligence-2026-09-05-r1/07-MIGRATION.md
Adopted: 2026-09-05
Status: Working supporting contract for feature 010. The intake source remains read-only provenance.
-->

# Миграция старой адресации и индексов

## Цель и главный запрет

Перейти от разных значений `project_id` к явным Space/Source/Checkout/View, сохранив данные, ссылки, права и работающих клиентов. Не присвоить старым смешанным code_chunks выдуманный worktree/commit. Не считать смену поля в JSON завершённой миграцией.
Миграция source indexing и предметных данных разделена: новый code index может приносить пользу, пока notes/issues ещё работают через совместимый mapping. Полный массовый rewrite не является предшественником UCI-1.

## MIG-0. Ограниченный census и резервное восстановление

Зафиксировать фактические схемы, к которым прикасается текущий срез: canonical project registry, aliases/bindings, code_chunks/index_sessions и first-party consumers. Для последующих domain batches расширять census по мере работы, не читать весь исторический архив заранее.
Для каждого legacy identifier различить домен, scheme, количество строк, разрешённый current owner/scope, source evidence и ambiguity. Одинаковая строка в двух разных схемах не сливает источники. Product-only project без Git является нормальным Space.
Перед изменением authority tables иметь проверенный backup/restore путь существующей БД. Read-only dry-run migration возвращает ожидаемые counts и ambiguous mappings, не credentials/body. В этом проектном проходе реальные production counts не получены.

`uci_exposures` и `uci_completion_evidence` — normal PostgreSQL evidence data, не rebuildable index projection. Backup/restore включает их как есть; после restore verifier проверяет append-only lifecycle, canonical binding digests, Source/Checkout/View relation, completion FK и closed enums. Нельзя replay code corpus или request log для восстановления evidence.

## MIG-1. Expand context registry

Добавить spaces, sources, checkout/view registry и typed aliases отдельными forward migrations с актуальными номерами. Не менять уже применённые migration168/169 и не брать предполагаемые170–172 без проверки HEAD.
Каждый однозначный legacy canonical project получает один Space и сохраняемый mapping. Никакого автоматического merge похожих labels. V3 repository/directory evidence создаёт связь с соответствующим Source после разрешённой регистрации. Если evidence недостаточно, Space создаётся без Source; предметная память при этом остаётся доступной по прежним правам.
Повтор миграции идемпотентен по `(legacy_domain,canonical_legacy_key)`. Mapping revision отдельна от row timestamps. Ошибка не создаёт половину пары Space+alias. При появлении конфликта старый readable data path сохраняется, новый ambiguous mutation закрывается.

## MIG-2. Новый индекс, не backfill выдуманной истории

Создать `ci_*` code-projection storage, `uci_exposures`/`uci_completion_evidence` durable evidence storage и новые source/view APIs. Для разрешённых активных checkout выполнить реальную начальную индексацию. Все новые index writes идут checkout-scoped и публикуются atomic finalize. Старые handlers не пишут в `ci_*` и не выполняют там project-wide sweep. Shared MCP boundary пишет UCI evidence только после authorized closed search/graph/read decision.
`code_chunks` и старые embeddings остаются `legacy_unscoped`. Это rebuildable projection, поэтому правильная миграция — пересобрать из source. Reuse старого embedding возможен только при доказанном совпадении точных input bytes, model/preprocessing/dimension и разрешённого source; неизвестный profile означает no reuse.
Установка новой версии не означает автоматического сканирования всех путей, найденных в старой БД. Выбрать current authorized roots и known indexed sources, остальные пометить pending discovery/offline. Неактуальные пути не становятся пустыми индексами.

## MIG-3. Переключение first-party code tools

Daemon связывает каждый MCP client с checkout. `codebase_search/status/index` идут в новый service. Старый аргумент project проходит bridge:
- при наличии client checkout binding и совпадении mapping — использовать этот checkout;
- при явном pinned legacy read — вернуть только старый snapshot с `legacy_unscoped` и без обещания worktree точности;
- при нескольких возможных source/checkout без binding — `CONTEXT_REQUIRED`;
- при конфликте argument vs bound context — ошибка, не расширение scope.

Совместимость не даёт права выбрать «main», последний обновлённый индекс или tree первого подключившегося клиента. Legacy diagnostics не участвует в обычном новом search corpus.
Новые private gRPC методы используют Source/Checkout handles; прежние protobuf поля не перенумеровываются. Для клиента, не понимающего новый контракт, версия/ошибка явны. Не возвращать старую форму успешного ответа с новым скрытым смыслом.

## MIG-4. Предметные данные: Space вместо предположения о Git

Мигрировать партиями через один application boundary, а не отдельными ad-hoc SQL в каждом handler. Новые routing поля, существующие ACL и version/provenance fields отделены. Transitional dual-write выполняется внутри одной DB transaction; два независимых consumer-writer не допускаются.

**Memories/notes.** Добавить `space_id`, optional typed source reference и сохранить original legacy routing fact. Backfill mapping1:1; visibility/owner/domain/source workstation/session остаются прежними. Не классифицировать автоматически note как code fact по словам в тексте. Existing memory IDs, versions, citations и status сохраняются.

**Issues.** Отдельно мигрировать source-space и target-space. Issue может координировать несколько sources продукта, а конкретный file/symbol ref — дополнительная ссылка. Закрытие/comment/source-authority права не ослабляются. Нельзя заменить оба старых проекта одним source_id.

**Rules.** Space/source applicability становится отдельным typed filter. Owner/approval/lifecycle не переписываются. Global rules не становятся глобальными code search grants. Rule text не индексируется как доказанное ребро кода.

**Versioned documents и collections.** Collection/domain authority сохраняется; space ownership и source references добавляются отдельно. Directory corpus derived copies не становятся новой версией authoritative document без доменной операции. Две видимые копии одного body могут иметь разные permissions.

**Credentials, tokens, encrypted settings.** Routing к Space не меняет ciphertext, keycard owner или server/workstation custody. Перед изменением любых crypto-bound identity полей проверить associated-data/derivation контракты. Нельзя подставить новый UUID в старый MAC/AAD. Использовать compatibility mapping вне криптографического сообщения либо явно версионированную migration с проверкой decrypt/verify; UCI-1 этого не требует.

**Sessions/state plane/goal/task.** Предметный scope переносится в Space, execution provenance — в optional Source/Checkout/View references. Прошлая задача не меняет место выполнения при branch rename. Session ID не является checkout ID и наоборот.

**Memory knowledge graph.** Его endpoints и lifecycle сохраняются. Если требуется связь со source symbol — typed external reference с view, не вставка code definition в memories. Синтаксический multigraph остаётся `ci_*`.

**IEP/HAP и audit receipts.** Immutable records не переписываются ради новых имён. Original schema/routing/cryptographic inputs сохраняются. Side mapping помогает навигации, но не меняет historical truth. Активный legacy session сохраняет свою identity epoch до завершения; миграция не создаёт второй occurrence из одного прежнего.

Новый Space, который должен быть доступен старому domain writer, получает opaque compatibility routing entry в той же transaction; ключ не выводится из label/remote. Где lossless legacy representation отсутствует, старому writer возвращается upgrade-required, а не выдуманное совместимое значение.

## MIG-5. Единственный writer и retirement

После перевода известных installed first-party clients и replay old/new contract matrix выключить legacy code writer. Наличие aliases для чтения истории не означает продолжение старого индексатора. Не оставлять две активно меняющие кодовую истину системы.
Проверить tools/list/call, daemon update/restart, active integration configs и наблюдаемые legacy calls на реальных клиентах. Не использовать нулевую телеметрию при отсутствии клиентов как доказательство отсутствия зависимостей. Известные внешние consumers перечислены явно; неизвестные не маскируются под «все мигрировали».
Старые project-shaped API/remove/purge routes убираются только после migration соответствующего домена. Alias history может оставаться ограниченной поддержкой old refs без runtime branching на каждом поиске.
Legacy code tables удаляются отдельной безопасной derived-data contraction после rollback window и restore проверки. Никаких `DROP memories`, массового merge or purge ради cleaner schema.

## Доказательство отсутствия расширения доступа

Для каждого мигрируемого домена сравнить old/new allow/deny на одинаковых principals, включая private/shared/global, empty principal/domain, revoked token, одинаковые labels и nested repos. Разрешённая область должна совпадать либо сознательно сузиться с documented migration warning; незаметное widening запрещено.
Backfill не лечит неоднозначный origin угадыванием. Запись остаётся в исходном Space/legacy namespace до подтверждённого mapping; новый поиск не смешивает её с чужим source.

## Rollback

До domain cutover: отключить новый code route, оставить `ci_*` code projections inert и `uci_*` evidence tables empty, предметные данные не меняются. Старый single-index path доступен только с явной legacy limitation, не как корректный multi-worktree fallback.
После expand/dual-write, но до contract: переключить application boundary на прежний routing с mapping, сохранить новые поля/историю. Проверить доступность старого binary с добавленной схемой на backup copy; не считать forward-compatible по предположению. UCI evidence восстанавливается только normal PostgreSQL backup/restore с integrity verification, не corpus rebuild.
После destructive derived index contraction: переиндексация источников допустима. После изменения authority schema восстановление использует проверенный backup/forward repair; этого не путать с простым «откатить Docker image». Никакой rollback не возвращает секреты в extension и не смешивает current checkout data.

## Почему миграция не должна снова стать церемонией

Один mapping registry, один рабочий миграционный отчёт с фактами и один текущий source of truth. Не требуются отдельные approvals на каждый alias, новые идентичности для каждой папки или повторная классификация всех воспоминаний человеком.
Первая полезная вертикаль заканчивается новым контекстным поиском двух worktree. Расширение domain migration продолжается после неё без переписывания всего продукта заранее.
