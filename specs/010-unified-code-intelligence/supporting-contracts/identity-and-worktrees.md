<!--
Adopted from: .agent/intake/engram-code-intelligence-2026-09-05-r1/03-IDENTITY-AND-WORKTREES.md
Adopted: 2026-09-05
Status: Working supporting contract for feature 010, synchronized with the accepted portable-project mandate of 2026-10-12. The intake source remains read-only historical provenance, not a live dual-project identity requirement.
-->

# Адресация, worktree и модель контекста

## Один логический Project ID, независимые Source/Checkout/View

Основание: `.agent/intake/engram-project-identity-rebuild-2026-10-12-r1/DELIVERY-BRIEF.md` и `MAIN-SEAMS.json`. Они заменяют прежний identity-as-authorization контракт и вторую идентичность проекта через Space UUID. Имя файла и существующие ссылки на этот supporting contract сохраняются; новый ADR или pipeline не вводится.

**Project** — логический проект знаний и работы, с Git или без него. `project_id` — один постоянный переносимый UUID, одинаковый в `.engram-project`, кеше, клиентском/серверном контексте, MCP, БД и UI. `name` — изменяемая подпись. UUID не credential, ACL, capability или owner изменяемого индекса. Путь, remote, branch, машина и principal не создают другую идентичность проекта.

**Source** — независимо адресуемый источник: Git repository или directory/document corpus. `source_id` — отдельный адрес источника, не второй Project ID и не хеш пути, origin, имени или содержимого. Принадлежность к проекту хранится в `sources.project_id` либо подтверждённой migration membership. Она не даёт права чтения и не выбирает рабочую копию. Несколько Source могут иметь один `project_id`.

Исторические `Space`, `space_id`, `spaces`, `space_sources` и прежние typed aliases сохраняются там, где нужны для чтения данных, receipts и безопасной миграции. Они не назначают второй UUID проекта, не служат нормальным project selector и не требуют регистрации Space для разрешения переносимого ID. Старые планы/data-model/migration notes, которые предписывали перенос всех предметных данных в новый Space, описывают заменённую модель, а не действующее поручение.

**Checkout** — конкретная локальная рабочая копия source на конкретной машине, включая linked worktree. Имеет собственный `checkout_id` и `incarnation_id`, локатор, owner, разрешения и текущие Git-факты. Один source имеет много checkout. Каталог, созданный заново по старому пути, не наследует старую incarnation.

**View** — неизменяемая версия индексируемого набора файлов checkout. `view_id` фиксирует generation, manifest digest, observed HEAD/ref, dirty state, policy/parser/build profile и границы наблюдения. Запрос читает ровно один view на source; для сравнения явно выбираются два. View не равен commit: два dirty checkout одного commit имеют разные views.

Отдельный `analysis_profile_id` задаёт среду интерпретации (языки/версии парсеров, GOOS/GOARCH/tags, tsconfig/solution identity, scope/ignore policy). Это параметр view, не новая пользовательская сущность. Auth realm и principals переиспользуются из Engram; отдельный tenant/IAM-продукт не строится.

## Пример без ручного набора идентификаторов

```text
Project: NovaScript (project_id: один переносимый UUID)
  Source: desktop (Git repo, source R1)
    Checkout: main на HYPERION (C1)
      View: V101, HEAD a1…, clean
    Checkout: editor-fix на HYPERION (C2)
      View: V204, HEAD a1…, dirty, generation 8
  Source: backend (Git repo, source R2)
  Source: design-documents (directory corpus, source R3)
```

`codebase_search(query=..., context omitted)` из C2 использует V204 после проверки текущего client binding и доступа. Переименование `editor-fix` или branch не меняет C2. Переключение branch создаёт следующий view C2. Read-only запрос по проекту явно фиксирует source→view set и включает только разрешённые источники; общий Project ID не выбирает автоматически все worktree или последний dirty view.

В каждом ответе есть display labels, relative path и адрес источника. Технический URI для ссылки: `engram://source/<source_uuid>/view/<view_uuid>/entity/<entity_key>`. URI — locator, не bearer capability и не доказательство доступа.

