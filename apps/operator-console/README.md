# engram Operator Console — static Vue runtime

This directory is deployable Vue 3/Vite source, not a frozen design-fidelity scaffold. The
authoritative operator outcome is [root `PRODUCT.md`](../../PRODUCT.md); Feature 011 D-A
defines the current Workspace journey: **Home → Workspace → readable Repository/Working
copy/Indexed snapshot → source → direct or reverse relation → released evidence**.

## Ownership

| Boundary | Owner |
|---|---|
| Product outcome, acceptance, scope | Root `PRODUCT.md` and `specs/011-operator-code-console/` |
| Private visual authoring | `.od/` design-source owner |
| Curated tracked snapshot | `design/operator-console/` through [PROMOTION-CONTRACT.md](../../design/operator-console/PROMOTION-CONTRACT.md) |
| Runtime pages, navigation, composables, components, locales, tests | Console implementation owner |
| Shared server routes, UCI composition, migrations | Single integration owner named by Feature 011 tasks |

The runtime owner may change pages, navigation, labels, and component structure when the
accepted product contract requires it. Reuse tokens, accessibility, secret handling, and
honest-state patterns; do not preserve a page, mock seam, route, label, or disabled control
only because it appears in an earlier design snapshot.

## Design and promotion boundary

OpenDesign authoring is private `.od/` and is not tracked or reconstructed here. Promotion
is one-way into `design/operator-console/`; it is not code generation and it does not
overwrite this runtime. `npm run parity` validates the curated snapshot ledger only. It does
not prove product acceptance, navigation discoverability, UCI authority, a real provider,
or installed behavior.

The reviewed 2026.09.17 snapshot is already promoted. `PROMOTION-MANIFEST.json` and `PARITY.json` agree on its design version and snapshot hash; that agreement records the checked design input, not runtime or visual parity. Every active parity row remains `drifted`, and the Code route's Chrome acceptance is pending.

The [responsive-layout receipt](../../specs/011-operator-code-console/acceptance/da-workspace-responsive-layout-receipt.json) proves Workspace shell layout and drawer behavior only. It does not reassert semantic-provider explorer evidence (`embedded_chunks=0 of 64`) or substitute for the installed exact-head normal-homepage DA01–DA04 release proof, which remains pending.

## Runtime invariants

- Source/Checkout/View and explicit grants remain server authority. Human repository,
  branch, device, and snapshot labels are presentation only.
- A registered checkout without a display name appears as a localized unnamed working copy,
  including when its View is published; the label is never a path or an access grant.
- A browser never reads local worktree paths or credentials and never asks for a manually supplied
  context identifier. It stores only the non-authorizing `view_ref` for a candidate; each pin
  uses the catalog's fresh `selection_ref` and requires a server success response. The owner grant
  list comes from paged `GET /api/code/grants` after reload; opaque `grant_ref` is used only for
  the displayed grant's revoke action, never as operator input or durable browser state.
- The server derives `source_ref` and `checkout_ref` from their canonical identities and
  `view_ref` from the exact subject/Source/Checkout/View/profile/generation tuple. These
  presentation keys stay stable when the catalog refreshes the sealed `selection_ref`;
  rotating selection authority does not imply a Source or Checkout switch.
- Failed catalog refresh removes candidate pin authority until a fresh successful catalog
  rebind; an already server-confirmed pin may remain visible in the same binding. After
  a successful grant issue or revoke, failed inventory refresh preserves the mutation
  confirmation but hides stale revoke actions until the inventory is verified again.
- Queued or running daemon indexing does not disable authorized catalog/status readback.
  Only in-flight browser requests block those reads; publication never switches the pinned
  snapshot without an explicit selection, and active intent controls still prevent duplicate jobs.
- The default D-A path is keyboard-operable, has visible focus and state announcements,
  supports RU/EN task language, and leaves zh navigation intact.
- A relation is evidence-led. It does not become a manual graph editor, and historical
  graph/book records are not relabeled as code facts.
- Manual graph writers and plaintext book intake are retired at UI and executable
  boundaries; their historical documents/provenance/readers remain governed by Feature 011.
- Workspace uses one result area with related relation/source panels. Mobile panel controls,
  Back and Escape preserve the query and selection; source code scrolls inside its viewer.
