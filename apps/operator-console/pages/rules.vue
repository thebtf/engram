<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useOperatorRules } from '../composables/useOperatorRules'
import type { RuleRow } from '../composables/useMockData'

const { t } = useI18n()
const {
  rows,
  scopeOptions,
  loadState,
  pending,
  error,
  refresh,
  createRule: runCreateRule,
  updateRule,
  reorderRules,
  deleteRule,
  enableGap,
  scopeChangeGap,
} = useOperatorRules()

const scopeFilter = ref('all')
const createOpen = ref(false)
const createContent = ref('')
const createScope = ref('global')
const editingId = ref<number | null>(null)
const editContent = ref('')
const confirmingDeleteId = ref<number | null>(null)
const draggingId = ref<number | null>(null)
const dragOverId = ref<number | null>(null)
const dragOverAfter = ref(false)

const sortedRows = computed(() => [...rows].sort(compareRules))
const visibleRows = computed(() => sortedRows.value.filter((rule) => scopeFilter.value === 'all' || rule.project === scopeFilter.value))
const createScopes = computed(() => scopeOptions.value)
const canCreate = computed(() => createContent.value.trim().length > 0 && !pending.value)
const editingRule = computed(() => rows.find((rule) => rule.id === editingId.value) || null)
const canSaveEdit = computed(() => Boolean(editingRule.value) && editContent.value.trim().length > 0 && !pending.value)

watch(scopeOptions, (options) => {
  if (!options.includes(createScope.value)) {
    createScope.value = 'global'
  }
  if (scopeFilter.value !== 'all' && !options.includes(scopeFilter.value)) {
    scopeFilter.value = 'all'
  }
}, { immediate: true })

function compareRules(left: RuleRow, right: RuleRow) {
  if (left.priority !== right.priority) {
    return right.priority - left.priority
  }
  return left.content.localeCompare(right.content)
}

function scopeLabel(scope: string) {
  return scope === 'global' ? t('rules.scope.global') : scope
}

function rowMeta(rule: RuleRow, index: number) {
  return [
    t('rules.meta.scope', { scope: scopeLabel(rule.project) }),
    t('rules.meta.priorityAuto', { priority: rule.priority }),
    t('rules.meta.version', { version: rule.version }),
    t('rules.meta.position', { position: index + 1 }),
    t('rules.meta.updated', { updated: rule.updated }),
  ]
}

function priorityForNewRule() {
  const scoped = rows.filter((rule) => createScope.value === 'global' ? rule.project === 'global' : rule.project === createScope.value)
  const highest = scoped.reduce((max, rule) => Math.max(max, rule.priority), 0)
  return highest + 10
}

function openCreate() {
  createContent.value = ''
  createScope.value = scopeFilter.value === 'all' ? 'global' : scopeFilter.value
  createOpen.value = true
}

function closeCreate() {
  createOpen.value = false
}

async function createRule() {
  if (!canCreate.value) return
  const result = await runCreateRule({
    content: createContent.value.trim(),
    priority: priorityForNewRule(),
    project: createScope.value === 'global' ? undefined : createScope.value,
    editedBy: 'operator-console',
  })
  if (isRollback(result)) return
  closeCreate()
}

function startEdit(rule: RuleRow) {
  editingId.value = rule.id
  editContent.value = rule.content
  confirmingDeleteId.value = null
}

function cancelEdit() {
  editingId.value = null
  editContent.value = ''
}

async function saveEdit(rule: RuleRow) {
  if (!canSaveEdit.value) return
  const result = await updateRule(rule.id, {
    content: editContent.value.trim(),
    editedBy: 'operator-console',
  })
  if (isRollback(result)) return
  cancelEdit()
}

async function confirmDelete(rule: RuleRow) {
  if (confirmingDeleteId.value !== rule.id) {
    confirmingDeleteId.value = rule.id
    return
  }
  const result = await deleteRule(rule.id)
  if (isRollback(result)) return
  confirmingDeleteId.value = null
  if (editingId.value === rule.id) {
    cancelEdit()
  }
}

async function moveRule(ruleId: number, direction: -1 | 1) {
  const current = visibleRows.value
  const from = current.findIndex((rule) => rule.id === ruleId)
  const to = from + direction
  if (from < 0 || to < 0 || to >= current.length || pending.value) return
  const next = [...current]
  const [moved] = next.splice(from, 1)
  next.splice(to, 0, moved)
  await reorderRules(next)
}