## Публичный ContextRef и каталог проектов

```json
{
  "source_id": "source-uuid",
  "checkout_id": "checkout-uuid",
  "view_id": "view-uuid",
  "generation": 18,
  "profile_id": "profile-uuid"
}
```

Это существующая JSON-форма выбранного кодового снимка: `source_id`, `checkout_id`, `view_id`, `generation`, `profile_id`. В Go поле профиля называется `AnalysisProfileID`; новый wire key `analysis_profile_id` не вводится. Клиент обычно передаёт opaque `context_handle` либо текущий host context. Server/daemon формируют ContextRef после проверки отношений и разрешений. Несоответствующие source/checkout/view связи отклоняются, даже если каждое значение существует. Project UUID — фильтр принадлежности, не замена этого tuple или source/view filter.

`POST /api/code/contexts` сохраняет существующий binding payload `{tab_binding_id,document_proof,...extra}` и поля `source_ref`, `checkout_ref`, `view_ref`, `indexed_snapshot`, `selection_ref`. Не подменять opaque browser refs сырыми UUID из примера ContextRef. Необязательный query `project=<UUID>` пересекает сохранённую принадлежность Source с существующими code-read разрешениями. Каждая resolved запись содержит `project_id`; значения для UI берутся с сервера, не вычисляются из repository label, path или hash. Один UUID может встречаться у нескольких независимых Source/Checkout/View.

У retained unresolved записи `project_id:null` и `unresolved_reference`. Она доступна в отдельном разделе через `GET /api/projects/unresolved/{reference}/code` с bounded pagination и прежними privacy/access checks. Этот reader не создаёт context, grant или pin и не объединяет снимки. Не скрывать неизвестную принадлежность успешным пустым ответом и не выдавать старый selector за UUID. Если существующий versioned serializer отдаёт необязательный исторический `space_id`, это metadata прежней группировки, не переносимый Project ID и не новый identity fence.

## Discovery: обычный Git и linked worktree

Daemon получает cwd именно текущего MCP client/session. Использует read-only Git plumbing с аргументами, а не shell interpolation: `rev-parse --show-toplevel`, absolute `--git-dir`, `--git-common-dir`, `--git-path HEAD`, `worktree list --porcelain -z`, HEAD/ref/object-format и status porcelain v2. Поддерживаемые версии Git проверяются при сборке; несовместимость даёт явный diagnostic, не парсинг human output.
Git child processes получают `GIT_OPTIONAL_LOCKS=0`, запрет интерактивного prompt, timeout и очищенные влияющие переменные. Не исполнять hooks, checkout, clean, reset, arbitrary config commands или репозиторные скрипты. Сами refs/config — недоверенные входные данные.

Common git dir группирует рабочие копии одного локального repository instance. Worktree-private git dir различает C1/C2, в том числе detached HEAD и одинаковые branch labels. Git-dir path сам не становится global ID.
Локальная SQLite registry связывает разрешённый root, OS filesystem identity где доступна, common/private git dir fingerprints и случайные instance IDs. Server регистрирует checkout под аутентифицированным workstation и проверенным source. Абсолютный Windows-путь остаётся локальным locator; remote queries не получают его без права на machine metadata.

Логический Project ID разрешает один Go-resolver по root текущего вызова: `.engram-project` содержит только `project_id` и необязательное `name`; реальный Git common-dir хранит локальный кеш принятого UUID в `engram-project.json`. Marker переносится между clone/машинами; common-dir — только локальный кеш. Marker не требует подписи, clean HEAD, staging или server approval. Старые branches без marker используют принятый common-dir cache. До закрепления ID допустим один atomic bootstrap по нормализованному первичному `origin` locator с проверкой доказанных aliases/migration mapping. После закрепления чтение marker/cache работает offline и не выполняет V3 authorization/registration/audit roundtrip. Настоящий marker/cache конфликт возвращает `PROJECT_IDENTITY_CONFLICT`, а не last-writer-wins или третий UUID. Нет marker/cache/locator — `PROJECT_CONTEXT_REQUIRED`, не path hash. Показ tools/list не зависит от project context; ошибка scoped operation не закрывает MCP transport.

