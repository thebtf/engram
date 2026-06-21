<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { resolvePageSize, usePersistentPageSize } from '../composables/usePersistentPageSize'
import {
  useOperatorIssues,
  type IssueUpdateInput,
  type OperatorIssue,
  type OperatorIssuePriority,
  type OperatorIssueStatus,
  type OperatorIssueType,
} from '../composables/useOperatorIssues'

type IssueFilter = 'all' | 'open' | 'work' | 'closed' | 'rejected'
type BulkField = 'status' | 'priority' | 'type'
type LabelMode = 'add' | 'remove' | 'replace'
type CreateTemplate = 'bug' | 'handoff' | 'question' | 'improvement'

const { t } = useI18n()
const {
  rows,
  comments,
  trackedProjects,
  registryTotal,
  detail,
  loadState,
  detailState,
  pending,
  error,
  routeChangeAction,
  refresh,
  openIssue,
  createIssue: runCreateIssue,
  updateIssue,
  commentIssue,
  rejectIssue,
  acknowledgeIssue,
  bulkAcknowledgeIssues,
  bulkUpdateIssues,
  deleteIssue,
} = useOperatorIssues()

const priorityOptions: OperatorIssuePriority[] = ['critical', 'high', 'medium', 'low']
const typeOptions: OperatorIssueType[] = ['bug', 'feature', 'improvement', 'task']
const statusOptions: OperatorIssueStatus[] = ['open', 'acknowledged', 'reopened', 'resolved', 'closed', 'rejected']
const filterOptions: IssueFilter[] = ['all', 'open', 'work', 'closed', 'rejected']
const templateOptions: CreateTemplate[] = ['bug', 'handoff', 'question', 'improvement']

const { pageSize, pageSizeOptions } = usePersistentPageSize('issues', 10)
const page = ref(1)
const filter = ref<IssueFilter>('all')
const selectedIds = ref<number[]>([])
const activeId = ref<number | null>(null)
const showCreate = ref(false)
const showReject = ref(false)
const showDelete = ref(false)
const showBulkReject = ref(false)
const showBulkField = ref(false)
const showBulkLabels = ref(false)
const notice = ref('')
const createBodyInput = ref<HTMLTextAreaElement | null>(null)
const commentInput = ref<HTMLTextAreaElement | null>(null)
const activeTemplate = ref<CreateTemplate>('bug')
const createSourceProject = ref('operator')
const selectedCommentKeys = ref<string[]>([])
const hoverIssue = ref<OperatorIssue | null>(null)
const hoverStyle = ref<Record<string, string>>({})
let hoverOpenTimer: ReturnType<typeof setTimeout> | null = null
let hoverCloseTimer: ReturnType<typeof setTimeout> | null = null
const ISSUE_HOVER_VIEWPORT_MARGIN = 12

const createTitle = ref('')
const createBody = ref('')
const createTargetProject = ref('engram')
const createPriority = ref<OperatorIssuePriority>('high')
const createType = ref<OperatorIssueType>('bug')
const createLabels = ref<string[]>(['operator-created'])

const statusDraft = ref<OperatorIssueStatus>('open')
const priorityDraft = ref<OperatorIssuePriority>('medium')
const typeDraft = ref<OperatorIssueType>('task')
const labelDraft = ref<string[]>([])
const commentDraft = ref('')
const rejectComment = ref('')
const bulkRejectComment = ref('')
const bulkField = ref<BulkField>('status')
const bulkValue = ref<string>('acknowledged')
const bulkLabelMode = ref<LabelMode>('add')
const bulkLabelValue = ref('operator-created')

const activeIssue = computed(() => {
  if (activeId.value === null) return null
  if (detail.value?.id === activeId.value) return detail.value
  return rows.find((issue) => issue.id === activeId.value) || null
})
const selectedIdSet = computed(() => new Set(selectedIds.value))
const selectedRows = computed(() => rows.filter((issue) => selectedIdSet.value.has(issue.id)))
const selectedCount = computed(() => selectedIds.value.length)
const hasLoadedRows = computed(() => rows.length > 0 || (Array.isArray(loadState.value.data) && loadState.value.data.length > 0))
const showInitialPending = computed(() => pending.value && !hasLoadedRows.value)
const showStatebar = computed(() => Boolean(notice.value || showInitialPending.value || error.value || loadState.value.kind === 'empty'))
const statebarKind = computed(() => error.value ? 'error' : showInitialPending.value ? 'pending' : loadState.value.kind)

const stats = computed(() => ({
  open: rows.filter((issue) => issue.status === 'open').length,
  work: rows.filter((issue) => issue.status === 'acknowledged' || issue.status === 'reopened').length,
  done: rows.filter((issue) => issue.status === 'resolved' || issue.status === 'closed').length,
  total: registryTotal.value,
}))

const filteredRows = computed(() => {
  const groups: Record<IssueFilter, OperatorIssueStatus[]> = {
    all: [],
    open: ['open'],
    work: ['acknowledged', 'reopened'],
    closed: ['resolved', 'closed'],
    rejected: ['rejected'],
  }
  if (filter.value === 'all') return rows
  return rows.filter((issue) => groups[filter.value].includes(issue.status))
})

const effectivePageSize = computed(() => resolvePageSize(pageSize.value, filteredRows.value.length))
const totalPages = computed(() => Math.max(1, Math.ceil(filteredRows.value.length / effectivePageSize.value)))
const pageRange = computed(() => {
  if (!filteredRows.value.length) return { from: 0, to: 0 }
  const from = (page.value - 1) * effectivePageSize.value + 1
  const to = Math.min(filteredRows.value.length, page.value * effectivePageSize.value)
  return { from, to }
})
const pageRows = computed(() => filteredRows.value.slice((page.value - 1) * effectivePageSize.value, page.value * effectivePageSize.value))
const canCreate = computed(() => createTitle.value.trim().length > 0 && createTargetProject.value.trim().length > 0 && !pending.value)
const canComment = computed(() => Boolean(activeIssue.value) && commentDraft.value.trim().length > 0 && !pending.value)
const canReject = computed(() => Boolean(activeIssue.value) && rejectComment.value.trim().length > 0 && !pending.value)
const canBulkReject = computed(() => selectedCount.value > 0 && bulkRejectComment.value.trim().length > 0 && !pending.value)
const projectOptions = computed(() => trackedProjects.value.length ? trackedProjects.value : ['engram'])
const pageTitle = computed(() => activeIssue.value ? t('issues.detail.title', { id: activeIssue.value.id }) : t('issues.title'))
const pageSubtitle = computed(() => activeIssue.value ? t('issues.detail.subtitle') : t('issues.subtitle'))
const labelOptions = computed(() => {
  const labels = new Set(['operator-created', 'bug', 'feature', 'handoff', 'evidence', 'must-build'])
  for (const issue of rows) {
    for (const label of issue.labels) {
      labels.add(label)
    }
  }
  return [...labels].sort((left, right) => left.localeCompare(right))
})
const selectedCommentSet = computed(() => new Set(selectedCommentKeys.value))
const selectedThreadCount = computed(() => selectedCommentKeys.value.length)
const hoverThreadStats = computed(() => {
  const issue = hoverIssue.value
  if (!issue) return null
  const activeComments = detail.value?.id === issue.id ? comments : []
  return {
    count: issue.comments || activeComments.length,
    participants: new Set(activeComments.map((comment) => comment.authorProject || comment.authorAgent)).size,
    last: activeComments[activeComments.length - 1]?.age || issue.age || '—',
    hasThread: Boolean(issue.comments || activeComments.length),
  }
})

watch(filter, () => {
  page.value = 1
})

watch(pageSize, () => {
  page.value = 1
})

watch(filteredRows, () => {
  if (page.value > totalPages.value) {
    page.value = totalPages.value
  }
})

watch(activeIssue, (issue) => {
  statusDraft.value = issue?.status || 'open'
  priorityDraft.value = issue?.priority || 'medium'
  typeDraft.value = issue?.type || 'task'
  labelDraft.value = issue ? [...issue.labels] : []
  commentDraft.value = ''
  rejectComment.value = ''
  selectedCommentKeys.value = []
  hideIssueHover()
  showReject.value = false
  showDelete.value = false
})

function resetCreate() {
  createTitle.value = ''
  createBody.value = ''
  createSourceProject.value = 'operator'
  createTargetProject.value = projectOptions.value.includes('engram') ? 'engram' : projectOptions.value[0] || 'engram'
  createPriority.value = 'high'
  createType.value = 'bug'
  createLabels.value = ['operator-created']
  activeTemplate.value = 'bug'
}

function setNotice(key: string, params: Record<string, unknown> = {}) {
  notice.value = t(key, params)
}

