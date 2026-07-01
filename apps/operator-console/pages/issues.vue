<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useOperatorIssues, type OperatorIssue, type OperatorIssuePriority, type OperatorIssueStatus, type OperatorIssueType } from '../composables/useOperatorIssues'

const { t } = useI18n()
const {
  rows,
  detail,
  loadState,
  detailState,
  pending,
  error,
  refresh,
  openIssue,
  createIssue: runCreateIssue,
  updateIssue,
  acknowledgeIssue,
  deleteIssue,
} = useOperatorIssues()

const statusTone: Record<OperatorIssueStatus, string> = {
  open: 'var(--state-warn)',
  acknowledged: 'var(--accent)',
  reopened: 'var(--accent)',
  resolved: 'var(--class-live)',
  closed: 'var(--muted)',
  rejected: 'var(--muted)',
}

const openId = ref<number | null>(null)
const deleteConfirm = ref(false)
const createTitle = ref('')
const createBody = ref('')
const createTargetProject = ref('engram')
const createPriority = ref<OperatorIssuePriority>('medium')
const createType = ref<OperatorIssueType>('task')
const titleDraft = ref('')
const bodyDraft = ref('')
const priorityDraft = ref<OperatorIssuePriority>('medium')
const typeDraft = ref<OperatorIssueType>('task')
const statusDraft = ref<OperatorIssueStatus>('open')

const priorityOptions: OperatorIssuePriority[] = ['critical', 'high', 'medium', 'low']
const typeOptions: OperatorIssueType[] = ['bug', 'feature', 'improvement', 'task']
const statusOptions: OperatorIssueStatus[] = ['open', 'acknowledged', 'resolved', 'reopened', 'closed']

const opened = computed(() => detail.value || rows.find((issue) => issue.id === openId.value) || null)
const canCreate = computed(() => createTitle.value.trim().length > 0 && createTargetProject.value.trim().length > 0 && !pending.value)
const canUpdate = computed(() => Boolean(opened.value) && titleDraft.value.trim().length > 0 && !pending.value)
const stats = computed(() => ({
  open: rows.filter((issue) => issue.status === 'open').length,
  work: rows.filter((issue) => issue.status === 'acknowledged' || issue.status === 'reopened').length,
  done: rows.filter((issue) => issue.status === 'resolved' || issue.status === 'closed').length,
  total: rows.length,
}))

watch(opened, (issue) => {
  titleDraft.value = issue?.title || ''
  bodyDraft.value = issue?.body || ''
  priorityDraft.value = issue?.priority || 'medium'
  typeDraft.value = issue?.type || 'task'
  statusDraft.value = issue?.status || 'open'
  deleteConfirm.value = false
})

function resetCreate() {
  createTitle.value = ''
  createBody.value = ''
  createTargetProject.value = 'engram'
  createPriority.value = 'medium'
  createType.value = 'task'
}

async function selectIssue(issue: OperatorIssue) {
  if (openId.value === issue.id) {
    openId.value = null
    return
  }

  openId.value = issue.id
  await openIssue(issue.id)
}

async function createIssue() {
  if (!canCreate.value) return
  await runCreateIssue({
    title: createTitle.value.trim(),
    body: createBody.value.trim(),
    priority: createPriority.value,
    type: createType.value,
    targetProject: createTargetProject.value.trim(),
  })
  resetCreate()
}

async function updateOpened() {
  if (!opened.value || !canUpdate.value) return
  await updateIssue(opened.value.id, {
    title: titleDraft.value.trim(),
    body: bodyDraft.value.trim(),
    priority: priorityDraft.value,
    type: typeDraft.value,
    status: statusDraft.value,
    comment: t('issues.detail.operatorComment'),
  })
}

async function acknowledgeOpened() {
  if (!opened.value) return
  await acknowledgeIssue(opened.value.id)
}

async function deleteOpened() {
  if (!opened.value) return
  if (!deleteConfirm.value) {
    deleteConfirm.value = true
    return
  }

  const id = opened.value.id
  openId.value = null
  deleteConfirm.value = false
  await deleteIssue(id)
}

function rowMeta(issue: OperatorIssue) {
  return [
    t(`issues.priority.${issue.priority}`),
    t(`issues.type.${issue.type}`),
    t('issues.meta.comments', { count: issue.comments }),
    issue.age,
    issue.targetProject,
  ]
}
</script>

