<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useOperatorProjectContext, projectName } from '../composables/useOperatorProjectContext'
import { useOperatorDocuments, type DocumentExportArtifact } from '../composables/useOperatorDocuments'
import { operatorApiUrl, operatorDiagnosisKey } from '../composables/useOperatorApi'
const { t, locale } = useI18n()
const route = useRoute()
const router = useRouter()
const context = useOperatorProjectContext()
const { project } = context
const docs = useOperatorDocuments(project)
const { documents, history, comments, primaryVersion, secondaryVersion, primaryDocument, secondaryDocument, documentsState, historyState, primaryState, secondaryState, commentsState, total, hasMore, refresh, openDocument, selectPrimaryVersion, selectSecondaryVersion, clearDocumentDetail } = docs
const query = computed(() => typeof route.query.q === 'string' ? route.query.q : '')
const prefix = computed(() => typeof route.query.pathPrefix === 'string' ? route.query.pathPrefix : typeof route.query.path_prefix === 'string' ? route.query.path_prefix : '')
const requestedPath = computed(() => typeof route.query.path === 'string' ? route.query.path : '')
const documentProject = computed(() => project.value === 'all' && typeof route.query.docProject === 'string' ? route.query.docProject : project.value)
const page = computed(() => Math.max(1, Number.parseInt(String(route.query.page || '1'), 10) || 1))
const pageSize = computed(() => [10, 20, 50].includes(Number(route.query.size)) ? Number(route.query.size) : 20)
const draft = ref(query.value)
const prefixDraft = ref(prefix.value)
const typeDraft = ref(typeof route.query.docType === 'string' ? route.query.docType : '')
const compareOpen = ref(false)
const commentDraft = ref('')
const lineStart = ref('')
const lineEnd = ref('')
const actionError = ref('')
const busy = ref(false)
const exportArtifact = ref<DocumentExportArtifact | null>(null)
const mutationResult = ref<Awaited<ReturnType<typeof docs.addComment>> | Awaited<ReturnType<typeof docs.runDocumentSelectionOperation>>['mutation'] | null>(null)
const detailHeading = ref<HTMLElement | null>(null)
let generation = 0
let exportGeneration = 0
watch([project, documentProject, requestedPath, primaryVersion, () => primaryDocument.value?.id, () => primaryState.value.kind], () => {
  exportGeneration += 1
  exportArtifact.value = null
}, { flush: 'sync' })
const filtered = computed(() => documents)
const pageCount = computed(() => Math.max(1, Math.ceil((total.value || 0) / pageSize.value)))
const pageRows = filtered

function updateQuery(values: Record<string, string | undefined>) {
  return router.push({ path: '/documents', query: { ...route.query, project: project.value || undefined, ...values } })
}
watch(query, (value) => { draft.value = value })
watch(prefix, (value) => { prefixDraft.value = value })
watch(() => route.query.docType, (value) => { typeDraft.value = typeof value === 'string' ? value : '' })
watch([project, prefix, query, page, pageSize, () => route.query.docType, () => route.query.allRead], () => {
  actionError.value = ''
  mutationResult.value = null
  void refresh()
}, { immediate: true, flush: 'sync' })

async function readDetail() {
  const path = requestedPath.value
  const scope = project.value
  const current = ++generation
  clearDocumentDetail()
  compareOpen.value = false
  exportArtifact.value = null
  const targetProject = documentProject.value
  if (!path || !scope || !targetProject || targetProject === 'all') return
  await nextTick()
  if (current !== generation) return
  detailHeading.value?.focus()
  detailHeading.value?.scrollIntoView({ block: 'start' })
  await openDocument({ id: '', path, project: targetProject, docType: '', author: '', createdAt: '', age: '', version: 0 })
  if (current !== generation || requestedPath.value !== path || project.value !== scope) return
  const version = Number(route.query.version)
  if (Number.isSafeInteger(version) && version > 0) await selectPrimaryVersion(version)
  if (current !== generation) return
  const comparison = Number(route.query.compare)
  if (Number.isSafeInteger(comparison) && comparison > 0) { compareOpen.value = true; await selectSecondaryVersion(comparison) }
  await nextTick()
  detailHeading.value?.focus()
}
watch([requestedPath, documentProject], () => { commentDraft.value = ''; lineStart.value = ''; lineEnd.value = ''; actionError.value = ''; void readDetail() }, { immediate: true, flush: 'post' })
watch(() => route.query.version, (value) => { const version = Number(value); if (Number.isSafeInteger(version) && version > 0) void selectPrimaryVersion(version) })
watch(() => route.query.compare, (value) => {
  const version = Number(value)
  compareOpen.value = Number.isSafeInteger(version) && version > 0
  if (compareOpen.value) void selectSecondaryVersion(version)
})
watch(requestedPath, async (value, previous) => {
  if (value || !previous) return
  await nextTick()
  for (const button of document.querySelectorAll<HTMLElement>('[data-document-path]')) if (button.dataset.documentPath === previous) { button.focus(); break }
})
onBeforeUnmount(() => { generation += 1; clearDocumentDetail() })