function startDrag(rule: RuleRow) {
  if (editingId.value === rule.id) return
  draggingId.value = rule.id
}

function overDrag(event: DragEvent, rule: RuleRow) {
  if (!draggingId.value || draggingId.value === rule.id) return
  const target = event.currentTarget as HTMLElement
  const box = target.getBoundingClientRect()
  dragOverId.value = rule.id
  dragOverAfter.value = event.clientY > box.top + box.height / 2
}

async function dropRule(rule: RuleRow) {
  if (!draggingId.value || draggingId.value === rule.id || pending.value) {
    clearDrag()
    return
  }
  const current = [...visibleRows.value]
  const from = current.findIndex((row) => row.id === draggingId.value)
  let to = current.findIndex((row) => row.id === rule.id)
  if (from < 0 || to < 0) {
    clearDrag()
    return
  }
  if (dragOverAfter.value) to += 1
  if (from < to) to -= 1
  const [moved] = current.splice(from, 1)
  current.splice(to, 0, moved)
  clearDrag()
  await reorderRules(current)
}

function clearDrag() {
  draggingId.value = null
  dragOverId.value = null
  dragOverAfter.value = false
}

function isRollback(result: unknown) {
  return Boolean(result && typeof result === 'object' && 'kind' in result && (result as { kind?: string }).kind === 'rollback')
}
</script>