## Первый source, новый clone и fork

При первом разрешённом indexing регистрируется Source/Checkout и сохраняется его принадлежность к уже выбранному логическому UUID. Существующие auth/registration permissions применяются к созданию источника и доступу к коду, не к чтению Project ID. Регистрация Space или identity capability не требуется.
Другой checkout одного common-dir получает тот же Project ID из кеша. Отдельный clone без marker может получить его через atomic bootstrap по первичному locator; Git-host API/OAuth не требуется. SSH/HTTPS, `.git` и стандартные порты нормализуются; case-sensitive пути и нестандартные порты не склеиваются. После закрепления ID смена remote его не меняет. Независимые offline-копии без общего marker/locator/binding нельзя гарантированно объединить по локальным данным.
Копия с тем же marker по умолчанию — тот же проект; намеренное отделение fork/template требует явного нового ID. Общий upstream, basename или HEAD не объединяет разные первичные locators. Source/Checkout/View сохраняют независимые адреса, grants и dirty snapshots, а не один mutable индекс. Открытие произвольной папки не создаёт проект; новый non-Git проект создаётся только явным `engram project init` в выбранном каталоге.

## Перемещение и пересоздание каталогов

Move внутри доступного filesystem при сохранении проверенной local instance identity обновляет locator checkout, не source/checkouts UUID. Если continuity достоверно восстановить нельзя (потеря local DB, copy вместо move, смена машины), создаётся новая incarnation; существующие views остаются историей, но не выдаются как новый current state.
Пересоздание пути после удаления всегда получает новую incarnation. Временная недоступность диска/сетевого mount означает `offline`, не удаление source. Git `worktree prune` не удаляет notes, issues или source. Явный checkout unregister прекращает watch и снимает current pointer; исторические index versions убираются политикой retention.

## Несколько агентов и один общий daemon

Binding хранится по transport client/session, а не в глобальном map `projectID→cwd`. Request path:
`client binding → local checkout evidence → server-authorized Source/Checkout → selected View`.
Подключение клиента B не меняет default context A. Один watcher/index owner обслуживает один checkout независимо от числа клиентов. Два разных checkout получают разные очереди и lease. Сессия, сменившая cwd, должна явно re-resolve binding; нельзя переиспользовать token с устаревшим root.
MCP-host без надёжного cwd использует явный source/checkout selection через `codebase_context`. При нескольких допустимых рабочих копиях ответ `context_required`, а не первый/последний индекс проекта. Старый project-only клиент не допускается к dirty view другого человека.

## Commit, branch и index

Branch ref — mutable alias к commit внутри source. Branch rename не меняет identity. Sanitized branch string не используется как уникальность. Detached HEAD и unborn HEAD — поддержанные состояния; unborn view имеет nullable commit и обычный file manifest.
Первый релиз читает saved working tree. Staged index и working-tree bytes не смешиваются: наличие staged изменений — metadata, индексируются реальные сохранённые файлы. Отдельный запрос staged/commit view требует явно выбранного view kind и своей проверенной materialization, не тихой подмены.
Исторический commit-view строится из Git object contents read-only, не временным checkout и не мутацией пользователя. Его manifest включает tracked files указанного commit; untracked/dirty files из текущей рабочей копии туда не добавляются.

## Monorepo, submodule, nested repository

Пакет/поддиректория monorepo — component/path filter внутри Source, не новый Source или Project автоматически. Только явный собственный переносимый marker задаёт вложенную логическую границу. Semantic module identity учитывает package/module namespace и profile.
Submodule — самостоятельный Git-root и Source с pinned gitlink reference из родительского view; логический проект разрешается по его собственному контексту, если явно не выбран иной проект. Разрешение на чтение parent не разрешает indexing child. Nested repo, включая `.agent` в Engram, подключается как отдельный Source только после разрешённого подключения; это не расширяет scan из родительского проекта.
По умолчанию `.agent`, `.git`, outputs/caches и чужие вложенные worktree исключены. Для приватных проектных документов `.agent/specs`/`intake` можно подключить отдельный document source с ограниченным allowlist. Это не расширяет scan на `.agent/worktrees`, `.env`, credentials и session transcripts.

