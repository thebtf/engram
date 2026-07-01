<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useOperatorRules } from '../composables/useOperatorRules'

const { t } = useI18n()
const {
  rows,
  loadState,
  pending,
  error,
  refresh,
  createRule: runCreateRule,
  updateRule,
  deleteRule,
  enableGap,
} = useOperatorRules()

const openId = ref<number | null>(null)
const createContent = ref('')
const createProject = ref('')
const createPriority = ref(0)
const contentDraft = ref('')
const projectDraft = ref('')
const priorityDraft = ref(0)

const sortedRows = computed(() => [...rows].sort((left, right) => {
  if (left.priority !== right.priority) {
    return right.priority - left.priority
  }
  return left.content.localeCompare(right.content)
}))
const opened = computed(() => rows.find((rule) => rule.id === openId.value) || null)
const canCreate = computed(() => createContent.value.trim().length > 0 && !pending.value)
const canUpdate = computed(() => Boolean(opened.value) && contentDraft.value.trim().length > 0 && !pending.value)

watch(opened, (rule) => {
  contentDraft.value = rule?.content || ''
  projectDraft.value = rule?.project === 'global' ? '' : rule?.project || ''
  priorityDraft.value = rule?.priority || 0
})

function resetCreate() {
  createContent.value = ''
  createProject.value = ''
  createPriority.value = 0
}

async function createRule() {
  if (!canCreate.value) return
  await runCreateRule({
    content: createContent.value.trim(),
    priority: createPriority.value,
    project: createProject.value.trim() || undefined,
    editedBy: 'operator-console',
  })
  resetCreate()
}

async function updateOpened() {
  if (!opened.value || !canUpdate.value) return
  await updateRule(opened.value.id, {
    content: contentDraft.value.trim(),
    priority: priorityDraft.value,
    editedBy: 'operator-console',
  })
}

async function deleteOpened() {
  if (!opened.value) return
  const id = opened.value.id
  openId.value = null
  await deleteRule(id)
}

function rowMeta(rule: typeof rows[number]) {
  return [
    t('rules.meta.priority', { priority: rule.priority }),
    t('rules.meta.project', { project: rule.project }),
    t('rules.meta.updated', { updated: rule.updated }),
  ]
}
</script>

<template>
  <div class="rules-page">
    <header class="head">
      <h1>{{ t('rules.title') }}</h1>
      <p>{{ t('rules.subtitle') }}</p>
    </header>

    <section v-if="pending || error || loadState.kind === 'empty'" class="statebar" :data-state="loadState.kind">
      <span v-if="pending">{{ t('rules.state.pending') }}</span>
      <span v-else-if="error">{{ t('rules.state.error', { message: error }) }}</span>
      <span v-else-if="loadState.kind === 'empty'">{{ t('rules.state.empty') }}</span>
      <button v-if="error" class="tbtn" @click="refresh">{{ t('rules.state.retry') }}</button>
    </section>

    <section class="create-card">
      <div class="section-head">
        <div>
          <h2>{{ t('rules.create.title') }}</h2>
          <p>{{ t('rules.create.subtitle') }}</p>
        </div>
        <HonestyBadge cls="live" />
      </div>
      <textarea v-model="createContent" class="text" :placeholder="t('rules.create.contentPlaceholder')" />
      <div class="inline-fields">
        <label>
          <span>{{ t('rules.create.project') }}</span>
          <input v-model="createProject" class="input" :placeholder="t('rules.create.global')" />
        </label>
        <label>
          <span>{{ t('rules.create.priority') }}</span>
          <input v-model.number="createPriority" class="input" type="number" />
        </label>
        <button class="primary" :disabled="!canCreate" @click="createRule">{{ t('rules.create.submit') }}</button>
      </div>
    </section>

    <div class="area-body" :class="{ 'detail-open': opened }">
      <section class="grid">
        <div class="grid-title">
          <h2>{{ t('rules.list.title') }}</h2>
          <span>{{ t('rules.list.count', { count: rows.length }) }}</span>
        </div>
        <EntityRow
          v-for="rule in sortedRows"
          :key="rule.id"
          :preview="rule.content"
          :meta="rowMeta(rule)"
          status="live"
          :open="openId === rule.id"
          @open="openId = openId === rule.id ? null : rule.id"
        >
          <template #side>
            <HonestyBadge cls="live" />
          </template>
        </EntityRow>
        <div v-if="!sortedRows.length && loadState.kind === 'empty'" class="empty">
          <b>{{ t('rules.empty.title') }}</b>
          <span>{{ t('rules.empty.body') }}</span>
        </div>
      </section>

      <aside v-if="opened" class="detail">
        <div class="detail-head">
          <h2>{{ t('rules.detail.title', { id: opened.id }) }}</h2>
          <button class="tbtn" @click="openId = null">×</button>
        </div>
        <label class="field">
          <span>{{ t('rules.detail.content') }}</span>
          <textarea v-model="contentDraft" class="text tall" />
        </label>
        <div class="inline-fields">
          <label>
            <span>{{ t('rules.detail.project') }}</span>
            <input v-model="projectDraft" class="input" disabled />
          </label>
          <label>
            <span>{{ t('rules.detail.priority') }}</span>
            <input v-model.number="priorityDraft" class="input" type="number" />
          </label>
        </div>
        <div class="evidence-card">
          <b>{{ t('rules.detail.enableTitle') }}</b>
          <span>{{ t('rules.detail.enableBody') }}</span>
          <code>{{ enableGap.evidence.endpoint }}</code>
        </div>
        <div class="actions">
          <button class="primary" :disabled="!canUpdate" @click="updateOpened">{{ t('rules.detail.save') }}</button>
          <button class="danger" :disabled="pending" @click="deleteOpened">{{ t('rules.detail.delete') }}</button>
          <button class="secondary" disabled>{{ t('rules.detail.enable') }} <span>{{ t('overview.badges.mustBuild') }}</span></button>
        </div>
      </aside>
    </div>
  </div>
