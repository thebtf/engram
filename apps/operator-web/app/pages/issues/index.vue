<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, shallowRef, watch } from 'vue'
import { useMutation, useQueryClient } from '@tanstack/vue-query'
import { createIssue, fetchIssueActor, IssueApiError, prepareIssueCreateIntent, toIssueApiError, type Issue, type IssueCreateIntent, type IssueCurrentActor } from '~/composables/useOperatorApi'
import { acknowledgeIssues } from '~/composables/useOperatorIssueActions'
import { useIssuesQuery } from '~/composables/useOperatorQueries'
import { useProjectCatalog } from '~/composables/useProjectCatalog'
import { useOperatorAuth } from '~/composables/useOperatorAuth'
import ProjectCatalogStatus from '~/components/ProjectCatalogStatus.vue'
import { useOperatorI18n } from '~/composables/useOperatorI18n'
import { formatRelativeTime } from '~/utils/formatters'

const queryClient = useQueryClient()
const { state: authState } = useOperatorAuth()

const { t, te, locale } = useOperatorI18n()
const { projects: trackedProjects, unresolved, selectedProject: projectFilter, selectionPending, selectionError, ready, unresolvedReference, projectName } = useProjectCatalog('all')
const statusFilter = ref('open,acknowledged,reopened')
const typeFilter = ref('')
const sourceProjectFilter = ref('')
const myIssuesOnly = ref(false)
const sourceAgentFilter = computed(() => myIssuesOnly.value ? 'human' : '')
const textFilter = ref('')
const ownerFilter = ref('')
const recipientFilter = ref('')
const sourceSessionFilter = ref('')
const participantSessionFilter = ref('')
const targetSessionFilter = ref('')
const listOffset = ref(0)
const listOffsets = ref<number[]>([])

const issuesQuery = useIssuesQuery({
  project: projectFilter,
  ready,
  status: statusFilter,
  type: typeFilter,
  sourceProject: sourceProjectFilter,
  sourceAgent: sourceAgentFilter,
  q: textFilter,
  ownerLabel: ownerFilter,
  recipientLabel: recipientFilter,
  sourceSession: sourceSessionFilter,
  participantSession: participantSessionFilter,
  targetSession: targetSessionFilter,
  offset: listOffset,
})


const newTitle = ref('')
const newBody = ref('')
const newTargetProject = ref('')
const newType = ref<'bug' | 'feature' | 'improvement' | 'task'>('task')
const newPriority = ref<'critical' | 'high' | 'medium' | 'low'>('medium')
const createError = ref('')
const newLabels = ref('[]')
const newMetadata = ref('{}')
const newOwner = ref('')
const newRecipient = ref('')
const newSourceSession = ref('')
const newTargetSession = ref('')
const createMessage = ref('')
type PendingCreate = { version: 1; actor: IssueCurrentActor; intent: IssueCreateIntent; outcome: 'pending' | 'rejected' | 'confirmed'; code?: string; id?: number }
const pendingCreate = shallowRef<PendingCreate | null>(null)
const createBusy = ref(false)
const storageReady = ref(false)
const storageBlocked = ref(false)
const pendingHidden = ref(false)
const storageKey = `engram.issue-create.v1:${useRuntimeConfig().public.apiBase}`
const formLocked = computed(() => createBusy.value || !storageReady.value || storageBlocked.value || pendingHidden.value || pendingCreate.value !== null)
const listRange = computed(() => issues.value.length === 0 ? t('issueFlow.emptyOffset', { offset: issuesQuery.data.value?.offset ?? listOffset.value }) : `${(issuesQuery.data.value?.offset ?? listOffset.value) + 1}–${(issuesQuery.data.value?.offset ?? listOffset.value) + issues.value.length}`)
const listNext = computed(() => issuesQuery.data.value?.next_offset ?? null)
const recoveryActionLabel = computed(() => pendingCreate.value?.code === 'ISSUE_GONE' ? t('issueFlow.reconcileDeleted') : t('issueFlow.reconcileCreate'))

const issues = computed(() => issuesQuery.data.value?.issues ?? [])
const total = computed(() => issuesQuery.data.value?.total ?? 0)
const selectedIssueIds = ref<Set<number>>(new Set())
const bulkActionError = ref('')
const bulkActionMessage = ref('')

const selectableIssueIds = computed(() =>
  issues.value
    .filter((issue) => issue.status === 'open')
    .map((issue) => issue.id),
)
const selectedOpenIds = computed(() =>
  selectableIssueIds.value.filter((id) => selectedIssueIds.value.has(id)),
)
const allOpenSelected = computed(() =>
  selectableIssueIds.value.length > 0 && selectedOpenIds.value.length === selectableIssueIds.value.length,
)

