<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useOperatorDocuments } from '../composables/useOperatorDocuments'

const { t } = useI18n()
const {
  projects,
  documents,
  history,
  comments,
  selectedProject,
  selectedPath,
  primaryVersion,
  secondaryVersion,
  activeDocument,
  primaryDocument,
  secondaryDocument,
  documentsState,
  historyState,
  primaryState,
  secondaryState,
  commentsState,
  pending,
  error,
  refresh,
  openDocument,
  selectPrimaryVersion,
  selectSecondaryVersion,
  addComment,
} = useOperatorDocuments()

const notice = ref<{ kind: 'success' | 'error'; text: string } | null>(null)
const commentDraft = ref('')
const lineStartDraft = ref('')
const lineEndDraft = ref('')
const commentBusy = ref(false)

const documentCount = computed(() => documents.length)
const versionCount = computed(() => history.length)
const commentCount = computed(() => comments.length)
const primaryVersionEntry = computed(() => history.find((row) => row.version === primaryVersion.value) || null)
const currentVersionEntry = computed(() => history[0] || null)
const compareSummary = computed(() => {
  if (!primaryDocument.value || !secondaryDocument.value) {
    return t('documents.summary.compareEmpty')
  }
  if (primaryDocument.value.contentHash && primaryDocument.value.contentHash === secondaryDocument.value.contentHash) {
    return t('documents.summary.compareSame')
  }
  return t('documents.summary.compareDifferent', {
    left: primaryDocument.value.version,
    right: secondaryDocument.value.version,
  })
})
const showStatebar = computed(() => Boolean(notice.value || pending.value || error.value || documentsState.value.kind === 'empty'))
const statebarKind = computed(() => {
  if (notice.value) return notice.value.kind
  if (error.value) return 'error'
  if (pending.value) return 'pending'
  return documentsState.value.kind
})

watch(selectedProject, (next, previous) => {
  if (next === previous) return
  notice.value = null
  void refresh()
})

async function onPrimaryChange(event: Event) {
  const value = Number((event.target as HTMLSelectElement | null)?.value || '0')
  if (value > 0) {
    await selectPrimaryVersion(value)
  }
}

async function onSecondaryChange(event: Event) {
  const value = Number((event.target as HTMLSelectElement | null)?.value || '0')
  if (value > 0) {
    await selectSecondaryVersion(value)
  }
}

async function onCommentSubmit() {
  const content = commentDraft.value.trim()
  if (!content || commentBusy.value) return

  const lineStart = lineStartDraft.value.trim() ? Number.parseInt(lineStartDraft.value, 10) : undefined
  const lineEnd = lineEndDraft.value.trim() ? Number.parseInt(lineEndDraft.value, 10) : undefined

  if (lineStartDraft.value.trim() && (!Number.isInteger(lineStart) || Number(lineStart) <= 0)) {
    notice.value = { kind: 'error', text: t('documents.comments.invalidLine') }
    return
  }
  if (lineEndDraft.value.trim() && (!Number.isInteger(lineEnd) || Number(lineEnd) <= 0)) {
    notice.value = { kind: 'error', text: t('documents.comments.invalidLine') }
    return
  }
  if (lineStart !== undefined && lineEnd !== undefined && lineEnd < lineStart) {
    notice.value = { kind: 'error', text: t('documents.comments.invalidRange') }
    return
  }

  commentBusy.value = true
  try {
    const result = await addComment({
      content,
      author: 'operator-console',
      lineStart,
      lineEnd,
    })

    if (result.kind === 'success') {
      commentDraft.value = ''
      lineStartDraft.value = ''
      lineEndDraft.value = ''
      notice.value = {
        kind: 'success',
        text: t('documents.comments.noticeSuccess', { version: currentVersionEntry.value?.version || '—' }),
      }
      return
    }

    notice.value = {
      kind: 'error',
      text: t('documents.comments.noticeError', {
        message: result.error.message || t('documents.comments.unknownError'),
      }),
    }
  } finally {
    commentBusy.value = false
  }
}
</script>