<template>
  <div class="issues-page">
    <header class="head">
      <h1>{{ t('issues.title') }}</h1>
      <p>{{ t('issues.subtitle') }}</p>
    </header>

    <section v-if="pending || error || loadState.kind === 'empty'" class="statebar" :data-state="loadState.kind">
      <span v-if="pending">{{ t('issues.state.pending') }}</span>
      <span v-else-if="error">{{ t('issues.state.error', { message: error }) }}</span>
      <span v-else-if="loadState.kind === 'empty'">{{ t('issues.state.empty') }}</span>
      <button v-if="error" class="tbtn" @click="refresh">{{ t('issues.state.retry') }}</button>
    </section>

    <section class="summary">
      <div><b>{{ stats.open }}</b><span>{{ t('issues.stats.open') }}</span></div>
      <div><b>{{ stats.work }}</b><span>{{ t('issues.stats.work') }}</span></div>
      <div><b>{{ stats.done }}</b><span>{{ t('issues.stats.done') }}</span></div>
      <div><b>{{ stats.total }}</b><span>{{ t('issues.stats.total') }}</span></div>
    </section>

    <section class="create-card">
      <div class="section-head">
        <div>
          <h2>{{ t('issues.create.title') }}</h2>
          <p>{{ t('issues.create.subtitle') }}</p>
        </div>
        <HonestyBadge cls="live" />
      </div>
      <input v-model="createTitle" class="input title" :placeholder="t('issues.create.titlePlaceholder')" />
      <textarea v-model="createBody" class="text" :placeholder="t('issues.create.bodyPlaceholder')" />
      <div class="inline-fields">
        <label>
          <span>{{ t('issues.create.targetProject') }}</span>
          <input v-model="createTargetProject" class="input" />
        </label>
        <label>
          <span>{{ t('issues.create.priority') }}</span>
          <select v-model="createPriority" class="input">
            <option v-for="priority in priorityOptions" :key="priority" :value="priority">{{ t(`issues.priority.${priority}`) }}</option>
          </select>
        </label>
        <label>
          <span>{{ t('issues.create.type') }}</span>
          <select v-model="createType" class="input">
            <option v-for="kind in typeOptions" :key="kind" :value="kind">{{ t(`issues.type.${kind}`) }}</option>
          </select>
        </label>
        <button class="primary" :disabled="!canCreate" @click="createIssue">{{ t('issues.create.submit') }}</button>
      </div>
    </section>

    <div class="area-body" :class="{ 'detail-open': opened }">
      <section class="grid">
        <div class="grid-title">
          <h2>{{ t('issues.list.title') }}</h2>
          <span>{{ t('issues.list.count', { count: rows.length }) }}</span>
        </div>
        <EntityRow
          v-for="issue in rows"
          :key="issue.id"
          :preview="`#${issue.id} · ${issue.title}`"
          :meta="rowMeta(issue)"
          :status="issue.status === 'resolved' || issue.status === 'closed' ? 'live' : 'warn'"
          :open="openId === issue.id"
          @open="selectIssue(issue)"
        >
          <template #side>
            <span class="chip" :style="{ color: statusTone[issue.status] }">{{ t(`issues.status.${issue.status}`) }}</span>
          </template>
        </EntityRow>
        <div v-if="!rows.length && loadState.kind === 'empty'" class="empty">
          <b>{{ t('issues.empty.title') }}</b>
          <span>{{ t('issues.empty.body') }}</span>
        </div>
      </section>

      <aside v-if="opened" class="detail">
        <div class="detail-head">
          <h2>{{ t('issues.detail.title', { id: opened.id }) }}</h2>
          <button class="tbtn" @click="openId = null">×</button>
        </div>
        <div v-if="detailState.kind === 'pending'" class="detail-state">{{ t('issues.detail.loading') }}</div>
        <div v-else-if="detailState.kind === 'error'" class="detail-state warn">{{ t('issues.detail.error') }}</div>
        <label class="field">
          <span>{{ t('issues.detail.issueTitle') }}</span>
          <input v-model="titleDraft" class="input" />
        </label>
        <label class="field">
          <span>{{ t('issues.detail.body') }}</span>
          <textarea v-model="bodyDraft" class="text tall" />
        </label>
        <div class="inline-fields detail-fields">
          <label>
            <span>{{ t('issues.detail.status') }}</span>
            <select v-model="statusDraft" class="input">
              <option v-for="status in statusOptions" :key="status" :value="status">{{ t(`issues.status.${status}`) }}</option>
            </select>
          </label>
          <label>
            <span>{{ t('issues.detail.priority') }}</span>
            <select v-model="priorityDraft" class="input">
              <option v-for="priority in priorityOptions" :key="priority" :value="priority">{{ t(`issues.priority.${priority}`) }}</option>
            </select>
          </label>
          <label>
            <span>{{ t('issues.detail.type') }}</span>
            <select v-model="typeDraft" class="input">
              <option v-for="kind in typeOptions" :key="kind" :value="kind">{{ t(`issues.type.${kind}`) }}</option>
            </select>
          </label>
        </div>
        <div class="actions">
          <button class="primary" :disabled="!canUpdate" @click="updateOpened">{{ t('issues.detail.save') }}</button>
          <button class="secondary" :disabled="pending" @click="acknowledgeOpened">{{ t('issues.detail.acknowledge') }}</button>
          <button class="danger" :disabled="pending" @click="deleteOpened">
            {{ deleteConfirm ? t('issues.detail.confirmDelete') : t('issues.detail.delete') }}
          </button>
        </div>
      </aside>
    </div>
  </div>
