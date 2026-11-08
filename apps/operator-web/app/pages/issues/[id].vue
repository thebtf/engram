<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useMutation, useQueryClient } from '@tanstack/vue-query'
import { assertIssueRevision, IssueApiError, toIssueApiError, type Issue, type IssueActor, type IssueDetailPageOptions } from '~/composables/useOperatorApi'
import { useIssueDetailQuery } from '~/composables/useOperatorQueries'
import {
  deleteIssue,
  updateIssue,
  type IssuePriority,
  type IssueType,
  type IssueUpdateFields,
} from '~/composables/useOperatorIssueActions'
import { formatAbsoluteDate } from '~/utils/formatters'
import { useProjectCatalog } from '~/composables/useProjectCatalog'
import ProjectCatalogStatus from '~/components/ProjectCatalogStatus.vue'
import { useOperatorI18n } from '~/composables/useOperatorI18n'

const { t, te } = useOperatorI18n()
const route = useRoute()
const router = useRouter()
const queryClient = useQueryClient()
const { projects, unresolved, selectedProject, selectionPending, selectionError, unresolvedReference, projectName } = useProjectCatalog('all')
const canAct = computed(() => !selectionPending.value && !selectionError.value && projects.value.some(project => project.project_id === selectedProject.value))

const issueId = computed(() => Number(route.params.id))
const detailPage = ref<IssueDetailPageOptions>({ comments_offset: 0, comments_limit: 50, history_offset: 0, history_limit: 50 })
watch(issueId, () => {
  detailPage.value = { comments_offset: 0, comments_limit: 50, history_offset: 0, history_limit: 50 }
  commentOffsets.value = []
  historyOffsets.value = []
}, { flush: 'sync' })
const issueQuery = useIssueDetailQuery(issueId, detailPage)
const issue = computed(() => issueQuery.data.value?.issue ?? null)
const comments = computed(() => issueQuery.data.value?.comments ?? [])
const history = computed(() => issueQuery.data.value?.history ?? [])
const commentOffsets = ref<number[]>([])
const historyOffsets = ref<number[]>([])
const commentsRange = computed(() => loadedRange(issueQuery.data.value?.comments_offset ?? 0, comments.value.length))
const historyRange = computed(() => loadedRange(issueQuery.data.value?.history_offset ?? 0, history.value.length))
const commentNext = computed(() => issueQuery.data.value?.comments_next_offset ?? null)
const historyNext = computed(() => issueQuery.data.value?.history_next_offset ?? null)
const commentsCompletion = computed(() => issueQuery.data.value?.comments_complete ? t('issueFlow.commentsComplete') : t('issueFlow.commentsRemain'))
const historyCompletion = computed(() => issueQuery.data.value?.history_complete ? t('issueFlow.historyComplete') : t('issueFlow.historyRemain'))
const sourceProjectName = computed(() =>
  issue.value
    ? (projects.value.some(project => project.project_id === issue.value?.source_project) ? projectName(issue.value.source_project) : issueQuery.data.value?.source_project_display_name || projectName(issue.value.source_project))
    : '',
)
const targetProjectName = computed(() =>
  issue.value
    ? (projects.value.some(project => project.project_id === issue.value?.target_project) ? projectName(issue.value.target_project) : issueQuery.data.value?.target_project_display_name || projectName(issue.value.target_project))
    : '',
)

const editing = ref(false)
const editTitle = ref('')
const editBody = ref('')
const editPriority = ref<IssuePriority>('medium')
const editType = ref<IssueType>('task')
const editLabels = ref('[]')
const editMetadata = ref('{}')
const editOwner = ref('')
const editRecipient = ref('')
const editBase = ref<Issue | null>(null)
const editSource = ref('')
const editComment = ref('')
const editCommentMetadata = ref('{}')
const statusComment = ref('')
const statusCommentMetadata = ref('{}')
const mutationSession = ref('')

const newComment = ref('')
const commentMetadata = ref('{}')
const commentNeedsReview = ref(false)
const commentRead = ref(false)
const rejectReason = ref('')
const showRejectPanel = ref(false)
const rejectBase = ref<Issue | null>(null)
const rejectSource = ref('')
const showDeleteConfirm = ref(false)
const actionError = ref('')
const actionMessage = ref('')
type MaterialDraft = { id: number; base: Issue; fields: IssueUpdateFields; source: string; kind: 'edit' | 'status' }
const losingDraft = ref<MaterialDraft | null>(null)
const reconciliationRead = ref(false)
const reconciledStatus = ref<MaterialDraft | null>(null)
const readingCurrent = ref(false)

const priorityOptions: IssuePriority[] = ['critical', 'high', 'medium', 'low']
const typeOptions: IssueType[] = ['task', 'bug', 'feature', 'improvement']

const invalidateIssue = async () => {
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: ['issue-detail', issueId.value] }),
    queryClient.invalidateQueries({ queryKey: ['issues'] }),
  ])
}

const issueMutation = useMutation({
  retry: false,
  mutationFn: (input: { id: number; fields: IssueUpdateFields; source: string; draft?: MaterialDraft }) => {
    if (!canAct.value || input.source !== selectedProject.value) throw new Error('Source changed')
    return updateIssue(input.id, { ...input.fields, source_project: input.source, source_agent: 'human' })
  },
  onError: (error, input) => {
    actionMessage.value = ''
    actionError.value = safeError(error)
    if (needsReconciliation(error)) {
      if (input.draft) {
        losingDraft.value = input.draft
        reconciliationRead.value = false
        reconciledStatus.value = null
      } else {
        commentNeedsReview.value = true
        commentRead.value = false
      }
    }
  },
})

const deleteMutation = useMutation({
  retry: false,
  mutationFn: () => {
    if (!canAct.value) throw new Error('Choose a canonical source project before acting on an issue.')
    return deleteIssue(issueId.value, selectedProject.value)
  },
  onSuccess: async () => {
    await queryClient.invalidateQueries({ queryKey: ['issues'] })
    await router.push({ path: '/issues', query: route.query })
  },
  onError: (error) => {
    actionMessage.value = ''
    actionError.value = safeError(error)
  },
})

