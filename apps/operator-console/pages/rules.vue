<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import type { MutationResult } from '../composables/useApi'
import type {
  RuleOperationItem,
  RuleSelectionOperationAction,
  RuleSelectionPage,
} from '../composables/useOperatorRules'
import { useOperatorRules } from '../composables/useOperatorRules'
import type { RuleRow } from '../composables/useMockData'
import {
  createOperatorSelection,
  type OperatorSelectionTarget,
} from '../composables/useOperatorSelection'

const { t } = useI18n()
const {
  rows,
  scopeOptions,
  loadState,
  pending,
  error,
  refresh,
  createRule: runCreateRule,
  saveRuleSelection,
  freezeRuleSelection,
  currentRuleSelection,
  loadRulePage,
  runRuleSelectionOperation,
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
const selectionPending = ref(false)
const selectionError = ref('')
const selectionPage = ref<RuleSelectionPage | null>(null)
const mutationResult = ref<MutationResult | null>(null)
const operationItems = ref<RuleOperationItem[]>([])
const selection = createOperatorSelection('rules')

const createScopes = computed(() => scopeOptions.value)
const busy = computed(() => pending.value || selectionPending.value)
const selectionTargets = computed(() => selectionPage.value?.targets ?? [])
const scopedRows = computed(() => rows.filter((rule) => scopeFilter.value === 'all' || rule.project === scopeFilter.value))
const visibleRows = computed(() => {
  if (!selectionPage.value) return scopedRows.value
  const rowsByID = new Map(scopedRows.value.map((rule) => [String(rule.id), rule]))
  return selectionPage.value.targets.flatMap((target) => {
    const row = rowsByID.get(target.id)
    return row === undefined ? [] : [row]
  })
})
const canCreate = computed(() => createContent.value.trim().length > 0 && !busy.value)
const editingRule = computed(() => rows.find((rule) => rule.id === editingId.value) || null)
const editChanged = computed(() => Boolean(editingRule.value) && editContent.value.trim() !== editingRule.value?.content)
const canSaveEdit = computed(() => editChanged.value && !busy.value)
const selectedCount = computed(() => selection.selectedCount.value)
const selectionActionReady = computed(() => selection.current.value.kind !== 'none'
  && !selection.current.value.reconfirmationRequired
  && selectedCount.value > 0
  && !busy.value)
const headerAriaChecked = computed<'false' | 'mixed' | 'true'>(() => {
  if (!selectionTargets.value.length) return 'false'
  const selected = selectionTargets.value.filter((target) => isTargetSelected(target)).length
  if (selected === 0) return 'false'
  if (selected === selectionTargets.value.length) return 'true'
  return 'mixed'
})
const canReorderScope = computed(() => {
  const page = selectionPage.value
  if (!page || scopeFilter.value === 'all' || page.nextCursor || page.total !== visibleRows.value.length || page.targets.length !== visibleRows.value.length) {
    return false
  }
  const targetIDs = new Set(page.targets.map((target) => target.id))
  return visibleRows.value.every((rule) => targetIDs.has(String(rule.id)))
})

watch(scopeOptions, (options) => {
  if (!options.includes(createScope.value)) {
    createScope.value = 'global'
  }
  if (scopeFilter.value !== 'all' && !options.includes(scopeFilter.value)) {
    scopeFilter.value = 'all'
  }
}, { immediate: true })

watch(scopeFilter, (scope) => {
  selectionPage.value = null
  selection.invalidate('filter_changed')
  selectionError.value = ''
  void refresh(scope)
})

onMounted(() => {
  void loadCurrentSelection()
})

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

function targetFor(rule: RuleRow): OperatorSelectionTarget {
  return { id: String(rule.id), expectedVersion: rule.version }
}

function isTargetSelected(target: OperatorSelectionTarget) {
  const current = selection.current.value
  switch (current.kind) {
    case 'none':
      return false
    case 'explicit':
    case 'page':
      return current.targets.some((candidate) => candidate.id === target.id)
    case 'frozen_filter':
      return !current.excludedIds.includes(target.id)
  }
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
  mutationResult.value = result
  operationItems.value = []
  if (result.kind === 'committed_verified') {
    closeCreate()
    await refresh(scopeFilter.value)
    await loadSelectionPage()
  }
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

async function loadCurrentSelection() {
  selectionPending.value = true
  selectionError.value = ''
  try {
    selection.applySnapshot(await currentRuleSelection())
  } catch (error) {
    selectionError.value = error instanceof Error ? error.message : t('rules.selection.loadError')
  } finally {
    selectionPending.value = false
  }
}

async function loadSelectionPage(cursor = '') {
  selectionPending.value = true
  selectionError.value = ''
  try {
    selectionPage.value = await loadRulePage({ scope: scopeFilter.value }, cursor)
  } catch (error) {
    selectionError.value = error instanceof Error ? error.message : t('rules.selection.pageError')
  } finally {
    selectionPending.value = false
  }
}

async function saveCurrentSelection() {
  const current = selection.current.value
  if (current.kind === 'frozen_filter') {
    selection.applySnapshot(await freezeRuleSelection({ scope: scopeFilter.value }, current.excludedIds))
    return
  }
  selection.applySnapshot(await saveRuleSelection(current))
}

async function selectCurrentPage() {
  if (!selectionPage.value) {
    await loadSelectionPage()
  }
  if (!selectionPage.value?.targets.length) return

  selectionPending.value = true
  selectionError.value = ''
  try {
    selection.selectCurrentPage(selectionPage.value.cursor, selectionPage.value.targets)
    await saveCurrentSelection()
  } catch (error) {
    selectionError.value = error instanceof Error ? error.message : t('rules.selection.saveError')
  } finally {
    selectionPending.value = false
  }
}

async function freezeCurrentFilter() {
  selectionPending.value = true
  selectionError.value = ''
  try {
    const exclusions = selection.current.value.kind === 'frozen_filter'
      ? selection.current.value.excludedIds
      : []
    selection.applySnapshot(await freezeRuleSelection({ scope: scopeFilter.value }, exclusions))
  } catch (error) {
    selectionError.value = error instanceof Error ? error.message : t('rules.selection.freezeError')
  } finally {
    selectionPending.value = false
  }
}

async function clearSelection() {
  selectionPending.value = true
  selectionError.value = ''
  try {
    selection.clear()
    await saveCurrentSelection()
  } catch (error) {
    selectionError.value = error instanceof Error ? error.message : t('rules.selection.saveError')
  } finally {
    selectionPending.value = false
  }
}

async function toggleHeaderSelection(event: KeyboardEvent | Event) {
  if (event instanceof KeyboardEvent) {
    if (event.key !== ' ' && event.key !== 'Enter') return
    const page = selectionPage.value
    if (selection.current.value.kind !== 'frozen_filter' && page) {
      selectionPending.value = true
      selectionError.value = ''
      try {
        if (selection.handleHeaderKeydown(event, page.cursor, page.targets)) {
          await saveCurrentSelection()
        }
      } catch (error) {
        selectionError.value = error instanceof Error ? error.message : t('rules.selection.saveError')
      } finally {
        selectionPending.value = false
      }
      return
    }
    event.preventDefault()
  }

  if (headerAriaChecked.value === 'true') {
    await clearSelection()
    return
  }
  await selectCurrentPage()
}

async function toggleRuleSelection(rule: RuleRow) {
  const target = targetFor(rule)
  selectionPending.value = true
  selectionError.value = ''
  try {
    const current = selection.current.value
    if (current.kind === 'frozen_filter') {
      const excludedIds = isTargetSelected(target)
        ? [...current.excludedIds, target.id]
        : current.excludedIds.filter((id) => id !== target.id)
      selection.applySnapshot(await freezeRuleSelection({ scope: scopeFilter.value }, excludedIds))
      return
    }

    const selected = current.kind === 'none'
      ? []
      : current.targets.filter((candidate) => candidate.id !== target.id)
    if (!isTargetSelected(target)) selected.push(target)
    if (!selected.length) {
      selection.clear()
    } else {
      selection.selectExplicit(selected)
    }
    await saveCurrentSelection()
  } catch (error) {
    selectionError.value = error instanceof Error ? error.message : t('rules.selection.saveError')
  } finally {
    selectionPending.value = false
  }
}

async function runSelectionAction(action: RuleSelectionOperationAction, fields: { content?: string; priority?: number; scope?: string; order?: Array<{ ruleId: number; expectedVersion: number }> } = {}) {
  if (!selectionActionReady.value) return false

  selectionPending.value = true
  selectionError.value = ''
  operationItems.value = []
  try {
    let current = selection.current.value
    if (current.kind !== 'frozen_filter' && current.version === 0) {
      selection.applySnapshot(await saveRuleSelection(current))
      current = selection.current.value
    }
    if (current.kind === 'none' || current.reconfirmationRequired) return false

    const result = await runRuleSelectionOperation({ action, selection: current, ...fields })
    mutationResult.value = result.mutation
    operationItems.value = result.items
    if (result.mutation.kind !== 'committed_verified') return false

    selection.invalidate('collection_changed')
    await refresh(scopeFilter.value)
    await loadSelectionPage()
    return true
  } catch (error) {
    selectionError.value = error instanceof Error ? error.message : t('rules.selection.operationError')
    return false
  } finally {
    selectionPending.value = false
  }
}

async function runSingleAction(rule: RuleRow, action: RuleSelectionOperationAction, fields: { content?: string; priority?: number } = {}) {
  selection.selectExplicit([targetFor(rule)])
  return runSelectionAction(action, fields)
}

async function saveEdit(rule: RuleRow) {
  if (!editChanged.value) {
    selectionError.value = t('rules.selection.noChanges')
    return
  }
  const completed = await runSingleAction(rule, 'update', {
    content: editContent.value.trim(),
  })
  if (completed) cancelEdit()
}

async function toggleRule(rule: RuleRow) {
  await runSingleAction(rule, rule.enabled ? 'disable' : 'enable')
}

async function confirmDelete(rule: RuleRow) {
  if (confirmingDeleteId.value !== rule.id) {
    confirmingDeleteId.value = rule.id
    return
  }
  const completed = await runSingleAction(rule, 'delete')
  if (completed && editingId.value === rule.id) cancelEdit()
  if (completed) confirmingDeleteId.value = null
}

async function moveRule(ruleId: number, direction: -1 | 1) {
  if (!canReorderScope.value || busy.value) return
  const current = visibleRows.value
  const from = current.findIndex((rule) => rule.id === ruleId)
  const to = from + direction
  if (from < 0 || to < 0 || to >= current.length) return

  const next = [...current]
  const [moved] = next.splice(from, 1)
  next.splice(to, 0, moved)
  selection.selectExplicit(next.map(targetFor))
  await runSelectionAction('reorder', {
    scope: scopeFilter.value,
    order: next.map((rule) => ({ ruleId: rule.id, expectedVersion: rule.version })),
  })
}

function startDrag(rule: RuleRow) {
  if (editingId.value === rule.id || !canReorderScope.value) return
  draggingId.value = rule.id
}

function overDrag(event: DragEvent, rule: RuleRow) {
  if (!draggingId.value || draggingId.value === rule.id || !canReorderScope.value) return
  const target = event.currentTarget
  if (!(target instanceof HTMLElement)) return
  const box = target.getBoundingClientRect()
  dragOverId.value = rule.id
  dragOverAfter.value = event.clientY > box.top + box.height / 2
}

async function dropRule(rule: RuleRow) {
  if (!draggingId.value || draggingId.value === rule.id || !canReorderScope.value || busy.value) {
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
  selection.selectExplicit(current.map(targetFor))
  await runSelectionAction('reorder', {
    scope: scopeFilter.value,
    order: current.map((row) => ({ ruleId: row.id, expectedVersion: row.version })),
  })
}

function clearDrag() {
  draggingId.value = null
  dragOverId.value = null
  dragOverAfter.value = false
}
</script>

<template>
  <div class="rules-page">
    <header class="head">
      <div>
        <h1>{{ t('rules.title') }}</h1>
        <p>{{ t('rules.subtitle') }}</p>
      </div>
      <button class="primary" :disabled="busy" @click="openCreate">{{ t('rules.create.open') }}</button>
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
    <MutationResultNotice :result="mutationResult" :recheck-label="t('rules.actions.refresh')" @recheck="refresh" />

    <section class="pane">
      <div class="rule-reorder-note">
        <span aria-hidden="true">⠿</span>
        <span>{{ t('rules.reorder.note') }}</span>
      </div>

      <div class="callout good">
        <HonestyBadge cls="live" />
        <span>{{ t('rules.reorder.liveCallout') }}</span>
        <code>GET /api/rules?all=true</code>
        <code>POST /api/collections/selection</code>
        <code>POST /api/rules</code>
      </div>

      <div class="toolbar">
        <label class="scope-filter">
          <span>{{ t('rules.scope.filter') }}</span>
          <select v-model="scopeFilter" class="fsel" :disabled="busy">
            <option value="all">{{ t('rules.scope.all') }}</option>
            <option v-for="scope in createScopes" :key="scope" :value="scope">{{ scopeLabel(scope) }}</option>
          </select>
        </label>
        <span class="toolbar-count">{{ t('rules.list.loaded', { count: visibleRows.length }) }}</span>
        <span class="spacer" />
        <button class="tbtn" :disabled="busy" @click="refresh(scopeFilter)">{{ t('rules.actions.refresh') }}</button>
      </div>

      <section class="selection-panel" aria-labelledby="rules-selection-title">
        <div class="selection-summary">
          <strong id="rules-selection-title">{{ t('rules.selection.title') }}</strong>
          <span data-testid="rules-selection-kind">{{ selection.current.value.kind === 'frozen_filter' ? t('rules.selection.kind.frozen_filter') : t(`rules.selection.kind.${selection.current.value.kind}`) }}</span>
          <span class="mono-data" data-testid="rules-selection-version">{{ selection.current.value.version ? t('rules.selection.version', { version: selection.current.value.version }) : t('rules.selection.unsaved') }}</span>
          <span v-if="selectedCount" class="selection-count">{{ t('common.selectedCount', { count: selectedCount }) }}</span>
        </div>
        <div class="selection-actions">
          <button class="tbtn" data-testid="rules-selection-load-page" :disabled="busy" @click="loadSelectionPage()">{{ t('rules.selection.loadPage') }}</button>
          <button class="act" data-testid="rules-selection-page" :disabled="busy || !selectionPage?.targets.length" @click="selectCurrentPage">{{ t('rules.selection.selectPage') }}</button>
          <button class="act primary-line" data-testid="rules-selection-freeze" :disabled="busy" @click="freezeCurrentFilter">{{ t('rules.selection.freeze') }}</button>
          <button class="tbtn" :disabled="busy || !selectedCount" @click="clearSelection">{{ t('common.clearSelection') }}</button>
        </div>
        <p v-if="selectionPage" class="selection-page" data-testid="rules-selection-page-info">
          {{ t('rules.selection.pageInfo', { count: selectionPage.targets.length, total: selectionPage.total ?? '?' }) }}
          <span v-if="selectionPage.nextCursor">{{ t('rules.selection.nextPageBound') }}</span>
        </p>
        <p v-if="selection.current.value.kind === 'frozen_filter'" class="selection-page" data-testid="rules-selection-frozen-info">
          {{ t('rules.selection.frozenInfo', { count: selection.current.value.targetCount, expires: selection.current.value.expiresAt }) }}
        </p>
        <p v-if="selection.current.value.reconfirmationRequired" class="selection-warning" data-testid="rules-selection-reconfirm">
          {{ t('rules.selection.reconfirm', { reason: selection.current.value.reconfirmationReason }) }}
        </p>
        <p v-if="selectionError" class="selection-warning" role="status">{{ selectionError }}</p>
      </section>

      <div v-if="selectedCount" class="bulk-actions" data-testid="rules-bulk-actions">
        <span>{{ t('common.selectedCount', { count: selectedCount }) }}</span>
        <button class="act" :disabled="!selectionActionReady" @click="runSelectionAction('enable')">{{ t('rules.selection.enable') }}</button>
        <button class="act" :disabled="!selectionActionReady" @click="runSelectionAction('disable')">{{ t('rules.selection.disable') }}</button>
        <button class="act danger" :disabled="!selectionActionReady" @click="runSelectionAction('delete')">{{ t('rules.selection.delete') }}</button>
      </div>

      <section v-if="operationItems.length" class="operation-readbacks" data-testid="rules-operation-readbacks" aria-live="polite">
        <strong>{{ t('rules.selection.readbacks') }}</strong>
        <ul>
          <li v-for="item in operationItems" :key="item.targetId">
            <code>#{{ item.targetId }}</code>
            <span>{{ t(`mutationOutcome.items.${item.outcome}`) }}</span>
            <template v-if="item.readback?.kind === 'current'">
              <span>{{ item.readback.current.enabled ? t('rules.detail.enabled') : t('rules.detail.disabled') }}</span>
              <code>v{{ item.readback.version ?? item.readback.current.version }}</code>
            </template>
            <span v-else-if="item.readback?.kind === 'authorized_absence'">{{ t('mutationOutcome.readbackKinds.authorized_absence') }}</span>
          </li>
        </ul>
      </section>

      <div class="rules-grid">
        <div class="grid-h rule-grid-header">
          <input
            id="rules-page-selection"
            type="checkbox"
            data-testid="rules-page-selection"
            :checked="headerAriaChecked === 'true'"
            :indeterminate="headerAriaChecked === 'mixed'"
            :aria-checked="headerAriaChecked"
            :aria-label="t('rules.selection.header')"
            :disabled="busy || !selectionTargets.length"
            @change="toggleHeaderSelection"
            @keydown="toggleHeaderSelection"
          >
          <label for="rules-page-selection">{{ t('rules.list.header') }}</label>
        </div>

        <div
          v-for="(rule, index) in visibleRows"
          :key="rule.id"
          class="rule-row"
          :class="{
            editing: editingId === rule.id,
            dragging: draggingId === rule.id,
            'disabled-rule': !rule.enabled,
            'drag-over': dragOverId === rule.id && !dragOverAfter,
            'drag-over-bottom': dragOverId === rule.id && dragOverAfter,
          }"
          :draggable="editingId !== rule.id && canReorderScope"
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
              <p v-if="!editChanged" class="selection-page">{{ t('rules.selection.noChanges') }}</p>
            </div>
          </template>

          <template v-else>
            <input
              class="rule-check"
              type="checkbox"
              :checked="isTargetSelected(targetFor(rule))"
              :aria-label="t('rules.selection.row', { id: rule.id })"
              :disabled="busy"
              @change="toggleRuleSelection(rule)"
            >
            <button
              class="rule-grip"
              :aria-label="t('rules.reorder.gripLabel', { position: index + 1, total: visibleRows.length })"
              :title="canReorderScope ? t('rules.reorder.gripTitle') : t('rules.selection.reorderBound')"
              :disabled="!canReorderScope || busy"
              @keydown.up.prevent="moveRule(rule.id, -1)"
              @keydown.down.prevent="moveRule(rule.id, 1)"
              @click.stop
            >⠿</button>
            <span class="rule-rank" :title="t('rules.reorder.rankTitle')">{{ index + 1 }}</span>
            <div class="rule-body">
              <div class="rule-preview">{{ rule.content }}</div>
              <div class="rule-meta">
                <span class="scope-chip">{{ scopeLabel(rule.project) }}</span>
                <span class="rule-status-chip" :class="{ off: !rule.enabled }" :data-testid="`rule-status-${rule.id}`">
                  {{ rule.enabled ? t('rules.detail.enabled') : t('rules.detail.disabled') }}
                </span>
                <span v-for="meta in rowMeta(rule, index)" :key="meta">{{ meta }}</span>
              </div>
            </div>
            <div class="rule-side">
              <button class="act" :disabled="busy" @click="startEdit(rule)">{{ t('rules.detail.edit') }}</button>
              <button
                class="toggle"
                :class="{ on: rule.enabled }"
                role="switch"
                :aria-checked="String(rule.enabled)"
                :aria-label="rule.enabled ? t('rules.detail.disable') : t('rules.detail.enable')"
                :title="t('rules.detail.enableBody')"
                :data-testid="`rule-enable-toggle-${rule.id}`"
                :disabled="busy"
                @click="toggleRule(rule)"
              >
                <span class="sr-only">{{ rule.enabled ? t('rules.detail.enabled') : t('rules.detail.disabled') }}</span>
              </button>
              <button
                class="act danger"
                :class="{ confirm: confirmingDeleteId === rule.id }"
                :disabled="busy"
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
.toolbar, .selection-actions, .bulk-actions, .rule-editor-row, .modal-actions { display:flex; align-items:center; gap:10px; flex-wrap:wrap; }
.scope-filter span, .field span, .scope-readonly span, .priority-preview span { display:block; margin-bottom:5px; color:var(--muted); font-size:var(--text-xs); font-weight:800; text-transform:uppercase; letter-spacing:.06em; }
.fsel, .text { border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface-warm); color:var(--fg); font:inherit; }
.fsel { min-height:34px; padding:0 10px; }
.text { width:100%; min-height:78px; padding:10px 11px; resize:vertical; font-size:var(--text-sm); line-height:1.45; }
.text.tall { min-height:170px; }
.toolbar-count { color:var(--muted); font-size:var(--text-sm); }
.spacer { flex:1; }
.selection-panel, .operation-readbacks { display:grid; gap:9px; padding:11px 12px; border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface-warm); }
.selection-summary { display:flex; align-items:baseline; gap:8px; flex-wrap:wrap; font-size:var(--text-sm); color:var(--fg-2); }
.selection-summary strong { color:var(--fg); }
.selection-count { color:var(--accent); font-weight:700; }
.selection-page, .selection-warning { margin:0; font-size:var(--text-xs); color:var(--muted); }
.selection-warning { color:var(--state-warn); }
.bulk-actions { padding:10px 12px; border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); color:var(--fg-2); font-size:var(--text-sm); }
.operation-readbacks { background:var(--surface); }
.operation-readbacks strong { font-size:var(--text-sm); }
.operation-readbacks ul { display:grid; gap:5px; margin:0; padding-left:18px; color:var(--fg-2); font-size:var(--text-xs); }
.operation-readbacks li { display:flex; flex-wrap:wrap; gap:8px; align-items:baseline; }
.mono-data, code { font-family:var(--font-mono); font-variant-numeric:tabular-nums; }
.rules-grid { overflow:hidden; border:1px solid var(--border); border-radius:var(--r-md); background:var(--bg); }
.grid-h { padding:10px 14px; border-bottom:1px solid var(--border); color:var(--muted); font-size:var(--text-xs); font-weight:900; letter-spacing:.08em; text-transform:uppercase; }
.rule-grid-header { display:flex; align-items:center; gap:10px; }
.rule-grid-header input, .rule-check { inline-size:16px; block-size:16px; accent-color:var(--accent); }
.rule-grid-header input:focus-visible, .rule-check:focus-visible { outline:2px solid var(--accent); outline-offset:2px; }
.rule-row { display:grid; grid-template-columns:24px 30px 30px minmax(0,1fr) auto; gap:12px; align-items:center; min-height:64px; padding:12px 14px; border-bottom:1px solid var(--border-soft); transition:box-shadow var(--motion-fast) var(--ease-standard), opacity var(--motion-fast) var(--ease-standard), background var(--motion-fast) var(--ease-standard); }
.rule-row:last-child { border-bottom:0; }
.rule-row:hover { background:var(--surface-warm); }
.rule-row.disabled-rule { opacity:.72; }
.rule-row.disabled-rule .rule-preview { color:var(--fg-2); }
.rule-row.dragging { opacity:.5; }
.rule-row.drag-over { box-shadow:inset 0 2px 0 var(--accent); }
.rule-row.drag-over-bottom { box-shadow:inset 0 -2px 0 var(--accent); }
.rule-row.editing { grid-template-columns:1fr; align-items:stretch; background:color-mix(in oklab,var(--accent),transparent 92%); box-shadow:inset 3px 0 0 var(--accent); }
.rule-grip { width:28px; height:32px; display:grid; place-items:center; border:0; border-radius:var(--r-sm); background:transparent; color:var(--muted); cursor:grab; font-size:18px; }
.rule-grip:hover:not(:disabled), .rule-grip:focus-visible { background:var(--surface-warm); color:var(--fg); outline:none; }
.rule-grip:active { cursor:grabbing; }
.rule-grip:disabled { cursor:not-allowed; opacity:.45; }
.rule-rank { width:28px; height:28px; display:grid; place-items:center; border-radius:var(--radius-pill); background:var(--surface-warm); color:var(--fg); font-family:var(--font-mono); font-size:var(--text-xs); font-variant-numeric:tabular-nums; }
.rule-body { min-width:0; }
.rule-preview { color:var(--fg); font-size:var(--text-sm); line-height:1.35; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.rule-meta { display:flex; align-items:center; gap:8px; flex-wrap:wrap; margin-top:5px; color:var(--muted); font-size:var(--text-xs); }
.scope-chip { border:1px solid color-mix(in oklab,var(--accent),transparent 60%); border-radius:var(--radius-pill); padding:2px 7px; color:var(--accent); background:color-mix(in oklab,var(--accent),transparent 91%); }
.rule-status-chip { border:1px solid color-mix(in oklab,var(--class-live),transparent 60%); border-radius:var(--radius-pill); padding:2px 7px; color:var(--class-live); background:color-mix(in oklab,var(--class-live),transparent 91%); }
.rule-status-chip.off { border-color:color-mix(in oklab,var(--class-dormant),transparent 60%); color:var(--class-dormant); background:color-mix(in oklab,var(--class-dormant),transparent 90%); }
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
.toggle { width:38px; height:22px; border:1px solid var(--border); border-radius:999px; background:var(--surface-warm); cursor:pointer; position:relative; transition:background var(--motion-fast) var(--ease-standard), border-color var(--motion-fast) var(--ease-standard); }
.toggle::after { content:""; position:absolute; top:4px; left:4px; width:12px; height:12px; border-radius:50%; background:var(--muted); transition:transform var(--motion-fast) var(--ease-standard), background var(--motion-fast) var(--ease-standard); }
.toggle.on { border-color:color-mix(in oklab,var(--class-live),transparent 55%); background:color-mix(in oklab,var(--class-live),transparent 78%); }
.toggle.on::after { transform:translateX(16px); background:var(--class-live); }
.toggle:disabled { opacity:.55; cursor:not-allowed; }
.sr-only { position:absolute; width:1px; height:1px; padding:0; margin:-1px; overflow:hidden; clip:rect(0,0,0,0); white-space:nowrap; border:0; }
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
  .rule-row { grid-template-columns:24px 28px 28px minmax(0,1fr); }
  .rule-side { grid-column:4; justify-content:flex-start; flex-wrap:wrap; }
  .modal-row { grid-template-columns:1fr; }
}
@media (max-width:480px) {
  .pane { padding:10px; }
  .rule-row { grid-template-columns:24px 28px minmax(0,1fr); gap:8px; padding:11px 10px; }
  .rule-rank { display:none; }
  .rule-side { grid-column:3; }
  .selection-actions > * { flex:1 1 132px; }
}
@media (pointer:coarse) {
  .primary, .act, .tbtn { min-height:44px; }
  .toggle { width:44px; height:28px; }
  .toggle::after { top:5px; left:5px; width:16px; height:16px; }
  .toggle.on::after { transform:translateX(16px); }
}
</style>
