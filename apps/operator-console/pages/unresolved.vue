<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useOperatorProjectContext } from '../composables/useOperatorProjectContext'
import { useOperatorUnresolved } from '../composables/useOperatorUnresolved'
import { operatorDiagnosisKey } from '../composables/useOperatorApi'
const { t } = useI18n()
const route = useRoute()
const context = useOperatorProjectContext()
const { unresolved, pending: catalogPending, error: catalogError } = context
const reader = useOperatorUnresolved(context)
const { recordMode, records, recordsPending, recordsError, reference, retained, availableKinds, kind, metadataOnly, unsupportedKind, q, id, selected, historyID, content, history, pending, historyPending, error, historyError, returnTo, originalLocation } = reader
const query = ref(q.value)
watch(q, (value) => { query.value = value })
const groupMode = ref<'readers' | 'metadata'>('readers')
const groupSearch = ref('')
const groupPage = ref(1)
const groupPageSize = 20
const readerGroupCount = computed(() => unresolved.value.filter(row => row.reader_kinds.length).length)
const metadataGroupCount = computed(() => unresolved.value.length - readerGroupCount.value)
const matchingGroups = computed(() => {
  const needle = groupSearch.value.trim().toLowerCase()
  return unresolved.value.filter(row => {
    if (Boolean(row.reader_kinds.length) !== (groupMode.value === 'readers')) return false
    return !needle || row.name.toLowerCase().includes(needle) || row.reason.toLowerCase().includes(needle) || row.reader_kinds.some(choice => choice.includes(needle) || t(choice === 'code' ? 'nav.items.code' : `projectCatalog.${choice}`).toLowerCase().includes(needle))
  })
})
const groupPageCount = computed(() => Math.max(1, Math.ceil(matchingGroups.value.length / groupPageSize)))
const groupRows = computed(() => matchingGroups.value.slice((groupPage.value - 1) * groupPageSize, groupPage.value * groupPageSize))
watch([groupMode, groupSearch], () => { groupPage.value = 1 }, { flush: 'sync' })
watch(groupPageCount, count => { groupPage.value = Math.min(groupPage.value, count) }, { flush: 'sync' })
function changeKind(event: Event) {
  if (event.target instanceof HTMLSelectElement) void reader.update({ kind: event.target.value, cursor: undefined, historyCursor: undefined, id: undefined, memory: undefined, path: undefined, version: undefined, compare: undefined, ...(recordMode.value ? { reference: undefined } : {}) })
}
</script>