function statusBadgeClass(status: string): string {
  switch (status) {
    case 'open':
      return 'border-[color:rgba(76,141,255,0.4)] bg-[color:rgba(76,141,255,0.12)] text-[var(--accent)]'
    case 'acknowledged':
      return 'border-[color:rgba(167,139,250,0.4)] bg-[color:rgba(167,139,250,0.12)] text-[var(--must-build)]'
    case 'resolved':
      return 'border-[color:rgba(52,211,153,0.4)] bg-[color:rgba(52,211,153,0.12)] text-[var(--success)]'
    case 'reopened':
      return 'border-[color:rgba(251,191,36,0.4)] bg-[color:rgba(251,191,36,0.12)] text-[var(--warn)]'
    case 'closed':
      return 'border-[color:rgba(107,114,128,0.4)] bg-[color:rgba(107,114,128,0.12)] text-[var(--stale)]'
    case 'rejected':
      return 'border-[color:rgba(248,113,113,0.4)] bg-[color:rgba(248,113,113,0.12)] text-[var(--danger)]'
    default:
      return 'border-[color:rgba(107,114,128,0.4)] bg-[color:rgba(107,114,128,0.12)] text-[var(--stale)]'
  }
}

function priorityBadgeClass(priority: string): string {
  switch (priority) {
    case 'critical':
      return 'border-[color:rgba(248,113,113,0.4)] bg-[color:rgba(248,113,113,0.12)] text-[var(--danger)]'
    case 'high':
      return 'border-[color:rgba(251,191,36,0.4)] bg-[color:rgba(251,191,36,0.12)] text-[var(--warn)]'
    case 'medium':
      return 'border-[color:rgba(76,141,255,0.4)] bg-[color:rgba(76,141,255,0.12)] text-[var(--accent)]'
    default:
      return 'border-[color:rgba(107,114,128,0.4)] bg-[color:rgba(107,114,128,0.12)] text-[var(--stale)]'
  }
}

function terminalStatus(status: Issue['status']): boolean {
  return status === 'closed' || status === 'rejected'
}

function safeError(error: unknown): string {
  const failure = toIssueApiError(error)
  const key = `issueFlow.error_${failure.code}`
  return te(key) ? `${failure.code}: ${t(key)}` : failure.message
}

function needsReconciliation(error: unknown): boolean {
  const failure = toIssueApiError(error)
  return failure.outcome === 'unknown' || ['REVISION_CONFLICT', 'REVISION_REQUIRED'].includes(failure.code)
}

function loadedRange(offset: number, length: number): string {
  return length === 0 ? t('issueFlow.emptyOffset', { offset }) : `${offset + 1}–${offset + length}`
}

function pretty(value: unknown): string {
  return JSON.stringify(value, null, 2)
}

function actorLabel(actor?: IssueActor | null): string {
  return actor ? `${actor.kind} · ${actor.reference ?? t('issueFlow.unknownReference')}` : t('issueFlow.unknownActor')
}

function historyActionLabel(action: string): string {
  if (action === 'created') return t('issueFlow.historyActionCreated')
  if (action === 'updated') return t('issueFlow.historyActionUpdated')
  return action
}

function parseLabels(text: string): string[] {
  const value: unknown = JSON.parse(text)
  if (!Array.isArray(value) || !value.every(label => typeof label === 'string')) throw new Error('Labels must be a JSON array of strings.')
  return value
}

function nextComments() {
  if (issueQuery.isFetching.value || commentNext.value === null) return
  commentOffsets.value.push(issueQuery.data.value?.comments_offset ?? 0)
  detailPage.value = { ...detailPage.value, comments_offset: commentNext.value }
}

function previousComments() {
  if (issueQuery.isFetching.value) return
  detailPage.value = { ...detailPage.value, comments_offset: commentOffsets.value.pop() ?? 0 }
}

function nextHistory() {
  if (issueQuery.isFetching.value || historyNext.value === null) return
  historyOffsets.value.push(issueQuery.data.value?.history_offset ?? 0)
  detailPage.value = { ...detailPage.value, history_offset: historyNext.value }
}

function previousHistory() {
  if (issueQuery.isFetching.value) return
  detailPage.value = { ...detailPage.value, history_offset: historyOffsets.value.pop() ?? 0 }
}

function startEdit() {
  if (!issue.value || !canAct.value || losingDraft.value || reconciledStatus.value || issueMutation.isPending.value) return
  editBase.value = JSON.parse(JSON.stringify(issue.value))
  editSource.value = selectedProject.value
  editTitle.value = issue.value.title
  editBody.value = issue.value.body ?? ''
  editPriority.value = issue.value.priority
  editType.value = issue.value.type
  editLabels.value = pretty(issue.value.labels ?? [])
  editMetadata.value = pretty(issue.value.metadata ?? {})
  editOwner.value = issue.value.owner_label ?? ''
  editRecipient.value = issue.value.recipient_label ?? ''
  editComment.value = ''
  editCommentMetadata.value = '{}'
  editing.value = true
  actionError.value = ''
  actionMessage.value = ''
}

function cancelEdit() {
  if (issueMutation.isPending.value || losingDraft.value) return
  editing.value = false
  editBase.value = null
  actionError.value = ''
}

function changedEditFields(base: Issue): IssueUpdateFields {
  const fields: IssueUpdateFields = {}
  if (editTitle.value !== base.title) fields.title = editTitle.value
  if (editBody.value !== (base.body ?? '')) fields.body = editBody.value
  if (editPriority.value !== base.priority) fields.priority = editPriority.value
  if (editType.value !== base.type) fields.type = editType.value
  if (editLabels.value !== pretty(base.labels ?? [])) fields.labels = parseLabels(editLabels.value)
  if (editMetadata.value !== pretty(base.metadata ?? {})) fields.metadataRaw = editMetadata.value
  if (editOwner.value !== (base.owner_label ?? '')) fields.owner_label = editOwner.value === '' ? null : editOwner.value
  if (editRecipient.value !== (base.recipient_label ?? '')) fields.recipient_label = editRecipient.value === '' ? null : editRecipient.value
  if (editComment.value !== '') {
    if (!editComment.value.trim()) throw new Error('Empty comment')
    fields.comment = editComment.value
    fields.commentMetadataRaw = editCommentMetadata.value
  } else if (editCommentMetadata.value !== '{}') throw new Error('Comment metadata requires comment')
  return fields
}

