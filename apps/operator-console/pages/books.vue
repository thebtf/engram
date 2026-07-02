<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { useOperatorBooks } from '../composables/useOperatorBooks'

const { t } = useI18n()
const {
  currentJob,
  currentProject,
  jobState,
  submitting,
  error,
  documentsHref,
  refreshJobStatus,
  ingestBook,
} = useOperatorBooks()

const form = reactive({
  sourceRef: '',
  project: currentProject.value || 'engram',
  author: 'operator-console',
  content: '',
})
const notice = ref<{ kind: 'success' | 'error'; text: string } | null>(null)
const fileMessage = ref<string | null>(null)

const statusLabelKey = computed(() => {
  const status = currentJob.value?.status || 'idle'
  if (status === 'pending' || status === 'processing' || status === 'done' || status === 'failed') {
    return `booksPage.job.stateLabel.${status}`
  }
  return 'booksPage.job.stateLabel.idle'
})
const statusTone = computed(() => {
  const status = currentJob.value?.status || 'idle'
  if (status === 'done') return 'done'
  if (status === 'failed') return 'failed'
  if (status === 'pending' || status === 'processing') return 'processing'
  return 'idle'
})
const showStatebar = computed(() => Boolean(notice.value || error.value || jobState.value.kind === 'pending'))
const documentsPrefix = computed(() => currentJob.value?.documentsPathPrefix || '—')
const canOpenDocuments = computed(() => currentJob.value?.status === 'done')
const supportHint = computed(() => {
  if (currentJob.value?.status === 'processing') return t('booksPage.job.processingBody')
  if (currentJob.value?.status === 'done') return t('booksPage.job.doneBody')
  if (currentJob.value?.status === 'failed') return t('booksPage.job.failedBody')
  if (currentJob.value?.status === 'pending') return t('booksPage.job.pendingBody')
  return t('booksPage.job.emptyBody')
})

async function onFileChange(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file) {
    return
  }

  form.sourceRef = file.name
  fileMessage.value = null
  notice.value = null

  const lowerName = file.name.toLowerCase()
  const isTextSource = lowerName.endsWith('.md') || lowerName.endsWith('.mdx') || lowerName.endsWith('.markdown') || lowerName.endsWith('.txt')
  if (!isTextSource) {
    form.content = ''
    fileMessage.value = t('booksPage.form.unsupportedFile')
    return
  }

  form.content = await file.text()
  fileMessage.value = t('booksPage.form.loadedFile', { name: file.name })
}

async function submitBook() {
  notice.value = null
  fileMessage.value = null

  if (!form.sourceRef.trim() || !form.content.trim()) {
    notice.value = { kind: 'error', text: t('booksPage.form.validation') }
    return
  }

  const result = await ingestBook({
    sourceRef: form.sourceRef,
    project: form.project,
    author: form.author,
    content: form.content,
  })

  if (result.kind === 'success') {
    currentProject.value = form.project.trim() || currentProject.value || 'engram'
    notice.value = { kind: 'success', text: t('booksPage.notice.queued', { id: result.data.id }) }
    return
  }

  notice.value = {
    kind: 'error',
    text: t('booksPage.notice.failed', { message: result.error.message || t('booksPage.notice.loadError') }),
  }
}
</script>