async function closeDetail() {
  const path = requestedPath.value
  await updateQuery({ path: undefined, docProject: undefined, version: undefined, compare: undefined })
  await nextTick()
  const buttons = document.querySelectorAll<HTMLElement>('[data-document-path]')
  for (const button of buttons) if (button.dataset.documentPath === path) { button.focus(); break }
}

async function compare() {
  const version = history.find((entry) => entry.version !== primaryVersion.value)?.version
  await updateQuery({ compare: compareOpen.value ? undefined : version ? String(version) : undefined })
}

async function comment() {
  const content = commentDraft.value.trim()
  if (!content || busy.value || !primaryDocument.value) return
  const start = lineStart.value ? Number(lineStart.value) : undefined
  const end = lineEnd.value ? Number(lineEnd.value) : undefined
  if ((start !== undefined && (!Number.isSafeInteger(start) || start <= 0)) || (end !== undefined && (!Number.isSafeInteger(end) || end <= 0)) || (start !== undefined && end !== undefined && end < start)) { actionError.value = t('documents.comments.invalidRange'); return }
  const scope = project.value
  const path = requestedPath.value
  const version = primaryVersion.value
  busy.value = true
  try {
    const result = await docs.addComment({ content, author: 'operator-console', lineStart: start, lineEnd: end })
    if (project.value !== scope || requestedPath.value !== path || primaryVersion.value !== version) return
    mutationResult.value = result
    if (result.kind === 'committed_verified') { commentDraft.value = ''; lineStart.value = ''; lineEnd.value = ''; if (version) await selectPrimaryVersion(version) }
  } catch { if (project.value === scope && requestedPath.value === path) actionError.value = t('knowledge.actionFailed') }
  finally { busy.value = false }
}

async function exportDocument() {
  const doc = primaryDocument.value
  if (!doc || busy.value) return
  const attempt = ++exportGeneration
  exportArtifact.value = null
  busy.value = true
  const scope = documentProject.value
  const path = requestedPath.value
  try {
    const result = await docs.runDocumentSelectionOperation({ action: 'export', target: { documentId: Number(doc.id), version: doc.version } })
    if (attempt !== exportGeneration || documentProject.value !== scope || requestedPath.value !== path || primaryDocument.value?.id !== doc.id || primaryDocument.value?.version !== doc.version) return
    mutationResult.value = result.mutation
    exportArtifact.value = result.mutation.kind === 'committed_verified' ? result.artifact || null : null
  } catch { if (attempt === exportGeneration && documentProject.value === scope && requestedPath.value === path) actionError.value = t('documents.actions.exportUnavailable') }
  finally { busy.value = false }
}
async function refreshView() {
  await refresh()
  if (requestedPath.value) await readDetail()
}

function time(value?: string) {
  return value && !Number.isNaN(Date.parse(value)) ? new Intl.DateTimeFormat(locale.value, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value)) : t('knowledge.unknown')
}
</script>

