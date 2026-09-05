<!--
Adopted from: .agent/intake/engram-code-intelligence-2026-09-05-r1/03-IDENTITY-AND-WORKTREES.md
Adopted: 2026-09-05
Status: Working supporting contract for feature 010. The intake source remains read-only provenance.
-->

# Адресация, worktree и модель контекста

## ADR-ID-01: отказаться от одного перегруженного project_id

Не заменять `project_id` на другой UUID с прежней семантикой и не добавлять branch suffix. Сейчас один термин означает пользовательский проект, Git repository, remote-derived slug, principal scope и owner изменяемого индекса. Эти сущности имеют разные сроки жизни и правила доступа.

Вводятся четыре понятия. Нового универсального EAV registry для всех объектов продукта не требуется.

**Space** — логическое пространство знаний/работы: например, продукт NovaScript с desktop, backend и документацией. Может не иметь Git. В нём живут notes/issues/documents/rules по существующим доменным правилам. `space_id` — стабильный server-assigned UUID; название — изменяемый label.

**Source** — независимо адресуемый источник: Git repository или directory/document corpus. `source_id` — UUID, не хеш пути, origin, имени или содержимого. Один source может быть связан с несколькими spaces; эта связь сама по себе не даёт права чтения.

**Checkout** — конкретная локальная рабочая копия source на конкретной машине, включая linked worktree. Имеет собственный `checkout_id` и `incarnation_id`, локатор, owner, разрешения и текущие Git-факты. Один source имеет много checkout. Каталог, созданный заново по старому пути, не наследует старую incarnation.

**View** — неизменяемая версия индексируемого набора файлов checkout. `view_id` фиксирует generation, manifest digest, observed HEAD/ref, dirty state, policy/parser/build profile и границы наблюдения. Запрос читает ровно один view на source; для сравнения явно выбираются два. View не равен commit: два dirty checkout одного commit имеют разные views.

Отдельный `analysis_profile_id` задаёт среду интерпретации (языки/версии парсеров, GOOS/GOARCH/tags, tsconfig/solution identity, scope/ignore policy). Это параметр view, не новая пользовательская сущность. Auth realm и principals переиспользуются из Engram; отдельный tenant/IAM-продукт не строится.

## Пример без ручного набора идентификаторов

```text
Space: NovaScript
  Source: desktop (Git repo, source R1)
    Checkout: main на HYPERION (C1)
      View: V101, HEAD a1…, clean
    Checkout: editor-fix на HYPERION (C2)
      View: V204, HEAD a1…, dirty, generation 8
  Source: backend (Git repo, source R2)
  Source: design-documents (directory corpus, source R3)
```

`codebase_search(query=..., context omitted)` из C2 использует V204. Переименование `editor-fix` или branch не меняет C2. Переключение branch создаёт следующий view C2. Read-only запрос по всему Space фиксирует source→view set и включает только разрешённые источники; он не ищет одновременно во всех worktree без явного желания пользователя.

В каждом ответе есть display labels, relative path и адрес источника. Технический URI для ссылки: `engram://source/<source_uuid>/view/<view_uuid>/entity/<entity_key>`. URI — locator, не bearer capability и не доказательство доступа.

## Публичный ContextRef

```json
{
  "space_id": "optional-space-uuid",
  "source_id": "source-uuid",
  "checkout_id": "checkout-uuid",
  "view_id": "view-uuid",
  "generation": 18,
  "analysis_profile_id": "profile-uuid"
}
```

Клиент обычно передаёт только opaque `context_handle` либо ничего. Server/daemon формируют канонический ContextRef после проверки. Полный объект из запроса — selector/evidence, не authority. Несоответствующие source/checkout/view связи отклоняются, даже если каждое значение по отдельности существует.
`space_id` необязателен для просмотра кода и никогда не используется вместо source/view filter. Наличие source в space не отменяет source grant или private checkout ACL.

## Discovery: обычный Git и linked worktree

Daemon получает cwd именно текущего MCP client/session. Использует read-only Git plumbing с аргументами, а не shell interpolation: `rev-parse --show-toplevel`, absolute `--git-dir`, `--git-common-dir`, `--git-path HEAD`, `worktree list --porcelain -z`, HEAD/ref/object-format и status porcelain v2. Поддерживаемые версии Git проверяются при сборке; несовместимость даёт явный diagnostic, не парсинг human output.
Git child processes получают `GIT_OPTIONAL_LOCKS=0`, запрет интерактивного prompt, timeout и очищенные влияющие переменные. Не исполнять hooks, checkout, clean, reset, arbitrary config commands или репозиторные скрипты. Сами refs/config — недоверенные входные данные.