<template>
  <div class="unresolved-page">
    <header><h1>{{ t(recordMode ? 'projectCatalog.savedRecords' : 'projectCatalog.unresolvedTitle') }}</h1><RouterLink :to="returnTo" data-testid="unresolved-back">{{ t('projectCatalog.back') }}</RouterLink> · <RouterLink to="/projects">{{ t('knowledge.chooseProject') }}</RouterLink></header>
    <nav class="views" :aria-label="t('projectCatalog.historicalViews')">
      <RouterLink :to="{ path: '/projects/unresolved', query: { view: 'retained_records', attribution_scope: 'unresolved_history', kind: 'memory', returnTo } }" :aria-current="recordMode ? 'page' : undefined">{{ t('projectCatalog.findRecords') }}</RouterLink>
      <RouterLink :to="{ path: '/projects/unresolved', query: { returnTo } }" :aria-current="!recordMode ? 'page' : undefined">{{ t('projectCatalog.groupDiscovery') }}</RouterLink>
    </nav>
    <section v-if="recordMode && !reference" class="record-discovery" :aria-label="t('projectCatalog.savedRecords')" :aria-busy="recordsPending">
      <form class="filters" @submit.prevent="reader.update({ q: query, cursor: undefined })">
        <label><span>{{ t('projectCatalog.kind') }}</span><select :value="kind" @change="changeKind"><option v-for="choice in ['memory', 'document', 'issue']" :key="choice" :value="choice">{{ t(`projectCatalog.${choice}`) }}</option></select></label>
        <label class="record-search"><span>{{ t('projectCatalog.searchSaved') }}</span><input v-model="query" type="search" /></label><button class="btn" type="submit">{{ t('projectCatalog.search') }}</button>
      </form>
      <p class="scope-note">{{ t('projectCatalog.historicalNote') }}</p>
      <p v-if="recordsPending" role="status">{{ t('knowledge.loading') }}</p>
      <div v-else-if="recordsError" role="alert"><p>{{ t('projectCatalog.queryError') }} {{ t(operatorDiagnosisKey(recordsError)) }}</p><button class="btn" @click="reader.retry">{{ t('knowledge.retry') }}</button><button v-if="route.query.cursor" class="btn" @click="reader.update({ cursor: undefined })">{{ t('projectCatalog.firstPage') }}</button></div>
      <template v-else-if="records">
        <p v-if="!records.items.length" role="status">{{ t(records.nextCursor ? 'projectCatalog.moreWork' : 'projectCatalog.exhausted') }}</p>
        <article v-for="item in records.items" :key="`${item.reference}-${item.id}`" class="record-result">
          <div class="object-actions"><h2>{{ item.title }}</h2><button class="btn" @click="reader.open(item)">{{ t('projectCatalog.readRecord') }}</button></div>
          <p v-if="item.fields.some(field => field.label === 'version')">{{ t('knowledge.version') }} {{ item.fields.find(field => field.label === 'version')?.value }}</p>
          <div v-if="item.content" class="body excerpt">{{ item.content }}</div>
          <details><summary>{{ t('projectCatalog.provenance') }}</summary><code>{{ item.reference }}</code><p>{{ t('projectCatalog.historicalNote') }}</p><dl><template v-for="field in item.fields" :key="field.label"><dt>{{ field.label }}</dt><dd>{{ field.value }}</dd></template></dl></details>
        </article>
        <p v-if="records.items.length && records.nextCursor" class="scope-note">{{ t('projectCatalog.moreWork') }}</p>
        <p v-else-if="records.items.length" class="scope-note">{{ t('projectCatalog.traversalEnd') }}</p>
        <nav class="paging" :aria-label="t('projectCatalog.recordPages')"><button v-if="route.query.cursor" class="btn" @click="reader.update({ cursor: undefined })">{{ t('projectCatalog.firstPage') }}</button><button v-if="records.nextCursor" class="btn" @click="reader.update({ cursor: records.nextCursor || undefined })">{{ t('projectCatalog.more') }}</button></nav>
      </template>
    </section>
    <p v-if="!recordMode && catalogPending" role="status">{{ t('knowledge.loading') }}</p>
    <div v-if="!recordMode && catalogError" role="alert"><p>{{ t(operatorDiagnosisKey(catalogError)) }}</p><button class="btn" @click="context.refresh">{{ t('knowledge.retry') }}</button></div>
    <p v-if="!recordMode && !reference && !catalogPending && !catalogError && !unresolved.length">{{ t('projectCatalog.noUnresolved') }}</p>
    <section v-if="reference" class="reader">
      <div class="object-actions"><h2>{{ recordMode && selected ? selected.title : retained?.name || t('projectCatalog.unknownProject') }}</h2><button v-if="recordMode" class="btn" @click="reader.update({ reference: undefined, id: undefined, historyCursor: undefined })">{{ t('projectCatalog.returnResults') }}</button></div>
      <p v-if="recordMode" class="scope-note">{{ t('projectCatalog.historicalNote') }}</p>
      <div v-if="recordMode && recordsError" role="alert"><p>{{ t('projectCatalog.queryError') }} {{ t(operatorDiagnosisKey(recordsError)) }}</p><button class="btn" @click="reader.retry">{{ t('knowledge.retry') }}</button></div>
      <details><summary>{{ t('projectCatalog.provenance') }}</summary><p>{{ t('projectCatalog.readOnlyHelp') }}</p><p v-if="retained">{{ retained.reason }}</p><code>{{ reference }}</code><p>{{ t('projectCatalog.referenceHelp') }}</p><code v-if="originalLocation" data-testid="unresolved-original-location">{{ originalLocation }}</code>
        <p v-if="retained && retained.families === undefined">{{ t('projectCatalog.familiesUnknown') }}</p>
        <template v-for="family in retained?.families" :key="family.table_name"><p>{{ family.kind }} · {{ family.table_name }} · {{ t('projectCatalog.membershipOccurrences', { count: family.membership_count }) }} · {{ t(`projectCatalog.familySupport.${family.reader_support}`) }}</p></template>
        <p v-if="retained?.families">{{ t('projectCatalog.familyHelp') }}</p>
      </details>
      <p v-if="metadataOnly" role="status" data-testid="unresolved-metadata-only">{{ t('projectCatalog.metadataOnly') }}</p>
      <p v-else-if="retained && !recordMode">{{ t('projectCatalog.readerKindsHelp') }}</p>
      <p v-if="unsupportedKind" role="alert" data-testid="unresolved-kind-unavailable">{{ t('projectCatalog.kindUnavailable') }}</p>
      <p v-if="route.query.path"><strong>{{ route.query.path }}</strong><span v-if="route.query.version"> · {{ t('knowledge.version') }} {{ route.query.version }}</span><span v-if="route.query.compare"> · {{ t('knowledge.compare') }} {{ route.query.compare }}</span></p>
      <form v-if="!recordMode && availableKinds.length" class="filters" @submit.prevent="reader.update({ q: query, cursor: undefined })">
        <label><span>{{ t('projectCatalog.kind') }}</span><select :value="kind" @change="changeKind"><option v-if="unsupportedKind" :value="kind" disabled>{{ t('projectCatalog.kindUnavailable') }}</option><option v-for="choice in availableKinds" :key="choice" :value="choice">{{ choice === 'code' ? t('nav.items.code') : t(`projectCatalog.${choice}`) }}</option></select></label>
        <template v-if="kind !== 'code'"><label><span>{{ t('projectCatalog.search') }}</span><input v-model="query" type="search" /></label><button class="btn" type="submit">{{ t('projectCatalog.search') }}</button></template>
      </form>
      <details v-if="!metadataOnly && !unsupportedKind && kind && kind !== 'code' && (historyID || historyError)" class="history" data-testid="unresolved-history" :open="Boolean(historyError || route.query.historyCursor || (!recordMode && historyID))">
        <summary>{{ t('projectCatalog.history') }} · {{ historyID }}</summary>
        <p v-if="historyPending" role="status">{{ t('knowledge.loading') }}</p>
        <div v-else-if="historyError" role="alert"><p>{{ t('projectCatalog.readerError') }} {{ t(operatorDiagnosisKey(historyError)) }}</p><button class="btn" @click="reader.retry">{{ t('knowledge.retry') }}</button></div>
        <template v-else-if="history">
          <p v-if="!history.items.length">{{ t('projectCatalog.noHistory') }}</p>
          <template v-if="kind === 'document'">
            <p v-if="route.query.version && !history.items.some(item => item.fields.some(field => field.label === 'version' && field.value === route.query.version))">{{ t('projectCatalog.versionNotOnPage') }} {{ t('knowledge.version') }} {{ route.query.version }}</p>
            <p v-if="route.query.compare && !history.items.some(item => item.fields.some(field => field.label === 'version' && field.value === route.query.compare))">{{ t('projectCatalog.versionNotOnPage') }} {{ t('knowledge.compare') }} {{ route.query.compare }}</p>
          </template>
          <article v-for="(item, index) in history.items" :key="`${item.id}-${index}`" :class="{ selected: kind === 'document' && item.fields.some(field => field.label === 'version' && (field.value === route.query.version || field.value === route.query.compare)) }">
            <h4>{{ item.title }}<span v-if="kind === 'document' && item.fields.some(field => field.label === 'version' && field.value === route.query.compare)"> · {{ t('knowledge.compare') }}</span></h4>
            <div v-if="item.content" class="body">{{ item.content }}</div><dl><template v-for="field in item.fields" :key="field.label"><dt>{{ field.label }}</dt><dd>{{ field.value }}</dd></template></dl>
          </article>
          <nav class="paging"><button v-if="route.query.historyCursor" class="btn" @click="reader.update({ historyCursor: undefined })">{{ t('projectCatalog.firstPage') }}</button><button v-if="history.nextCursor" class="btn" @click="reader.update({ historyCursor: history.nextCursor || undefined })">{{ t('projectCatalog.more') }}</button></nav>
        </template>
      </details>
      <p v-if="pending" role="status">{{ t('knowledge.loading') }}</p>
      <div v-else-if="error" role="alert" data-testid="unresolved-error"><p>{{ t('projectCatalog.readerError') }} {{ t(operatorDiagnosisKey(error)) }}</p><button class="btn" @click="reader.retry">{{ t('knowledge.retry') }}</button><RouterLink to="/projects/unresolved">{{ t('projectCatalog.review') }}</RouterLink></div>
      <template v-else-if="content">
        <p v-if="!content.items.length">{{ t('projectCatalog.noContent') }}</p>
        <p v-if="kind !== 'code' && (id || route.query.path) && !selected">{{ t('projectCatalog.notOnPage') }} <code>{{ id || route.query.path }}</code></p>
        <article v-for="(item, index) in content.items" :key="`${item.id}-${index}`" :data-testid="`unresolved-content-${item.id}`" :class="{ selected: selected?.id === item.id }">
          <div class="object-actions"><h3>{{ item.title }}</h3><button v-if="kind !== 'code' && !recordMode" class="btn" @click="reader.open(item)">{{ t('projectCatalog.history') }}</button></div>
          <div v-if="item.content" class="body">{{ item.content }}</div>
          <details><summary>{{ t(kind === 'code' ? 'knowledge.technical' : 'projectCatalog.provenance') }}</summary><dl><template v-for="field in item.fields" :key="field.label"><dt>{{ field.label }}</dt><dd>{{ field.value }}</dd></template></dl></details>
        </article>
        <nav v-if="!recordMode" class="paging"><button v-if="route.query.cursor" class="btn" @click="reader.update({ cursor: undefined })">{{ t('projectCatalog.firstPage') }}</button><button v-if="content.nextCursor" class="btn" @click="reader.update({ cursor: content.nextCursor || undefined })">{{ t('projectCatalog.more') }}</button></nav>
      </template>
    </section>
    <section v-if="!recordMode && !catalogError && unresolved.length" class="group-discovery" aria-labelledby="retained-groups-heading">
      <h2 id="retained-groups-heading">{{ t('projectCatalog.groupDiscovery') }}</h2>
      <div class="filters">
        <button class="btn" :aria-pressed="groupMode === 'readers'" data-testid="unresolved-reader-groups" @click="groupMode = 'readers'">{{ t('projectCatalog.readerGroups', { count: readerGroupCount }) }}</button>
        <button class="btn" :aria-pressed="groupMode === 'metadata'" data-testid="unresolved-metadata-groups" @click="groupMode = 'metadata'">{{ t('projectCatalog.metadataGroups', { count: metadataGroupCount }) }}</button>
        <label><span>{{ t('projectCatalog.searchGroups') }}</span><input v-model="groupSearch" type="search" data-testid="unresolved-group-search" /></label>
      </div>
      <details><summary>{{ t('projectCatalog.provenance') }}</summary><p>{{ t(groupMode === 'metadata' ? 'projectCatalog.metadataOnly' : 'projectCatalog.readerKindsHelp') }}</p><p v-if="!catalogPending" role="status" data-testid="unresolved-group-counts">{{ t('projectCatalog.groupCounts', { total: unresolved.length, readers: readerGroupCount, metadata: metadataGroupCount }) }}</p></details>
      <nav class="retained-list" :aria-label="t('projectCatalog.groupDiscovery')">
        <div v-for="row in groupRows" :key="row.reference" class="group-row">
          <RouterLink :to="{ path: '/projects/unresolved', query: { reference: row.reference, returnTo, originalLocation: typeof route.query.originalLocation === 'string' ? route.query.originalLocation : undefined }, hash: route.hash }" :aria-current="reference === row.reference ? 'page' : undefined"><strong>{{ row.name }}</strong><span v-if="!row.reader_kinds.length">{{ t('projectCatalog.metadataOnly') }}</span><span v-else>{{ row.reader_kinds.map(choice => choice === 'code' ? t('nav.items.code') : t(`projectCatalog.${choice}`)).join(' · ') }}</span></RouterLink>
          <details><summary>{{ t('projectCatalog.provenance') }}</summary><p>{{ row.reason }}</p><code>{{ row.reference }}</code><p v-if="row.families === undefined">{{ t('projectCatalog.familiesUnknown') }}</p><template v-for="family in row.families" :key="family.table_name"><p>{{ family.kind }} · {{ family.table_name }} · {{ t('projectCatalog.membershipOccurrences', { count: family.membership_count }) }} · {{ t(`projectCatalog.familySupport.${family.reader_support}`) }}</p></template><p v-if="row.families">{{ t('projectCatalog.familyHelp') }}</p></details>
        </div>
      </nav>
      <p v-if="!matchingGroups.length">{{ t(groupSearch.trim() ? 'projectCatalog.noGroupMatches' : groupMode === 'readers' ? 'projectCatalog.noReaderGroups' : 'projectCatalog.noMetadataGroups') }}</p>
      <nav class="paging" :aria-label="t('projectCatalog.groupPages')">
        <button class="btn" :disabled="groupPage <= 1" data-testid="unresolved-groups-previous" @click="groupPage--">{{ t('knowledge.previous') }}</button>
        <span role="status">{{ t('knowledge.pageOf', { page: groupPage, total: groupPageCount }) }}</span>
        <button class="btn" :disabled="groupPage >= groupPageCount" data-testid="unresolved-groups-next" @click="groupPage++">{{ t('knowledge.next') }}</button>
      </nav>
    </section>
  </div>
