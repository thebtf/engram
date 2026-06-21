---
name: engram Operator Console
description: >-
  Single-operator admin surface for persistent shared-memory infrastructure.
  Dark-first product UI with committed density, single accent, honesty contract
  over polish.
design_version: "2026.06.21"   # contract stamp — bump on any token/component/screen change;
                               # PARITY.json records which port pages are synced to which stamp.
                               # 2026.06.21: app shell redesigned — full-height nav column,
                               # collapse/icon-rail, brand moved into nav, breadcrumb removed.
colors:
  neutral-bg: "#0b0d10"
  surface: "#14171c"
  surface-warm: "#1c2027"
  fg: "#e6e9ef"
  fg-2: "#c2c8d2"
  muted: "#8b94a3"
  border: "#262b33"
  border-soft: "#1c2027"
  accent: "#4c8dff"
  accent-on: "#04131d"
  success: "#34d399"
  warn: "#fbbf24"
  danger: "#f87171"
  class-live: "#34d399"
  class-dormant: "#fbbf24"
  class-stale: "#6b7280"
  class-mustbuild: "#a78bfa"
  state-danger: "#f87171"
  state-warn: "#fb923c"
  heat-cited: "#34d399"
  heat-uncited: "#5c6470"
typography:
  display:
    fontFamily: "Inter, system-ui, sans-serif"
    fontSize: "56px"
    fontWeight: 700
    lineHeight: 1.1
    letterSpacing: -0.015
  title:
    fontFamily: "Inter, system-ui, sans-serif"
    fontSize: "22px"
    fontWeight: 600
    lineHeight: 1.25
  body:
    fontFamily: "Inter, system-ui, sans-serif"
    fontSize: "15px"
    fontWeight: 400
    lineHeight: 1.48
  label:
    fontFamily: "Inter, system-ui, sans-serif"
    fontSize: "11px"
    fontWeight: 700
    lineHeight: 1
    letterSpacing: 0.06
  mono:
    fontFamily: '"IBM Plex Mono", ui-monospace, Menlo, monospace'
    fontSize: "13px"
    fontWeight: 400
    lineHeight: 1.48
rounded:
  sm: "6px"
  md: "10px"
  lg: "14px"
  pill: "9999px"
spacing:
  xs: "4px"
  sm: "8px"
  md: "16px"
  lg: "24px"
  xl: "32px"
  xxl: "48px"
components:
  button-tbtn:
    backgroundColor: transparent
    textColor: "{colors.fg-2}"
    rounded: "{rounded.md}"
    padding: "6px 10px"
    height: "32px"
  button-tbtn-primary:
    backgroundColor: "{colors.accent}"
    textColor: "{colors.accent-on}"
    rounded: "{rounded.md}"
    padding: "6px 14px"
  button-act:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.fg-2}"
    rounded: "{rounded.sm}"
    padding: "6px 11px"
    border: "1px solid var(--border)"
  toggle:
    backgroundColor: "{colors.border}"
    rounded: "{rounded.pill}"
    size: "42px × 24px"
  toggle-checked:
    backgroundColor: "{colors.success}"
    rounded: "{rounded.pill}"
    size: "42px × 24px"
  fchip:
    backgroundColor: "{colors.surface-warm}"
    textColor: "{colors.fg-2}"
    rounded: "{rounded.pill}"
    padding: "5px 14px"
  fchip-pressed:
    backgroundColor: "{colors.accent}"
    textColor: "{colors.accent-on}"
    rounded: "{rounded.pill}"
    padding: "5px 14px"
  issue-chip:
    backgroundColor: "color-mix(in oklab, currentColor, transparent 88%)"
    textColor: "currentColor"
    rounded: "6px"
    padding: "2px 8px"
---

# Дизайн-система: engram Operator Console

## 1. Overview

**Creative North Star: «Операторский щит»** — интерфейс, который можно держать открытым на втором мониторе сутками и сразу видеть, что горит, что за флагом, а что уже не существует. Система не про красоту — она про честность: каждый элемент честно показывает своё состояние, ни один контрол не притворяется работающим, если backend не построен.

Дизайн обслуживает продукт (product register). Operator console — это инструмент для одного оператора в control-room контексте. Тёмная тема по умолчанию (wall display / второй монитор), светлая как опциональный режим. Плотность — две ступени: Comfortable (оператор читает с расстояния) и Compact (максимум данных на экране).