let pageActive = true
let authGeneration = 0
const createIssueMutation = useMutation({
  retry: false,
  mutationFn: (pending: PendingCreate) => createIssue(pending.intent),
  onSuccess: async (result, pending) => {
    if (!pageActive) return
    const generation = authGeneration
    const confirmed: PendingCreate = { ...pending, outcome: 'confirmed', id: result.id }
    pendingCreate.value = confirmed
    try {
      savePending(confirmed)
      const actor = await fetchIssueActor()
      if (!pageActive) return
      if (generation !== authGeneration || !sameActor(actor, pending.actor) || projectFilter.value !== pending.intent.source_project) {
        pendingHidden.value = true
        clearForm()
        createError.value = t('issueFlow.actorChangedResult')
        return
      }
      finishConfirmed(confirmed)
      await queryClient.invalidateQueries({ queryKey: ['issues'] })
    } catch {
      pendingHidden.value = true
      clearForm()
      createError.value = t('issueFlow.resultUnavailable')
    }
  },
  onError: (error, pending) => {
    if (!pageActive) return
    const failure = toIssueApiError(error)
    createError.value = safeError(failure)
    const retained: PendingCreate = { ...pending, outcome: failure.outcome === 'rejected' ? 'rejected' : 'pending', code: failure.code }
    pendingCreate.value = retained
    if (failure.code === 'ACTOR_CHANGED') { pendingHidden.value = true; clearForm() }
    try { savePending(retained) } catch { storageBlocked.value = true }
  },
})

const bulkAcknowledgeMutation = useMutation({
  retry: false,
  mutationFn: () => acknowledgeIssues(selectedOpenIds.value, projectFilter.value),
  onSuccess: async (result) => {
    selectedIssueIds.value = new Set()
    bulkActionError.value = ''
    bulkActionMessage.value = t('issueFlow.bulkDone', { count: result.acknowledged })
    await queryClient.invalidateQueries({ queryKey: ['issues'] })
  },
  onError: (error) => {
    bulkActionMessage.value = ''
    bulkActionError.value = safeError(error)
  },
})

watch(issues, (currentIssues) => {
  const visibleIds = new Set(currentIssues.map((issue) => issue.id))
  selectedIssueIds.value = new Set([...selectedIssueIds.value].filter((id) => visibleIds.has(id)))
})
watch([projectFilter, statusFilter, typeFilter, sourceProjectFilter, sourceAgentFilter, textFilter, ownerFilter, recipientFilter, sourceSessionFilter, participantSessionFilter, targetSessionFilter], () => {
  listOffset.value = 0
  listOffsets.value = []
  selectedIssueIds.value = new Set()
}, { flush: 'sync' })

onMounted(() => { void restorePending() })
onUnmounted(() => { pageActive = false; restoreGeneration++ })
watch(projectFilter, () => {
  if (storageReady.value && !createBusy.value) void restorePending()
})
watch([() => authState.value.phase, () => authState.value.user, () => authState.value.authDisabled], () => {
  authGeneration++
  restoreGeneration++
  createMessage.value = ''
  if (pendingCreate.value || pendingHidden.value) {
    pendingHidden.value = true
    pendingCreate.value = null
    clearForm()
    createError.value = t('issueFlow.authChangedPending')
  }
}, { flush: 'sync' })

function safeError(error: unknown): string {
  const failure = toIssueApiError(error)
  const key = `issueFlow.error_${failure.code}`
  return te(key) ? `${failure.code}: ${t(key)}` : failure.message
}

function sameActor(left: IssueCurrentActor | null, right: IssueCurrentActor): boolean {
  return left !== null && left.kind === right.kind && left.reference === right.reference
}

function clearForm() {
  newTitle.value = ''
  newBody.value = ''
  newTargetProject.value = ''
  newType.value = 'task'
  newPriority.value = 'medium'
  newLabels.value = '[]'
  newMetadata.value = '{}'
  newOwner.value = ''
  newRecipient.value = ''
  newSourceSession.value = ''
  newTargetSession.value = ''
}

function populateForm(intent: IssueCreateIntent) {
  newTitle.value = intent.title
  newBody.value = intent.body ?? ''
  newTargetProject.value = intent.target_project
  newType.value = intent.type ?? 'task'
  newPriority.value = intent.priority ?? 'medium'
  newLabels.value = JSON.stringify(intent.labels ?? [], null, 2)
  newMetadata.value = intent.metadataRaw ?? JSON.stringify(intent.metadata ?? {}, null, 2)
  newOwner.value = intent.owner_label ?? ''
  newRecipient.value = intent.recipient_label ?? ''
  newSourceSession.value = intent.created_by_session ?? ''
  newTargetSession.value = intent.target_session ?? ''
}

function savePending(pending: PendingCreate) {
  try { assertStoredIntent(pending); sessionStorage.setItem(storageKey, JSON.stringify(pending)) } catch {
    storageBlocked.value = true
    throw new Error('Recovery storage unavailable')
  }
}

function assertStoredIntent(pending: PendingCreate) {
  const text = sessionStorage.getItem(storageKey)
  if (text === null) return
  const envelope = record(JSON.parse(text))
  const actor = record(envelope.actor)
  const intent = record(envelope.intent)
  if (intent.request_key !== pending.intent.request_key || intent.source_project !== pending.intent.source_project || actor.kind !== pending.actor.kind || actor.reference !== pending.actor.reference) throw new Error('Another pending intent must be preserved')
}