<template>
  <div class="rules-page">
    <header class="head">
      <div>
        <h1>{{ t('rules.title') }}</h1>
        <p>{{ t('rules.subtitle') }}</p>
      </div>
      <button class="primary" :disabled="pending" @click="openCreate">{{ t('rules.create.open') }}</button>
    </header>

    <nav class="tabs" :aria-label="t('rules.tabs.label')">
      <button class="tab active">{{ t('rules.tabs.rules') }}</button>
      <button class="tab stale" disabled>
        {{ t('rules.tabs.instincts') }}
        <HonestyBadge cls="stale" :evidence="t('rules.tabs.instinctsEvidence')" />
      </button>
    </nav>

    <section v-if="pending || error || loadState.kind === 'empty'" class="statebar" :data-state="loadState.kind">
      <span v-if="pending">{{ t('rules.state.pending') }}</span>
      <span v-else-if="error">{{ t('rules.state.error', { message: error }) }}</span>
      <span v-else-if="loadState.kind === 'empty'">{{ t('rules.state.empty') }}</span>
      <button v-if="error" class="tbtn" @click="refresh">{{ t('rules.state.retry') }}</button>
    </section>

    <section class="pane">
      <div class="rule-reorder-note">
        <span aria-hidden="true">⠿</span>
        <span>{{ t('rules.reorder.note') }}</span>
      </div>

      <div class="callout good">
        <HonestyBadge cls="live" />
        <span>{{ t('rules.reorder.liveCallout') }}</span>
        <code>GET /api/rules?all=true</code>
        <code>PATCH /api/rules/{id}</code>
      </div>

      <div class="toolbar">
        <label class="scope-filter">
          <span>{{ t('rules.scope.filter') }}</span>
          <select v-model="scopeFilter" class="fsel">
            <option value="all">{{ t('rules.scope.all') }}</option>
            <option v-for="scope in createScopes" :key="scope" :value="scope">{{ scopeLabel(scope) }}</option>
          </select>
        </label>
        <span class="toolbar-count">{{ t('rules.list.shown', { shown: visibleRows.length, total: rows.length }) }}</span>
        <span class="spacer" />
        <button class="tbtn" :disabled="pending" @click="refresh">{{ t('rules.actions.refresh') }}</button>
      </div>

      <div class="rules-grid">
        <div class="grid-h">{{ t('rules.list.header') }}</div>

        <div
          v-for="(rule, index) in visibleRows"
          :key="rule.id"
          class="rule-row"
          :class="{
            editing: editingId === rule.id,
            dragging: draggingId === rule.id,
            'drag-over': dragOverId === rule.id && !dragOverAfter,
            'drag-over-bottom': dragOverId === rule.id && dragOverAfter,
          }"
          :draggable="editingId !== rule.id"
          @dragstart="startDrag(rule)"
          @dragover.prevent="overDrag($event, rule)"
          @dragleave="clearDrag"
          @dragend="clearDrag"
          @drop.prevent="dropRule(rule)"
        >
          <template v-if="editingId === rule.id">
            <div class="rule-editor">
              <textarea v-model="editContent" class="text" :aria-label="t('rules.detail.content')" />
              <div class="rule-editor-row">
                <label class="scope-readonly">
                  <span>{{ t('rules.detail.project') }}</span>
                  <select class="fsel" :value="rule.project" disabled :title="scopeChangeGap.evidence.endpoint">
                    <option :value="rule.project">{{ scopeLabel(rule.project) }}</option>
                  </select>
                </label>
                <span class="rule-prio">{{ t('rules.meta.priorityAuto', { priority: rule.priority }) }} · {{ t('rules.meta.position', { position: index + 1 }) }}</span>
                <HonestyBadge cls="mustbuild" :evidence="scopeChangeGap.evidence.endpoint" :label="t('rules.scope.changeMustBuild')" />
                <span class="spacer" />
                <button class="act" @click="cancelEdit">{{ t('rules.detail.cancel') }}</button>
                <button class="act primary-line" :disabled="!canSaveEdit" @click="saveEdit(rule)">{{ t('rules.detail.save') }}</button>
              </div>
            </div>
          </template>

          <template v-else>
            <button
              class="rule-grip"
              :aria-label="t('rules.reorder.gripLabel', { position: index + 1, total: visibleRows.length })"
              :title="t('rules.reorder.gripTitle')"
              @keydown.up.prevent="moveRule(rule.id, -1)"
              @keydown.down.prevent="moveRule(rule.id, 1)"
              @click.stop
            >⠿</button>
            <span class="rule-rank" :title="t('rules.reorder.rankTitle')">{{ index + 1 }}</span>
            <div class="rule-body">
              <div class="rule-preview">{{ rule.content }}</div>
              <div class="rule-meta">
                <span class="scope-chip">{{ scopeLabel(rule.project) }}</span>
                <span v-for="meta in rowMeta(rule, index)" :key="meta">{{ meta }}</span>
              </div>
            </div>
            <div class="rule-side">
              <button class="act" @click="startEdit(rule)">{{ t('rules.detail.edit') }}</button>
              <button class="toggle" disabled role="switch" aria-checked="true" :title="enableGap.evidence.endpoint" />
              <button
                class="act danger"
                :class="{ confirm: confirmingDeleteId === rule.id }"
                :disabled="pending"
                @click="confirmDelete(rule)"
              >
                {{ confirmingDeleteId === rule.id ? t('rules.detail.confirmDelete') : t('rules.detail.delete') }}
              </button>
            </div>
          </template>
        </div>

        <div v-if="!visibleRows.length && loadState.kind !== 'pending'" class="empty">
          <b>{{ t('rules.empty.title') }}</b>
          <span>{{ scopeFilter === 'all' ? t('rules.empty.body') : t('rules.empty.scopeBody', { scope: scopeLabel(scopeFilter) }) }}</span>
        </div>
      </div>
    </section>

    <div v-if="createOpen" class="modal-backdrop" role="presentation" @click.self="closeCreate">
      <section class="modal" role="dialog" aria-modal="true" :aria-label="t('rules.create.title')">
        <div class="modal-head">
          <div>
            <h2>{{ t('rules.create.title') }}</h2>
            <p>{{ t('rules.create.subtitle') }}</p>
          </div>
          <button class="tbtn" :aria-label="t('issues.modal.close')" @click="closeCreate">×</button>
        </div>

        <label class="field">
          <span>{{ t('rules.detail.content') }}</span>
          <textarea v-model="createContent" class="text tall" :placeholder="t('rules.create.contentPlaceholder')" />
        </label>

        <div class="modal-row">
          <label class="field">
            <span>{{ t('rules.create.project') }}</span>
            <select v-model="createScope" class="fsel">
              <option v-for="scope in createScopes" :key="scope" :value="scope">{{ scopeLabel(scope) }}</option>
            </select>
          </label>
          <div class="priority-preview">
            <span>{{ t('rules.create.priority') }}</span>
            <b>{{ priorityForNewRule() }}</b>
            <small>{{ t('rules.create.priorityHelp') }}</small>
          </div>
        </div>

        <div class="modal-actions">
          <button class="act" @click="closeCreate">{{ t('issues.modal.cancel') }}</button>
          <button class="primary" :disabled="!canCreate" @click="createRule">{{ t('rules.create.submit') }}</button>
        </div>
      </section>
    </div>
  </div>