</template>

<style scoped>
.unresolved-page { display:grid; gap:24px; max-width:1100px; min-width:0; } h1 { font-size:var(--text-xl); margin:0 0 8px; } h2 { font-size:var(--text-lg); } h3,h4 { margin:0 0 12px; } p,dt { color:var(--muted); } .retained-list { display:grid; gap:12px; } .retained-list a { display:grid; gap:6px; padding:14px; border:1px solid var(--border); color:var(--fg); } .retained-list [aria-current=page],article.selected { border-color:var(--accent); } .retained-list span { color:var(--muted); } .reader { display:grid; gap:16px; min-width:0; } .filters,.paging { display:flex; align-items:end; gap:12px; flex-wrap:wrap; } label { display:grid; gap:6px; } input,select,.btn { min-height:44px; padding:8px 12px; color:var(--fg); background:var(--surface); border:1px solid var(--border); border-radius:var(--r-sm); } .btn { cursor:pointer; } article { padding:18px; border:1px solid var(--border); border-radius:var(--r-sm); min-width:0; } .body { white-space:pre-wrap; overflow-wrap:anywhere; line-height:1.6; } dl { display:grid; grid-template-columns:minmax(100px,180px) minmax(0,1fr); gap:8px 12px; } dt,dd,code { overflow-wrap:anywhere; } dd { margin:0; white-space:pre-wrap; } summary { cursor:pointer; min-height:44px; } .history { border-top:1px solid var(--border); padding-top:20px; display:grid; gap:16px; } @media(max-width:760px) { dl { grid-template-columns:1fr; } dt { margin-top:8px; } .filters label,.filters input { width:100%; } }
.group-discovery { display:grid; gap:16px; min-width:0; } .group-discovery input { max-width:100%; } .btn:disabled { cursor:default; opacity:0.55; } .btn[aria-pressed=true] { border-color:var(--accent); } .retained-list a { overflow-wrap:anywhere; }
.record-discovery { display:grid; gap:16px; min-width:0; } .views,.object-actions { display:flex; align-items:center; flex-wrap:wrap; gap:8px 16px; } .views a { min-height:44px; display:inline-flex; align-items:center; } .views [aria-current=page] { color:var(--fg); text-decoration:underline; text-underline-offset:4px; } .record-search { flex:1; min-width:12rem; } .record-search input { width:100%; box-sizing:border-box; } .record-result { border-top:1px solid var(--border); padding:16px 0; display:grid; gap:8px; min-width:0; } .record-result h2,.object-actions h2,.object-actions h3 { margin:0; overflow-wrap:anywhere; } .object-actions h2,.object-actions h3 { flex:1; min-width:0; } .excerpt { display:-webkit-box; -webkit-line-clamp:3; -webkit-box-orient:vertical; overflow:hidden; } .scope-note { max-width:72ch; margin:0; } .group-row { min-width:0; } .group-row>details { padding:8px 14px; } details code,details p { overflow-wrap:anywhere; } summary { cursor:pointer; min-height:44px; display:list-item; align-content:center; } input { caret-color:var(--accent); } :where(a,button,input,select,summary):focus-visible { outline:2px solid var(--accent); outline-offset:3px; } ::selection { background:var(--accent); color:var(--accent-on); }
.record-result { border:0; border-top:1px solid var(--border); border-radius:0; } .history:not([open]) { display:block; }
@media(max-width:480px) { .record-discovery .filters { gap:8px; } .record-discovery .filters>label { min-width:0; } .record-search { flex-basis:100%; order:1; } .record-discovery .filters>button { margin-left:auto; } .record-result { padding:12px 0; } }
</style>