## Права: знания и исходники — разные измерения

Project membership не ACL. Source grant определяет доступ к общим исходникам; checkout grant может дополнительно ограничивать неопубликованную работу. Private dirty view не становится виден всем читателям проекта. Один owner у разных Sources не означает cross-source разрешение; выбор UUID в UI не создаёт grant.
Notes/issues/rules/docs/state сохраняют текущие private/project/shared/global и principal/domain semantics. Их проектная принадлежность использует тот же логический UUID; visibility/owner остаются отдельными полями. Глобальные данные и credentials не превращаются в проектные. Исторические Space references и immutable receipts читаются по исходной схеме без переписывания cryptographic inputs.
Queries/graph traversal/embeddings cache lookup разрешают только объекты выбранного контекста. Привилегия content-addressed dedup не транслируется в публичный API «существует ли этот hash у другого principal».

## Typed aliases и миграция

Исторический alias key `(auth_realm, legacy_domain, scheme, value, optional_client_namespace)` различает происхождение старых значений. Realm/domain здесь — контекст миграции и данных, не часть переносимого Project ID. `legacy_slug`, V2 binding, V3 anchor и прежний Space UUID не становятся normal project choices. Alias mapping сохраняет state `resolved|ambiguous|unmapped|retired`, provenance и revision; он не выбирает Checkout/View и не выдаёт доступ. Нормальное разрешение уже закреплённого UUID не обращается к старому resolver.

Доказанная группа получает один сохраняемый пригодный UUID, а не новый server/Space UUID поверх marker. Совпадение name/basename/hash или общего upstream — только подсказка, не основание merge. Один смешанный selector требует per-record membership. Одному проекту могут принадлежать несколько Sources, но неизвестная принадлежность остаётся unresolved с доступным происхождением, а не угадывается по пути или истории Space. Прежние aliases — история и bounded migration translation; после перевода consumers временный runtime удаляется, постоянный shim не добавляется.

Dry-run, репетиция на восстановленной копии и согласованный cutover сохраняют IDs объектов, версии, даты, links/comments, различные значения, privacy, grants и decryptability, включая project-key/AAD. Unique-key коллизии разбираются явно, не через потерю строк в `ON CONFLICT DO NOTHING`. Повтор после сбоя и восстановление учитывают новые записи между snapshot и cutover. Production mutation этим контрактом не выполнена.

Смена Project ID не переиндексирует и не переэмбеддит неизменённый код: сохраняются membership, опубликованные Views и существующая история; перестраивается только конкретный derived cache с изменившимся ключом. Старый индекс без достоверной checkout/view provenance остаётся `legacy_unscoped` для безопасного чтения/rollback, без выдуманного HEAD, Checkout или View. Если нужен новый снимок, он строится из настоящих разрешённых файлов, а не из догадки о старом selector.

## Проверяемые инварианты

Каждый published view принадлежит ровно одному source, checkout/incarnation и analysis profile. SourceRef одного запроса нельзя незаметно заменить на source соседа.
Один Project UUID проходит marker/cache/client/server/MCP/DB/UI без второго Space identity fence. Путь/remote/branch/label изменяемы без потери предметной памяти. Два dirty trees не смешиваются даже при одинаковых Project ID, Source, branch и HEAD.
Legacy migration не расширяет права; ambiguous mapping не выполняет mutation и не прячет unresolved данные. Нормальное чтение принятого ID не требует повторного onboarding на turn/worktree. Source/Checkout/View registration, приватность, fencing, leases, bounded queries и ресурсные лимиты сохраняют свои существующие границы независимо от identity.
Ссылка `migration.md` сохраняет прежние правила backup/restore, privacy, history и безопасной работы с derived data. Её исторические требования нового Space, переноса предметных данных на `space_id` и V3 identity approval заменены текущим мандатом, а не разрешают второй project runtime. Исходный intake, старые plans/checklists и receipts — исторический контекст; эта синхронизация не утверждает behavioral, installed или production acceptance.