<template>
  <div class="documents-page" @keydown.esc="requestedPath && closeDetail()">
    <header class="page-head"><div><h1>{{ t('nav.items.documents') }}</h1><p>{{ t('knowledge.documentsLead') }}</p></div><button class="btn" :disabled="documentsState.kind === 'pending'" @click="refreshView">{{ t('knowledge.refresh') }}</button></header>
    <div v-if="!project" class="empty"><h2>{{ t('knowledge.chooseProject') }}</h2><p>{{ t('knowledge.chooseProjectHelp') }}</p><RouterLink :to="context.to('/projects')">{{ t('nav.items.projects') }}</RouterLink></div>
    <template v-else>
      <form class="toolbar" @submit.prevent="updateQuery({ q: draft.trim() || undefined, pathPrefix: prefixDraft.trim() || undefined, docType: typeDraft.trim() || undefined, path_prefix: undefined, page: '1', path: undefined, version: undefined, compare: undefined, allRead: project === 'all' ? '1' : undefined })">
        <label class="search-field"><span>{{ t('knowledge.searchDocuments') }}</span><input v-model="draft" type="search" /></label>
        <button class="btn primary" type="submit">{{ t('knowledge.find') }}</button>
        <details><summary>{{ t('knowledge.filters') }}</summary><label><span>{{ t('knowledge.pathPrefix') }}</span><input v-model="prefixDraft" /></label><label><span>{{ t('knowledge.docType') }}</span><input v-model="typeDraft" /></label></details>
      </form>
      <div v-if="documentsState.kind === 'pending'" class="state" role="status">{{ t('knowledge.loading') }}</div>
      <div v-else-if="documentsState.kind === 'error'" class="state error" role="status"><p>{{ t(operatorDiagnosisKey(documentsState.error)) }}</p><button class="btn" @click="refresh">{{ t('knowledge.retry') }}</button><details><summary>{{ t('knowledge.technical') }}</summary>{{ documentsState.error.message }}</details></div>
      <p v-if="project === 'all'" class="state">{{ t('knowledge.allOrder') }}</p>
      <p v-if="documentsState.kind === 'gated'" class="state" role="status">{{ t('knowledge.allReadHelp') }}</p>
      <div class="docs-workspace" :class="{ 'detail-open': requestedPath }">
        <section class="docs-list" :aria-label="t('knowledge.documentResults')">
          <div class="list-head"><span role="status">{{ total === null ? t('knowledge.unknown') : t('knowledge.matches', { count: total }) }}</span><label><span>{{ t('knowledge.perPage') }}</span><select :value="pageSize" @change="updateQuery({ size: ($event.target as HTMLSelectElement).value, page: '1', path: undefined })"><option :value="10">10</option><option :value="20">20</option><option :value="50">50</option></select></label></div>
          <button v-for="doc in pageRows" :key="`${doc.project}:${doc.path}`" class="doc-row" :class="{ active: requestedPath === doc.path && documentProject === doc.project }" :aria-pressed="requestedPath === doc.path && documentProject === doc.project" :data-document-path="doc.path" @click="updateQuery({ path: doc.path, docProject: project === 'all' ? doc.project : undefined, version: undefined, compare: undefined })"><strong>{{ doc.path }}</strong><span>{{ t('knowledge.version') }} {{ doc.version }} · {{ doc.author || t('knowledge.notRecorded') }} · {{ time(doc.createdAt) }}<template v-if="project === 'all'"> · {{ projectName(doc.project) }}</template></span></button>
          <div v-if="documentsState.kind === 'empty'" class="empty" data-testid="documents-empty">
            <h2>{{ t(total && page > 1 ? 'knowledge.pageEmpty' : query || prefix || typeDraft ? 'knowledge.noMatches' : 'knowledge.noDocuments') }}</h2><p>{{ t(query || prefix || typeDraft ? 'knowledge.noDocumentMatchesHelp' : 'knowledge.noDocumentsHelp') }}</p>
            <button v-if="total && page > 1" class="btn" @click="updateQuery({ page: '1', path: undefined })">{{ t('knowledge.firstPage') }}</button>
            <button v-else-if="query || prefix || typeDraft" class="btn" @click="draft = ''; prefixDraft = ''; typeDraft = ''; updateQuery({ q: undefined, docType: undefined, pathPrefix: undefined, path_prefix: undefined, page: '1', path: undefined })">{{ t('knowledge.clearFilters') }}</button>
            <RouterLink v-else :to="context.to('/memory')">{{ t('knowledge.openMemory') }}</RouterLink>
          </div>
          <nav class="pagination" :aria-label="t('knowledge.pagination')"><button class="btn" :disabled="documentsState.kind === 'pending' || page <= 1" @click="updateQuery({ page: String(page - 1), path: undefined })">{{ t('knowledge.previous') }}</button><span>{{ total === null ? t('knowledge.unknown') : t('knowledge.pageOf', { page, total: pageCount }) }}</span><button class="btn" :disabled="documentsState.kind === 'pending' || !hasMore" @click="updateQuery({ page: String(page + 1), path: undefined })">{{ t('knowledge.next') }}</button></nav>
        </section>
        <article v-if="requestedPath" class="reader" aria-labelledby="document-reader-heading">
          <button class="btn back" @click="closeDetail">{{ t('knowledge.backResults') }}</button>
          <h2 id="document-reader-heading" ref="detailHeading" tabindex="-1">{{ requestedPath }}</h2>
          <p v-if="documentProject === 'all'">{{ t('knowledge.chooseRecordProject') }}</p>
          <p v-else-if="primaryState.kind === 'pending'" role="status">{{ t('knowledge.loading') }}</p>
          <div v-else-if="primaryState.kind === 'error'" class="state error" role="status"><p>{{ t(operatorDiagnosisKey(primaryState.error)) }}</p><button class="btn" @click="readDetail">{{ t('knowledge.retry') }}</button><details><summary>{{ t('knowledge.technical') }}</summary>{{ primaryState.error.message }}</details></div>
          <template v-else-if="primaryDocument">
            <p class="currentness">{{ t('knowledge.version') }} {{ primaryDocument.version }} · {{ history[0] ? t(primaryDocument.version === history[0].version ? 'knowledge.currentVersion' : 'knowledge.historicalVersion') : t('knowledge.currentnessUnknown') }}</p>
            <pre class="document-content" data-testid="document-content">{{ primaryDocument.content }}</pre>
            <section><h3>{{ t('knowledge.origin') }}</h3><dl><dt>{{ t('knowledge.project') }}</dt><dd>{{ projectName(documentProject) }}</dd><dt>{{ t('knowledge.author') }}</dt><dd>{{ primaryDocument.author || t('knowledge.notRecorded') }}</dd><dt>{{ t('knowledge.created') }}</dt><dd>{{ time(primaryDocument.createdAt) }}</dd><dt>{{ t('documents.compare.metaType') }}</dt><dd>{{ primaryDocument.docType }}</dd></dl><RouterLink :to="context.to('/memory')">{{ t('knowledge.openMemory') }}</RouterLink></section>
          </template>
          <section v-if="documentProject && documentProject !== 'all'" class="history">
            <h3>{{ t('knowledge.history') }}</h3>
            <p v-if="historyState.kind === 'pending'" role="status">{{ t('knowledge.loading') }}</p>
            <div v-else-if="historyState.kind === 'error'" class="state error"><p>{{ t(operatorDiagnosisKey(historyState.error)) }}</p><button class="btn" @click="readDetail">{{ t('knowledge.retry') }}</button></div>
            <p v-else-if="!history.length">{{ t('knowledge.noDocumentHistory') }}</p>
            <ol v-else><li v-for="entry in history" :key="entry.id"><button :aria-pressed="primaryVersion === entry.version" @click="updateQuery({ version: String(entry.version) })"><strong>{{ t('knowledge.version') }} {{ entry.version }}</strong><span>{{ entry.author || t('knowledge.notRecorded') }} · {{ time(entry.createdAt) }}</span><span v-if="entry.version === history[0].version">{{ t('knowledge.currentVersion') }}</span></button></li></ol>
            <button v-if="history.length > 1" class="btn" :aria-expanded="compareOpen" @click="compare">{{ t('knowledge.compareVersions') }}</button>
            <div v-if="compareOpen" class="comparison">
              <label><span>{{ t('knowledge.compareWith') }}</span><select :value="secondaryVersion || ''" @change="updateQuery({ compare: ($event.target as HTMLSelectElement).value })"><option v-for="entry in history" :key="entry.id" :value="entry.version">{{ t('knowledge.version') }} {{ entry.version }}</option></select></label>
              <p v-if="secondaryState.kind === 'pending'" role="status">{{ t('knowledge.loading') }}</p>
              <div v-else-if="secondaryState.kind === 'error'" class="state error"><p>{{ t(operatorDiagnosisKey(secondaryState.error)) }}</p><button class="btn" @click="selectSecondaryVersion(secondaryVersion || history[0].version)">{{ t('knowledge.retry') }}</button></div>
              <template v-else-if="secondaryDocument"><p v-if="primaryDocument">{{ t(primaryDocument.contentHash === secondaryDocument.contentHash ? 'documents.summary.compareSame' : 'documents.summary.compareDifferent', { left: primaryDocument.version, right: secondaryDocument.version }) }}</p><pre class="document-content">{{ secondaryDocument.content }}</pre></template>
            </div>
          </section>
          <template v-if="primaryDocument">
            <details class="technical"><summary>{{ t('knowledge.technical') }}</summary><dl><dt>ID</dt><dd>{{ primaryDocument.id }}</dd><dt>{{ t('knowledge.projectIdentity') }}</dt><dd>{{ documentProject }}</dd><dt>{{ t('documents.compare.metaHash') }}</dt><dd><code>{{ primaryDocument.contentHash }}</code></dd></dl><pre class="metadata">{{ primaryDocument.metadata }}</pre></details>
            <section class="comments"><h3>{{ t('documents.comments.title') }}</h3><p>{{ t('documents.comments.threadFor', { version: primaryVersion }) }}</p>
              <p v-if="commentsState.kind === 'pending'" role="status">{{ t('knowledge.loading') }}</p><div v-else-if="commentsState.kind === 'error'" class="state error"><p>{{ t(operatorDiagnosisKey(commentsState.error)) }}</p><button class="btn" @click="selectPrimaryVersion(primaryDocument.version)">{{ t('knowledge.retry') }}</button></div><p v-else-if="!comments.length">{{ t('documents.comments.emptyBody') }}</p>
              <ol v-else><li v-for="entry in comments" :key="entry.id"><strong>{{ entry.author || t('knowledge.notRecorded') }}</strong><time>{{ time(entry.createdAt) }}</time><p>{{ entry.content }}</p><span v-if="entry.lineStart">{{ t('documents.comments.lineRange', { start: entry.lineStart, end: entry.lineEnd || entry.lineStart }) }}</span></li></ol>
              <form v-if="history.some((entry) => entry.version === primaryVersion)" class="comment-form" @submit.prevent="comment"><label><span>{{ t('documents.comments.contentLabel') }}</span><textarea v-model="commentDraft" rows="3" /></label><details><summary>{{ t('knowledge.commentLines') }}</summary><div class="line-range"><label><span>{{ t('documents.comments.lineStart') }}</span><input v-model="lineStart" type="number" min="1" step="1" /></label><label><span>{{ t('documents.comments.lineEnd') }}</span><input v-model="lineEnd" type="number" min="1" step="1" /></label></div></details><button class="btn" :disabled="busy || !commentDraft.trim()">{{ t('documents.comments.submit') }}</button></form>
            </section>
            <details><summary>{{ t('knowledge.documentActions') }}</summary><button class="btn" :disabled="busy" @click="exportDocument" data-testid="document-export">{{ t('documents.actions.export') }}</button><a v-if="exportArtifact && mutationResult?.kind === 'committed_verified'" class="btn" :href="operatorApiUrl(exportArtifact.downloadUrl)" :download="exportArtifact.filename" data-testid="document-export-download">{{ t('documents.actions.download') }}</a></details>
          </template>
        </article>
      </div>
      <p v-if="actionError" class="state error" role="status">{{ actionError }}</p><MutationResultNotice :result="mutationResult" :recheck-label="t('knowledge.refresh')" @recheck="refreshView" />
    </template>
  </div>