</template>

<style scoped>
.rules-page { display:flex; flex-direction:column; gap:14px; }
.head { padding-bottom:14px; border-bottom:1px solid var(--border); }
.head h1 { margin:0 0 4px; font-size:var(--text-xl); font-weight:800; letter-spacing:var(--tracking-display); }
.head p { margin:0; font-size:var(--text-sm); color:var(--muted); }
.statebar { display:flex; align-items:center; justify-content:space-between; gap:12px; padding:10px 12px; border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); color:var(--fg-2); font-size:var(--text-sm); }
.statebar[data-state="pending"] { border-color:color-mix(in oklab,var(--accent),transparent 55%); }
.statebar[data-state="error"] { border-color:color-mix(in oklab,var(--state-warn),transparent 45%); color:var(--state-warn); }
.statebar[data-state="empty"] { color:var(--muted); }
.create-card, .detail, .grid { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); }
.create-card { padding:14px; }
.section-head, .grid-title, .detail-head { display:flex; align-items:flex-start; justify-content:space-between; gap:12px; }
.section-head h2, .grid-title h2, .detail-head h2 { margin:0; font-size:var(--text-lg); }
.section-head p { margin:4px 0 0; color:var(--muted); font-size:var(--text-sm); }
.text, .input { width:100%; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface-warm); color:var(--fg); font:inherit; }
.text { min-height:86px; margin-top:12px; padding:10px 11px; resize:vertical; font-family:var(--font-mono); font-size:var(--text-sm); line-height:1.45; }
.text.tall { min-height:150px; }
.input { min-height:34px; padding:0 10px; }
.inline-fields { display:grid; grid-template-columns:minmax(0,1fr) 140px auto; gap:10px; align-items:end; margin-top:10px; }
label span, .field span { display:block; margin-bottom:5px; color:var(--muted); font-size:var(--text-xs); font-weight:800; text-transform:uppercase; letter-spacing:.06em; }
.primary, .secondary, .danger, .tbtn { min-height:34px; padding:7px 11px; border:1px solid var(--border); border-radius:var(--r-sm); font-size:var(--text-xs); font-weight:900; cursor:pointer; }
.primary { background:var(--accent); border-color:var(--accent); color:var(--accent-on); }
.secondary { background:var(--surface); color:var(--fg-2); }
.danger { background:transparent; border-color:color-mix(in oklab,var(--state-warn),transparent 45%); color:var(--state-warn); }
.tbtn { min-width:34px; background:var(--surface); color:var(--fg-2); }
.primary:disabled, .secondary:disabled, .danger:disabled { opacity:.5; cursor:not-allowed; }
.area-body { display:grid; grid-template-columns:minmax(0,1fr); gap:14px; }
.area-body.detail-open { grid-template-columns:minmax(0,1fr) minmax(330px,400px); align-items:start; }
.grid { overflow:hidden; }
.grid-title { padding:12px 14px; border-bottom:1px solid var(--border); }
.grid-title span { color:var(--muted); font-size:var(--text-xs); font-weight:800; }
.empty { display:flex; flex-direction:column; align-items:center; justify-content:center; gap:5px; min-height:180px; color:var(--muted); }
.empty b { color:var(--fg-2); font-size:var(--text-lg); }
.detail { padding:14px; position:sticky; top:0; }
.field { display:block; margin-top:12px; }
.evidence-card { display:flex; flex-direction:column; gap:5px; margin-top:12px; padding:11px; border:1px solid color-mix(in oklab,var(--class-mustbuild),transparent 55%); border-radius:var(--r-sm); background:color-mix(in oklab,var(--class-mustbuild),transparent 91%); color:var(--fg-2); font-size:var(--text-sm); }
.evidence-card code { width:max-content; color:var(--muted); font-family:var(--font-mono); font-size:var(--text-xs); }
.actions { display:flex; align-items:center; gap:8px; flex-wrap:wrap; margin-top:14px; }
.actions span { margin-left:5px; color:var(--class-mustbuild); }
@media (max-width:980px) {
  .area-body.detail-open { grid-template-columns:1fr; }
  .detail { position:static; }
}
@media (max-width:760px) {
  .inline-fields { grid-template-columns:1fr; }
}
</style>