- Choosing another context hides the previous released bodies and readiness immediately.
  A definitive HTTP 403 pin refusal leaves the server pin unchanged; the Console may restore
  the previously confirmed scope and recheck it. A lost, timed-out, or otherwise ambiguous
  pin response clears local pin authority and contextual results, even if the server committed
  the switch. Refresh choices and explicitly confirm a snapshot before reading code again.
- Reconfirming the same Source/Checkout/View retains the tracked index intent, its durable
  reload resume and bounded status polling. Only an actual switch to another context clears
  that tracking; publication still never selects a newer View automatically.

## Работа с кодом: инструкция оператора

Инструкция описывает текущую реализацию Vue, а не завершённую проверку в браузере
или установленном клиенте. Нужны разрешённый сервер с согласованными версиями
консоли, native-клиента, daemon и parser, а также настоящие Git-копии с кодом.
Для изолированного запуска Main должен передать фактический разрешённый адрес
и состав компонентов. Публикация релиза не обязательна для такого запуска.
На момент подготовки этой инструкции согласованный M1 target не передан;
номер опубликованной версии или старый preview не заменяет этот target.

Используй уже настроенный сервер. Обычный single-user HTTP LAN-путь без
аутентификации не требует логина, browser grant или нового HTTPS-прокси.
Установка с аутентификацией сохраняет свои проверки доступа; не обходи их.
Браузер не читает локальный Git. Названия помогают выбрать копию, но не дают права.
**Снимок индекса** — неизменяемый серверный View, а не текущий файл на диске.

### Подключить репозиторий и две рабочие копии

Копии A и B ниже — две уже существующие Git-копии одного репозитория.
Браузер не создаёт worktree. Если копия уже есть в каталоге, перейди к выбору
снимка; повторная регистрация ради чтения не нужна.

1. Открой главную страницу разрешённой консоли.
2. Выбери **Рабочее место** на главной странице или в основном меню.
3. Открой **Добавить / подключить репозиторий**. При пустом каталоге раздел открыт сразу.
4. На компьютере с копией A открой обычный установленный агент с Engram из её Git-корня.
5. Проверь, что агент использует тот же сервер, что и консоль.
6. В поле **Что подключить** выбери **Новый репозиторий**.
7. Введи читаемое **Название репозитория**. Путь и UUID не нужны.
8. Нажми **Копировать задачу**. Если буфер обмена недоступен, используй
   **Выделить задачу** и обычную команду копирования с клавиатуры.
9. Передай показанную **Задачу для установленного агента** агенту копии A.
10. Дождись ответа агента о публикации View, ошибке или продолжающейся работе.
11. Нажми **Подключение выполнено — обновить каталог** в консоли.
12. Проверь серверные подписи **Репозиторий** и **Рабочая копия**.
13. Если снимок A появился, закрепи его по следующему разделу.
14. Открой вторую вкладку через главную страницу → **Рабочее место**.
15. Открой установленный агент из Git-корня копии B на том же сервере.
16. В разделе подключения выбери **Другую копию существующего репозитория**.
17. Выбери нужный **Репозиторий** из предложенного списка.
18. Передай показанную задачу агенту копии B.
19. После ответа агента обнови каталог во второй вкладке.
20. Выбери и закрепи отдельный снимок B. В первой вкладке сохрани выбор A.

Задача A вызывает native `codebase_context register` с `source_label`, без
`locator`: Git-корень определяет native host. Задача B сначала получает свежий
`codebase_context list`, затем использует `select` и серверный `context_handle`
для `register`. Одинаковое название не разрешает объединять Source.
При неоднозначном выборе агент должен показать варианты, а не угадать.
Вариант подключения B доступен только при наличии опубликованного View в каталоге.

Обе задачи используют свой checkout-bound `context_handle` в той же сессии:
`codebase_status` → `codebase_index` → `codebase_status` с `after_barrier.token`
из `run_id`. Оператор не вводит эти значения вручную и не переносит handle между
сессиями. `wait_ms:60000` задаёт предел ожидания клиента; отдельный вызов daemon
ждёт не более 250 мс. `running`, `timed_out`, `started` и `already_running`
не означают готовый снимок. Агент обновляет статус того же задания, не запускает
дубликат и сообщает ожидание, если работа продолжается. Копирование задачи само
по себе ничего не подключает. Не меняй `.engram-project`, профиль, авторизацию
или провайдера ради подключения.