<template>
  <div class="documents-page">
    <header class="page-head">
      <div>
        <h1>{{ t('documents.title') }}</h1>
        <p>{{ t('documents.subtitle') }}</p>
      </div>
      <HonestyBadge cls="live" evidence="/api/documents/*" />
    </header>

    <section class="docs-brief">
      <div class="metric">
        <b>{{ documentCount }}</b>
        <span>{{ t('documents.summary.documents', documentCount, { count: documentCount }) }}</span>
      </div>
      <div class="metric">
        <b>{{ versionCount }}</b>
        <span>{{ t('documents.summary.versions', versionCount, { count: versionCount }) }}</span>
      </div>
      <div class="metric">
        <b>{{ commentCount }}</b>
        <span>{{ t('documents.summary.comments', commentCount, { count: commentCount }) }}</span>
      </div>
      <div class="brief-copy">
        <strong>{{ t('documents.summary.compareTitle') }}</strong>
        <span>{{ compareSummary }}</span>
      </div>
    </section>

    <section class="ops">
      <div class="ops-left">
        <label class="rows">
          <span>{{ t('documents.filters.project') }}</span>
          <select id="documents-project-filter" v-model="selectedProject" class="select" name="documents-project-filter">
            <option v-for="project in projects" :key="project" :value="project">{{ project }}</option>
          </select>
        </label>
        <button class="tbtn" @click="notice = null; refresh()">{{ t('documents.actions.refresh') }}</button>
      </div>
      <div class="ops-right">
        <span class="cnt" v-if="activeDocument">{{ t('documents.actions.selectedPath', { path: activeDocument.path }) }}</span>
      </div>
    </section>

    <section v-if="showStatebar" class="statebar" :data-state="statebarKind">
      <span v-if="notice">{{ notice.text }}</span>
      <span v-else-if="pending">{{ t('documents.state.pending') }}</span>
      <span v-else-if="error">{{ t('documents.state.error', { message: error }) }}</span>
      <span v-else-if="documentsState.kind === 'empty'">{{ t('documents.state.empty') }}</span>
      <button v-if="error" class="tbtn" @click="refresh">{{ t('documents.state.retry') }}</button>
      <button v-else-if="notice" class="tbtn" @click="notice = null">{{ t('common.hide') }}</button>
    </section>

    <div class="docs-workspace">
      <section class="panel docs-list">
        <div class="panel-head">
          <div>
            <h2>{{ t('documents.list.title') }}</h2>
            <p>{{ t('documents.list.count', documentCount, { count: documentCount }) }}</p>
          </div>
        </div>

        <div v-if="documentsState.kind === 'empty'" class="empty" data-testid="documents-empty">
          <b>{{ t('documents.list.emptyTitle') }}</b>
          <span>{{ t('documents.list.emptyBody') }}</span>
        </div>

        <div v-else class="doc-rows">
          <button
            v-for="doc in documents"
            :key="doc.id || `${doc.project}:${doc.path}`"
            type="button"
            class="doc-row"
            :class="{ active: selectedPath === doc.path }"
            @click="notice = null; openDocument(doc)"
          >
            <span class="doc-path">{{ doc.path }}</span>
            <span class="doc-meta">{{ t('documents.list.rowMeta', { version: doc.version, docType: doc.docType, author: doc.author, age: doc.age }) }}</span>
          </button>
        </div>
      </section>

      <section class="docs-center">
        <div class="panel history-panel">
          <div class="panel-head">
            <div>
              <h2>{{ t('documents.history.title') }}</h2>
              <p>{{ t('documents.history.count', versionCount, { count: versionCount }) }}</p>
            </div>
          </div>

          <div v-if="!activeDocument" class="empty">
            <b>{{ t('documents.history.noSelectionTitle') }}</b>
            <span>{{ t('documents.history.noSelectionBody') }}</span>
          </div>
          <div v-else-if="historyState.kind === 'pending'" class="mini-state">{{ t('documents.history.pending') }}</div>
          <div v-else-if="historyState.kind === 'error'" class="mini-state" data-state="error">{{ t('documents.history.error', { message: historyState.error.message }) }}</div>
          <div v-else-if="historyState.kind === 'empty'" class="empty">
            <b>{{ t('documents.history.emptyTitle') }}</b>
            <span>{{ t('documents.history.emptyBody') }}</span>
          </div>
          <div v-else class="history-list">
            <button
              v-for="entry in history"
              :key="entry.id"
              type="button"
              class="version-chip"
              :class="{ active: primaryVersion === entry.version }"
              @click="selectPrimaryVersion(entry.version)"
            >
              <span class="version-chip__title">{{ t('documents.history.versionLabel', { version: entry.version }) }}</span>
              <span class="version-chip__meta">{{ t('documents.history.versionMeta', { author: entry.author, age: entry.age }) }}</span>
            </button>
          </div>
        </div>

        <div class="panel compare-panel">
          <div class="panel-head">
            <div>
              <h2>{{ t('documents.compare.title') }}</h2>
              <p>{{ t('documents.compare.subtitle') }}</p>
            </div>
          </div>

          <div v-if="!activeDocument" class="empty">
            <b>{{ t('documents.compare.noSelectionTitle') }}</b>
            <span>{{ t('documents.compare.noSelectionBody') }}</span>
          </div>
          <template v-else>
            <div class="compare-controls">
              <label class="rows">
                <span>{{ t('documents.compare.leftLabel') }}</span>
                <select id="documents-left-version" class="select" name="documents-left-version" :value="primaryVersion || ''" @change="onPrimaryChange">
                  <option v-for="entry in history" :key="`left-${entry.id}`" :value="entry.version">{{ t('documents.history.versionOption', { version: entry.version, author: entry.author }) }}</option>
                </select>
              </label>
              <label class="rows">
                <span>{{ t('documents.compare.rightLabel') }}</span>
                <select id="documents-right-version" class="select" name="documents-right-version" :value="secondaryVersion || ''" @change="onSecondaryChange">
                  <option v-for="entry in history" :key="`right-${entry.id}`" :value="entry.version">{{ t('documents.history.versionOption', { version: entry.version, author: entry.author }) }}</option>
                </select>
              </label>
            </div>

            <div class="compare-grid">
              <article class="compare-column">
                <header class="compare-column__head">
                  <strong>{{ t('documents.compare.leftTitle') }}</strong>
                  <span v-if="primaryVersionEntry">{{ t('documents.history.versionMeta', { author: primaryVersionEntry.author, age: primaryVersionEntry.age }) }}</span>
                </header>
                <div v-if="primaryState.kind === 'pending'" class="mini-state">{{ t('documents.compare.pending') }}</div>
                <div v-else-if="primaryState.kind === 'error'" class="mini-state" data-state="error">{{ t('documents.compare.error', { message: primaryState.error.message }) }}</div>
                <div v-else-if="!primaryDocument" class="empty compact">
                  <b>{{ t('documents.compare.panelEmptyTitle') }}</b>
                  <span>{{ t('documents.compare.panelEmptyBody') }}</span>
                </div>
                <template v-else>
                  <dl class="compare-meta">
                    <dt>{{ t('documents.compare.metaVersion') }}</dt><dd>{{ primaryDocument.version }}</dd>
                    <dt>{{ t('documents.compare.metaAuthor') }}</dt><dd>{{ primaryDocument.author }}</dd>
                    <dt>{{ t('documents.compare.metaType') }}</dt><dd>{{ primaryDocument.docType }}</dd>
                    <dt>{{ t('documents.compare.metaCreated') }}</dt><dd>{{ primaryDocument.createdAt }}</dd>
                    <dt>{{ t('documents.compare.metaHash') }}</dt><dd class="mono">{{ primaryDocument.contentHash || '—' }}</dd>
                  </dl>
                  <pre class="doc-content">{{ primaryDocument.content }}</pre>
                </template>
              </article>

              <article class="compare-column">
                <header class="compare-column__head">
                  <strong>{{ t('documents.compare.rightTitle') }}</strong>
                  <span v-if="secondaryVersion">{{ t('documents.compare.comparedVersion', { version: secondaryVersion }) }}</span>
                </header>
                <div v-if="secondaryState.kind === 'pending'" class="mini-state">{{ t('documents.compare.pending') }}</div>
                <div v-else-if="secondaryState.kind === 'error'" class="mini-state" data-state="error">{{ t('documents.compare.error', { message: secondaryState.error.message }) }}</div>
                <div v-else-if="!secondaryDocument" class="empty compact">
                  <b>{{ t('documents.compare.panelEmptyTitle') }}</b>
                  <span>{{ t('documents.compare.panelEmptyBody') }}</span>
                </div>
                <template v-else>
                  <dl class="compare-meta">
                    <dt>{{ t('documents.compare.metaVersion') }}</dt><dd>{{ secondaryDocument.version }}</dd>
                    <dt>{{ t('documents.compare.metaAuthor') }}</dt><dd>{{ secondaryDocument.author }}</dd>
                    <dt>{{ t('documents.compare.metaType') }}</dt><dd>{{ secondaryDocument.docType }}</dd>
                    <dt>{{ t('documents.compare.metaCreated') }}</dt><dd>{{ secondaryDocument.createdAt }}</dd>
                    <dt>{{ t('documents.compare.metaHash') }}</dt><dd class="mono">{{ secondaryDocument.contentHash || '—' }}</dd>
                  </dl>
                  <pre class="doc-content">{{ secondaryDocument.content }}</pre>
                </template>
              </article>
            </div>
          </template>
        </div>
      </section>

      <aside class="panel docs-comments">
        <div class="panel-head">
          <div>
            <h2>{{ t('documents.comments.title') }}</h2>
            <p>{{ t('documents.comments.count', commentCount, { count: commentCount }) }}</p>
          </div>
        </div>

        <div v-if="!currentVersionEntry" class="empty">
          <b>{{ t('documents.comments.noSelectionTitle') }}</b>
          <span>{{ t('documents.comments.noSelectionBody') }}</span>
        </div>
        <template v-else>
          <div class="thread-label">{{ t('documents.comments.threadFor', { version: currentVersionEntry.version }) }}</div>

          <form class="comment-form" @submit.prevent="onCommentSubmit">
            <label class="field">
              <span>{{ t('documents.comments.contentLabel') }}</span>
              <textarea v-model="commentDraft" class="textarea" rows="4" :placeholder="t('documents.comments.contentPlaceholder')" />
            </label>
            <div class="line-grid">
              <label class="field compact-field">
                <span>{{ t('documents.comments.lineStart') }}</span>
                <input v-model="lineStartDraft" class="input" inputmode="numeric" :placeholder="t('documents.comments.lineStartPlaceholder')" />
              </label>
              <label class="field compact-field">
                <span>{{ t('documents.comments.lineEnd') }}</span>
                <input v-model="lineEndDraft" class="input" inputmode="numeric" :placeholder="t('documents.comments.lineEndPlaceholder')" />
              </label>
            </div>
            <button class="act primary wide" :disabled="commentBusy || !commentDraft.trim()">
              {{ commentBusy ? t('documents.comments.submitting') : t('documents.comments.submit') }}
            </button>
          </form>

          <div v-if="commentsState.kind === 'pending'" class="mini-state">{{ t('documents.comments.pending') }}</div>
          <div v-else-if="commentsState.kind === 'error'" class="mini-state" data-state="error">{{ t('documents.comments.error', { message: commentsState.error.message }) }}</div>
          <div v-else-if="commentsState.kind === 'empty'" class="empty compact">
            <b>{{ t('documents.comments.emptyTitle') }}</b>
            <span>{{ t('documents.comments.emptyBody') }}</span>
          </div>
          <div v-else class="comment-list">
            <article v-for="comment in comments" :key="comment.id" class="comment-card">
              <div class="comment-head">
                <strong>{{ comment.author }}</strong>
                <span>{{ comment.age }}</span>
              </div>
              <div class="comment-meta">
                <span>{{ t('documents.comments.statusLabel', { status: comment.status }) }}</span>
                <span v-if="comment.lineStart">{{ t('documents.comments.lineRange', { start: comment.lineStart, end: comment.lineEnd || comment.lineStart }) }}</span>
              </div>
              <p>{{ comment.content }}</p>
            </article>
          </div>
        </template>
      </aside>
    </div>
  </div>