function mutationOk(result: unknown) {
  return Boolean(result && typeof result === 'object' && 'kind' in result && (result as { kind?: string }).kind === 'success')
}

function mutationError(result: unknown) {
  if (result && typeof result === 'object' && 'error' in result) {
    return (result as { error?: { message?: string } }).error?.message || ''
  }
  return ''
}

function selectedCountText() {
  return t('issues.bulk.selected', selectedCount.value, { count: selectedCount.value })
}

function rowRoute(issue: OperatorIssue) {
  return `${issue.sourceDisplayName} → ${issue.targetDisplayName}`
}

function isSelected(id: number) {
  return selectedIdSet.value.has(id)
}

function toggleSelection(id: number) {
  selectedIds.value = isSelected(id)
    ? selectedIds.value.filter((selected) => selected !== id)
    : [...selectedIds.value, id]
}

function selectAllFiltered() {
  selectedIds.value = filteredRows.value.map((issue) => issue.id)
}

function clearSelection() {
  selectedIds.value = []
}

function resetFilter() {
  filter.value = 'all'
  page.value = 1
}

function hasFilter() {
  return filter.value !== 'all'
}

async function selectIssue(issue: OperatorIssue) {
  activeId.value = issue.id
  await openIssue(issue.id)
}

function closeWorkspace() {
  activeId.value = null
}

function openCreate() {
  resetCreate()
  showCreate.value = true
}

function toggleCreateLabel(label: string) {
  createLabels.value = createLabels.value.includes(label)
    ? createLabels.value.filter((item) => item !== label)
    : [...createLabels.value, label]
}

function applyTemplate(kind: CreateTemplate) {
  activeTemplate.value = kind
  createType.value = kind === 'improvement' ? 'improvement' : kind === 'handoff' ? 'task' : kind === 'question' ? 'task' : 'bug'
  createPriority.value = kind === 'bug' ? 'high' : 'medium'
  createBody.value = t(`issues.create.templates.${kind}.body`)
}

function insertMarkdown(target: 'create' | 'comment', before: string, after = '', fallback = '') {
  const area = target === 'create' ? createBodyInput.value : commentInput.value
  const valueRef = target === 'create' ? createBody : commentDraft
  const value = valueRef.value
  const start = area?.selectionStart ?? value.length
  const end = area?.selectionEnd ?? value.length
  const selected = value.slice(start, end) || fallback
  valueRef.value = `${value.slice(0, start)}${before}${selected}${after}${value.slice(end)}`
  requestAnimationFrame(() => {
    area?.focus()
    const cursor = start + before.length + selected.length + after.length
    area?.setSelectionRange(cursor, cursor)
  })
}

function commentKey(comment: { id?: number }, index: number) {
  return comment.id ? `comment:${comment.id}` : `comment:${activeIssue.value?.id || 'active'}:${index}`
}

function isCommentSelected(comment: { id?: number }, index: number) {
  return selectedCommentSet.value.has(commentKey(comment, index))
}

function toggleCommentSelection(comment: { id?: number }, index: number) {
  const key = commentKey(comment, index)
  selectedCommentKeys.value = selectedCommentSet.value.has(key)
    ? selectedCommentKeys.value.filter((item) => item !== key)
    : [...selectedCommentKeys.value, key]
}

function selectAllThread() {
  selectedCommentKeys.value = comments.map((comment, index) => commentKey(comment, index))
}

function clearThreadSelection() {
  selectedCommentKeys.value = []
}

async function updateCurrentField(field: 'status' | 'priority' | 'type', value: string) {
  if (!activeIssue.value || pending.value) return
  const patch: IssueUpdateInput =
    field === 'status'
      ? { status: value as OperatorIssueStatus }
      : field === 'priority'
        ? { priority: value as OperatorIssuePriority }
        : { type: value as OperatorIssueType }
  const result = await updateIssue(activeIssue.value.id, {
    ...patch,
    comment: t('issues.detail.fieldChangeComment', { field: t(`issues.detail.${field}`) }),
  })
  setNotice(mutationOk(result) ? 'issues.notice.saved' : 'issues.notice.error', {
    message: mutationError(result) || t('issues.notice.unknownError'),
  })
}

async function toggleIssueLabel(label: string) {
  if (!activeIssue.value || pending.value) return
  const labels = labelDraft.value.includes(label)
    ? labelDraft.value.filter((item) => item !== label)
    : [...labelDraft.value, label]
  labelDraft.value = labels
  const result = await updateIssue(activeIssue.value.id, {
    labels,
    comment: t('issues.detail.labelsComment'),
  })
  setNotice(mutationOk(result) ? 'issues.notice.saved' : 'issues.notice.error', {
    message: mutationError(result) || t('issues.notice.unknownError'),
  })
}

function showIssueHover(issue: OperatorIssue, event: MouseEvent) {
  if (activeIssue.value || !import.meta.client || window.matchMedia('(pointer: coarse)').matches) return
  cancelIssueHoverClose()
  if (hoverOpenTimer) clearTimeout(hoverOpenTimer)
  const row = event.currentTarget as HTMLElement
  hoverOpenTimer = setTimeout(() => {
    hoverIssue.value = issue
    const rowRect = row.getBoundingClientRect()
    const width = Math.min(440, window.innerWidth - ISSUE_HOVER_VIEWPORT_MARGIN * 2)
    let left = rowRect.right + ISSUE_HOVER_VIEWPORT_MARGIN
    if (left + width > window.innerWidth - ISSUE_HOVER_VIEWPORT_MARGIN) {
      left = Math.max(ISSUE_HOVER_VIEWPORT_MARGIN, rowRect.left - width - ISSUE_HOVER_VIEWPORT_MARGIN)
    }
    hoverStyle.value = {
      width: `${width}px`,
      left: `${left}px`,
      top: `${clampIssueHoverTop(rowRect.top)}px`,
    }
    void nextTick().then(() => {
      requestAnimationFrame(() => {
        const hover = document.querySelector<HTMLElement>('.issue-hover')
        if (!hover || hoverIssue.value?.id !== issue.id) return
        hoverStyle.value = {
          ...hoverStyle.value,
          top: `${clampIssueHoverTop(rowRect.top, hover.getBoundingClientRect().height)}px`,
        }
      })
    })
  }, 280)
}

function clampIssueHoverTop(anchorTop: number, measuredHeight = Math.min(560, window.innerHeight - ISSUE_HOVER_VIEWPORT_MARGIN * 2)) {
  const safeHeight = Math.min(measuredHeight, window.innerHeight - ISSUE_HOVER_VIEWPORT_MARGIN * 2)
  const maxTop = Math.max(ISSUE_HOVER_VIEWPORT_MARGIN, window.innerHeight - safeHeight - ISSUE_HOVER_VIEWPORT_MARGIN)
  return Math.max(ISSUE_HOVER_VIEWPORT_MARGIN, Math.min(anchorTop, maxTop))
}

function cancelIssueHoverClose() {
  if (hoverCloseTimer) clearTimeout(hoverCloseTimer)
  hoverCloseTimer = null
}

function scheduleIssueHoverHide() {
  if (hoverOpenTimer) clearTimeout(hoverOpenTimer)
  hoverOpenTimer = null
  if (hoverCloseTimer) clearTimeout(hoverCloseTimer)
  hoverCloseTimer = setTimeout(() => {
    hideIssueHover()
  }, 350)
}

function hideIssueHover() {
  if (hoverOpenTimer) clearTimeout(hoverOpenTimer)
  if (hoverCloseTimer) clearTimeout(hoverCloseTimer)
  hoverOpenTimer = null
  hoverCloseTimer = null
  hoverIssue.value = null
}

async function createNewIssue() {
  if (!canCreate.value) return
  const result = await runCreateIssue({
    title: createTitle.value.trim(),
    body: createBody.value.trim(),
    priority: createPriority.value,
    type: createType.value,
    sourceProject: createSourceProject.value.trim() || 'operator',
    targetProject: createTargetProject.value.trim(),
    labels: createLabels.value,
  })

  if (mutationOk(result)) {
    const id = (result as { data?: { id?: number } }).data?.id
    showCreate.value = false
    setNotice('issues.notice.created', { id })
    if (id) {
      activeId.value = id
      await openIssue(id)
    }
    resetCreate()
    return
  }

  setNotice('issues.notice.error', { message: mutationError(result) || t('issues.notice.unknownError') })
}

async function acknowledgeCurrent() {
  if (!activeIssue.value) return
  const result = await acknowledgeIssue(activeIssue.value.id)
  setNotice(mutationOk(result) ? 'issues.notice.acknowledged' : 'issues.notice.error', {
    message: mutationError(result) || t('issues.notice.unknownError'),
  })
}