function submitMaterial(draft: MaterialDraft) {
  if (issueMutation.isPending.value || losingDraft.value || !canAct.value || draft.id !== issueId.value || draft.source !== selectedProject.value) return
  try {
    assertIssueRevision(draft.base.revision)
  } catch {
    actionError.value = t('issueFlow.revisionRequired')
    return
  }
  const captured: MaterialDraft = JSON.parse(JSON.stringify({ ...draft, fields: { ...draft.fields, ...(draft.fields.session_id === undefined && mutationSession.value ? { session_id: mutationSession.value } : {}), expected_revision: draft.base.revision } }))
  actionError.value = ''
  actionMessage.value = ''
  issueMutation.mutate({ id: captured.id, fields: captured.fields, source: captured.source, draft: captured }, {
    onSuccess: async (result) => {
      reconciledStatus.value = null
      if (captured.kind === 'edit') { editing.value = false; editBase.value = null }
      if (captured.fields.status === 'rejected') { showRejectPanel.value = false; rejectReason.value = '' }
      if (captured.kind === 'status' && statusComment.value === captured.fields.comment) { statusComment.value = ''; statusCommentMetadata.value = '{}' }
      actionMessage.value = captured.kind === 'edit'
        ? (result.changed ? t('issueFlow.fieldsUpdated') : result.comment_id !== null ? t('issueFlow.commentOnlyAdded') : t('issueFlow.noChanges'))
        : t('issueFlow.statusChanged', { status: captured.fields.status })
      await invalidateIssue()
    },
  })
}

function saveEdit() {
  if (!editBase.value || losingDraft.value || issueMutation.isPending.value) return
  if (!editTitle.value.trim()) { actionError.value = t('issueFlow.titleRequired'); return }
  let fields: IssueUpdateFields
  try { fields = changedEditFields(editBase.value) } catch (error) {
    actionError.value = error instanceof IssueApiError ? safeError(error) : t('issueFlow.editInvalid')
    return
  }
  if (Object.keys(fields).length === 0) { editing.value = false; return }
  submitMaterial({ id: editBase.value.id, base: editBase.value, fields, source: editSource.value, kind: 'edit' })
}

function updateStatus(status: Issue['status']) {
  if (!issue.value || editing.value || losingDraft.value || reconciledStatus.value || commentNeedsReview.value) return
  let fields: IssueUpdateFields = { status }
  try {
    if (statusComment.value !== '') {
      if (!statusComment.value.trim()) throw new Error('Empty comment')
      fields = { ...fields, comment: statusComment.value, commentMetadataRaw: statusCommentMetadata.value }
    } else if (statusCommentMetadata.value !== '{}') throw new Error('Comment metadata requires comment')
  } catch (error) {
    actionError.value = error instanceof IssueApiError ? safeError(error) : t('issueFlow.statusInvalid')
    return
  }
  submitMaterial({ id: issueId.value, base: issue.value, fields, source: selectedProject.value, kind: 'status' })
}

async function readForReconciliation() {
  if (readingCurrent.value || issueMutation.isPending.value) return
  readingCurrent.value = true
  reconciliationRead.value = false
  commentRead.value = false
  const result = await issueQuery.refetch()
  readingCurrent.value = false
  if (result.error || !result.data) { actionError.value = safeError(result.error); return }
  reconciliationRead.value = losingDraft.value?.id === result.data.issue.id
  commentRead.value = true
}

function acceptCurrentRevision() {
  if (!reconciliationRead.value || !losingDraft.value || !issue.value || losingDraft.value.id !== issue.value.id || losingDraft.value.source !== selectedProject.value) return
  const retained = losingDraft.value
  if (retained.kind === 'edit') editBase.value = JSON.parse(JSON.stringify(issue.value))
  else reconciledStatus.value = { ...retained, base: JSON.parse(JSON.stringify(issue.value)) }
  losingDraft.value = null
  reconciliationRead.value = false
  actionError.value = ''
  actionMessage.value = t('issueFlow.revisionSelected')
}

function resubmitStatus() {
  if (reconciledStatus.value) submitMaterial(reconciledStatus.value)
}

function acceptReadOutcome() {
  if (!reconciliationRead.value || !losingDraft.value || !issue.value || losingDraft.value.id !== issue.value.id) return
  losingDraft.value = null
  reconciledStatus.value = null
  editing.value = false
  editBase.value = null
  showRejectPanel.value = false
  actionError.value = ''
  actionMessage.value = t('issueFlow.readAccepted')
}

function startReject() {
  if (!issue.value || !canAct.value || editing.value || issueMutation.isPending.value || losingDraft.value || reconciledStatus.value || commentNeedsReview.value) return
  if (showRejectPanel.value) { showRejectPanel.value = false; return }
  rejectBase.value = JSON.parse(JSON.stringify(issue.value))
  rejectSource.value = selectedProject.value
  showRejectPanel.value = true
}

function releaseCommentReview() {
  if (!commentRead.value) return
  commentNeedsReview.value = false
  actionError.value = ''
  actionMessage.value = t('issueFlow.anotherAppend')
}

function submitComment() {
  if (!newComment.value.trim() || !canAct.value || issueMutation.isPending.value || commentNeedsReview.value || losingDraft.value) return
  const body = newComment.value
  const metadataText = commentMetadata.value
  actionError.value = ''
  actionMessage.value = ''
  issueMutation.mutate({ id: issueId.value, fields: { comment: body, commentMetadataRaw: metadataText, ...(mutationSession.value ? { session_id: mutationSession.value } : {}) }, source: selectedProject.value }, {
    onSuccess: async () => {
      if (newComment.value === body) newComment.value = ''
      if (commentMetadata.value === metadataText) commentMetadata.value = '{}'
      actionMessage.value = t('issueFlow.commentAdded')
      await invalidateIssue()
    },
  })
}