**Ключевые характеристики:**
- Классификация — каждое поверхность маркирована: live / dormant (за флагом) / stale (надгробие, никогда не работает) / must-build
- «Акцент — один, используется ≤10% экрана» — единственный `--accent` (#4c8dff тёмная, #0ea5e9 светлая) для выделения
- Состояние первым: цвет и бейдж до текста
- Техника вторым слоем: endpoint, env-имена, gate-флаги — evidence, а не заголовки
- Запрещены стеклянные панели, градиенты, неон, градиентный текст и side-stripe карточки
- Без анимации входа страниц — продукт загружается в задачу, а не в хореографию

## 2. Colors

Палитра — restrained (два нейтральных слоя + один акцент + четыре семантических цвета + четыре классификационных). Тёмная тема — база, светлая — зеркальный вариант с теми же ролями.

### Primary (Accent)
- **Неоновый лёд** `#4c8dff` (dark) / `#0ea5e9` (light): единственный акцент. Только для выбранных строк, primary actions, фокусных колец и статус-индикаторов. Никогда — для декорации.

### Neutral
- **Ночная сталь** `#0b0d10` (`--bg` dark): фон подложки. Угольно-чёрный с минимальным холодным отливом.
- **Шифер** `#14171c` (`--surface` dark): основной фон панелей и карточек.
- **Мокрый гранит** `#1c2027` (`--surface-warm` dark): подсветка при наведении, warmer-альтернатива для панелей.
- **Платина** `#e6e9ef` (`--fg` text dark): основной текст.
- **Сталь** `#c2c8d2` (`--fg-2` dark): вторичный текст.
- **Дымка** `#8b94a3` (`--muted` dark): мета-информация, placeholders.
- **Тёмная сталь** `#262b33` (`--border` dark): границы элементов.
- **Чёрная сталь** `#1c2027` (`--border-soft` dark): мягкие разделители.

### Semantic
- **Изумруд** `#34d399` (dark) / `#16a34a` (light): успех, live, cited.
- **Амбер** `#fbbf24` (dark) / `#d97706` (light): dormant, предупреждения.
- **Сигнальный** `#f87171` (dark) / `#dc2626` (light): опасность, ошибка.
- **Сигнальный-оранж** `#fb923c` (dark) / `#ea580c` (light): warning-level (noise, near-critical).
- **Лавандовый** `#a78bfa` / `#7c3aed`: must-build (фича, которую ещё предстоит реализовать).
- **Тусклый серый** `#6b7280`: stale (наследие, не трогать).

### Named Rules

**The One Accent Rule.** `--accent` используется на ≤10% любого экрана. Его редкость — и есть его информационная ценность. Если акцента стало много — он перестал быть сигналом.

**The Classification Rule.** Четыре цвета (live / dormant / stale / mustbuild) читаются на любом фоне. Stale — только контурный круг, никогда не залитый. mustbuild — всегда с evidence (endpoint или gate-флаг) рядом.

## 3. Typography

**Display Font:** Inter (700, 56px, -0.015em letter-spacing)
**Body Font:** Inter (400, 15px, 1.48 line-height)
**Mono Font:** IBM Plex Mono (400, 13px, tabular-nums)

**Характер:** одна гротескная семья на всё — Inter. Mono-шрифт только для данных: ID, версии, endpoint, флаги, числовые показатели с tabular-nums. Размерная сетка фиксированная (rem-ish px), не fluid — продуктовая UI смотрит на consistent DPI, а не на viewport. 

### Hierarchy
- **Display** (Inter 700, 56px, 1.1): герои — только числовые показатели на инструментальных панелях (gauge, noise ratio, big metric).
- **Title** (Inter 600, 22px, 1.25): заголовки разделов, модальных окон.
- **Sub** (Inter 600, 17px, 1.3): подзаголовки, заголовки карточек.
- **Body** (Inter 400, 15px, 1.48): основной текст, описания, мета. Max-width: 76ch для длинной прозы.
- **Small** (Inter 400, 13px, 1.48): вспомогательный текст, preview строк.
- **Label / Eyebrow** (Inter 700, 10–11px, 0.06–0.08em letter-spacing, uppercase): заголовки колонок, секций, таблиц. Единственная uppercase роль в системе.
- **Caption** (Inter 400, 11px): так же как label но без uppercase. Сноски, метрики.
- **Mono** (IBM Plex Mono, 13px, 1.48): все идентификаторы, версии, пути, env-имена, флаги, коды. `font-variant-numeric: tabular-nums` — обязательное условие.

### Named Rules
**The Tabular Figures Rule.** Mono, числовые и кодовые элементы используют `font-variant-numeric: tabular-nums`. Цифры не меняют ширину при смене значения — ни один badge не дёргается, колонки не плывут.

**The Fixed Scale Rule.** Работа продукта — rem-ish px, не clamp(). Пользователь UI консоли смотрит с consistent расстояния; fluid типографика только добавляет ненужную вариативность.

## 4. Elevation

Плоские поверхности с тонкими границами (`--elev-ring: 0 0 0 1px var(--border)`). Тени появляются только как реакция на состояние:
- `--elev-raised` (0 18px 46px, 0.5 black в тёмной, 0.10 в светлой) — модалы, popover, toast, bulkbar.
- Никаких вложенных теней, никаких «слоистых» карточек. Глубина передаётся через смещение акцентного цвета (inset box-shadow на выбранной строке), а не через подъём.

**The Flat-By-Default Rule.** Поверхности плоские в покое. Тень — только для модальных и всплывающих элементов. Рядовая строка или карточка не должна «парить».

## 5. Components

### Buttons
- **tbtn** (toolbar button): прозрачный фон, `--fg-2` текст, `--rounded-md` (10px). Единственный ховер — `--surface-warm` фон + `--fg` текст.
- **tbtn.primary**: `--accent` фон, `--accent-on` текст. Только для финального подтверждения формы.
- **act** (action button): `--surface` фон, 1px `--border` рамка, `--rounded-sm` (6px). Hover — рамка меняется на `--accent`.
- **act.danger**: красная рамка (`--state-danger`). Hover — красный фон 10%.
- **Тумблер (toggle)**: `42×24px`, pill shape. `--border` в off, `--class-live` в on. Danger-вариант — `--state-danger` в on. 18px кружок с белой заливкой прыгает left/right. `aria-checked` управляет состоянием.

### Form Controls
- **txt** (text input): 1px `--border`, `--rounded-sm`, `--surface` фон. Focus — `--accent` border.
- **sel2** (select-like): то же что txt, с `--surface-warm` disabled.
- **stepper**: inline-flex группа, `--border` контейнер, три части: кнопка — поле ввода — кнопка.
- **secret**: строка с кнопкой «раскрыть», vset (установлено) / vempty (не задано), editb для редактирования.
- **settings-choice**: карточка выбора, `aria-pressed`. On — `--accent` border + 1px inset box-shadow.

### Chips & Badges
- **bdg**: 10px, 700 weight, 0.03em spacing, uppercase. Категории: `gate` (dormant), `mb` (must-build), `scope-global`/`scope-project` (danger/blue), `pri-critical`/`pri-high` (danger/warn), `status` (нейтральный).
- **rbadge**: 9px, 700 weight, uppercase. Для Settings: `live`/`restart`/`noeffect`.
- **issue-chip**: `10px`, 800 weight, uppercase, `0.03em` letter-spacing. Цвет через `currentColor` + 88% прозрачности фон. Тоны: critical/danger, high/warn, medium/dormant, low/muted, bug/danger, feature/accent, task/fg-2, improvement/success, handoff/mustbuild, question/dormant. Есть `.subtle` вариант — нейтральный.
- **fchip** (filter chip): pill, `--surface-warm` в покое, `aria-pressed="true"` — `--accent` фон.
- **tag**: `10px`, mono, pill, `--border` рамка.

### Cards & Containers
- **grid**: 1px `--border`, `--rounded-md` (10px), `--surface` фон. Содержит `grid-h` (заголовок) и `erow` (строки).
- **card**: `--border-soft` разделители, `--space-5` padding. Без тени.
- **panel** (ds-panel): 1px `--border`, `--rounded-md`, `--surface`. Head + body структура.
- **surface-card**: `--surface`, 1px `--border`, `--rounded-md`, `--space-4` padding.

### Navigation
- **nav**: левая колонка **во всю высоту** (`236px` развёрнуто / `60px` свёрнуто — icon-rail), `1px --border` справа. Шапка панели `navhead` (`48px`): brand glyph + wordmark слева, кнопка-сворачивания **справа** (НЕ в левом верхнем углу). Группа заголовков (`10px`, `0.08em` spacing, `--muted`), `nav-item` со статус-точкой слева от иконки. Подпункты класса `.sub`.
- **collapse (icon-rail)**: панель сворачивается до `60px` — только иконки (крупнее) + цветная статус-точка как бейдж слева; лейблы/счётчики/заголовки групп скрыты. Состояние персистится в `localStorage`. Наведение на свёрнутый rail временно разворачивает панель поверх контента (Gemini-стиль), лейаут не сдвигается. Анимируется только `width` (плавно, без рывка). Логотип и кнопка-сворачивания живут в шапке nav, не в топбаре.
- **topbar**: `48px` высота, **над контентом** (не во всю ширину — слева от него полновысотный nav). Global search (левый край выровнен по границе nav/контент). Density seg. Language switch. Theme button. Identity chip. Без brand (переехал в nav) и без хлебной крошки (дублировала `h1` страницы).
- **statusbar**: `26px` высота. `--surface` фон + `--border` top border. Mono 11px текст.

### Modals & Overlays
- **overlay**: `position:fixed`, `rgba(8,12,20,.55)` фон, z-index 60. `.show` включает `display:flex`.
- **modal**: `--surface`, 1px `--border`, `--rounded-lg` (14px), `--elev-raised`. Ширина `min(540px,100%)`. Спецварианты: `.settings-modal` (960px), `.issue-create-modal` (1280px).
- **toast**: фиксированная позиция bottom/center, `--fg` фон + `--bg` текст. 9px 15px padding. `.show` — opacity 1. Undo-кнопка в `--accent`.

### Data Display
- **erow** (entity row): flex-строка, `--row-y` 14px/6px padding, `--row-gap` 13px/8px (comfortable/compact). Компоненты: `echk` (чекбокс), `estate` (статус-дот), `ebody` (preview + meta), `eside` (row-меню + доп. контролы). `.sel` — подцветка акцентом (inset 2px штрих). `.open` — открыт в detail (inset 3px штрих).
- **issue-row**: CSS Grid с 9 колонками (22px 44px 88px 112px 1fr 112px 150px 74px 42px). `--issue-status-color` левый штрих через `::before`.
- **registry-table**: полная таблица с `--rounded-md` обёрткой. `--surface-warm` заголовки.
- **gauge**: 172×172 conic-gradient круг. `--p` CSS-переменная. После послевкусия — 13px inset белый круг. `.good` — зелёный fill.
- **instr-band**: горизонтальная приборная панель. `--surface` + 1px `--border` + `--rounded-md`.

### Markdown
Подмножество: p, strong, em, code (mono 11px), pre, ul/ol, blockquote (accent-tinted 6% фон), h3. Без таблиц, h1–h2, изображений, ссылок в markdown-контексте.

## 6. Do's and Don'ts

### Do:
- **Do** классифицировать каждую поверхность: live / dormant / stale / must-build. Каждая маркировка — честная.
- **Do** использовать `--accent` не более чем на 10% площади экрана. Его редкость = его сигнал.
- **Do** показывать техническую информацию (endpoint, gate flag, env) вторым слоем — как evidence, а не заголовок.
- **Do** использовать tabular-nums везде, где цифры несут смысл (счётчики, ID, метрики).
- **Do** давать пустым состояниям конкретный следующий шаг, а не «пока ничего нет».
- **Do** показывать stale-элементы как надгробия (контурные, неактивные), никогда не operable.
- **Do** добавлять reload-badge (restart-required) на каждый switch, который требует рестарта сервера.
- **Do** использовать один тост для успеха, один danger-блок для опасного действия — без декоративных вариаций.
- **Do** адаптировать touch-цели до 44px на `pointer:coarse`.

### Don't:
- **Don't** использовать градиентный текст, glassmorphism, неон, или любые декоративные эффекты на фоне.
- **Don't** ставить тень на статичные карточки. Тень — только для модальных / всплывающих элементов.
- **Don't** показывать строчный акцент-штрих (`border-left`) — используйте полную рамку или фоновый оттенок.
- **Don't** помещать модальные окна в `overflow:auto` родителя — используйте `position:fixed` overlay.
- **Don't** давать inert-кнопки на must-build поверхностях. Если серверного действия нет — контрол не показывается.
- **Don't** анимировать вход страниц. Продукт загружается в задачу — хореография не нужна.
- **Don't** использовать display-шрифты для UI-лейблов, кнопок и данных. Inter на всём, mono только для данных.
- **Don't** повторять числовой приоритет вручную — система вычисляет его из порядка. Оператор перетаскивает, priority присваивается автоматически.
- **Don't** использовать несколько акцентов. Система имеет ровно один `--accent`, и он не переопределяется.
- **Don't** смешивать несколько визульных метафор интерфейса — всё подчиняется одному product register.