Common git dir группирует рабочие копии одного локального repository instance. Worktree-private git dir различает C1/C2, в том числе detached HEAD и одинаковые branch labels. Git-dir path сам не становится global ID.
Локальная SQLite registry связывает разрешённый root, OS filesystem identity где доступна, common/private git dir fingerprints и случайные instance IDs. Server регистрирует checkout под аутентифицированным workstation и проверенным source. Абсолютный Windows-путь остаётся локальным locator; remote queries не получают его без права на machine metadata.

Для существующего V3 `.engram-project` выполняется текущая авторизованная V3 resolution, затем lookup в migration mapping. Anchor — evidence/alias, не ключ доступа. Source регистрации новых репозиториев не требует обязательно коммитить текстовый marker; default identity хранится в server/local DB. Опциональный portable source hint допустим, но не даёт прав и не объединяет forks автоматически.

## Первый source, новый clone и fork

При первом разрешённом indexing source регистрируется server-side в выбранном space или без space. Существующие auth/registration permissions применяются до creation. Это нормальный onboarding, не ручная классификация каждого файла.
Другой checkout того же common Git instance автоматически получает тот же source после проверки локального binding. Новый clone по одному remote URL не объединяется без дополнительного подтверждённого registry mapping. GitHub repository ID/проверенный hosting locator могут быть evidence, но не заменяют server permissions.
Скопированный anchor, одинаковый HEAD или переименованный origin не являются достаточными для merge identities. Fork по умолчанию отдельный source; возможна связь `fork_of`, не общий изменяемый индекс. Ambiguous legacy source остаётся отдельным, а не угадывается по большинству строк.

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

Пакет/поддиректория monorepo — component/path filter внутри source, не новый source автоматически. Semantic module identity учитывает package/module namespace и profile.
Submodule — отдельный source с pinned gitlink reference из родительского view. Разрешение на чтение parent не является разрешением индексировать child. Nested repo, включая `.agent` в Engram, — отдельный source только после разрешённого подключения.
По умолчанию `.agent`, `.git`, outputs/caches и чужие вложенные worktree исключены. Для приватных проектных документов `.agent/specs`/`intake` можно подключить отдельный document source с ограниченным allowlist. Это не расширяет scan на `.agent/worktrees`, `.env`, credentials и session transcripts.

## Права: знания и исходники — разные измерения

Space grouping не ACL. Source grant определяет доступ к общим исходникам; checkout grant может дополнительно ограничивать неопубликованную работу. Private dirty view по умолчанию не становится виден всем читателям repository. Один и тот же owner у разных sources не означает автоматическое cross-source разрешение.
Notes/issues/rules сохраняют текущие private/project/shared/global и principal/domain semantics при миграции. Новый `space_id` определяет предметную принадлежность, а visibility/owner остаются отдельными полями. Immutable исторические receipts интерпретируются по исходной схеме.
Queries/graph traversal/embeddings cache lookup разрешают только объекты выбранного контекста. Привилегия content-addressed dedup не транслируется в публичный API «существует ли этот hash у другого principal».

## Typed aliases и миграция

Alias key: `(auth_realm, legacy_domain, scheme, value, optional_client_namespace)`; value не глобально уникален без scheme. `legacy_slug`, V2 binding, V3 anchor и canonical UUID не складываются в одну строковую карту.
Alias mapping хранит target space/source по роли, state `resolved|ambiguous|unmapped|retired`, provenance и revision. Одно старое значение может быть разными facts разных доменов; разрешение требует указать домен.
Legacy project UUID сначала отображается на один Space без merge и переписывания данных. Проверенный repository anchor может дополнительно привязать Source. Если один старый project фактически объединял несколько репозиториев, это один Space и несколько Sources, а не lossful split его memories.
Старый code index не имеет достоверного checkout/view provenance. Его строки остаются помеченными `legacy_unscoped` для rollback/диагностики, но никогда не получают фиктивный HEAD или checkout. Новый индекс восстанавливается из настоящих разрешённых файлов.

## Проверяемые инварианты

Каждый published view принадлежит ровно одному source, checkout/incarnation и analysis profile. SourceRef одного запроса нельзя незаметно заменить на source соседа.
Путь/remote/branch/label изменяемы без потери предметной памяти. Смешение двух dirty trees невозможно даже при одинаковом source, branch и HEAD.
Legacy migration не расширяет права; ambiguous mapping не выполняет mutation. Нормальный известный source не требует повторного onboarding на каждый turn/worktree.
Полная стратегия expand/migrate/contract приведена в `migration.md`; она охватывает весь Engram, а не только новые code tables.