</template>

<style scoped>
.documents-page {
  display: grid;
  gap: 18px;
}

.page-head,
.ops,
.panel-head,
.compare-column__head,
.comment-head,
.comment-meta {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.page-head h1,
.panel-head h2 {
  margin: 0;
}

.page-head p,
.panel-head p,
.brief-copy span,
.empty span,
.mini-state,
.doc-meta,
.version-chip__meta,
.thread-label,
.comment-meta,
.compare-column__head span {
  color: var(--muted);
}

.docs-brief {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 12px;
}

.metric,
.brief-copy,
.panel,
.statebar {
  border: 1px solid var(--border);
  border-radius: 16px;
  background: rgba(255, 255, 255, 0.02);
}

.metric,
.brief-copy {
  padding: 14px 16px;
  display: grid;
  gap: 6px;
}

.metric b,
.brief-copy strong {
  font-size: 20px;
}

.ops {
  flex-wrap: wrap;
}

.ops-left,
.ops-right,
.compare-controls,
.line-grid {
  display: flex;
  align-items: center;
  gap: 12px;
}

.rows,
.field {
  display: grid;
  gap: 6px;
}

.rows span,
.field span {
  font-size: 12px;
  color: var(--muted);
}

.select,
.input,
.textarea {
  width: 100%;
  border-radius: 12px;
  border: 1px solid var(--border);
  background: rgba(255, 255, 255, 0.02);
  color: var(--fg);
  padding: 10px 12px;
}

.textarea {
  resize: vertical;
  min-height: 96px;
}

.tbtn,
.act {
  border-radius: 999px;
  border: 1px solid var(--border);
  background: transparent;
  color: var(--fg);
  padding: 9px 14px;
  cursor: pointer;
}

.act.primary {
  background: var(--accent);
  border-color: var(--accent);
  color: #08121f;
}

.act.wide {
  width: 100%;
  justify-content: center;
}

.tbtn:disabled,
.act:disabled {
  opacity: 0.55;
  cursor: not-allowed;
}

.statebar {
  padding: 12px 14px;
}

.statebar[data-state='pending'] {
  border-color: color-mix(in oklab, var(--accent), transparent 55%);
}

.statebar[data-state='error'] {
  border-color: color-mix(in oklab, var(--state-warn), transparent 45%);
  color: var(--state-warn);
}
.statebar[data-state='empty'] {
  color: var(--muted);
}

.statebar[data-state='success'] {
  border-color: color-mix(in oklab, var(--class-live), transparent 45%);
}
.docs-workspace {
  display: grid;
  grid-template-columns: minmax(240px, 0.95fr) minmax(420px, 1.5fr) minmax(300px, 1fr);
  gap: 16px;
  align-items: start;
}

.docs-center {
  display: grid;
  gap: 16px;
}

.panel {
  padding: 16px;
  display: grid;
  gap: 14px;
}

.doc-rows,
.history-list,
.comment-list {
  display: grid;
  gap: 10px;
}

.doc-row,
.version-chip {
  width: 100%;
  text-align: left;
  border: 1px solid var(--border);
  border-radius: 14px;
  background: transparent;
  padding: 12px;
  display: grid;
  gap: 6px;
  cursor: pointer;
}

.doc-row.active,
.version-chip.active {
  border-color: rgba(107, 180, 255, 0.55);
  background: rgba(71, 141, 255, 0.08);
}

.doc-path,
.version-chip__title,
.thread-label,
.comment-head strong {
  font-weight: 600;
}

.compare-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 14px;
}