</template>

<style scoped>
.documents-page { display:grid; gap:20px; min-width:0; }
.page-head { display:flex; align-items:start; justify-content:space-between; gap:16px; } h1 { font-size:var(--text-xl); margin:0 0 6px; } h2 { font-size:var(--text-lg); margin:0 0 16px; overflow-wrap:anywhere; } h3 { font-size:var(--text-base); margin:0 0 12px; }
p { color:var(--muted); margin:0 0 12px; }
.toolbar { display:flex; gap:12px; align-items:end; flex-wrap:wrap; } label { display:grid; gap:6px; color:var(--muted); font-size:var(--text-sm); } .search-field { flex:1; min-width:180px; }
input, select, textarea { min-height:40px; padding:8px 10px; color:var(--fg); background:var(--surface); border:1px solid var(--border); border-radius:var(--r-sm); max-width:100%; } textarea { width:100%; resize:vertical; }
.btn { display:inline-flex; align-items:center; justify-content:center; min-height:40px; padding:8px 12px; color:var(--fg); background:var(--surface); border:1px solid var(--border); border-radius:var(--r-sm); cursor:pointer; }
.btn:hover:not(:disabled), .doc-row:hover { background:var(--surface-warm); } .btn:disabled { opacity:.5; cursor:not-allowed; } .btn.primary { color:var(--accent-on); background:var(--accent); }
.state { padding:14px; border:1px solid var(--border); border-radius:var(--r-sm); display:flex; flex-wrap:wrap; align-items:center; gap:12px; color:var(--muted); } .state.error { color:var(--state-warn); }
.docs-workspace { display:grid; gap:24px; min-width:0; } .docs-workspace.detail-open { grid-template-columns:minmax(0,.75fr) minmax(0,1.4fr); align-items:start; }
.docs-list { min-width:0; border:1px solid var(--border); border-radius:var(--r-md); overflow:hidden; } .list-head { padding:12px; color:var(--muted); border-bottom:1px solid var(--border); font-size:var(--text-sm); }
.doc-row { width:100%; display:grid; gap:8px; text-align:left; padding:16px; color:var(--fg); border:0; border-bottom:1px solid var(--border-soft); background:transparent; cursor:pointer; } .doc-row strong { font-size:var(--text-base); font-weight:500; overflow-wrap:anywhere; } .doc-row span { color:var(--muted); font-size:var(--text-sm); overflow-wrap:anywhere; } .doc-row.active { background:var(--surface-warm); }
.pagination { display:flex; flex-wrap:wrap; align-items:center; justify-content:space-between; gap:8px; padding:12px; font-size:var(--text-sm); }
.reader { min-width:0; padding:20px; background:var(--surface); border:1px solid var(--border); border-radius:var(--r-md); } .back { margin-bottom:20px; }
.document-content { margin:0; white-space:pre-wrap; overflow-wrap:anywhere; max-width:75ch; font-family:var(--font-body); font-size:var(--text-base); line-height:1.65; }
.reader > section, .reader > details { margin-top:28px; padding-top:18px; border-top:1px solid var(--border); } .currentness { color:var(--fg-2); }
dl { display:grid; grid-template-columns:120px minmax(0,1fr); gap:8px 12px; margin:0 0 16px; font-size:var(--text-sm); } dt { color:var(--muted); } dd { margin:0; overflow-wrap:anywhere; }
ol { margin:0 0 16px; padding:0; list-style:none; } .history li button { display:flex; flex-wrap:wrap; gap:8px 16px; width:100%; padding:12px 0; background:transparent; color:var(--fg); text-align:left; border:0; border-bottom:1px solid var(--border-soft); cursor:pointer; } .history li button[aria-pressed=true] { color:var(--accent); } .history li span { color:var(--muted); font-size:var(--text-sm); }
.comparison { display:grid; gap:16px; padding-top:16px; } summary { min-height:40px; padding:8px 0; cursor:pointer; color:var(--fg-2); } .technical dl { margin-top:12px; } .metadata { white-space:pre-wrap; overflow-wrap:anywhere; font-size:var(--text-sm); }
.comments li { display:grid; gap:6px; border-bottom:1px solid var(--border-soft); padding:12px 0; } .comments li p { white-space:pre-wrap; overflow-wrap:anywhere; color:var(--fg); } .comments li time, .comments li > span { color:var(--muted); font-size:var(--text-sm); } .comment-form { display:grid; gap:12px; } .comment-form .btn { justify-self:start; } .line-range { display:flex; gap:12px; } .line-range label { min-width:0; flex:1; }
.empty { padding:32px 20px; }
.documents-page :is(section, article, form, label, details, input, select, textarea) { min-width:0; }
.reader h2 { scroll-margin-top:20px; }
@media(max-width:760px) { .docs-workspace.detail-open { grid-template-columns:1fr; } .detail-open .docs-list { display:none; } .reader { padding:16px; } .toolbar { align-items:stretch; } .search-field { flex-basis:100%; } .page-head { flex-wrap:wrap; } input, select, .btn { min-height:44px; } dl { grid-template-columns:90px minmax(0,1fr); } }
</style>