<template>
  <div class="books-page">
    <header class="page-head">
      <div>
        <h1>{{ t('booksPage.title') }}</h1>
        <p>{{ t('booksPage.subtitle') }}</p>
      </div>
      <HonestyBadge cls="live" evidence="/api/books/*" />
    </header>

    <section class="books-brief">
      <div class="metric">
        <b>{{ t(statusLabelKey) }}</b>
        <span>{{ t('booksPage.metrics.status') }}</span>
      </div>
      <div class="metric">
        <b>{{ currentJob?.sourceRef || '—' }}</b>
        <span>{{ t('booksPage.metrics.source') }}</span>
      </div>
      <div class="metric">
        <b>{{ currentJob?.documentsPathPrefix || '—' }}</b>
        <span>{{ t('booksPage.metrics.prefix') }}</span>
      </div>
      <div class="brief-copy">
        <strong>{{ t('booksPage.brief.title') }}</strong>
        <span>{{ t('booksPage.brief.body') }}</span>
      </div>
    </section>

    <section v-if="showStatebar" class="statebar" :data-state="error ? 'error' : notice?.kind || 'pending'">
      <span v-if="notice">{{ notice.text }}</span>
      <span v-else-if="error">{{ t('booksPage.notice.failed', { message: error }) }}</span>
      <span v-else>{{ t('booksPage.job.processingBody') }}</span>
      <button class="tbtn" type="button" @click="refreshJobStatus">{{ t('booksPage.actions.refresh') }}</button>
    </section>

    <div class="books-grid">
      <section class="panel form-panel">
        <div class="panel-head">
          <div>
            <h2>{{ t('booksPage.form.title') }}</h2>
            <p>{{ t('booksPage.form.lead') }}</p>
          </div>
        </div>

        <label>
          <span>{{ t('booksPage.form.file') }}</span>
          <input class="input file-input" type="file" accept=".md,.mdx,.markdown,.txt,text/markdown,text/plain" @change="onFileChange" />
          <small>{{ t('booksPage.form.fileHint') }}</small>
        </label>

        <label>
          <span>{{ t('booksPage.form.sourceRef') }}</span>
          <input v-model="form.sourceRef" class="input" type="text" :placeholder="t('booksPage.form.sourceRefHint')" />
        </label>

        <div class="inline-grid">
          <label>
            <span>{{ t('booksPage.form.project') }}</span>
            <input v-model="form.project" class="input" type="text" placeholder="engram" />
          </label>
          <label>
            <span>{{ t('booksPage.form.author') }}</span>
            <input v-model="form.author" class="input" type="text" placeholder="operator-console" />
          </label>
        </div>

        <label>
          <span>{{ t('booksPage.form.content') }}</span>
          <textarea v-model="form.content" class="area" rows="18" :placeholder="t('booksPage.form.contentHint')" />
        </label>

        <p v-if="fileMessage" class="inline-note">{{ fileMessage }}</p>

        <div class="actions">
          <button class="act primary" type="button" :disabled="submitting" @click="submitBook">
            {{ submitting ? t('booksPage.form.submitting') : t('booksPage.form.submit') }}
          </button>
          <button class="tbtn" type="button" @click="refreshJobStatus">{{ t('booksPage.actions.refresh') }}</button>
        </div>
      </section>

      <aside class="panel status-panel">
        <div class="panel-head">
          <div>
            <h2>{{ t('booksPage.job.title') }}</h2>
            <p>{{ supportHint }}</p>
          </div>
          <span class="status-chip" :data-state="statusTone">{{ t(statusLabelKey) }}</span>
        </div>

        <div v-if="!currentJob" class="empty-card">
          <b>{{ t('booksPage.job.emptyTitle') }}</b>
          <span>{{ t('booksPage.job.emptyBody') }}</span>
        </div>

        <template v-else>
          <dl class="fields">
            <dt>{{ t('booksPage.job.status') }}</dt><dd>{{ t(statusLabelKey) }}</dd>
            <dt>{{ t('booksPage.job.source') }}</dt><dd>{{ currentJob.sourceRef }}</dd>
            <dt>{{ t('booksPage.job.prefix') }}</dt><dd class="mono">{{ documentsPrefix }}</dd>
            <dt>{{ t('booksPage.job.createdAt') }}</dt><dd>{{ currentJob.createdAt || '—' }}</dd>
            <dt>{{ t('booksPage.job.updatedAt') }}</dt><dd>{{ currentJob.updatedAt || '—' }}</dd>
          </dl>

          <div v-if="currentJob.error" class="typed-error">
            <strong>{{ t('booksPage.job.error') }}</strong>
            <p>{{ currentJob.error }}</p>
          </div>

          <div class="actions stack">
            <NuxtLink v-if="canOpenDocuments" :to="documentsHref" class="act primary">{{ t('booksPage.job.openDocuments') }}</NuxtLink>
            <button v-else class="act muted" type="button" disabled>{{ t('booksPage.job.openDocuments') }}</button>
            <button class="tbtn" type="button" @click="refreshJobStatus">{{ t('booksPage.actions.refresh') }}</button>
          </div>
        </template>
      </aside>
    </div>
  </div>
</template>