async function resolveCurrent() {
  if (!activeIssue.value) return
  const result = await updateIssue(activeIssue.value.id, {
    status: 'resolved',
    comment: t('issues.detail.resolveComment'),
  })
  setNotice(mutationOk(result) ? 'issues.notice.resolved' : 'issues.notice.error', {
    message: mutationError(result) || t('issues.notice.unknownError'),
  })
}

async function addComment() {
  if (!activeIssue.value || !canComment.value) return
  const result = await commentIssue(activeIssue.value.id, commentDraft.value.trim())
  if (mutationOk(result)) {
    setNotice('issues.notice.commented')
    commentDraft.value = ''
    await openIssue(activeIssue.value.id)
    return
  }
  setNotice('issues.notice.error', { message: mutationError(result) || t('issues.notice.unknownError') })
}

async function rejectCurrent() {
  if (!activeIssue.value || !canReject.value) return
  const result = await rejectIssue(activeIssue.value.id, rejectComment.value.trim())
  if (mutationOk(result)) {
    showReject.value = false
    setNotice('issues.notice.rejected')
    await openIssue(activeIssue.value.id)
    return
  }
  setNotice('issues.notice.error', { message: mutationError(result) || t('issues.notice.unknownError') })
}

async function deleteCurrent() {
  if (!activeIssue.value) return
  const id = activeIssue.value.id
  const result = await deleteIssue(id)
  if (mutationOk(result)) {
    showDelete.value = false
    activeId.value = null
    setNotice('issues.notice.deleted', { id })
    return
  }
  setNotice('issues.notice.error', { message: mutationError(result) || t('issues.notice.unknownError') })
}

function openBulkEditor(field: BulkField) {
  bulkField.value = field
  bulkValue.value = field === 'status' ? 'acknowledged' : field === 'priority' ? 'high' : 'task'
  showBulkField.value = true
}

async function applyBulkField() {
  if (!selectedCount.value) return
  const ids = [...selectedIds.value]
  let result: unknown
  if (bulkField.value === 'status' && bulkValue.value === 'acknowledged') {
    result = await bulkAcknowledgeIssues(ids)
  } else {
    result = await bulkUpdateIssues(ids, { [bulkField.value]: bulkValue.value } as IssueUpdateInput)
  }
  if (mutationOk(result)) {
    showBulkField.value = false
    clearSelection()
    setNotice('issues.notice.bulkUpdated')
    return
  }
  setNotice('issues.notice.error', { message: mutationError(result) || t('issues.notice.unknownError') })
}

async function applyBulkReject() {
  if (!canBulkReject.value) return
  const result = await bulkUpdateIssues([...selectedIds.value], {
    status: 'rejected',
    comment: bulkRejectComment.value.trim(),
  })
  if (mutationOk(result)) {
    showBulkReject.value = false
    bulkRejectComment.value = ''
    clearSelection()
    setNotice('issues.notice.bulkRejected')
    return
  }
  setNotice('issues.notice.error', { message: mutationError(result) || t('issues.notice.unknownError') })
}

async function applyBulkLabels() {
  if (!selectedCount.value || !bulkLabelValue.value) return
  const selected = [...selectedRows.value]
  const value = bulkLabelValue.value
  if (bulkLabelMode.value === 'replace') {
    const result = await bulkUpdateIssues(selected.map((issue) => issue.id), { labels: [value] })
    if (!mutationOk(result)) {
      setNotice('issues.notice.error', { message: mutationError(result) || t('issues.notice.unknownError') })
      return
    }
  } else {
    for (const issue of selected) {
      const labels = new Set(issue.labels)
      if (bulkLabelMode.value === 'add') labels.add(value)
      if (bulkLabelMode.value === 'remove') labels.delete(value)
      const result = await updateIssue(issue.id, { labels: [...labels] })
      if (!mutationOk(result)) {
        setNotice('issues.notice.error', { message: mutationError(result) || t('issues.notice.unknownError') })
        return
      }
    }
  }
  showBulkLabels.value = false
  clearSelection()
  setNotice('issues.notice.bulkUpdated')
}

async function copyIssueLink() {
  if (!activeIssue.value || !import.meta.client) return
  const url = `${window.location.origin}${window.location.pathname}#/issues/${activeIssue.value.id}`
  try {
    await navigator.clipboard?.writeText(url)
    setNotice('issues.notice.linkCopied')
  } catch {
    setNotice('issues.notice.linkCopyFailed')
  }
}

function escapeHtml(value: string) {
  return value
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#039;')
}