function finishConfirmed(pending: PendingCreate) {
  assertStoredIntent(pending)
  sessionStorage.removeItem(storageKey)
  pendingCreate.value = null
  pendingHidden.value = false
  clearForm()
  createError.value = ''
  createMessage.value = t('issueFlow.confirmed', { id: pending.id })
}

function record(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('Invalid stored intent')
  return value as Record<string, unknown>
}

async function decodePending(text: string): Promise<PendingCreate> {
  const envelope = record(JSON.parse(text))
  const actor = record(envelope.actor)
  const input = record(envelope.intent)
  const outcome = envelope.outcome
  if (envelope.version !== 1 || (outcome !== 'pending' && outcome !== 'rejected' && outcome !== 'confirmed')
    || (actor.kind !== 'client' && actor.kind !== 'operator') || typeof actor.reference !== 'string' || !actor.reference
    || typeof input.title !== 'string' || typeof input.source_project !== 'string' || typeof input.target_project !== 'string' || typeof input.request_key !== 'string'
    || (input.body !== undefined && typeof input.body !== 'string')
    || (input.metadataRaw !== undefined && typeof input.metadataRaw !== 'string')
    || (input.type !== undefined && (typeof input.type !== 'string' || !['bug', 'feature', 'improvement', 'task'].includes(input.type)))
    || (input.priority !== undefined && (typeof input.priority !== 'string' || !['critical', 'high', 'medium', 'low'].includes(input.priority)))
    || (input.source_agent !== undefined && typeof input.source_agent !== 'string')
    || (input.created_by_session !== undefined && typeof input.created_by_session !== 'string')
    || (input.target_session !== undefined && typeof input.target_session !== 'string')
    || (input.labels !== undefined && (!Array.isArray(input.labels) || !input.labels.every(label => typeof label === 'string')))
    || (input.owner_label !== undefined && input.owner_label !== null && typeof input.owner_label !== 'string')
    || (input.recipient_label !== undefined && input.recipient_label !== null && typeof input.recipient_label !== 'string')) throw new Error('Invalid stored intent')
  const expectation = record(input.expected_actor)
  if (expectation.kind !== actor.kind || expectation.reference !== actor.reference) throw new Error('Invalid stored actor expectation')
  const intent = await prepareIssueCreateIntent(input as unknown as IssueCreateIntent)
  if (outcome === 'confirmed' && (typeof envelope.id !== 'number' || !Number.isSafeInteger(envelope.id) || envelope.id <= 0)) throw new Error('Invalid confirmed outcome')
  return { version: 1, actor: { kind: actor.kind, reference: actor.reference }, intent, outcome, ...(typeof envelope.code === 'string' ? { code: envelope.code } : {}), ...(typeof envelope.id === 'number' ? { id: envelope.id } : {}) }
}

let restoreGeneration = 0
async function restorePending() {
  const generation = ++restoreGeneration
  const actorGeneration = authGeneration
  try {
    const text = sessionStorage.getItem(storageKey)
    storageReady.value = true
    storageBlocked.value = false
    if (text === null) { pendingCreate.value = null; pendingHidden.value = false; return }
    pendingHidden.value = true
    const actor = await fetchIssueActor()
    const stored = record(JSON.parse(text))
    const savedActor = record(stored.actor)
    const savedIntent = record(stored.intent)
    if (!pageActive || generation !== restoreGeneration || actorGeneration !== authGeneration) return
    if (!actor || actor.kind !== savedActor.kind || actor.reference !== savedActor.reference || savedIntent.source_project !== projectFilter.value) {
      pendingCreate.value = null
      clearForm()
      createError.value = t('issueFlow.actorChangedStored')
      return
    }
    const pending = await decodePending(text)
    if (!pageActive || generation !== restoreGeneration || actorGeneration !== authGeneration) return
    pendingCreate.value = pending
    pendingHidden.value = false
    populateForm(pending.intent)
    createError.value = pending.code ? safeError({ data: { error: { code: pending.code } } }) : ''
    createMessage.value = t('issueFlow.intentRestored')
  } catch {
    if (!pageActive || generation !== restoreGeneration || actorGeneration !== authGeneration) return
    storageReady.value = true
    storageBlocked.value = true
    pendingHidden.value = true
    createError.value = t('issueFlow.recoveryUnavailable')
  }
}

function nextIssues() {
  if (issuesQuery.isFetching.value || listNext.value === null) return
  listOffsets.value.push(issuesQuery.data.value?.offset ?? listOffset.value)
  listOffset.value = listNext.value
}

function previousIssues() {
  if (issuesQuery.isFetching.value) return
  listOffset.value = listOffsets.value.pop() ?? 0
}