<style scoped>
.books-page { display:flex; flex-direction:column; gap:14px; }
.page-head { display:flex; align-items:flex-start; justify-content:space-between; gap:18px; padding-bottom:14px; border-bottom:1px solid var(--border); }
.page-head h1 { margin:0 0 4px; font-size:var(--text-xl); font-weight:800; letter-spacing:var(--tracking-display); }
.page-head p { margin:0; color:var(--muted); font-size:var(--text-sm); }
.books-brief { display:grid; grid-template-columns:repeat(3, minmax(120px, 1fr)) minmax(260px, 1.15fr); gap:12px; }
.metric, .brief-copy, .panel { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); }
.metric, .brief-copy { padding:14px; }
.metric { display:flex; flex-direction:column; gap:3px; }
.metric b { color:var(--fg); font-family:var(--font-mono); font-size:var(--text-lg); line-height:1.2; word-break:break-word; }
.metric span, .brief-copy span, .panel-head p, .inline-note, .form-panel small { color:var(--muted); font-size:var(--text-xs); }
.brief-copy { display:flex; flex-direction:column; justify-content:center; gap:5px; }
.brief-copy strong { color:var(--fg-2); font-size:var(--text-sm); }
.statebar, .typed-error { display:flex; align-items:flex-start; justify-content:space-between; gap:12px; padding:10px 12px; border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); font-size:var(--text-sm); }
.statebar[data-state="pending"] { border-color:color-mix(in oklab,var(--accent),transparent 55%); }
.statebar[data-state="success"] { border-color:color-mix(in oklab,var(--class-live),transparent 45%); }
.statebar[data-state="error"], .typed-error { border-color:color-mix(in oklab,var(--state-warn),transparent 45%); }
.books-grid { display:grid; grid-template-columns:minmax(0, 1.35fr) minmax(320px, .95fr); gap:12px; align-items:start; }
.panel { padding:14px; display:flex; flex-direction:column; gap:12px; }
.panel-head { display:flex; align-items:flex-start; justify-content:space-between; gap:10px; }
.panel-head h2 { margin:0; font-size:var(--text-sm); font-weight:900; letter-spacing:.04em; text-transform:uppercase; }
.form-panel label { display:flex; flex-direction:column; gap:6px; color:var(--muted); font-size:var(--text-xs); }
.inline-grid { display:grid; grid-template-columns:repeat(2, minmax(0, 1fr)); gap:12px; }
.input, .area, .file-input { width:100%; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface); color:var(--fg); font-size:var(--text-sm); }
.input, .file-input { min-height:36px; padding:0 10px; }
.file-input { padding:6px 10px; }
.area { min-height:320px; padding:10px; resize:vertical; }
.actions { display:flex; align-items:center; gap:10px; flex-wrap:wrap; }
.actions.stack { align-items:stretch; flex-direction:column; }
.tbtn, .act { display:inline-flex; align-items:center; justify-content:center; min-height:34px; padding:6px 10px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface); color:var(--fg-2); font-size:var(--text-xs); font-weight:800; cursor:pointer; text-decoration:none; }
.tbtn:hover:not(:disabled), .act:hover:not(:disabled) { border-color:var(--accent); color:var(--fg); }
.tbtn:disabled, .act:disabled { opacity:.45; cursor:not-allowed; }
.act.primary { border-color:color-mix(in oklab,var(--class-live),transparent 45%); color:var(--class-live); }
.act.muted { color:var(--muted); }
.status-panel { position:sticky; top:0; }
.status-chip { display:inline-flex; align-items:center; gap:6px; min-height:28px; padding:0 10px; border:1px solid var(--border); border-radius:999px; font-size:var(--text-xs); font-weight:800; }
.status-chip[data-state="processing"] { border-color:color-mix(in oklab,var(--accent),transparent 50%); color:var(--accent); }
.status-chip[data-state="done"] { border-color:color-mix(in oklab,var(--class-live),transparent 45%); color:var(--class-live); }
.status-chip[data-state="failed"] { border-color:color-mix(in oklab,var(--state-warn),transparent 45%); color:var(--state-warn); }
.status-chip[data-state="idle"] { color:var(--muted); }
.fields { display:grid; grid-template-columns:110px minmax(0,1fr); gap:6px 12px; margin:0; font-size:var(--text-sm); }
.fields dt { color:var(--muted); }
.fields dd { margin:0; color:var(--fg); word-break:break-word; }
.mono { font-family:var(--font-mono); }
.empty-card { display:flex; flex-direction:column; gap:6px; min-height:180px; justify-content:center; color:var(--muted); }
.empty-card b, .typed-error strong { color:var(--fg); font-size:var(--text-lg); }
.typed-error { flex-direction:column; }
.typed-error p { margin:0; color:var(--muted); }
@media (max-width:1120px) {
  .books-brief, .books-grid, .inline-grid { grid-template-columns:1fr; }
  .status-panel { position:static; }
}
@media (max-width:760px) {
  .page-head, .panel-head, .statebar { flex-direction:column; align-items:stretch; }
}
</style>