</template>

<style scoped>
.rules-page { display:flex; flex-direction:column; gap:14px; }
.head { display:flex; align-items:flex-start; justify-content:space-between; gap:16px; padding-bottom:14px; border-bottom:1px solid var(--border); }
.head h1 { margin:0 0 4px; font-size:var(--text-xl); font-weight:800; letter-spacing:var(--tracking-display); }
.head p { margin:0; max-width:820px; font-size:var(--text-sm); color:var(--muted); }
.tabs { display:flex; align-items:center; gap:8px; flex-wrap:wrap; }
.tab { min-height:34px; padding:7px 13px; border:1px solid var(--border); border-radius:var(--radius-pill); background:var(--surface); color:var(--fg-2); font-size:var(--text-xs); font-weight:900; }
.tab.active { background:color-mix(in oklab,var(--accent),transparent 85%); border-color:color-mix(in oklab,var(--accent),transparent 55%); color:var(--fg); }
.tab.stale { display:inline-flex; align-items:center; gap:8px; opacity:.72; }
.statebar { display:flex; align-items:center; justify-content:space-between; gap:12px; padding:10px 12px; border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); color:var(--fg-2); font-size:var(--text-sm); }
.statebar[data-state="pending"] { border-color:color-mix(in oklab,var(--accent),transparent 55%); }
.statebar[data-state="error"] { border-color:color-mix(in oklab,var(--state-warn),transparent 45%); color:var(--state-warn); }
.statebar[data-state="empty"] { color:var(--muted); }
.pane { display:flex; flex-direction:column; gap:12px; border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:14px; }
.rule-reorder-note { display:flex; align-items:center; gap:8px; color:var(--fg-2); font-size:var(--text-sm); }
.rule-reorder-note > span:first-child { color:var(--accent); font-size:18px; }
.callout { display:flex; align-items:center; gap:9px; flex-wrap:wrap; padding:10px 12px; border-radius:var(--r-sm); font-size:var(--text-sm); }
.callout.good { border:1px solid color-mix(in oklab,var(--class-live),transparent 65%); background:color-mix(in oklab,var(--class-live),transparent 92%); color:var(--fg-2); }
.callout code { font-family:var(--font-mono); font-size:var(--text-xs); color:var(--fg); }
.toolbar, .rule-editor-row, .modal-actions { display:flex; align-items:center; gap:10px; flex-wrap:wrap; }
.scope-filter span, .field span, .scope-readonly span, .priority-preview span { display:block; margin-bottom:5px; color:var(--muted); font-size:var(--text-xs); font-weight:800; text-transform:uppercase; letter-spacing:.06em; }
.fsel, .text { border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface-warm); color:var(--fg); font:inherit; }
.fsel { min-height:34px; padding:0 10px; }
.text { width:100%; min-height:78px; padding:10px 11px; resize:vertical; font-size:var(--text-sm); line-height:1.45; }
.text.tall { min-height:170px; }
.toolbar-count { color:var(--muted); font-size:var(--text-sm); }
.spacer { flex:1; }
.rules-grid { overflow:hidden; border:1px solid var(--border); border-radius:var(--r-md); background:var(--bg); }
.grid-h { padding:10px 14px; border-bottom:1px solid var(--border); color:var(--muted); font-size:var(--text-xs); font-weight:900; letter-spacing:.08em; text-transform:uppercase; }
.rule-row { display:grid; grid-template-columns:30px 30px minmax(0,1fr) auto; gap:12px; align-items:center; min-height:64px; padding:12px 14px; border-bottom:1px solid var(--border-soft); transition:box-shadow var(--motion-fast) var(--ease-standard), opacity var(--motion-fast) var(--ease-standard), background var(--motion-fast) var(--ease-standard); }
.rule-row:last-child { border-bottom:0; }
.rule-row:hover { background:var(--surface-warm); }
.rule-row.dragging { opacity:.5; }
.rule-row.drag-over { box-shadow:inset 0 2px 0 var(--accent); }
.rule-row.drag-over-bottom { box-shadow:inset 0 -2px 0 var(--accent); }
.rule-row.editing { grid-template-columns:1fr; align-items:stretch; background:color-mix(in oklab,var(--accent),transparent 92%); box-shadow:inset 3px 0 0 var(--accent); }
.rule-grip { width:28px; height:32px; display:grid; place-items:center; border:0; border-radius:var(--r-sm); background:transparent; color:var(--muted); cursor:grab; font-size:18px; }
.rule-grip:hover, .rule-grip:focus-visible { background:var(--surface-warm); color:var(--fg); outline:none; }
.rule-grip:active { cursor:grabbing; }
.rule-rank { width:28px; height:28px; display:grid; place-items:center; border-radius:var(--radius-pill); background:var(--surface-warm); color:var(--fg); font-family:var(--font-mono); font-size:var(--text-xs); font-variant-numeric:tabular-nums; }
.rule-body { min-width:0; }
.rule-preview { color:var(--fg); font-size:var(--text-sm); line-height:1.35; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.rule-meta { display:flex; align-items:center; gap:8px; flex-wrap:wrap; margin-top:5px; color:var(--muted); font-size:var(--text-xs); }
.scope-chip { border:1px solid color-mix(in oklab,var(--accent),transparent 60%); border-radius:var(--radius-pill); padding:2px 7px; color:var(--accent); background:color-mix(in oklab,var(--accent),transparent 91%); }
.rule-side { display:flex; align-items:center; gap:8px; }
.rule-editor { display:grid; gap:10px; }
.rule-prio { color:var(--muted); font-family:var(--font-mono); font-size:var(--text-xs); font-variant-numeric:tabular-nums; }
.primary, .act, .tbtn { min-height:34px; padding:7px 11px; border:1px solid var(--border); border-radius:var(--r-sm); font-size:var(--text-xs); font-weight:900; cursor:pointer; }
.primary { background:var(--accent); border-color:var(--accent); color:var(--accent-on); }
.act, .tbtn { background:var(--surface); color:var(--fg-2); }
.primary-line { border-color:var(--accent); color:var(--accent); }
.danger { border-color:color-mix(in oklab,var(--state-warn),transparent 45%); color:var(--state-warn); }
.danger.confirm { background:color-mix(in oklab,var(--state-warn),transparent 88%); }
.primary:disabled, .act:disabled, .tbtn:disabled { opacity:.5; cursor:not-allowed; }
.toggle { width:36px; height:20px; border:1px solid var(--border); border-radius:999px; background:var(--surface-warm); opacity:.55; cursor:not-allowed; position:relative; }
.toggle::after { content:""; position:absolute; top:3px; right:4px; width:12px; height:12px; border-radius:50%; background:var(--muted); }
.empty { display:flex; flex-direction:column; align-items:center; justify-content:center; gap:5px; min-height:180px; color:var(--muted); }
.empty b { color:var(--fg-2); font-size:var(--text-lg); }
.modal-backdrop { position:fixed; inset:0; z-index:50; display:grid; place-items:center; padding:24px; background:rgba(0,0,0,.62); }
.modal { width:min(720px,100%); border:1px solid var(--border); border-radius:var(--r-lg); background:var(--surface); box-shadow:var(--shadow-lg); padding:16px; }
.modal-head { display:flex; align-items:flex-start; justify-content:space-between; gap:14px; padding-bottom:12px; border-bottom:1px solid var(--border); }
.modal-head h2 { margin:0 0 4px; font-size:var(--text-lg); }
.modal-head p { margin:0; color:var(--muted); font-size:var(--text-sm); }
.field { display:block; margin-top:12px; }
.modal-row { display:grid; grid-template-columns:minmax(0,1fr) 180px; gap:12px; align-items:end; margin-top:12px; }
.priority-preview { min-height:66px; padding:10px 12px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface-warm); }
.priority-preview b { display:block; color:var(--fg); font-family:var(--font-mono); font-size:var(--text-xl); font-variant-numeric:tabular-nums; }
.priority-preview small { color:var(--muted); font-size:var(--text-xs); }
.modal-actions { justify-content:flex-end; margin-top:16px; }
@media (max-width:900px) {
  .head { flex-direction:column; }
  .rule-row { grid-template-columns:28px 28px minmax(0,1fr); }
  .rule-side { grid-column:3; justify-content:flex-start; flex-wrap:wrap; }
  .modal-row { grid-template-columns:1fr; }
}
</style>