async function reviseRejectedIntent() {
  const pending = pendingCreate.value
  if (!pending || pending.outcome !== 'rejected' || pendingHidden.value || createBusy.value || projectFilter.value !== pending.intent.source_project || !['INVALID_ARGUMENT', 'PAYLOAD_TOO_LARGE', 'PRIVACY_REJECTED'].includes(pending.code ?? '')) return
  createBusy.value = true
  const generation = authGeneration
  try {
    const actor = await fetchIssueActor()
    if (!pageActive || generation !== authGeneration) return
    if (!sameActor(actor, pending.actor)) {
      pendingHidden.value = true
      clearForm()
      createError.value = t('issueFlow.actorChangedRevise')
      return
    }
    assertStoredIntent(pending)
    sessionStorage.removeItem(storageKey)
    pendingCreate.value = null
    createError.value = ''
    createMessage.value = t('issueFlow.rejectedNoWrites')
  } catch (error) { createError.value = safeError(error) } finally { createBusy.value = false }
}

async function reconcileCreate() {
  const pending = pendingCreate.value
  if (!pending || pendingHidden.value || createBusy.value || storageBlocked.value || projectFilter.value !== pending.intent.source_project) return
  createBusy.value = true
  const generation = authGeneration
  try {
    const actor = await fetchIssueActor()
    if (!pageActive || generation !== authGeneration) return
    if (!sameActor(actor, pending.actor)) {
      pendingHidden.value = true
      clearForm()
      createError.value = t('issueFlow.actorChangedKey')
      return
    }
    if (pending.outcome === 'confirmed') { finishConfirmed(pending); await queryClient.invalidateQueries({ queryKey: ['issues'] }); return }
    if (pending.outcome === 'rejected' && pending.code === 'ISSUE_GONE') {
      assertStoredIntent(pending)
      sessionStorage.removeItem(storageKey)
      pendingCreate.value = null
      clearForm()
      createError.value = ''
      createMessage.value = t('issueFlow.deletedReconciled')
      return
    }
    await createIssueMutation.mutateAsync(pending)
  } catch (error) { createError.value = safeError(error) } finally { createBusy.value = false }
}

function toggleMyIssues() {
  if (!ready.value || !trackedProjects.value.some(project => project.project_id === projectFilter.value)) {
    createError.value = t('projectCatalog.selectionRequired')
    return
  }
  myIssuesOnly.value = !myIssuesOnly.value
  sourceProjectFilter.value = myIssuesOnly.value ? projectFilter.value : ''
}
watch(projectFilter, project => {
  if (myIssuesOnly.value) {
    if (!trackedProjects.value.some(item => item.project_id === project)) myIssuesOnly.value = false
    sourceProjectFilter.value = myIssuesOnly.value ? project : ''
  }
})

async function submitIssue() {
  if (formLocked.value || createIssueMutation.isPending.value) return
  if (!ready.value || !trackedProjects.value.some(project => project.project_id === projectFilter.value) || !newTitle.value.trim() || !trackedProjects.value.some(project => project.project_id === newTargetProject.value)) {
    createError.value = t('issueFlow.createRequired')
    return
  }
  createBusy.value = true
  const generation = authGeneration
  createMessage.value = ''
  try {
    if (sessionStorage.getItem(storageKey) !== null) { await restorePending(); return }
    const labels: unknown = JSON.parse(newLabels.value)
    if (!Array.isArray(labels) || !labels.every(label => typeof label === 'string')) throw new Error('Invalid labels')
    const intent = await prepareIssueCreateIntent({
      request_key: crypto.randomUUID(), title: newTitle.value, body: newBody.value,
      type: newType.value, priority: newPriority.value, target_project: newTargetProject.value,
      source_project: projectFilter.value, source_agent: 'human', labels, metadataRaw: newMetadata.value,
      ...(newSourceSession.value ? { created_by_session: newSourceSession.value } : {}),
      ...(newTargetSession.value ? { target_session: newTargetSession.value } : {}),
      owner_label: newOwner.value === '' ? null : newOwner.value,
      recipient_label: newRecipient.value === '' ? null : newRecipient.value,
    })
    const actor = await fetchIssueActor()
    if (!pageActive || generation !== authGeneration) { createError.value = t('issueFlow.authChangedBefore'); return }
    if (!actor) { createError.value = t('issueFlow.actorRequired'); return }
    if (projectFilter.value !== intent.source_project) { createError.value = t('issueFlow.sourceChanged'); return }
    const boundIntent = await prepareIssueCreateIntent({ ...intent, expected_actor: actor })
    if (!pageActive || generation !== authGeneration || projectFilter.value !== boundIntent.source_project) return
    const pending: PendingCreate = { version: 1, actor, intent: boundIntent, outcome: 'pending' }
    savePending(pending)
    pendingCreate.value = pending
    await createIssueMutation.mutateAsync(pending)
  } catch (error) {
    createError.value = pendingCreate.value || error instanceof IssueApiError
      ? safeError(error)
      : t('issueFlow.createInvalid')
  } finally { createBusy.value = false }
}

function canSelectIssue(issue: Issue): boolean {
  return issue.status === 'open'
}

function onIssueCheckboxChange(id: number, event: Event) {
  const checked = event.target instanceof HTMLInputElement ? event.target.checked : false
  const next = new Set(selectedIssueIds.value)
  if (checked) {
    next.add(id)
  } else {
    next.delete(id)
  }
  selectedIssueIds.value = next
  bulkActionError.value = ''
  bulkActionMessage.value = ''
}