function renderMarkdown(value: string) {
  const escaped = escapeHtml(value || '')
  return escaped
    .replace(/```([\s\S]*?)```/g, '<pre><code>$1</code></pre>')
    .replace(/^### (.*)$/gm, '<h3>$1</h3>')
    .replace(/\*\*(.*?)\*\*/g, '<strong>$1</strong>')
    .replace(/`([^`]+)`/g, '<code>$1</code>')
    .replace(/^- \[ \] (.*)$/gm, '<p class="task">□ $1</p>')
    .replace(/^- (.*)$/gm, '<p>• $1</p>')
    .replace(/\n{2,}/g, '</p><p>')
    .replace(/\n/g, '<br>')
    .replace(/^(.+)$/s, '<p>$1</p>')
}
</script>

<template>
  <div class="issues-page">
    <header class="head">
      <h1>{{ pageTitle }}</h1>
      <p>{{ pageSubtitle }}</p>
    </header>

    <section v-if="showStatebar" class="statebar" :data-state="statebarKind">
      <span v-if="notice">{{ notice }}</span>
      <span v-else-if="showInitialPending">{{ t('issues.state.pending') }}</span>
      <span v-else-if="error">{{ t('issues.state.error', { message: error }) }}</span>
      <span v-else-if="loadState.kind === 'empty'">{{ t('issues.state.empty') }}</span>
      <button v-if="error" class="tbtn" @click="refresh">{{ t('issues.state.retry') }}</button>
    </section>

    <section v-if="!activeIssue" class="area-body">
      <div class="pane">
        <div class="list-ops">
          <div class="op-left">
            <button class="tbtn" :disabled="!filteredRows.length" @click="selectAllFiltered">{{ t('issues.actions.selectAll') }}</button>
            <button class="tbtn" :disabled="!selectedCount" @click="clearSelection">{{ t('issues.actions.clearSelection') }}</button>
            <button class="tbtn" :disabled="!hasFilter()" @click="resetFilter">{{ t('issues.actions.resetFilter') }}</button>
            <span class="fcount">{{ t('issues.list.countInFilter', { shown: filteredRows.length, total: rows.length }) }}</span>
          </div>
          <div class="op-right">
            <label class="rows">
              <span>{{ t('issues.list.rows') }}</span>
              <select v-model.number="pageSize" class="select" name="issues-page-size">
                <option v-for="size in pageSizeOptions" :key="size" :value="size">
                  {{ size === 0 ? t('issues.list.allRows') : size }}
                </option>
              </select>
            </label>
            <span class="fcount">{{ t('issues.list.pageRange', { from: pageRange.from, to: pageRange.to, total: filteredRows.length }) }}</span>
            <button class="pager" :disabled="page <= 1" @click="page--">‹</button>
            <button class="pager on">{{ page }}</button>
            <button class="pager" :disabled="page >= totalPages" @click="page++">›</button>
          </div>
        </div>

        <div class="issue-create-strip">
          <button class="tbtn" @click="openCreate">{{ t('issues.create.open') }}</button>
        </div>

        <div class="instr-band">
          <div class="lead">
            <div class="ln">{{ stats.open }}</div>
            <div class="ll">{{ t('issues.stats.open') }}</div>
          </div>
          <div class="bfigs">
            <div class="bfig warn"><div class="bn">{{ stats.work }}</div><div class="bl">{{ t('issues.stats.work') }}</div></div>
            <div class="bfig good"><div class="bn">{{ stats.done }}</div><div class="bl">{{ t('issues.stats.done') }}</div></div>
            <div class="bfig"><div class="bn">{{ stats.total }}</div><div class="bl">{{ t('issues.stats.total') }}</div></div>
          </div>
        </div>

        <div class="filterbar">
          <button
            v-for="item in filterOptions"
            :key="item"
            class="fchip"
            :aria-pressed="filter === item"
            @click="filter = item"
          >
            {{ t(`issues.filters.${item}`) }}
          </button>
          <span class="fcount">{{ t('issues.list.filteredTotal', { shown: filteredRows.length, total: registryTotal }) }}</span>
        </div>

        <div class="grid issues-grid">
          <div class="grid-h">
            <span aria-hidden="true"></span>
            <span>#</span>
            <span>{{ t('issues.table.priority') }}</span>
            <span>{{ t('issues.table.type') }}</span>
            <span>{{ t('issues.table.topic') }}</span>
            <span>{{ t('issues.table.status') }}</span>
            <span>{{ t('issues.table.route') }}</span>
            <span>{{ t('issues.table.date') }}</span>
            <span>{{ t('issues.table.comments') }}</span>
          </div>

          <button
            v-for="issue in pageRows"
            :key="issue.id"
            class="issue-row"
            :class="{ sel: isSelected(issue.id), open: activeId === issue.id }"
            :data-status="issue.status"
            type="button"
            @click="selectIssue(issue)"
            @mouseenter="showIssueHover(issue, $event)"
            @mouseleave="scheduleIssueHoverHide"
            @focus="showIssueHover(issue, $event)"
            @blur="scheduleIssueHoverHide"
          >
            <span
              class="echk issue-row-check"
              :class="{ on: isSelected(issue.id) }"
              @click.stop="toggleSelection(issue.id)"
            >{{ isSelected(issue.id) ? '✓' : '' }}</span>
            <span class="issue-row-id">#{{ issue.id }}</span>
            <span class="issue-chip" :data-tone="issue.priority">{{ t(`issues.priority.${issue.priority}`) }}</span>
            <span class="issue-chip" :data-tone="issue.type">{{ t(`issues.type.${issue.type}`) }}</span>
            <span class="issue-row-title">
              <b>{{ issue.title }}</b>
              <span class="tags">
                <span v-for="label in issue.labels" :key="label" class="issue-chip subtle">{{ label }}</span>
              </span>
            </span>
            <span class="issue-chip" :data-tone="issue.status">{{ t(`issues.status.${issue.status}`) }}</span>
            <span class="issue-route-mini">{{ rowRoute(issue) }}</span>
            <span class="issue-age">{{ issue.age }}</span>
            <span class="issue-comments">▱ {{ issue.comments }}</span>
          </button>

          <div v-if="!pageRows.length" class="empty">
            <b>{{ t('issues.empty.title') }}</b>
            <span>{{ t('issues.empty.body') }}</span>
          </div>
        </div>

        <div class="bulkbar" :class="{ show: selectedCount > 0 }">
          <span class="bc">{{ selectedCountText() }}</span>
          <span class="bsp"></span>
          <button class="act" @click="openBulkEditor('status')">{{ t('issues.bulk.status') }}</button>
          <button class="act" @click="openBulkEditor('priority')">{{ t('issues.bulk.priority') }}</button>
          <button class="act" @click="openBulkEditor('type')">{{ t('issues.bulk.type') }}</button>
          <button class="act" disabled :title="routeChangeAction.evidence.reason">{{ t('issues.bulk.route') }}</button>
          <button class="act" @click="showBulkLabels = true">{{ t('issues.bulk.labels') }}</button>
          <button class="act danger" @click="showBulkReject = true">{{ t('issues.bulk.reject') }}</button>
          <span class="bulk-note">{{ t('issues.bulk.note') }}</span>
          <button class="tbtn" @click="clearSelection">{{ t('issues.bulk.cancel') }}</button>
        </div>
      </div>
    </section>

    <section v-else class="issue-workspace">
      <div class="issue-open-head">
        <div class="issue-open-left">
          <button class="act issue-open-back" @click="closeWorkspace">{{ t('issues.detail.back') }}</button>
          <div class="issue-open-title">
            <div class="issue-open-badges">
              <span class="issue-row-id">#{{ activeIssue.id }}</span>
              <span class="issue-chip" :data-tone="activeIssue.type">{{ t(`issues.type.${activeIssue.type}`) }}</span>
              <span class="issue-chip" :data-tone="activeIssue.priority">{{ t(`issues.priority.${activeIssue.priority}`) }}</span>
              <span class="issue-chip" :data-tone="activeIssue.status">{{ t(`issues.status.${activeIssue.status}`) }}</span>
            </div>
            <h2>{{ activeIssue.title }}</h2>
            <div class="route">{{ rowRoute(activeIssue) }} · {{ t('issues.meta.comments', activeIssue.comments, { count: activeIssue.comments }) }}</div>
          </div>
        </div>
        <div class="dactions issue-danger-actions">
          <button class="act" :disabled="pending" @click="resolveCurrent">{{ t('issues.detail.resolve') }}</button>
          <button class="act danger" :disabled="pending" @click="showReject = true">{{ t('issues.detail.reject') }}</button>
        </div>
      </div>

      <div v-if="detailState.kind === 'pending' || detailState.kind === 'error'" class="detail-state" :data-state="detailState.kind">
        <span v-if="detailState.kind === 'pending'">{{ t('issues.detail.loading') }}</span>
        <span v-else>{{ t('issues.detail.error') }}</span>
      </div>

      <div class="issue-open-grid">
        <section class="issue-main">
          <div class="issue-card">
            <div class="dsh">{{ t('issues.detail.bodyMarkdown') }}</div>
            <div class="issue-body md" v-html="renderMarkdown(activeIssue.body || t('issues.detail.emptyBody'))"></div>
          </div>

          <div class="issue-card">
            <div class="issue-thread-head">
              <div class="dsh">{{ t('issues.thread.title') }}</div>
              <span class="count">{{ t('issues.thread.count', comments.length, { count: comments.length }) }}</span>
            </div>
            <div class="thread-actions">
              <div class="left">
                <button class="act" :disabled="!comments.length" @click="selectAllThread">{{ t('issues.thread.selectAll') }}</button>
                <button class="act" :disabled="!selectedThreadCount" @click="clearThreadSelection">{{ t('issues.thread.clear') }}</button>
                <span class="issue-chip subtle">{{ t('issues.thread.selected', selectedThreadCount, { count: selectedThreadCount }) }}</span>
              </div>
              <div class="dactions">
                <span class="issue-chip" data-tone="handoff">{{ t('issues.thread.mustBuild') }}</span>
                <button class="act" disabled :title="t('issues.thread.mustBuildEvidence')">{{ t('issues.thread.export') }}</button>
                <button class="act" disabled :title="t('issues.thread.mustBuildEvidence')">{{ t('issues.thread.pin') }}</button>
              </div>
            </div>
            <div class="thread">
              <article
                v-for="(comment, index) in comments"
                :key="comment.id || comment.createdAt"
                class="comment"
                :class="{ selected: isCommentSelected(comment, index) }"
              >
                <div class="cm">
                  <span
                    class="echk issue-row-check"
                    :class="{ on: isCommentSelected(comment, index) }"
                    @click.stop="toggleCommentSelection(comment, index)"
                  >{{ isCommentSelected(comment, index) ? '✓' : '' }}</span>
                  <span class="agent">{{ comment.authorProject }}</span>
                  <span>{{ comment.authorAgent }}</span>
                  <span>{{ comment.age }}</span>
                </div>
                <div class="body md" v-html="renderMarkdown(comment.body)"></div>
              </article>
              <div v-if="!comments.length" class="thread-empty">{{ t('issues.thread.empty') }}</div>
            </div>
            <div class="composer">
              <div class="dsh">{{ t('issues.thread.operatorReply') }}</div>
              <label class="field comment-kind">
                <span>{{ t('issues.thread.kind') }}</span>
                <select class="txt" disabled :title="t('issues.thread.kindEvidence')">
                  <option>{{ t('issues.thread.kindOperatorNote') }}</option>
                </select>
              </label>
              <div class="md-editor">
                <div class="md-toolbar">
                  <button type="button" @click="insertMarkdown('comment', '**', '**', t('issues.markdown.boldFallback'))">{{ t('issues.markdown.bold') }}</button>
                  <button type="button" @click="insertMarkdown('comment', '*', '*', t('issues.markdown.italicFallback'))">{{ t('issues.markdown.italic') }}</button>
                  <button type="button" @click="insertMarkdown('comment', '> ', '', t('issues.markdown.quoteFallback'))">{{ t('issues.markdown.quote') }}</button>
                  <button type="button" @click="insertMarkdown('comment', '`', '`', t('issues.markdown.codeFallback'))">{{ t('issues.markdown.code') }}</button>
                  <button type="button" @click="insertMarkdown('comment', '- ', '', t('issues.markdown.listFallback'))">{{ t('issues.markdown.list') }}</button>
                </div>
                <textarea ref="commentInput" v-model="commentDraft" class="txt" name="issue-comment" :placeholder="t('issues.thread.placeholder')" />
              </div>
              <div class="dactions">
                <button class="act primary" :disabled="!canComment" @click="addComment">{{ t('issues.thread.submit') }}</button>
              </div>
            </div>
          </div>
        </section>

        <aside class="issue-rail">
          <div class="issue-card">
            <div class="dsh">{{ t('issues.detail.fields') }}</div>
            <div class="edit-grid">
              <label class="edit-field">
                <span>{{ t('issues.detail.status') }}</span>
                <select v-model="statusDraft" class="txt edit-select" name="issue-status" :disabled="pending" @change="updateCurrentField('status', statusDraft)">
                  <option v-for="status in statusOptions" :key="status" :value="status">{{ t(`issues.status.${status}`) }}</option>
                </select>
              </label>
              <label class="edit-field">
                <span>{{ t('issues.detail.priority') }}</span>
                <select v-model="priorityDraft" class="txt edit-select" name="issue-priority" :disabled="pending" @change="updateCurrentField('priority', priorityDraft)">
                  <option v-for="priority in priorityOptions" :key="priority" :value="priority">{{ t(`issues.priority.${priority}`) }}</option>
                </select>
              </label>
              <label class="edit-field">
                <span>{{ t('issues.detail.type') }}</span>
                <select v-model="typeDraft" class="txt edit-select" name="issue-type" :disabled="pending" @change="updateCurrentField('type', typeDraft)">
                  <option v-for="kind in typeOptions" :key="kind" :value="kind">{{ t(`issues.type.${kind}`) }}</option>
                </select>
              </label>
              <label class="edit-field">
                <span>{{ t('issues.detail.route') }}</span>
                <select class="txt edit-select" name="issue-route-target" disabled :title="routeChangeAction.evidence.reason">
                  <option>{{ activeIssue.targetDisplayName }}</option>
                </select>
              </label>
            </div>
            <p class="edit-hint">{{ t('issues.detail.routeMustBuild', { endpoint: routeChangeAction.evidence.endpoint }) }}</p>
            <div class="chip-picker">
              <button
                v-for="label in labelOptions"
                :key="label"
                type="button"
                class="tag-option"
                :class="{ on: labelDraft.includes(label) }"
                :disabled="pending"
                @click="toggleIssueLabel(label)"
              >
                {{ label }}
              </button>
            </div>
            <div class="dactions">
              <button class="act" :disabled="pending" @click="acknowledgeCurrent">{{ t('issues.detail.acknowledge') }}</button>
            </div>
          </div>

          <div class="issue-card">
            <div class="dsh">{{ t('issues.detail.actions') }}</div>
            <div class="dactions">
              <button class="act" @click="copyIssueLink">{{ t('issues.detail.copyLink') }}</button>
              <button class="act danger" @click="showDelete = true">{{ t('issues.detail.delete') }}</button>
            </div>
          </div>
        </aside>
      </div>
    </section>

    <div v-if="showCreate" class="overlay show">
      <section class="modal issue-create-modal" role="dialog" :aria-label="t('issues.create.modalTitle')">
        <div class="issue-modal-head">
          <div>
            <h2>{{ t('issues.create.modalTitle') }}</h2>
            <p>{{ t('issues.create.modalSubtitle') }}</p>
          </div>
          <button class="tbtn" @click="showCreate = false">{{ t('issues.modal.close') }}</button>
        </div>
        <div class="issue-modal-body">
          <div class="issue-draft-main">
            <div class="issue-draft-card issue-description-card">
              <label class="field">
                <span>{{ t('issues.create.whatHappened') }}</span>
                <input v-model="createTitle" class="txt issue-title-input" name="new-issue-title" :placeholder="t('issues.create.titlePlaceholder')" />
              </label>
              <div class="issue-template-row" role="group" :aria-label="t('issues.create.templateGroup')">
                <button
                  v-for="template in templateOptions"
                  :key="template"
                  type="button"
                  class="issue-template"
                  :aria-pressed="activeTemplate === template"
                  @click="applyTemplate(template)"
                >
                  <b>{{ t(`issues.create.templates.${template}.title`) }}</b>
                  <span>{{ t(`issues.create.templates.${template}.hint`) }}</span>
                </button>
              </div>
            </div>
            <div class="issue-draft-card">
              <h3>{{ t('issues.create.description') }}</h3>
              <p>{{ t('issues.create.descriptionHelp') }}</p>
              <div class="md-editor issue-create-editor">
                <div class="md-toolbar">
                  <button type="button" @click="insertMarkdown('create', '**', '**', t('issues.markdown.boldFallback'))">{{ t('issues.markdown.bold') }}</button>
                  <button type="button" @click="insertMarkdown('create', '*', '*', t('issues.markdown.italicFallback'))">{{ t('issues.markdown.italic') }}</button>
                  <button type="button" @click="insertMarkdown('create', '> ', '', t('issues.markdown.quoteFallback'))">{{ t('issues.markdown.quote') }}</button>
                  <button type="button" @click="insertMarkdown('create', '`', '`', t('issues.markdown.codeFallback'))">{{ t('issues.markdown.code') }}</button>
                  <button type="button" @click="insertMarkdown('create', '- ', '', t('issues.markdown.listFallback'))">{{ t('issues.markdown.list') }}</button>
                </div>
                <textarea ref="createBodyInput" v-model="createBody" class="txt" name="new-issue-body" :placeholder="t('issues.create.bodyPlaceholder')" />
                <div class="md-preview md" v-html="renderMarkdown(createBody || t('issues.create.previewEmpty'))"></div>
              </div>
            </div>
          </div>
          <div class="issue-draft-side">
            <div class="issue-draft-card">
              <h3>{{ t('issues.create.route') }}</h3>
              <p>{{ t('issues.create.routeHelp') }}</p>
              <div class="issue-route-grid">
                <label class="field">
                  <span>{{ t('issues.create.sourceProject') }}</span>
                  <input v-model="createSourceProject" class="txt" name="new-issue-source-project" :placeholder="t('issues.create.sourcePlaceholder')" />
                </label>
                <label class="field">
                  <span>{{ t('issues.create.targetProject') }}</span>
                  <select v-model="createTargetProject" class="txt" name="new-issue-target-project">
                    <option v-for="project in projectOptions" :key="project" :value="project">{{ project }}</option>
                  </select>
                </label>
                <label class="field">
                  <span>{{ t('issues.create.priority') }}</span>
                  <select v-model="createPriority" class="txt" name="new-issue-priority">
                    <option v-for="priority in priorityOptions" :key="priority" :value="priority">{{ t(`issues.priority.${priority}`) }}</option>
                  </select>
                </label>
                <label class="field">
                  <span>{{ t('issues.create.type') }}</span>
                  <select v-model="createType" class="txt" name="new-issue-type">
                    <option v-for="kind in typeOptions" :key="kind" :value="kind">{{ t(`issues.type.${kind}`) }}</option>
                  </select>
                </label>
              </div>
            </div>
            <div class="issue-draft-card">
              <h3>{{ t('issues.create.labels') }}</h3>
              <p>{{ t('issues.create.labelsHelp') }}</p>
              <div class="issue-label-picker">
                <button
                  v-for="label in labelOptions"
                  :key="label"
                  type="button"
                  class="tag-option"
                  :class="{ on: createLabels.includes(label) }"
                  @click="toggleCreateLabel(label)"
                >
                  {{ label }}
                </button>
              </div>
            </div>
            <div class="issue-draft-card">
              <h3>{{ t('issues.create.qualityTitle') }}</h3>
              <div class="issue-quality">
                <div class="qitem"><span class="qdot">1</span><span>{{ t('issues.create.qualityTitleClear') }}</span></div>
                <div class="qitem"><span class="qdot">2</span><span>{{ t('issues.create.qualityOwner') }}</span></div>
                <div class="qitem"><span class="qdot">3</span><span>{{ t('issues.create.qualityEvidence') }}</span></div>
              </div>
            </div>
          </div>
        </div>
        <div class="issue-modal-foot">
          <span class="hint">{{ t('issues.create.footerHint') }}</span>
          <div class="dactions">
            <button class="tbtn" @click="showCreate = false">{{ t('issues.modal.cancel') }}</button>
            <button class="tbtn primary" :disabled="!canCreate" @click="createNewIssue">{{ t('issues.create.submit') }}</button>
          </div>
        </div>
      </section>
    </div>

    <div
      v-if="hoverIssue"
      class="issue-hover show"
      :style="hoverStyle"
      role="tooltip"
      tabindex="-1"
      @mouseenter="cancelIssueHoverClose"
      @mouseleave="scheduleIssueHoverHide"
      @focusin="cancelIssueHoverClose"
      @focusout="scheduleIssueHoverHide"
    >
      <div class="ih-head">
        <span class="ih-id">#{{ hoverIssue.id }}</span>
        <span class="ih-title">{{ hoverIssue.title }}</span>
      </div>
      <div class="ih-meta">
        <span class="issue-chip" :data-tone="hoverIssue.priority">{{ t(`issues.priority.${hoverIssue.priority}`) }}</span>
        <span class="issue-chip" :data-tone="hoverIssue.type">{{ t(`issues.type.${hoverIssue.type}`) }}</span>
        <span class="issue-chip" :data-tone="hoverIssue.status">{{ t(`issues.status.${hoverIssue.status}`) }}</span>
        <span class="ih-route">{{ rowRoute(hoverIssue) }} · {{ hoverIssue.age }}</span>
      </div>
      <div class="ih-body md" v-html="renderMarkdown(hoverIssue.body || t('issues.detail.emptyBody'))"></div>
      <div v-if="hoverThreadStats" class="ih-stats">
        <div class="ih-stats-grid">
          <div class="ih-stat"><div class="k">{{ t('issues.hover.messages') }}</div><div class="v">{{ hoverThreadStats.count }}</div></div>
          <div class="ih-stat"><div class="k">{{ t('issues.hover.participants') }}</div><div class="v">{{ hoverThreadStats.participants || '—' }}</div></div>
          <div class="ih-stat"><div class="k">{{ t('issues.hover.last') }}</div><div class="v">{{ hoverThreadStats.last }}</div></div>
        </div>
        <div v-if="!hoverThreadStats.hasThread" class="ih-empty">{{ t('issues.hover.noThread') }}</div>
      </div>
    </div>

    <div v-if="showReject" class="overlay show">
      <section class="modal" role="dialog" :aria-label="t('issues.reject.title', { id: activeIssue?.id })">
        <h2 class="danger-title">{{ t('issues.reject.title', { id: activeIssue?.id }) }}</h2>
        <p>{{ t('issues.reject.body') }}</p>
        <textarea v-model="rejectComment" class="txt confirm-input" name="issue-reject-comment" :placeholder="t('issues.reject.placeholder')" />
        <div class="mf">
          <button class="tbtn" @click="showReject = false">{{ t('issues.modal.cancel') }}</button>
          <button class="tbtn danger-fill" :disabled="!canReject" @click="rejectCurrent">{{ t('issues.reject.submit') }}</button>
        </div>
      </section>
    </div>

    <div v-if="showDelete" class="overlay show">
      <section class="modal" role="dialog" :aria-label="t('issues.delete.title', { id: activeIssue?.id })">
        <h2 class="danger-title">{{ t('issues.delete.title', { id: activeIssue?.id }) }}</h2>
        <p>{{ t('issues.delete.body') }}</p>
        <div class="mf">
          <button class="tbtn" @click="showDelete = false">{{ t('issues.modal.cancel') }}</button>
          <button class="tbtn danger-fill" @click="deleteCurrent">{{ t('issues.delete.submit') }}</button>
        </div>
      </section>
    </div>

    <div v-if="showBulkReject" class="overlay show">
      <section class="modal" role="dialog" :aria-label="t('issues.bulkReject.title')">
        <h2 class="danger-title">{{ t('issues.bulkReject.title') }}</h2>
        <p>{{ t('issues.bulkReject.body', selectedCount, { count: selectedCount }) }}</p>
        <textarea v-model="bulkRejectComment" class="txt confirm-input" name="issue-bulk-reject-comment" :placeholder="t('issues.bulkReject.placeholder')" />
        <div class="mf">
          <button class="tbtn" @click="showBulkReject = false">{{ t('issues.modal.cancel') }}</button>
          <button class="tbtn danger-fill" :disabled="!canBulkReject" @click="applyBulkReject">{{ t('issues.bulkReject.submit') }}</button>
        </div>
      </section>
    </div>

    <div v-if="showBulkField" class="overlay show">
      <section class="modal" role="dialog" :aria-label="t('issues.bulkField.title')">
        <h2>{{ t('issues.bulkField.title') }}</h2>
        <p>{{ t('issues.bulkField.body', selectedCount, { count: selectedCount }) }}</p>
        <label class="field">
          <span>{{ t(`issues.bulkField.${bulkField}`) }}</span>
          <select v-model="bulkValue" class="txt" name="issue-bulk-value">
            <option
              v-for="option in (bulkField === 'status' ? statusOptions : bulkField === 'priority' ? priorityOptions : typeOptions)"
              :key="option"
              :value="option"
            >
              {{ t(`issues.${bulkField === 'status' ? 'status' : bulkField === 'priority' ? 'priority' : 'type'}.${option}`) }}
            </option>
          </select>
        </label>
        <div class="mf">
          <button class="tbtn" @click="showBulkField = false">{{ t('issues.modal.cancel') }}</button>
          <button class="tbtn primary" @click="applyBulkField">{{ t('issues.bulkField.submit') }}</button>
        </div>
      </section>
    </div>

    <div v-if="showBulkLabels" class="overlay show">
      <section class="modal" role="dialog" :aria-label="t('issues.bulkLabels.title')">
        <h2>{{ t('issues.bulkLabels.title') }}</h2>
        <p>{{ t('issues.bulkLabels.body', selectedCount, { count: selectedCount }) }}</p>
        <label class="field">
          <span>{{ t('issues.bulkLabels.mode') }}</span>
          <select v-model="bulkLabelMode" class="txt" name="issue-bulk-label-mode">
            <option value="add">{{ t('issues.bulkLabels.add') }}</option>
            <option value="remove">{{ t('issues.bulkLabels.remove') }}</option>
            <option value="replace">{{ t('issues.bulkLabels.replace') }}</option>
          </select>
        </label>
        <label class="field">
          <span>{{ t('issues.bulkLabels.label') }}</span>
          <select v-model="bulkLabelValue" class="txt" name="issue-bulk-label-value">
            <option v-for="label in labelOptions" :key="label" :value="label">{{ label }}</option>
          </select>
        </label>
        <div class="mf">
          <button class="tbtn" @click="showBulkLabels = false">{{ t('issues.modal.cancel') }}</button>
          <button class="tbtn primary" @click="applyBulkLabels">{{ t('issues.bulkLabels.submit') }}</button>
        </div>
      </section>
    </div>
  </div>
</template>

<style scoped>
.issues-page { display:flex; flex-direction:column; gap:16px; min-width:0; }
.head { padding-bottom:14px; border-bottom:1px solid var(--border); }
.head h1 { margin:0 0 4px; font-size:var(--text-xl); font-weight:800; letter-spacing:var(--tracking-display); }
.head p { margin:0; font-size:var(--text-sm); color:var(--muted); }
.statebar { display:flex; align-items:center; justify-content:space-between; gap:12px; padding:10px 12px; border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); color:var(--fg-2); font-size:var(--text-sm); }
.statebar[data-state="pending"] { border-color:color-mix(in oklab,var(--accent),transparent 55%); }
.statebar[data-state="error"] { border-color:color-mix(in oklab,var(--state-warn),transparent 45%); color:var(--state-warn); }
.statebar[data-state="empty"] { color:var(--muted); }
.area-body { display:block; min-width:0; }
.pane { display:flex; flex-direction:column; gap:14px; min-width:0; }
.list-ops { display:flex; align-items:center; justify-content:space-between; gap:12px; flex-wrap:wrap; }
.op-left, .op-right, .issue-create-strip, .filterbar, .dactions, .mf { display:flex; align-items:center; gap:8px; flex-wrap:wrap; }
.op-right { margin-left:auto; }
.rows { display:flex; align-items:center; gap:7px; color:var(--muted); font-size:var(--text-xs); }
.select, .txt { border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface-warm); color:var(--fg); font:inherit; }
.select { min-height:32px; padding:0 8px; }
.txt { width:100%; min-height:34px; padding:8px 10px; }
textarea.txt { min-height:130px; resize:vertical; line-height:1.5; font-family:var(--font-mono); }
.tbtn, .act, .pager { min-height:34px; padding:7px 11px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface); color:var(--fg-2); font-size:var(--text-xs); font-weight:900; cursor:pointer; }
.act.primary, .tbtn.primary, .pager.on { background:var(--accent); border-color:var(--accent); color:var(--accent-on); }
.act.danger, .danger-title { color:var(--state-warn); }
.danger-fill { background:var(--state-warn); border-color:var(--state-warn); color:var(--bg); }
.tbtn:disabled, .act:disabled, .pager:disabled { opacity:.45; cursor:not-allowed; }
.fcount { color:var(--muted); font-size:var(--text-sm); }
.instr-band { display:flex; align-items:center; gap:var(--space-6); flex-wrap:wrap; padding:14px 18px; border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); margin-bottom:14px; }
.lead .ln, .bfig .bn { font-family:var(--font-mono); font-size:34px; line-height:1; color:var(--fg); font-weight:800; }
.lead .ll, .bfig .bl { margin-top:4px; color:var(--muted); font-size:var(--text-xs); text-transform:uppercase; letter-spacing:.06em; font-weight:800; }
.lead { display:flex; flex-direction:column; gap:3px; padding-right:var(--space-6); border-right:1px solid var(--border-soft); }
.bfigs { display:flex; gap:var(--space-6); flex-wrap:wrap; }
.bfig { padding-left:14px; border-left:3px solid var(--border); }
.bfig.warn { border-color:var(--state-warn); }
.bfig.good { border-color:var(--success); }
.filterbar { align-items:center; }
.fchip { border:1px solid var(--border); border-radius:var(--radius-pill); background:var(--surface); color:var(--fg-2); padding:7px 12px; font-weight:900; cursor:pointer; }
.fchip[aria-pressed="true"] { background:var(--accent); border-color:var(--accent); color:var(--accent-on); }
.issues-grid { border:0; border-radius:0; background:transparent; overflow:visible; }
.issues-grid .grid-h, .issue-row { display:grid; grid-template-columns:22px 44px 88px 112px minmax(242px,1fr) 112px minmax(116px,150px) 74px 42px; gap:10px; align-items:center; }
.issues-grid .grid-h { padding:7px 8px 8px; border:0; border-bottom:1px solid var(--border); background:transparent; color:var(--muted); font-size:10px; text-transform:uppercase; letter-spacing:.06em; font-weight:800; }
.issues-grid .grid-h > span { min-width:0; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.issue-row { width:100%; min-height:46px; padding:7px 8px; border:0; border-bottom:1px solid var(--border-soft); border-radius:0; background:transparent; color:inherit; text-align:left; cursor:pointer; position:relative; }
.issue-row:hover { background:var(--surface-warm); }
.issue-row.sel { background:color-mix(in oklab,var(--accent),transparent 92%); }
.issue-row.open { background:color-mix(in oklab,var(--accent),transparent 88%); }
.issue-row::before { content:""; position:absolute; left:0; top:8px; bottom:8px; width:3px; border-radius:var(--radius-pill); background:var(--issue-status-color,var(--border)); }
.issue-row[data-status="open"] { --issue-status-color:var(--accent); }
.issue-row[data-status="acknowledged"] { --issue-status-color:var(--class-dormant); }
.issue-row[data-status="reopened"] { --issue-status-color:var(--warn); }
.issue-row[data-status="resolved"] { --issue-status-color:var(--success); }
.issue-row[data-status="closed"] { --issue-status-color:var(--class-stale); }
.issue-row[data-status="rejected"] { --issue-status-color:var(--danger); }
.echk { display:inline-grid; place-items:center; width:18px; height:18px; border:1px solid var(--border); border-radius:5px; color:var(--accent-on); font-size:11px; }
.echk.on { background:var(--accent); border-color:var(--accent); }
.issue-row-id { font-family:var(--font-mono); color:var(--fg-2); font-size:var(--text-xs); }
.issue-row-title { min-width:0; }
.issue-row-title b { display:block; color:var(--fg); font-size:var(--text-sm); line-height:1.32; white-space:nowrap; overflow:hidden; text-overflow:ellipsis; }
.issue-row-title .tags { display:flex; flex-wrap:wrap; gap:5px; margin-top:4px; }
.issue-route-mini, .issue-age, .issue-comments { color:var(--muted); font-family:var(--font-mono); font-size:var(--text-xs); line-height:1.25; overflow:hidden; text-overflow:ellipsis; }
.issue-comments { text-align:right; }
.issue-chip { display:inline-flex; align-items:center; width:max-content; max-width:100%; white-space:nowrap; overflow:hidden; text-overflow:ellipsis; border:1px solid currentColor; border-radius:6px; padding:2px 8px; font-size:10px; font-weight:800; line-height:1.35; text-transform:uppercase; letter-spacing:.06em; background:color-mix(in oklab,currentColor,transparent 88%); }
.issue-chip[data-tone="critical"], .issue-chip[data-tone="bug"], .issue-chip[data-tone="rejected"] { color:var(--danger); }
.issue-chip[data-tone="high"], .issue-chip[data-tone="reopened"] { color:var(--state-warn); }
.issue-chip[data-tone="medium"], .issue-chip[data-tone="acknowledged"] { color:var(--class-dormant); }
.issue-chip[data-tone="low"], .issue-chip[data-tone="closed"] { color:var(--muted); }
.issue-chip[data-tone="feature"], .issue-chip[data-tone="open"] { color:var(--accent); }
.issue-chip[data-tone="task"] { color:var(--fg-2); }
.issue-chip[data-tone="improvement"], .issue-chip[data-tone="resolved"] { color:var(--success); }
.issue-chip.subtle { color:var(--muted); background:var(--surface-warm); border-color:var(--border); text-transform:none; font-weight:700; letter-spacing:0; }
.empty, .thread-empty { display:flex; flex-direction:column; align-items:center; justify-content:center; gap:5px; min-height:180px; color:var(--muted); }
.empty b { color:var(--fg-2); font-size:var(--text-lg); }
.bulkbar { display:none; position:sticky; bottom:10px; z-index:15; align-items:center; gap:8px; padding:10px; border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); box-shadow:var(--elev-raised); }
.bulkbar.show { display:flex; flex-wrap:wrap; }
.bc { font-family:var(--font-mono); color:var(--fg); font-weight:800; }
.bsp { flex:1; }
.bulk-note { color:var(--muted); font-family:var(--font-mono); font-size:11px; }
.issue-workspace { display:block; overflow-y:auto; min-width:0; padding:var(--space-5) var(--space-6) 96px; }
.issue-open-head { display:flex; align-items:flex-start; justify-content:space-between; gap:var(--space-4); margin-bottom:var(--space-4); }
.issue-open-left { display:flex; align-items:flex-start; gap:var(--space-3); min-width:0; }
.issue-open-title { min-width:0; }
.issue-open-title h2 { margin:0 0 6px; font-size:var(--text-xl); letter-spacing:var(--tracking-display); }
.issue-open-title .route { font-family:var(--font-mono); color:var(--muted); font-size:var(--text-xs); }
.issue-open-badges { display:flex; flex-wrap:wrap; gap:6px; margin-bottom:8px; }
.issue-open-grid { display:grid; grid-template-columns:minmax(0,1fr) minmax(360px,.92fr); gap:var(--space-4); align-items:start; }
.issue-main, .issue-rail { display:flex; flex-direction:column; gap:var(--space-3); min-width:0; }
.issue-card { background:var(--surface); border:1px solid var(--border); border-radius:var(--r-md); padding:var(--space-4); }
.issue-main .issue-card { background:transparent; border:0; border-top:1px solid var(--border); border-radius:0; padding:var(--space-4) 0; }
.dsh { font-size:10px; text-transform:uppercase; letter-spacing:.06em; color:var(--muted); font-weight:700; margin-bottom:8px; }
.issue-body { color:var(--fg-2); line-height:1.52; border:1px solid var(--border-soft); border-radius:var(--r-sm); background:var(--surface); padding:var(--space-3); }
.issue-thread-head { display:flex; align-items:center; justify-content:space-between; gap:var(--space-3); margin-bottom:var(--space-2); }
.issue-thread-head .count { font-family:var(--font-mono); color:var(--muted); font-size:var(--text-xs); }
.thread-actions { display:flex; flex-wrap:wrap; gap:var(--space-2); align-items:center; justify-content:space-between; padding:8px 10px; border:1px solid var(--border-soft); border-radius:var(--r-sm); background:var(--surface-warm); margin-bottom:var(--space-2); }
.thread-actions .left { display:flex; gap:var(--space-2); align-items:center; flex-wrap:wrap; }
.thread { display:flex; flex-direction:column; gap:0; margin-top:var(--space-3); border-left:1px solid var(--border); padding-left:14px; }
.comment { border:0; background:transparent; border-radius:0; padding:4px 0 13px 12px; position:relative; }
.comment::before { content:""; position:absolute; left:-20px; top:11px; width:9px; height:9px; border-radius:50%; background:var(--muted); box-shadow:0 0 0 3px var(--bg); }
.comment.selected { background:color-mix(in oklab,var(--accent),transparent 94%); }
.comment .cm { display:flex; align-items:center; gap:7px; color:var(--muted); font-size:var(--text-xs); margin-bottom:6px; }
.comment .cm .agent { color:var(--fg); font-weight:700; }
.comment .body { display:block; font-size:var(--text-sm); line-height:1.52; color:var(--fg-2); }
.composer { margin-top:var(--space-3); border:1px dashed var(--border); border-radius:var(--r-sm); padding:10px; background:var(--surface); }
.composer textarea { min-height:150px; }
.md { color:var(--fg-2); }
.md :deep(p) { margin:0 0 10px; }
.md :deep(strong) { color:var(--fg); font-weight:700; }
.md :deep(code) { font-family:var(--font-mono); font-size:11px; color:var(--fg); background:var(--surface-warm); border:1px solid var(--border-soft); border-radius:var(--radius-sm); padding:1px 5px; }
.md :deep(pre) { margin:8px 0; padding:10px 12px; overflow:auto; background:var(--surface-warm); border:1px solid var(--border-soft); border-radius:var(--r-sm); }
.edit-grid { display:grid; grid-template-columns:repeat(auto-fit,minmax(170px,1fr)); gap:10px; margin:var(--space-3) 0; }
.edit-field, .field { display:flex; flex-direction:column; gap:5px; min-width:0; }
.edit-field.span2 { margin-bottom:10px; }
.edit-field span, .field span { color:var(--muted); font-size:10px; text-transform:uppercase; letter-spacing:.06em; font-weight:700; }
.body-edit { min-height:180px; }
.edit-hint, .issue-draft-card p { font-size:var(--text-xs); color:var(--muted); line-height:1.42; margin-top:6px; }
.chip-picker, .issue-label-picker { display:flex; flex-wrap:wrap; gap:7px; margin-top:8px; }
.tag-option { border:1px solid var(--border); background:var(--surface); color:var(--fg-2); border-radius:var(--radius-pill); padding:4px 8px; font-family:var(--font-mono); font-size:10px; cursor:pointer; }
.tag-option.on { border-color:var(--accent); background:color-mix(in oklab,var(--accent),transparent 88%); color:var(--fg); }
.detail-state { margin-bottom:12px; padding:9px 11px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface); color:var(--muted); font-size:var(--text-sm); }
.detail-state[data-state="error"] { color:var(--state-warn); border-color:color-mix(in oklab,var(--state-warn),transparent 45%); }
.overlay { position:fixed; inset:0; background:rgba(8,12,20,.55); z-index:60; display:none; align-items:center; justify-content:center; padding:var(--space-5); }
.overlay.show { display:flex; }
.modal { background:var(--surface); border:1px solid var(--border); border-radius:var(--r-lg); width:min(540px,100%); max-height:84vh; overflow:auto; box-shadow:var(--elev-raised); padding:var(--space-5); }
.modal.issue-create-modal { width:min(1280px,calc(100vw - 32px)); height:min(920px,calc(100vh - 32px)); max-height:calc(100vh - 32px); overflow:hidden; padding:0; }
.issue-modal-head, .issue-modal-foot { display:flex; align-items:center; justify-content:space-between; gap:var(--space-3); padding:var(--space-4) var(--space-5); border-bottom:1px solid var(--border); }
.issue-modal-foot { border-top:1px solid var(--border); border-bottom:0; }
.issue-modal-head h2, .modal h2 { margin:0 0 6px; }
.issue-modal-head p, .modal p { margin:0; color:var(--muted); }
.issue-modal-body { height:calc(100% - 142px); display:grid; grid-template-columns:minmax(0,1.2fr) minmax(320px,.8fr); gap:var(--space-4); padding:var(--space-4); overflow:hidden; }
.issue-draft-main, .issue-draft-side { display:flex; flex-direction:column; gap:var(--space-3); min-width:0; overflow:auto; }
.issue-draft-card { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:var(--space-4); }
.issue-template-row { display:grid; grid-template-columns:repeat(4,minmax(0,1fr)); gap:8px; margin-top:12px; }
.issue-template { border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface-warm); color:var(--fg-2); padding:10px; text-align:left; cursor:pointer; }
.issue-template:hover, .issue-template[aria-pressed="true"] { border-color:var(--accent); background:color-mix(in oklab,var(--accent),transparent 90%); }
.issue-template b { display:block; color:var(--fg); }
.issue-template span { display:block; margin-top:4px; color:var(--muted); font-size:var(--text-xs); line-height:1.35; }
.md-editor { display:grid; grid-template-columns:minmax(0,1.1fr) minmax(300px,.9fr); gap:10px; align-items:stretch; }
.composer .md-editor { display:flex; flex-direction:column; gap:8px; }
.composer .md-toolbar { grid-column:auto; }
.md-toolbar { grid-column:1 / -1; display:flex; flex-wrap:wrap; gap:6px; align-items:center; }
.md-toolbar button { border:1px solid var(--border); background:var(--surface); color:var(--fg-2); border-radius:var(--r-sm); padding:5px 8px; font-weight:700; font-size:var(--text-xs); cursor:pointer; }
.md-toolbar button:hover { border-color:var(--accent); color:var(--fg); }
.md-preview { border:1px solid var(--border-soft); border-radius:var(--r-sm); background:var(--surface-warm); padding:10px 12px; min-height:160px; overflow:auto; }
.issue-create-editor { grid-template-rows:auto minmax(360px,1fr); }
.issue-create-editor textarea.txt { min-height:360px; height:100%; resize:vertical; font-family:var(--font-mono); line-height:1.55; }
.issue-create-editor .md-preview { min-height:360px; }
.issue-route-grid { display:grid; grid-template-columns:1fr; gap:10px; }
.issue-quality { display:flex; flex-direction:column; gap:8px; }
.qitem { display:flex; align-items:flex-start; gap:8px; color:var(--fg-2); font-size:var(--text-sm); }
.qdot { display:inline-grid; place-items:center; flex:0 0 auto; width:20px; height:20px; border-radius:50%; background:var(--surface-warm); border:1px solid var(--border); font-family:var(--font-mono); font-size:11px; color:var(--muted); }
.issue-hover { position:fixed; z-index:72; max-height:min(560px, calc(100vh - 24px)); overflow:auto; background:var(--surface); border:1px solid var(--border); border-radius:var(--r-md); box-shadow:var(--elev-raised); padding:14px 16px; pointer-events:none; opacity:0; transform:translateY(4px); transition:opacity var(--motion-fast) var(--ease-standard), transform var(--motion-fast) var(--ease-standard); }
.issue-hover.show { opacity:1; transform:none; pointer-events:auto; }
.issue-hover .ih-head { display:flex; align-items:flex-start; gap:8px; }
.issue-hover .ih-id { font-family:var(--font-mono); font-size:var(--text-xs); color:var(--muted); flex:none; padding-top:2px; }
.issue-hover .ih-title { font-size:var(--text-base); font-weight:700; line-height:1.32; color:var(--fg); }
.issue-hover .ih-meta { display:flex; flex-wrap:wrap; gap:6px; margin:9px 0 4px; align-items:center; }
.issue-hover .ih-route { font-family:var(--font-mono); font-size:var(--text-xs); color:var(--muted); }
.issue-hover .ih-body { border-top:1px solid var(--border-soft); margin-top:11px; padding-top:11px; }
.issue-hover .ih-body.md { font-size:var(--text-sm); color:var(--fg-2); }
.issue-hover .ih-stats { border-top:1px solid var(--border-soft); margin-top:11px; padding-top:11px; }
.issue-hover .ih-stats-grid { display:grid; grid-template-columns:repeat(3,1fr); gap:8px 12px; }
.issue-hover .ih-stat .k { font-size:10px; text-transform:uppercase; letter-spacing:.06em; color:var(--muted); }
.issue-hover .ih-stat .v { font-family:var(--font-mono); font-size:var(--text-base); font-weight:700; font-variant-numeric:tabular-nums; color:var(--fg); }
.issue-hover .ih-empty { font-size:var(--text-xs); color:var(--muted); margin-top:10px; }
@media (pointer:coarse){ .issue-hover { display:none !important; } }
.hint { color:var(--muted); font-size:var(--text-xs); line-height:1.35; }
.confirm-input { margin-top:14px; min-height:110px; }
.mf { justify-content:flex-end; margin-top:14px; }
@media (max-width:1100px) {
  .issues-grid .grid-h { display:none; }
  .issue-row { grid-template-columns:22px 42px 1fr 42px; gap:8px; align-items:start; }
  .issue-row > .issue-chip, .issue-row > .issue-route-mini, .issue-row > .issue-age { display:none; }
  .issue-open-grid, .issue-modal-body { grid-template-columns:1fr; }
  .issue-open-head { flex-direction:column; }
  .issue-template-row { grid-template-columns:1fr 1fr; }
  .md-editor { grid-template-columns:1fr; }
  .md-toolbar, .md-preview { grid-column:auto; }
  .issue-create-editor { grid-template-rows:auto minmax(280px,42vh) minmax(180px,auto); }
  .issue-create-editor textarea.txt { min-height:280px; }
  .issue-create-editor .md-preview { min-height:180px; }
}
@media (max-width:720px) {
  .instr-band, .bfigs { display:grid; grid-template-columns:1fr; }
  .lead { padding-right:0; border-right:0; }
  .issue-template-row { grid-template-columns:1fr; }
  .modal.issue-create-modal { width:calc(100vw - 24px); height:calc(100vh - 24px); }
}
</style>