</template>

<style scoped>
.issues-page { display:flex; flex-direction:column; gap:14px; }
.head { padding-bottom:14px; border-bottom:1px solid var(--border); }
.head h1 { margin:0 0 4px; font-size:var(--text-xl); font-weight:800; letter-spacing:var(--tracking-display); }
.head p { margin:0; font-size:var(--text-sm); color:var(--muted); }
.statebar { display:flex; align-items:center; justify-content:space-between; gap:12px; padding:10px 12px; border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); color:var(--fg-2); font-size:var(--text-sm); }
.statebar[data-state="pending"] { border-color:color-mix(in oklab,var(--accent),transparent 55%); }
.statebar[data-state="error"] { border-color:color-mix(in oklab,var(--state-warn),transparent 45%); color:var(--state-warn); }
.statebar[data-state="empty"] { color:var(--muted); }
.summary { display:grid; grid-template-columns:repeat(4,minmax(0,1fr)); gap:10px; }
.summary div { padding:14px; border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); }
.summary b { display:block; color:var(--fg); font-size:var(--text-xl); font-family:var(--font-mono); }
.summary span { color:var(--muted); font-size:var(--text-xs); font-weight:800; text-transform:uppercase; letter-spacing:.06em; }
.create-card, .detail, .grid { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); }
.create-card { padding:14px; }
.section-head, .grid-title, .detail-head { display:flex; align-items:flex-start; justify-content:space-between; gap:12px; }
.section-head h2, .grid-title h2, .detail-head h2 { margin:0; font-size:var(--text-lg); }
.section-head p { margin:4px 0 0; color:var(--muted); font-size:var(--text-sm); }
.text, .input { width:100%; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface-warm); color:var(--fg); font:inherit; }
.input { min-height:34px; padding:0 10px; }
.input.title { margin-top:12px; }
.text { min-height:76px; margin-top:10px; padding:10px 11px; resize:vertical; font-family:var(--font-mono); font-size:var(--text-sm); line-height:1.45; }
.text.tall { min-height:150px; }
.inline-fields { display:grid; grid-template-columns:minmax(0,1fr) 150px 150px auto; gap:10px; align-items:end; margin-top:10px; }
.inline-fields.detail-fields { grid-template-columns:repeat(3,minmax(0,1fr)); }
label span, .field span { display:block; margin-bottom:5px; color:var(--muted); font-size:var(--text-xs); font-weight:800; text-transform:uppercase; letter-spacing:.06em; }
.primary, .secondary, .danger, .tbtn { min-height:34px; padding:7px 11px; border:1px solid var(--border); border-radius:var(--r-sm); font-size:var(--text-xs); font-weight:900; cursor:pointer; }
.primary { background:var(--accent); border-color:var(--accent); color:var(--accent-on); }
.secondary { background:var(--surface); color:var(--fg-2); }
.danger { background:transparent; border-color:color-mix(in oklab,var(--state-warn),transparent 45%); color:var(--state-warn); }
.tbtn { min-width:34px; background:var(--surface); color:var(--fg-2); }
.primary:disabled, .secondary:disabled, .danger:disabled { opacity:.5; cursor:not-allowed; }
.area-body { display:grid; grid-template-columns:minmax(0,1fr); gap:14px; }
.area-body.detail-open { grid-template-columns:minmax(0,1fr) minmax(350px,430px); align-items:start; }
.grid { overflow:hidden; }
.grid-title { padding:12px 14px; border-bottom:1px solid var(--border); }
.grid-title span { color:var(--muted); font-size:var(--text-xs); font-weight:800; }
.chip { font-size:10px; font-weight:900; text-transform:uppercase; letter-spacing:.04em; padding:3px 8px; border-radius:var(--radius-pill); border:1px solid currentColor; background:color-mix(in oklab,currentColor,transparent 88%); }
.empty { display:flex; flex-direction:column; align-items:center; justify-content:center; gap:5px; min-height:180px; color:var(--muted); }
.empty b { color:var(--fg-2); font-size:var(--text-lg); }
.detail { padding:14px; position:sticky; top:0; }
.field { display:block; margin-top:12px; }
.detail-state { margin-top:12px; padding:9px 11px; border-radius:var(--r-sm); background:var(--surface-warm); color:var(--muted); font-size:var(--text-sm); }
.detail-state.warn { color:var(--state-warn); }
.actions { display:flex; align-items:center; gap:8px; flex-wrap:wrap; margin-top:14px; }
@media (max-width:1080px) {
  .area-body.detail-open { grid-template-columns:1fr; }
  .detail { position:static; }
}
@media (max-width:820px) {
  .summary { grid-template-columns:repeat(2,minmax(0,1fr)); }
  .inline-fields, .inline-fields.detail-fields { grid-template-columns:1fr; }
}
</style>