function submitReject() {
  if (!rejectReason.value.trim()) { actionError.value = t('issueFlow.rejectRequired'); return }
  if (rejectBase.value) submitMaterial({ id: rejectBase.value.id, base: rejectBase.value, fields: { status: 'rejected', comment: rejectReason.value }, source: rejectSource.value, kind: 'status' })
}

function confirmDelete() {
  if (!canAct.value || issueMutation.isPending.value || losingDraft.value || commentNeedsReview.value) return
  actionError.value = ''
  actionMessage.value = ''
  deleteMutation.mutate()
}
</script>

<template>
  <section class="space-y-5">
    <ProjectCatalogStatus :records="unresolved" :pending="selectionPending" :error="selectionError"
      :reference="unresolvedReference" />
    <label class="block text-sm text-[var(--fg-2)]">{{ t('issueFlow.sourceProject') }}
      <select v-model="selectedProject" :disabled="selectionPending"
        class="ml-2 rounded border border-[var(--border)] bg-[var(--surface)] p-2">
        <option value="all" disabled>{{ t('issueFlow.chooseSource') }}</option>
        <option value="" disabled>{{ t('issueFlow.chooseSource') }}</option>
        <option v-for="project in projects" :key="project.project_id" :value="project.project_id">{{
          projectName(project.project_id) }}</option>
      </select>
    </label>
    <label class="block text-sm">{{ t('issueFlow.mutationSession') }}
      <input v-model="mutationSession" :disabled="issueMutation.isPending.value || !!losingDraft || !!reconciledStatus"
        class="ml-2 rounded border p-2" />
      <span class="ml-2 text-xs text-[var(--muted)]">{{ t('issueFlow.sessionHelp') }}</span>
    </label>
    <div class="surface-panel p-5">
      <div v-if="issueQuery.isPending.value" class="text-sm text-[var(--muted)]">
        {{ t('issueFlow.loadingIssues') }}
      </div>
      <div v-if="issueQuery.isError.value" role="alert" class="text-sm text-[var(--danger)]">
        {{ safeError(issueQuery.error.value) }}
        <button class="ml-2 rounded border px-3 py-2" :disabled="issueQuery.isFetching.value"
          @click="issueQuery.refetch()">{{ t('issueFlow.readIssueAgain') }}</button>
      </div>
      <div v-if="issue" class="space-y-5">
        <NuxtLink :to="{ path: '/issues', query: route.query }"
          class="inline-flex text-sm font-semibold text-[var(--accent)] hover:underline">
          ← {{ t('issueFlow.backIssues') }}
        </NuxtLink>

        <div v-if="actionError" role="alert"
          class="rounded-xl border border-[color:rgba(248,113,113,0.4)] bg-[color:rgba(248,113,113,0.12)] p-3 text-sm text-[var(--danger)]">
          {{ actionError }}
        </div>
        <div v-else-if="actionMessage" role="status"
          class="rounded-xl border border-[color:rgba(52,211,153,0.4)] bg-[color:rgba(52,211,153,0.12)] p-3 text-sm text-[var(--success)]">
          {{ actionMessage }}
        </div>

        <div v-if="losingDraft" class="surface-panel-warm space-y-3 p-4" role="status">
          <h2 class="font-semibold">{{ t('issueFlow.retainedDraft', {
            id: losingDraft.id, revision:
              losingDraft.base.revision
          }) }}</h2>
          <p class="text-sm text-[var(--fg-2)]">{{ t('issueFlow.draftWarning') }}</p>
          <p class="text-xs text-[var(--muted)]">{{ t('issueFlow.retainedSource', {
            source:
              projectName(losingDraft.source)
          }) }}</p>
          <details>
            <summary class="cursor-pointer text-sm">{{ t('issueFlow.inspectDraft') }}</summary>
            <pre class="mt-2 max-h-96 overflow-auto whitespace-pre-wrap break-words text-xs">{{ pretty({
              base:
                losingDraft.base, draft: losingDraft.fields
            }) }}</pre>
          </details>
          <button class="rounded border border-[var(--border)] px-3 py-2 text-sm"
            :disabled="readingCurrent || issueQuery.isFetching.value" @click="readForReconciliation">{{
              t('issueFlow.readReconcile') }}</button>
          <button class="ml-2 rounded border border-[var(--accent)] px-3 py-2 text-sm"
            :disabled="!reconciliationRead || !canAct || selectedProject !== losingDraft.source"
            @click="acceptCurrentRevision">{{ t('issueFlow.useRevision') }}</button>
          <button class="ml-2 rounded border px-3 py-2 text-sm" :disabled="!reconciliationRead"
            @click="acceptReadOutcome">{{ t('issueFlow.discardDraft') }}</button>
        </div>
        <div v-if="reconciledStatus" class="surface-panel-warm space-y-3 p-4">
          <p class="text-sm">{{ t('issueFlow.retainedStatus', {
            revision: reconciledStatus.base.revision, fields:
              pretty(reconciledStatus.fields)
          }) }}</p>
          <button class="rounded border border-[var(--accent)] px-3 py-2 text-sm"
            :disabled="!canAct || issueMutation.isPending.value || selectedProject !== reconciledStatus.source"
            @click="resubmitStatus">{{ t('issueFlow.submitStatus') }}</button>
        </div>
        <div v-if="commentNeedsReview" class="surface-panel-warm space-y-3 p-4" role="status">
          <p class="text-sm">{{ t('issueFlow.commentUnknown') }}</p>
          <button class="rounded border px-3 py-2 text-sm" :disabled="readingCurrent || issueQuery.isFetching.value"
            @click="readForReconciliation">{{ t('issueFlow.readComments') }}</button>
          <button class="ml-2 rounded border px-3 py-2 text-sm" :disabled="!commentRead"
            @click="releaseCommentReview">{{ t('issueFlow.allowAppend') }}</button>
        </div>

        <div class="flex flex-wrap items-start justify-between gap-4">
          <div class="min-w-0 flex-1">
            <div class="mb-2 flex flex-wrap items-center gap-2">
              <span class="mono-data text-xs uppercase tracking-[0.08em] text-[var(--muted)]">
                {{ t('issueFlow.issueRevision', { id: issue.id, revision: issue.revision }) }}
              </span>
              <span class="rounded-full border px-2.5 py-1 text-[10px] font-semibold uppercase tracking-[0.04em]"
                :class="statusBadgeClass(issue.status)">
                {{ issue.status === 'resolved' ? t('issueFlow.statusResolved') : issue.status === 'closed' ?
                  t('issueFlow.statusClosed') : issue.status }}
              </span>
              <span class="rounded-full border px-2.5 py-1 text-[10px] font-semibold uppercase tracking-[0.04em]"
                :class="priorityBadgeClass(issue.priority)">
                {{ t('issueFlow.value_' + issue.priority) }}
              </span>
              <span
                class="rounded-full border border-[var(--border)] bg-[var(--surface-warm)] px-2.5 py-1 text-[10px] font-semibold uppercase tracking-[0.04em] text-[var(--fg-2)]">
                {{ t('issueFlow.value_' + issue.type) }}
              </span>
            </div>
            <h1 class="text-2xl font-semibold">{{ issue.title }}</h1>
            <p class="mt-2 text-sm text-[var(--fg-2)]">
              {{ sourceProjectName }} → {{ targetProjectName }} · {{ t('issueFlow.createdAt', {
                date:
                  formatAbsoluteDate(issue.created_at)
              }) }}
            </p>
          </div>

          <div class="flex flex-wrap gap-2">
            <button
              class="rounded-lg border border-[var(--border)] bg-[var(--surface-warm)] px-3 py-2 text-xs font-semibold text-[var(--fg-2)]"
              :disabled="!canAct || issueMutation.isPending.value || editing || !!losingDraft || !!reconciledStatus || commentNeedsReview"
              @click="startEdit">
              {{ t('issueFlow.editAction') }}
            </button>
            <button v-if="issue.status === 'open'"
              class="rounded-lg border border-[var(--must-build)] bg-[color:rgba(167,139,250,0.12)] px-3 py-2 text-xs font-semibold text-[var(--must-build)]"
              :disabled="!canAct || issueMutation.isPending.value || editing || !!losingDraft || !!reconciledStatus || commentNeedsReview"
              @click="updateStatus('acknowledged')">
              {{ t('issueFlow.acknowledgeAction') }}
            </button>
            <button v-if="!terminalStatus(issue.status) && issue.status !== 'resolved'"
              class="rounded-lg border border-[var(--success)] bg-[color:rgba(52,211,153,0.12)] px-3 py-2 text-xs font-semibold text-[var(--success)]"
              :disabled="!canAct || issueMutation.isPending.value || editing || !!losingDraft || !!reconciledStatus || commentNeedsReview"
              @click="updateStatus('resolved')">
              {{ t('issueFlow.resolveAction') }}
            </button>
            <button v-if="issue.status === 'resolved' || issue.status === 'closed'"
              class="rounded-lg border border-[var(--warn)] bg-[color:rgba(251,191,36,0.12)] px-3 py-2 text-xs font-semibold text-[var(--warn)]"
              :disabled="!canAct || issueMutation.isPending.value || editing || !!losingDraft || !!reconciledStatus || commentNeedsReview"
              @click="updateStatus('reopened')">
              {{ t('issueFlow.reopenAction') }}
            </button>
            <button v-if="!terminalStatus(issue.status)"
              class="rounded-lg border border-[var(--stale)] bg-[color:rgba(107,114,128,0.12)] px-3 py-2 text-xs font-semibold text-[var(--stale)]"
              :disabled="!canAct || issueMutation.isPending.value || editing || !!losingDraft || !!reconciledStatus || commentNeedsReview"
              @click="updateStatus('closed')">
              {{ t('issueFlow.closeAction') }}
            </button>
            <button v-if="issue.status !== 'rejected'"
              class="rounded-lg border border-[var(--danger)] bg-[color:rgba(248,113,113,0.12)] px-3 py-2 text-xs font-semibold text-[var(--danger)]"
              :disabled="!canAct || issueMutation.isPending.value || editing || !!losingDraft || !!reconciledStatus || commentNeedsReview"
              @click="startReject">
              {{ t('issueFlow.rejectAction') }}
            </button>
            <button
              class="rounded-lg border border-[var(--danger)] bg-[var(--surface)] px-3 py-2 text-xs font-semibold text-[var(--danger)]"
              :disabled="deleteMutation.isPending.value || issueMutation.isPending.value || !canAct || !!losingDraft || commentNeedsReview"
              @click="showDeleteConfirm = !showDeleteConfirm">
              {{ t('issueFlow.deleteAction') }}
            </button>
          </div>
        </div>

        <fieldset class="surface-panel-warm space-y-3 p-4"
          :disabled="issueMutation.isPending.value || !!losingDraft || !!reconciledStatus || editing || commentNeedsReview">
          <label class="block text-sm">{{ t('issueFlow.statusComment') }}<textarea v-model="statusComment" rows="2"
              class="mt-1 w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm" /></label>
          <label class="block text-sm">{{ t('issueFlow.statusMetadata') }}<textarea v-model="statusCommentMetadata"
              rows="2"
              class="mono-data mt-1 w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm" /></label>
          <p class="text-xs text-[var(--muted)]">{{ t('issueFlow.statusHelp') }}</p>
        </fieldset>

        <div v-if="editing" class="surface-panel-warm p-4">
          <div class="mb-3 text-xs uppercase tracking-[0.08em] text-[var(--muted)]">
            {{ t('issueFlow.editBase', { revision: editBase?.revision }) }}
          </div>
          <p class="mb-3 text-xs text-[var(--muted)]">{{ t('issueFlow.draftSource', {
            id: editBase?.id, source:
              projectName(editSource)
          }) }}</p>
          <NuxtLink v-if="editBase && editBase.id !== issueId"
            :to="{ path: `/issues/${editBase.id}`, query: { project: editSource } }"
            class="mb-3 inline-flex text-sm text-[var(--accent)] underline">{{ t('issueFlow.returnDraft') }}</NuxtLink>
          <fieldset :disabled="issueMutation.isPending.value" class="space-y-3">
            <label class="block">
              <span class="mb-1 block text-xs text-[var(--muted)]">{{ t('issueFlow.title') }}</span>
              <input v-model="editTitle" type="text"
                class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm text-[var(--fg)]" />
            </label>

            <label class="block">
              <span class="mb-1 block text-xs text-[var(--muted)]">{{ t('issueFlow.description') }}</span>
              <textarea v-model="editBody" rows="5"
                class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm text-[var(--fg)]" />
              <span class="mt-1 block text-xs text-[var(--muted)]">
                {{ t('issueFlow.descriptionHelp') }}
              </span>
            </label>

            <div class="grid gap-3 sm:grid-cols-2">
              <label class="block">
                <span class="mb-1 block text-xs text-[var(--muted)]">{{ t('issueFlow.type') }}</span>
                <select v-model="editType"
                  class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm text-[var(--fg)]">
                  <option v-for="type in typeOptions" :key="type" :value="type">{{ t('issueFlow.value_' + type) }}
                  </option>
                </select>
              </label>
              <label class="block">
                <span class="mb-1 block text-xs text-[var(--muted)]">{{ t('issueFlow.priority') }}</span>
                <select v-model="editPriority"
                  class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm text-[var(--fg)]">
                  <option v-for="priority in priorityOptions" :key="priority" :value="priority">{{ t('issueFlow.value_'
                    + priority) }}</option>
                </select>
              </label>
            </div>

            <label class="block"><span class="mb-1 block text-xs text-[var(--muted)]">{{ t('issueFlow.labelsJson')
                }}</span><textarea v-model="editLabels" rows="2"
                class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm" /></label>
            <label class="block"><span class="mb-1 block text-xs text-[var(--muted)]">{{ t('issueFlow.metadataJson')
                }}</span><textarea v-model="editMetadata" rows="5"
                class="mono-data w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm" /><span
                class="text-xs text-[var(--muted)]">{{ t('issueFlow.editMetadataHelp') }}</span></label>
            <div class="grid gap-3 sm:grid-cols-2">
              <label class="block"><span class="mb-1 block text-xs text-[var(--muted)]">{{ t('issueFlow.ownerLabel')
                  }}</span><input v-model="editOwner"
                  class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm" /></label>
              <label class="block"><span class="mb-1 block text-xs text-[var(--muted)]">{{ t('issueFlow.recipientLabel')
                  }}</span><input v-model="editRecipient"
                  class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm" /></label>
            </div>
            <p class="text-xs text-[var(--muted)]">{{ t('issueFlow.editOwnerHelp') }}</p>
            <label class="block text-sm">{{ t('issueFlow.editComment') }}<textarea v-model="editComment" rows="3"
                class="mt-1 w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm" /></label>
            <label class="block text-sm">{{ t('issueFlow.editCommentMetadata') }}<textarea v-model="editCommentMetadata"
                rows="2"
                class="mono-data mt-1 w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm" /></label>
            <p class="text-xs text-[var(--muted)]">{{ t('issueFlow.editCommitHelp') }}</p>

            <div class="flex flex-wrap gap-2">
              <button
                class="rounded-lg border border-[var(--accent)] bg-[var(--accent)] px-4 py-2 text-sm font-semibold text-[var(--accent-on)] disabled:cursor-not-allowed disabled:opacity-60"
                :disabled="issueMutation.isPending.value || !canAct || !!losingDraft || selectedProject !== editSource || editBase?.id !== issueId"
                @click="saveEdit">
                {{ issueMutation.isPending.value ? t('issueFlow.saving') : t('issueFlow.saveEdit') }}
              </button>
              <button
                class="rounded-lg border border-[var(--border)] bg-[var(--surface)] px-4 py-2 text-sm font-semibold text-[var(--fg-2)]"
                :disabled="issueMutation.isPending.value || !!losingDraft" @click="cancelEdit">
                {{ t('issueFlow.cancel') }}
              </button>
            </div>
          </fieldset>
        </div>

        <div v-if="showRejectPanel" class="surface-panel-warm p-4">
          <div class="mb-2 text-xs uppercase tracking-[0.08em] text-[var(--danger)]">
            {{ t('issueFlow.rejectHeading') }}
          </div>
          <label class="block text-sm">{{ t('issueFlow.rejectReason') }}
            <textarea v-model="rejectReason" :disabled="issueMutation.isPending.value" rows="3"
              :placeholder="t('issueFlow.rejectPlaceholder')"
              class="mt-1 w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm text-[var(--fg)]" />
          </label>
          <div class="mt-3 flex flex-wrap gap-2">
            <button
              class="rounded-lg border border-[var(--danger)] bg-[color:rgba(248,113,113,0.12)] px-4 py-2 text-sm font-semibold text-[var(--danger)] disabled:cursor-not-allowed disabled:opacity-60"
              :disabled="!canAct || issueMutation.isPending.value || !!losingDraft || !!reconciledStatus || commentNeedsReview"
              @click="submitReject">
              {{ issueMutation.isPending.value ? t('issueFlow.rejecting') : t('issueFlow.rejectSubmit') }}
            </button>
            <button
              class="rounded-lg border border-[var(--border)] bg-[var(--surface)] px-4 py-2 text-sm font-semibold text-[var(--fg-2)]"
              @click="showRejectPanel = false; rejectReason = ''">
              {{ t('issueFlow.cancel') }}
            </button>
          </div>
        </div>

        <div v-if="showDeleteConfirm" class="surface-panel-warm border-[color:rgba(248,113,113,0.35)] p-4">
          <div class="mb-2 text-sm font-semibold text-[var(--danger)]">
            {{ t('issueFlow.deleteConfirm', { id: issue.id }) }}
          </div>
          <p class="text-sm text-[var(--fg-2)]">
            {{ t('issueFlow.deleteHelp') }}
          </p>
          <div class="mt-3 flex flex-wrap gap-2">
            <button
              class="rounded-lg border border-[var(--danger)] bg-[color:rgba(248,113,113,0.12)] px-4 py-2 text-sm font-semibold text-[var(--danger)] disabled:cursor-not-allowed disabled:opacity-60"
              :disabled="deleteMutation.isPending.value || issueMutation.isPending.value || !canAct || !!losingDraft || commentNeedsReview"
              @click="confirmDelete">
              {{ deleteMutation.isPending.value ? t('issueFlow.deleting') : t('issueFlow.deletePermanently') }}
            </button>
            <button
              class="rounded-lg border border-[var(--border)] bg-[var(--surface)] px-4 py-2 text-sm font-semibold text-[var(--fg-2)]"
              @click="showDeleteConfirm = false">
              {{ t('issueFlow.cancel') }}
            </button>
          </div>
        </div>

        <div class="surface-panel-warm p-4">
          <div class="mb-2 text-xs uppercase tracking-[0.08em] text-[var(--muted)]">
            {{ t('issueFlow.description') }}
          </div>
          <div class="whitespace-pre-wrap text-sm leading-6 text-[var(--fg-2)]">
            {{ issue.body || t('issueFlow.noDescription') }}
          </div>
        </div>

        <div class="grid gap-4 lg:grid-cols-[280px,minmax(0,1fr)]">
          <div class="surface-panel-warm p-4">
            <div class="mb-3 text-xs uppercase tracking-[0.08em] text-[var(--muted)]">
              {{ t('issueFlow.metaHeading') }}
            </div>
            <div class="space-y-3 text-sm text-[var(--fg-2)]">
              <div>
                <div class="text-xs text-[var(--muted)]">{{ t('issueFlow.type') }}</div>
                <div>{{ t('issueFlow.value_' + issue.type) }}</div>
              </div>
              <div>
                <div class="text-xs text-[var(--muted)]">{{ t('issueFlow.priority') }}</div>
                <div>{{ t('issueFlow.value_' + issue.priority) }}</div>
              </div>
              <div>
                <div class="text-xs text-[var(--muted)]">{{ t('issueFlow.sourceProject') }}</div>
                <div>{{ sourceProjectName }}</div>
                <div class="mono-data text-xs text-[var(--muted)]">{{ issue.source_project }}</div>
              </div>
              <div>
                <div class="text-xs text-[var(--muted)]">{{ t('issueFlow.targetProject') }}</div>
                <div>{{ targetProjectName }}</div>
                <div class="mono-data text-xs text-[var(--muted)]">{{ issue.target_project }}</div>
              </div>
              <div>
                <div class="text-xs text-[var(--muted)]">{{ t('issueFlow.updatedLabel') }}</div>
                <div class="mono-data">{{ formatAbsoluteDate(issue.updated_at) }}</div>
              </div>
              <div>
                <div class="text-xs text-[var(--muted)]">{{ t('issueFlow.ownerRecipientLabel') }}</div>
                <div>{{ issue.owner_label ?? t('issueFlow.unassigned') }} / {{ issue.recipient_label ??
                  t('issueFlow.unassigned') }}</div>

              </div>
              <div>
                <div class="text-xs text-[var(--muted)]">{{ t('issueFlow.sourceSession') }}</div>
                <div class="break-words">{{ issue.created_by_session || t('issueFlow.noneRecorded') }}</div>
              </div>
              <div>
                <div class="text-xs text-[var(--muted)]">{{ t('issueFlow.targetSession') }}</div>
                <div class="break-words">{{ issue.target_session || t('issueFlow.noneRecorded') }}</div>
              </div>
              <div>
                <div class="text-xs text-[var(--muted)]">{{ t('issueFlow.actor') }}</div>
                <div class="break-words">{{ actorLabel(issue.actor) }}</div>
              </div>
              <div>
                <div class="text-xs text-[var(--muted)]">{{ t('issueFlow.labels') }}</div>
                <pre class="whitespace-pre-wrap break-words text-xs">{{ pretty(issue.labels) }}</pre>
              </div>
              <details>
                <summary class="cursor-pointer text-xs">{{ t('issueFlow.viewMetadata') }}</summary>
                <pre class="mt-2 max-h-96 overflow-auto whitespace-pre-wrap break-words text-xs">{{
                  pretty(issue.metadata) }}</pre>
              </details>
            </div>
          </div>

          <div class="space-y-4">
            <div class="surface-panel-warm p-4">
              <div class="mb-3 text-xs uppercase tracking-[0.08em] text-[var(--muted)]">
                {{ t('issueFlow.addComment') }}
              </div>
              <label class="block text-sm">{{ t('issueFlow.comment') }}
                <textarea v-model="newComment" rows="4"
                  class="mt-1 w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm text-[var(--fg)]"
                  :placeholder="t('issueFlow.writeComment')" @keydown.ctrl.enter.prevent="submitComment"
                  @keydown.meta.enter.prevent="submitComment" />
              </label>
              <label class="mt-3 block text-sm">{{ t('issueFlow.commentMetadata') }}<textarea v-model="commentMetadata"
                  rows="3"
                  class="mono-data mt-1 w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm" /></label>
              <div class="mt-3 flex items-center justify-between gap-3">
                <span class="text-xs text-[var(--muted)]">{{ t('issueFlow.commentShortcut') }}</span>
                <button
                  class="rounded-lg border border-[var(--accent)] bg-[var(--accent)] px-4 py-2 text-sm font-semibold text-[var(--accent-on)] disabled:cursor-not-allowed disabled:opacity-60"
                  :disabled="!canAct || issueMutation.isPending.value || !newComment.trim() || commentNeedsReview || !!losingDraft"
                  @click="submitComment">
                  {{ issueMutation.isPending.value ? t('issueFlow.sending') : t('issueFlow.sendComment') }}
                </button>
              </div>
            </div>

            <div class="surface-panel-warm p-4">
              <div class="mb-3 flex flex-wrap items-center justify-between gap-3">
                <div class="text-xs uppercase tracking-[0.08em] text-[var(--muted)]">
                  {{ t('issueFlow.comments') }}
                </div>
                <div class="mono-data text-xs text-[var(--muted)]">
                  {{ t('issueFlow.commentCount', {
                    range: commentsRange, count: issueQuery.data.value?.comment_count,
                    completion: commentsCompletion
                  }) }}
                </div>
              </div>
              <div v-if="comments.length === 0" class="text-sm text-[var(--muted)]">
                {{ t('issueFlow.emptyComments') }}
              </div>
              <div v-else class="space-y-3">
                <article v-for="comment in comments" :key="comment.id"
                  class="rounded-xl border border-[var(--border)] bg-[var(--surface)] p-4">
                  <div class="mb-2 flex flex-wrap items-center justify-between gap-3">
                    <div class="text-sm font-semibold">
                      {{ comment.author_project }} · {{ comment.author_agent || t('issueFlow.unknownAgent') }}
                    </div>
                    <div class="mono-data text-xs text-[var(--muted)]">
                      {{ formatAbsoluteDate(comment.created_at) }}
                    </div>
                  </div>
                  <p class="mb-2 text-xs text-[var(--muted)]">{{ t('issueFlow.commentActor', {
                    actor:
                      actorLabel(comment.actor)
                  }) }}</p>
                  <p v-if="issueQuery.data.value?.comments_oversized" class="mb-2 text-sm text-[var(--warn)]">{{
                    t('issueFlow.oversizedComment') }}</p>
                  <details v-if="issueQuery.data.value?.comments_oversized">
                    <summary class="cursor-pointer text-sm">{{ t('issueFlow.viewOversizedComment') }}</summary>
                    <div class="mt-2 max-h-96 overflow-auto whitespace-pre-wrap break-words text-sm">{{ comment.body }}
                    </div>
                  </details>
                  <div v-else class="whitespace-pre-wrap break-words text-sm leading-6 text-[var(--fg-2)]">{{
                    comment.body }}</div>
                  <details>
                    <summary class="mt-2 cursor-pointer text-xs">{{ t('issueFlow.viewCommentMetadata') }}</summary>
                    <pre class="mt-2 max-h-96 overflow-auto whitespace-pre-wrap break-words text-xs">{{
                      pretty(comment.metadata) }}</pre>
                  </details>
                </article>
              </div>
              <div class="mt-3 flex flex-wrap gap-2">
                <button class="rounded border border-[var(--border)] px-3 py-2 text-sm"
                  :disabled="issueQuery.isFetching.value || (issueQuery.data.value?.comments_offset ?? 0) === 0"
                  @click="previousComments">{{ t('issueFlow.previousComments') }}</button>
                <button class="rounded border border-[var(--border)] px-3 py-2 text-sm"
                  :disabled="issueQuery.isFetching.value || commentNext === null" @click="nextComments">{{
                    t('issueFlow.nextComments') }}</button>
              </div>
            </div>
            <div class="surface-panel-warm p-4">
              <h2 class="mb-3 text-xs uppercase tracking-[0.08em] text-[var(--muted)]">{{ t('issueFlow.history') }}</h2>
              <p class="mb-3 text-xs text-[var(--muted)]">{{ t('issueFlow.historyCount', {
                range: historyRange, count:
                  issueQuery.data.value?.history_count, completion: historyCompletion
              }) }}</p>
              <p v-if="issueQuery.data.value?.history_oversized" class="mb-3 text-sm text-[var(--warn)]">{{
                t('issueFlow.oversizedHistory') }}</p>
              <p v-if="history.length === 0" class="text-sm text-[var(--muted)]">{{ t('issueFlow.emptyHistory') }}</p>
              <div v-else class="space-y-3">
                <article v-for="entry in history" :key="entry.revision"
                  class="rounded-xl border border-[var(--border)] bg-[var(--surface)] p-4">
                  <h3 class="text-sm font-semibold">{{ t('issueFlow.historyRevision', {
                    revision: entry.revision,
                    action: historyActionLabel(entry.action)
                  }) }}</h3>
                  <p class="mt-1 text-xs text-[var(--muted)]">{{ t('issueFlow.previousRevision', {
                    revision:
                      entry.previous_revision ?? t('issueFlow.unknown'), date: formatAbsoluteDate(entry.created_at)
                  }) }}
                  </p>
                  <p class="mt-1 break-words text-xs">{{ t('issueFlow.historyActor', {
                    actor: actorLabel(entry.actor),
                    source: entry.attribution.source_project ?? t('issueFlow.unknown'), agent:
                      entry.attribution.source_agent ?? t('issueFlow.unknown'), session: entry.attribution.session ??
                        t('issueFlow.unknown')
                  }) }}</p>
                  <p class="mt-1 text-xs">{{ t('issueFlow.changedFields', {
                    fields: entry.changed_fields.join(', ') ||
                      t('issueFlow.noneRecorded')
                  }) }}</p>
                  <details>
                    <summary class="mt-2 cursor-pointer text-sm">{{ t('issueFlow.viewHistory') }}</summary>
                    <pre class="mt-2 max-h-96 overflow-auto whitespace-pre-wrap break-words text-xs">{{
                      pretty(entry.changes) }}</pre>
                    <p class="text-xs text-[var(--muted)]">{{ t('issueFlow.authorizedValues') }}</p>
                  </details>
                </article>
              </div>
              <div class="mt-3 flex flex-wrap gap-2">
                <button class="rounded border border-[var(--border)] px-3 py-2 text-sm"
                  :disabled="issueQuery.isFetching.value || (issueQuery.data.value?.history_offset ?? 0) === 0"
                  @click="previousHistory">{{ t('issueFlow.previousHistory') }}</button>
                <button class="rounded border border-[var(--border)] px-3 py-2 text-sm"
                  :disabled="issueQuery.isFetching.value || historyNext === null" @click="nextHistory">{{
                    t('issueFlow.nextHistory') }}</button>
              </div>
              <p class="mt-2 text-xs text-[var(--muted)]">{{ t('issueFlow.pagingHelp') }}</p>
            </div>
          </div>
        </div>
      </div>
    </div>
  </section>
</template>