.compare-column {
  border: 1px solid var(--border);
  border-radius: 14px;
  padding: 14px;
  display: grid;
  gap: 12px;
  min-height: 360px;
}

.compare-meta {
  margin: 0;
  display: grid;
  grid-template-columns: auto 1fr;
  gap: 6px 10px;
  font-size: 12px;
}

.compare-meta dt {
  color: var(--muted);
}

.compare-meta dd {
  margin: 0;
}

.doc-content {
  margin: 0;
  white-space: pre-wrap;
  overflow: auto;
  font-size: 12px;
  line-height: 1.5;
  border-radius: 12px;
  background: rgba(255, 255, 255, 0.03);
  padding: 12px;
}

.mono {
  font-family: ui-monospace, SFMono-Regular, SFMono-Regular, Menlo, Monaco, Consolas, Liberation Mono, monospace;
  word-break: break-all;
}

.comment-form {
  display: grid;
  gap: 12px;
}

.compact-field {
  min-width: 0;
  flex: 1;
}

.comment-card {
  border: 1px solid var(--border);
  border-radius: 14px;
  padding: 12px;
  display: grid;
  gap: 8px;
}

.comment-card p {
  margin: 0;
  white-space: pre-wrap;
}

.empty,
.mini-state {
  border: 1px dashed var(--border);
  border-radius: 14px;
  padding: 14px;
  display: grid;
  gap: 6px;
}

.mini-state[data-state='error'] {
  border-color: color-mix(in oklab, var(--state-warn), transparent 45%);
  color: var(--state-warn);
}

.empty.compact {
  min-height: 120px;
}

@media (max-width: 1400px) {
  .docs-workspace {
    grid-template-columns: 1fr;
  }

  .compare-grid,
  .docs-brief {
    grid-template-columns: 1fr;
  }
}
</style>