Источник шагов и native-задач: [Home](pages/index.vue#L241-L248),
[форма подключения](components/code/CodeRepositoryConnect.vue#L19-L78),
[RU-текст](i18n/locales/ru.json#L2598-L2623).

### Выбрать снимок и проверить индекс

1. В **Контекст кода** выбери **Репозиторий**.
2. Выбери **Рабочая копия**. Сравни подписи и число снимков, если названия совпадают.
3. Если копия ещё без снимка и есть **Индексировать рабочую копию**, нажми эту кнопку.
   Если кнопки нет, выполни задачу подключения в агенте этой копии и обнови каталог.
4. Если запрос индексации ещё активен, используй **Проверить текущее состояние**.
   «Отправлен», «В очереди», «Подтверждён» и «Выполняется» не означают завершение.
5. После публикации выбери **Снимок индекса**.
6. Раскрой **Сведения о выбранном снимке**. Проверь подпись, **Ревизия** и
   **Опубликован**, если сервер выдал эти поля.
7. Нажми **Закрепить снимок**.
8. Дождись **Закреплено сервером** и строки **Выбранный снимок** с нужной копией.
   Выбор в списке и «Вкладка подключена» ещё не подтверждают снимок.
9. Проверь **Покрытие**, **В индексе**, **Свежесть**, **Эмбеддинги кода** и **Причина**,
   если причина показана.
10. Для свежего чтения состояния нажми **Обновить статус** вверху страницы.

**В индексе** показывает `embedded_chunks / total_chunks` выбранного View:
чанки с готовыми эмбеддингами кода / все его чанки. Это не число файлов,
результатов поиска или эмбеддингов памяти. **Индекс готов** позволяет проверить
смысловой поиск, но ещё не доказывает его. Нулевой знаменатель тоже не доказательство.
**Смысловой поиск не готов** означает неполные или недоступные эмбеддинги;
исходник и лексический поиск могут оставаться доступными.

Если нужно обновить выбранную копию, используй **Запросить переиндексацию**
или **Запросить сверку**. При активном запросе новый запуск недоступен.
«Завершён» не закрепляет новый View: обнови разрешённые варианты, выбери новый
снимок и снова нажми **Закрепить снимок**. Сохранение файла, обновление каталога
и публикация нового View не подменяют снимки A или B автоматически.

Чтобы переключить копию в одной вкладке, повтори выбор репозитория, копии
и снимка с явным закреплением. При смене выбора прежние результаты и готовность
сразу скрываются. Сервер подтверждает новый выбор только для этой вкладки.

Источник: [выбор и подтверждение](components/code/CodeContextPicker.vue#L127-L188),
[запрос индексации](components/code/IndexIntentStatus.vue#L20-L53),
[готовность и счётчики](components/code/CodeResults.vue#L71-L127),
[серверное закрепление и смена контекста](composables/useOperatorCode.ts#L1398-L1578).

### Найти поведение, пройти связь и открыть исходник

1. В поле **Искать в закреплённом снимке** опиши реальное поведение нужного кода
   без известного имени функции. Используй задачу своего репозитория, не выдуманный пример.
2. Нажми **Искать**.
3. Проверь **Режим ответа** и **Покрытие векторами**.
4. Проверь **Структурное покрытие**, **Неразрешённые ссылки**,
   **Неподдерживаемые файлы**, предупреждения и причины деградации.
5. У подходящего результата проверь путь, строки, язык и источники совпадения рядом с ними.
6. Если нужна следующая страница, нажми **Загрузить следующую страницу поиска**,
   когда кнопка доступна. Она выдаёт следующую страницу, а не общий итог корпуса.
7. У результата нажми **Открыть связи**.
8. Для прямого или обратного обхода раскрой **Направление и фильтры связей**.
9. Выбери **Исходящие** или **Входящие** в поле **Направление**.
10. Если нужен определённый тип, отметь его в **Типы связей**, например `calls`.
11. Выбери исходный результат в **Результат для обхода**, чтобы применить направление и фильтры.
12. Нажми **Список связей**, чтобы прочитать связи без графического представления.
13. Выбери выданную связь. Проверь её тип, `evidenceKind`, пояснение и `precision` доказательства.
14. Если доступно **Читать место ссылки в опубликованном исходнике**, нажми эту кнопку.
15. Для исходника выбранного узла используй **Показать точный исходник**, если кнопка доступна.
16. В панели **Точный исходник** проверь путь и строки.
17. Раскрой **Техническое подтверждение**, чтобы прочитать границы байтов и дайджест.

Console передаёт `membership_id` из выбранного результата или `source_read` вместе
с entity/span/digest. Это UUID конкретного расположения в закреплённом View:
одинаковые байты у двух путей не позволяют подменить выбранный файл. UUID не надо
вводить вручную. Если старая цитата его не содержит, повтори поиск после обновления
согласованного server/Console комплекта. Неоднозначный узел графа не выдаёт случайный
исходник: кнопка чтения доступна только для единственного подходящего расположения.

`lexical` — полезный текстовый ответ, но не смысловой успех. `hybrid` сообщает
использование векторов и лексического поиска; `vector` в источниках конкретного
совпадения подтверждает вклад векторов. Полное покрытие, русский запрос,
оценка результата или имя внутреннего режима `FTS` сами по себе этого не доказывают.
Неизвестный режим остаётся неизвестным. Нужны фактические `retrieval.mode`,
`items[].match_sources` и конфигурация провайдера именно выбранного сервера/View.
Настройку провайдера проверяет его владелец; подключение копии её не меняет.
Даже вклад векторов не доказывает качество: сверь результат с реальным поведением кода.
Для приёмки концептуального поиска нужен реальный провайдер и более 50 подходящих
кандидатов; число на странице не доказывает этот размер корпуса.

Связи извлекаются автоматически. `calls` и `may_call` имеют разный смысл;
`entity-level`, `heuristic` и `ambiguous` не означают точное место вызова.
Если описатель исходника не выдан, консоль сообщает это и не читает локальный файл.
Исходник узла не заменяет доказательство места ссылки. Для результата поиска
отдельно доступно **Прочитать исходник**. Все эти чтения остаются в том же
подтверждённом View, включая сохранённый старый текст. При предупреждении о частичном
фрагменте показаны только первые, не более 8 КиБ; это не полный файл.
**Загрузить следующие ограниченные связи** продолжает обход выбранного узла
либо серверную страницу, если узел не выбран. Предел обхода не доказывает отсутствие связей.
**Назад**, Escape и панели **Результаты**, **Связи**, **Точный исходник** позволяют
вернуться без потери запроса и выбора, в том числе на узком экране.

Источник: [поиск, режим и source](components/code/CodeResults.vue#L134-L241),
[направление, список и доказательства](components/code/CodeGraph.vue#L100-L191),
[тот же View и чтение выданного фрагмента](composables/useOperatorCode.ts#L1613-L1759),
[HTTP-контракт](../../specs/011-operator-code-console/contracts/operator-code-http.md#L11-L28).

### Если результат недоступен или устарел

| Состояние | Что означает и что делать |
|---|---|
| Пустой каталог | В этой вкладке нет доступных вариантов, а не «во всей базе нет кода». Выполни задачу подключения и обнови каталог. |
| Копия без снимка | Регистрация есть, опубликованного View нет. Используй доступное индексирование или локального агента этой копии. |
| **Обновляется** / **Доступен более новый снимок** | Старый подтверждённый View не подменён. Для нового текста обнови разрешённые варианты и явно закрепи новый снимок. |
| **Устарело** / **Свежесть неизвестна** | Не считай результат актуальным. Обнови статус; при наличии нового View выбери его явно. |
| **Снимок офлайн** | Владелец копии офлайн; разрешённый опубликованный View может оставаться читаемым. Новое индексирование требует живого владельца. |
| **Нет сети** / сервер недоступен | Браузер не получил новый ответ. Восстанови соединение и повтори **Обновить статус**, **Обновить разрешённые варианты** или **Повторить проверку сервера**, когда действие доступно. Это не пустой успешный поиск. |
| Ошибка чтения каталога или статуса | Прежний подтверждённый View может сохраниться, но новые варианты не подтверждены. Повтори чтение; не считай старые данные свежими. |
| Переключение отклонено | Новый снимок не принят. Консоль возвращает прежний подтверждённый View и проверяет его; прежние результаты поиска нужно запросить заново. Для нового выбора обнови варианты. |
| Текущий снимок отклонён / **Доступ запрещён** | Снятое закрепление скрывает код. Обнови каталог и закрепи доступный снимок. Если доступ не возвращён, передай отказ владельцу; UUID, grant или SQL вручную не вводи. |
| **Ошибка индекса** / **Смысловой поиск не готов** | Прочитай **Причина** и состояние эмбеддингов. Сохрани хороший View; передай ошибку владельцу провайдера. После устранения причины запроси нужную работу и обнови статус. |
| **Пусто** после поиска | В разрешённом ответе нет совпадений. Проверь выбранную копию, режим и ограничения; это не доказательство отсутствия кода вне данного ответа. |
| **Частично** / **Недоступно** / **Таймаут** | Сервер не выдал полный поддерживаемый результат. Прочитай ограничения; не объявляй отсутствующими связи или исходник. |
| Следующая страница отклонена | Страница не применена. Повтори исходный поиск в подтверждённом View; не переноси курсор между запросами или вкладками. |
| **Копия вкладки** / **Неоднозначный запуск** | Новый binding начинается без закрепления. Выбери снимок и подтверди его в этой вкладке. |

Состояния и действия заданы в [RU-словаре](i18n/locales/ru.json#L2289-L2437)
и [готовности Workspace](i18n/locales/ru.json#L2659-L2691).
Этот порядок — инструкция по исходникам, не протокол успешного прогона.
Реальную связную проверку A/B, semantic/hybrid, relation/source и затем
установленных конечных компонентов проводит назначенный владелец проверки.

## Build and host boundary

Vite 8 builds the existing Vue pages and components. Vue Router preserves the unprefixed
routes; Vue I18n preserves RU/EN/ZH dictionaries, fallback/plural rules and the
`engram_console_lang` cookie. Native Vue refs hold the existing shared presentation state.
The existing saved `nuxt-color-mode` key survives the cutover; theme classes and density
remain owned by the console, not by a second component-library plugin. No runtime page
uses Nuxt UI components, so the unused Nuxt UI dependency is removed with Nuxt/Nitro.

`.output/public/index.html`, `/_nuxt/` assets and `/i18n/locales/` remain the Go embed
contract. `.output/server/index.mjs` is a dependency-free Node static host and streaming
API relay for the separate console image and existing live/browser fixtures; it does not
implement backend authorization. Go continues to own every API, cookie/session, grant
and mutation decision. Configure `ENGRAM_OPERATOR_API_TARGET` with the absolute Go
backend URL; an empty or invalid target fails closed. `ENGRAM_PUBLIC_API_BASE` defaults
to `/api`, and `ENGRAM_PUBLIC_API_DISPLAY_HOST` changes the displayed host only.
For local development, set `ENGRAM_OPERATOR_API_TARGET` before `npm run dev`.
The standalone Compose file maps `OPERATOR_WEB_API_TARGET` to that same
`ENGRAM_OPERATOR_API_TARGET`, defaulting to `http://host.docker.internal:37777`.
Its static host listens on `HOST=0.0.0.0` and `PORT=3000`; no Nuxt/Nitro
environment aliases remain in this deployment path.

Use Node 22.22.3 or newer. The release script and Docker build pin the same Linux Node
version; the release archive checksum and Docker digest remain independent pins. Run
`npm ci`, `npm audit --package-lock-only --audit-level=high --json`, `npm run build`
and `node scripts/check-static-assets.mjs` from this directory. `generate` uses the same
static build, not a second Nitro/server build. Do not omit dev dependencies from the
mandatory locked-graph audit or substitute/suppress vulnerable packages. The historical
promoted design snapshot remains unchanged; its old framework suggestions are not runtime
installation instructions. Use this README and the actual source/package files instead.

## Commands

```bash
npm run build
npm run test:seam
npm run test:parity
npm run test:browser
npm run test:browser:live
```

Run only the focused commands selected by the active Feature 011 task from the exact bound
candidate. Mock browser and parity checks are scoped evidence, not complete D-A acceptance.
The mock Rules selection follows the real browser-session boundary: browser tests set a
unique `mock-rule-session` cookie before loading Rules so parallel contexts cannot replace
one another's selected operation. Secrets browser tests use a separate `mock-vault-session`
cookie so concurrent reveal and delete journeys do not mutate one another's fixture vault.
These mock cookies are test-only and grant no authority.

The focused `tests/browser/code-workbench.spec.ts` fixture exercises keyboard/mobile
navigation, same-View source descriptors, late-response isolation, definitive historical
switch refusal, committed switches with lost/timed-out responses, same-context intent
continuity across reconfirmation/reload, and current-snapshot rejection. This is UI-layer
smoke evidence, not a real-API, physical-LAN, semantic-provider, installed or release
acceptance claim.