function toggleAllOpenIssues() {
  const next = new Set(selectedIssueIds.value)
  if (allOpenSelected.value) {
    for (const id of selectableIssueIds.value) next.delete(id)
  } else {
    for (const id of selectableIssueIds.value) next.add(id)
  }
  selectedIssueIds.value = next
  bulkActionError.value = ''
  bulkActionMessage.value = ''
}

function submitBulkAcknowledge() {
  if (!ready.value || !trackedProjects.value.some(project => project.project_id === projectFilter.value)) {
    bulkActionError.value = t('projectCatalog.selectionRequired')
    return
  }
  if (selectedOpenIds.value.length === 0) {
    bulkActionError.value = t('issueFlow.bulkNeedOpen')
    bulkActionMessage.value = ''
    return
  }
  bulkAcknowledgeMutation.mutate()
}

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
</script>

<template>
  <section class="space-y-5">
    <label class="block text-sm">Language / Язык / 语言
      <select v-model="locale" class="ml-2 rounded border p-2">
        <option value="en">English</option>
        <option value="ru">Русский</option>
        <option value="zh">中文</option>
      </select>
    </label>
    <ProjectCatalogStatus :records="unresolved" :pending="selectionPending" :error="selectionError"
      :reference="unresolvedReference" />
    <label class="block text-sm">{{ t('issueFlow.sourceContext') }}
      <select v-model="projectFilter" :disabled="selectionPending" class="ml-2 rounded border p-2">
        <option value="all">{{ t('common.allProjects') }}</option>
        <option v-for="project in trackedProjects" :key="project.project_id" :value="project.project_id">{{
          projectName(project.project_id) }}</option>
      </select>
    </label>
    <div class="surface-panel p-5">
      <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 class="text-xl font-semibold">{{ t('issueFlow.issueHeading') }}</h1>
          <p class="mt-2 text-sm text-[var(--fg-2)]">
            {{ t('issueFlow.intro') }}
          </p>
        </div>
        <div class="mono-data text-sm text-[var(--muted)]">
          {{ t('issueFlow.authorizedRows', { range: listRange, count: total }) }}
        </div>
      </div>

      <div class="mb-4 flex flex-wrap gap-2">
        <button class="rounded-full border px-3 py-1 text-xs" :class="statusFilter === 'open,acknowledged,reopened'
          ? 'border-[var(--accent)] bg-[color:rgba(76,141,255,0.12)] text-[var(--fg)]'
          : 'border-[var(--border)] bg-[var(--surface-warm)] text-[var(--fg-2)]'"
          @click="statusFilter = 'open,acknowledged,reopened'">
          {{ t('issueFlow.statusActive') }}
        </button>
        <button class="rounded-full border px-3 py-1 text-xs" :class="statusFilter === 'open'
          ? 'border-[var(--accent)] bg-[color:rgba(76,141,255,0.12)] text-[var(--fg)]'
          : 'border-[var(--border)] bg-[var(--surface-warm)] text-[var(--fg-2)]'" @click="statusFilter = 'open'">
          {{ t('issueFlow.statusOpen') }}
        </button>
        <button class="rounded-full border px-3 py-1 text-xs" :class="statusFilter === 'resolved'
          ? 'border-[var(--accent)] bg-[color:rgba(76,141,255,0.12)] text-[var(--fg)]'
          : 'border-[var(--border)] bg-[var(--surface-warm)] text-[var(--fg-2)]'" @click="statusFilter = 'resolved'">
          {{ t('issueFlow.statusResolved') }}
        </button>
        <button class="rounded-full border px-3 py-1 text-xs" :class="statusFilter === 'closed'
          ? 'border-[var(--accent)] bg-[color:rgba(76,141,255,0.12)] text-[var(--fg)]'
          : 'border-[var(--border)] bg-[var(--surface-warm)] text-[var(--fg-2)]'" @click="statusFilter = 'closed'">
          {{ t('issueFlow.statusClosed') }}
        </button>
        <button class="rounded-full border px-3 py-1 text-xs" :class="statusFilter === ''
          ? 'border-[var(--accent)] bg-[color:rgba(76,141,255,0.12)] text-[var(--fg)]'
          : 'border-[var(--border)] bg-[var(--surface-warm)] text-[var(--fg-2)]'" @click="statusFilter = ''">
          {{ t('issueFlow.statusAll') }}
        </button>
        <button class="rounded-full border px-3 py-1 text-xs" :class="typeFilter === 'bug'
          ? 'border-[var(--accent)] bg-[color:rgba(76,141,255,0.12)] text-[var(--fg)]'
          : 'border-[var(--border)] bg-[var(--surface-warm)] text-[var(--fg-2)]'"
          @click="typeFilter = typeFilter === 'bug' ? '' : 'bug'">
          {{ t('issueFlow.value_bug') }}
        </button>
        <button class="rounded-full border px-3 py-1 text-xs" :class="myIssuesOnly
          ? 'border-[var(--accent)] bg-[color:rgba(76,141,255,0.12)] text-[var(--fg)]'
          : 'border-[var(--border)] bg-[var(--surface-warm)] text-[var(--fg-2)]'" @click="toggleMyIssues">
          {{ t('issueFlow.humanLabels') }}
        </button>
      </div>

      <div class="mb-4 grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <label class="block text-xs text-[var(--muted)]">{{ t('issueFlow.search') }}<input v-model="textFilter"
            type="search"
            class="mt-1 w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm text-[var(--fg)]" /></label>
        <label class="block text-xs text-[var(--muted)]">{{ t('issueFlow.ownerFilter') }}<input v-model="ownerFilter"
            class="mt-1 w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm text-[var(--fg)]" /></label>
        <label class="block text-xs text-[var(--muted)]">{{ t('issueFlow.recipientFilter') }}<input
            v-model="recipientFilter"
            class="mt-1 w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm text-[var(--fg)]" /></label>
        <label class="block text-xs text-[var(--muted)]">{{ t('issueFlow.issueType') }}<select v-model="typeFilter"
            class="mt-1 w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm text-[var(--fg)]">
            <option value="">{{ t('issueFlow.allTypes') }}</option>
            <option value="task">{{ t('issueFlow.value_task') }}</option>
            <option value="bug">{{ t('issueFlow.value_bug') }}</option>
            <option value="feature">{{ t('issueFlow.value_feature') }}</option>
            <option value="improvement">{{ t('issueFlow.value_improvement') }}</option>
          </select></label>
        <label class="block text-xs text-[var(--muted)]">{{ t('issueFlow.sourceSessionFilter') }}<input
            v-model="sourceSessionFilter" class="mt-1 w-full rounded-lg border p-2" /></label>
        <label class="block text-xs text-[var(--muted)]">{{ t('issueFlow.participantSessionFilter') }}<input
            v-model="participantSessionFilter" class="mt-1 w-full rounded-lg border p-2" /></label>
        <label class="block text-xs text-[var(--muted)]">{{ t('issueFlow.targetSessionFilter') }}<input
            v-model="targetSessionFilter" class="mt-1 w-full rounded-lg border p-2" /></label>
      </div>
      <p class="mb-4 text-xs text-[var(--muted)]">{{ t('issueFlow.labelsDisclaimer') }}</p>

      <div class="mb-4 rounded-xl border border-[var(--border)] bg-[var(--surface-warm)] p-3">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <div>
            <div class="text-sm font-semibold text-[var(--fg)]">{{ t('issueFlow.bulkTitle') }}</div>
            <div class="mt-1 text-xs text-[var(--muted)]">
              {{ t('issueFlow.bulkHelp') }}
            </div>
          </div>
          <div class="flex flex-wrap items-center gap-2">
            <button
              class="rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-xs font-semibold text-[var(--fg-2)] disabled:cursor-not-allowed disabled:opacity-60"
              :disabled="selectableIssueIds.length === 0" @click="toggleAllOpenIssues">
              {{ allOpenSelected ? t('issueFlow.bulkClear') : t('issueFlow.bulkSelect', {
                count:
                  selectableIssueIds.length
              }) }}
            </button>
            <button
              class="rounded-lg border border-[var(--accent)] bg-[var(--accent)] px-3 py-2 text-xs font-semibold text-[var(--accent-on)] disabled:cursor-not-allowed disabled:opacity-60"
              :disabled="!ready || !trackedProjects.some(project => project.project_id === projectFilter) || selectedOpenIds.length === 0 || bulkAcknowledgeMutation.isPending.value"
              @click="submitBulkAcknowledge">
              {{ bulkAcknowledgeMutation.isPending.value ? t('issueFlow.bulkRunning') : t('issueFlow.bulkSubmit', {
                count: selectedOpenIds.length
              }) }}
            </button>
          </div>
        </div>
        <div v-if="bulkActionError" class="mt-3 text-sm text-[var(--danger)]">
          {{ bulkActionError }}
        </div>
        <div v-else-if="bulkActionMessage" class="mt-3 text-sm text-[var(--success)]">
          {{ bulkActionMessage }}
        </div>
      </div>

      <div class="grid gap-4 xl:grid-cols-[minmax(0,1fr),360px]">
        <div class="surface-panel-warm p-4">
          <div v-if="issuesQuery.isPending.value" class="text-sm text-[var(--muted)]">
            {{ t('issueFlow.loadingIssues') }}
          </div>
          <div v-else-if="issuesQuery.isError.value" role="alert" class="text-sm text-[var(--danger)]">
            {{ safeError(issuesQuery.error.value) }}
            <button class="ml-2 rounded border px-3 py-2" :disabled="issuesQuery.isFetching.value"
              @click="issuesQuery.refetch()">{{ t('issueFlow.readIssuesAgain') }}</button>
          </div>
          <div v-else-if="issues.length === 0" class="text-sm text-[var(--muted)]">
            {{ t('issueFlow.noIssues') }}
          </div>
          <div v-else class="space-y-3">
            <div v-for="issue in issues" :key="issue.id"
              class="rounded-xl border border-[var(--border)] bg-[var(--surface)] p-4 transition-colors hover:border-[var(--accent)]">
              <div class="flex gap-3">
                <label
                  class="mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded-md border border-[var(--border)] bg-[var(--surface-warm)]">
                  <input type="checkbox" :aria-label="t('issueFlow.bulkSelectIssue', { id: issue.id })"
                    class="h-4 w-4 accent-[var(--accent)] disabled:cursor-not-allowed disabled:opacity-40"
                    :checked="selectedIssueIds.has(issue.id)" :disabled="!canSelectIssue(issue)"
                    :title="canSelectIssue(issue) ? t('issueFlow.selectForBulk') : t('issueFlow.bulkOnlyOpen')"
                    @change="onIssueCheckboxChange(issue.id, $event)" />
                </label>

                <NuxtLink :to="{ path: `/issues/${issue.id}`, query: { project: projectFilter } }"
                  class="min-w-0 flex-1">
                  <div class="mb-2 flex flex-wrap items-start justify-between gap-3">
                    <div>
                      <div class="text-sm font-semibold">#{{ issue.id }} · {{ issue.title }}</div>
                      <div class="mt-1 text-xs text-[var(--muted)]">
                        {{ projectName(issue.source_project) }} → {{ projectName(issue.target_project) }}
                      </div>
                    </div>
                    <div class="flex flex-wrap gap-2">
                      <span class="rounded-full border px-2 py-1 text-[10px] font-semibold uppercase tracking-[0.04em]"
                        :class="statusBadgeClass(issue.status)">
                        {{ issue.status === 'resolved' ? t('issueFlow.statusResolved') : issue.status === 'closed' ?
                          t('issueFlow.statusClosed') : issue.status }}
                      </span>
                      <span class="rounded-full border px-2 py-1 text-[10px] font-semibold uppercase tracking-[0.04em]"
                        :class="priorityBadgeClass(issue.priority)">
                        {{ t('issueFlow.value_' + issue.priority) }}
                      </span>
                    </div>
                  </div>

                  <p class="mb-3 text-xs text-[var(--muted)]">{{ t('issueFlow.revisionCard', {
                    revision: issue.revision
                  }) }}</p>
                  <p class="mb-3 text-xs text-[var(--muted)]">{{ t('issueFlow.ownerRecipient', {
                    owner:
                      issue.owner_label ?? t('issueFlow.unassigned'), recipient: issue.recipient_label ??
                        t('issueFlow.unassigned')
                  }) }}</p>
                  <div class="flex flex-wrap items-center justify-between gap-3 text-xs text-[var(--muted)]">
                    <span>{{ t('issueFlow.value_' + issue.type) }} · {{ t('issueFlow.commentTotal', {
                      count:
                        issue.comment_count || 0
                    }) }}</span>
                    <span class="mono-data">{{ formatRelativeTime(issue.created_at) }}</span>
                  </div>
                </NuxtLink>
              </div>
            </div>
          </div>
          <div class="mt-4 flex flex-wrap gap-2">
            <button class="rounded border border-[var(--border)] px-3 py-2 text-sm"
              :disabled="issuesQuery.isFetching.value || listOffset === 0" @click="previousIssues">{{
                t('issueFlow.previousIssues') }}</button>
            <button class="rounded border border-[var(--border)] px-3 py-2 text-sm"
              :disabled="issuesQuery.isFetching.value || listNext === null" @click="nextIssues">{{
                t('issueFlow.nextIssues') }}</button>
            <button class="rounded border border-[var(--border)] px-3 py-2 text-sm"
              :disabled="issuesQuery.isFetching.value"
              @click="listOffset = 0; listOffsets = []; selectedIssueIds = new Set(); issuesQuery.refetch()">{{
                t('issueFlow.restartList') }}</button>
          </div>

        </div>

        <div class="surface-panel-warm p-4">
          <div class="mb-3 text-xs uppercase tracking-[0.08em] text-[var(--muted)]">
            {{ t('issueFlow.newIssue') }}
          </div>

          <p class="mb-3 text-xs text-[var(--muted)]">{{ t('issueFlow.createPrivacy') }}</p>
          <div v-if="pendingCreate && !pendingHidden" class="mb-3 space-y-3 rounded-lg border border-[var(--warn)] p-3"
            role="status">
            <p class="text-sm">{{ t('issueFlow.retainedIntent', {
              outcome: t('issueFlow.outcome_' +
                pendingCreate.outcome)
            }) }}</p>
            <button class="rounded border border-[var(--border)] px-3 py-2 text-sm"
              :disabled="createBusy || storageBlocked || projectFilter !== pendingCreate.intent.source_project"
              @click="reconcileCreate">{{ recoveryActionLabel }}</button>
            <button
              v-if="pendingCreate.outcome === 'rejected' && ['INVALID_ARGUMENT', 'PAYLOAD_TOO_LARGE', 'PRIVACY_REJECTED'].includes(pendingCreate.code ?? '')"
              class="rounded border border-[var(--border)] px-3 py-2 text-sm" :disabled="createBusy"
              @click="reviseRejectedIntent">{{ t('issueFlow.editRejected') }}</button>
          </div>
          <div v-if="pendingHidden || storageBlocked" class="mb-3 space-y-2 rounded-lg border border-[var(--warn)] p-3"
            role="status">
            <p class="text-sm">{{ t('issueFlow.recoveryHeld') }}</p>
            <button class="rounded border px-3 py-2 text-sm" :disabled="createBusy" @click="restorePending">{{
              t('issueFlow.checkRecovery') }}</button>
          </div>

          <div class="space-y-3">
            <label class="block">
              <span class="mb-1 block text-xs text-[var(--muted)]">{{ t('issueFlow.title') }}</span>
              <input v-model="newTitle" :disabled="formLocked" type="text"
                class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm text-[var(--fg)]" />
            </label>

            <label class="block">
              <span class="mb-1 block text-xs text-[var(--muted)]">{{ t('issueFlow.description') }}</span>
              <textarea v-model="newBody" :disabled="formLocked" rows="5"
                class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm text-[var(--fg)]" />
            </label>

            <label class="block">
              <span class="mb-1 block text-xs text-[var(--muted)]">{{ t('issueFlow.targetProject') }}</span>
              <select v-model="newTargetProject" :disabled="formLocked"
                class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm text-[var(--fg)]">
                <option value="">{{ t('issueFlow.selectTarget') }}</option>
                <option v-for="project in trackedProjects" :key="project.project_id" :value="project.project_id">
                  {{ projectName(project.project_id) }}
                </option>
              </select>
            </label>
            <label class="block text-sm">{{ t('issueFlow.sourceSession') }}
              <input v-model="newSourceSession" :disabled="formLocked" class="mt-1 w-full rounded-lg border p-2" />
            </label>
            <label class="block text-sm">{{ t('issueFlow.targetSession') }}
              <input v-model="newTargetSession" :disabled="formLocked" class="mt-1 w-full rounded-lg border p-2" />
            </label>
            <p class="text-xs text-[var(--muted)]">{{ t('issueFlow.sessionHelp') }}</p>

            <div class="grid gap-3 sm:grid-cols-2">
              <label class="block">
                <span class="mb-1 block text-xs text-[var(--muted)]">{{ t('issueFlow.type') }}</span>
                <select v-model="newType" :disabled="formLocked"
                  class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm text-[var(--fg)]">
                  <option value="task">{{ t('issueFlow.value_task') }}</option>
                  <option value="bug">{{ t('issueFlow.value_bug') }}</option>
                  <option value="feature">{{ t('issueFlow.value_feature') }}</option>
                  <option value="improvement">{{ t('issueFlow.value_improvement') }}</option>
                </select>
              </label>

              <label class="block">
                <span class="mb-1 block text-xs text-[var(--muted)]">{{ t('issueFlow.priority') }}</span>
                <select v-model="newPriority" :disabled="formLocked"
                  class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm text-[var(--fg)]">
                  <option value="medium">{{ t('issueFlow.value_medium') }}</option>
                  <option value="low">{{ t('issueFlow.value_low') }}</option>
                  <option value="high">{{ t('issueFlow.value_high') }}</option>
                  <option value="critical">{{ t('issueFlow.value_critical') }}</option>
                </select>
              </label>
            </div>

            <label class="block"><span class="mb-1 block text-xs text-[var(--muted)]">{{ t('issueFlow.labelsJson')
                }}</span><textarea v-model="newLabels" :disabled="formLocked" rows="2"
                class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm" /></label>
            <label class="block"><span class="mb-1 block text-xs text-[var(--muted)]">{{ t('issueFlow.metadataJson')
                }}</span><textarea v-model="newMetadata" :disabled="formLocked" rows="4"
                class="mono-data w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm" /></label>
            <label class="block"><span class="mb-1 block text-xs text-[var(--muted)]">{{ t('issueFlow.ownerLabel')
                }}</span><input v-model="newOwner" :disabled="formLocked"
                class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm" /></label>
            <label class="block"><span class="mb-1 block text-xs text-[var(--muted)]">{{ t('issueFlow.recipientLabel')
                }}</span><input v-model="newRecipient" :disabled="formLocked"
                class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm" /></label>
            <p class="text-xs text-[var(--muted)]">{{ t('issueFlow.createFieldsHelp') }}</p>

            <div v-if="createError" role="alert"
              class="rounded-lg border border-[color:rgba(248,113,113,0.4)] bg-[color:rgba(248,113,113,0.12)] px-3 py-2 text-sm text-[var(--danger)]">
              {{ createError }}
            </div>

            <p v-if="createMessage" role="status" class="text-sm text-[var(--success)]">{{ createMessage }}</p>

            <button
              class="rounded-lg border border-[var(--accent)] bg-[var(--accent)] px-4 py-2 text-sm font-semibold text-[var(--accent-on)] disabled:cursor-not-allowed disabled:opacity-60"
              :disabled="formLocked || !ready || !trackedProjects.some(project => project.project_id === projectFilter) || createIssueMutation.isPending.value"
              @click="submitIssue">
              {{ createBusy ? t('issueFlow.creating') : t('issueFlow.create') }}
            </button>
          </div>
        </div>
      </div>
    </div>
  </section>
</template>
