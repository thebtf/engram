<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useOperatorProjectContext } from '../composables/useOperatorProjectContext'
import { useOperatorUnresolved } from '../composables/useOperatorUnresolved'
import { operatorDiagnosisKey } from '../composables/useOperatorApi'
const { t } = useI18n()
const route = useRoute()
const context = useOperatorProjectContext()
const { unresolved, pending: catalogPending, error: catalogError } = context
const reader = useOperatorUnresolved()
const { reference, kind, q, id, selected, historyID, content, history, pending, historyPending, error, historyError, returnTo, originalLocation } = reader
const retained = computed(() => unresolved.value.find((row) => row.reference === reference.value))
const query = ref(q.value)
watch(q, (value) => { query.value = value })
function changeKind(event: Event) {
  if (event.target instanceof HTMLSelectElement) void reader.update({ kind: event.target.value, cursor: undefined, historyCursor: undefined, id: undefined, memory: undefined, path: undefined, version: undefined, compare: undefined })
}
</script>

<template>
  <div class="unresolved-page">
    <header><h1>{{ t('projectCatalog.unresolvedTitle') }}</h1><p>{{ t('projectCatalog.readOnlyHelp') }}</p><RouterLink :to="returnTo" data-testid="unresolved-back">{{ t('projectCatalog.back') }}</RouterLink> · <RouterLink to="/projects">{{ t('knowledge.chooseProject') }}</RouterLink></header>
    <p v-if="catalogPending" role="status">{{ t('knowledge.loading') }}</p>
    <div v-if="catalogError" role="alert"><p>{{ t(operatorDiagnosisKey(catalogError)) }}</p><button class="btn" @click="context.refresh">{{ t('knowledge.retry') }}</button></div>
    <nav class="retained-list" :aria-label="t('projectCatalog.unresolvedTitle')">
      <RouterLink v-for="row in unresolved" :key="row.reference" :to="{ path: '/projects/unresolved', query: { reference: row.reference, kind, returnTo } }" :aria-current="reference === row.reference ? 'page' : undefined"><strong>{{ row.name }}</strong><span>{{ row.reason }}</span></RouterLink>
    </nav>
    <p v-if="!reference && !catalogPending && !catalogError && !unresolved.length">{{ t('projectCatalog.noUnresolved') }}</p>
    <p v-else-if="!reference">{{ t('projectCatalog.chooseUnresolved') }}</p>
    <section v-else class="reader">
      <h2>{{ retained?.name || t('projectCatalog.unknownProject') }}</h2>
      <p v-if="retained">{{ retained.reason }}</p>
      <details><summary>{{ t('knowledge.technical') }}</summary><code>{{ reference }}</code><p>{{ t('projectCatalog.referenceHelp') }}</p><code v-if="originalLocation" data-testid="unresolved-original-location">{{ originalLocation }}</code></details>
      <p v-if="route.query.path"><strong>{{ route.query.path }}</strong><span v-if="route.query.version"> · {{ t('knowledge.version') }} {{ route.query.version }}</span><span v-if="route.query.compare"> · {{ t('knowledge.compare') }} {{ route.query.compare }}</span></p>
      <form class="filters" @submit.prevent="reader.update({ q: query, cursor: undefined })">
        <label><span>{{ t('projectCatalog.kind') }}</span><select :value="kind" @change="changeKind"><option value="memory">{{ t('projectCatalog.memory') }}</option><option value="document">{{ t('projectCatalog.document') }}</option><option value="issue">{{ t('projectCatalog.issue') }}</option><option value="code">{{ t('nav.items.code') }}</option></select></label>
        <template v-if="kind !== 'code'"><label><span>{{ t('projectCatalog.search') }}</span><input v-model="query" type="search" /></label><button class="btn" type="submit">{{ t('projectCatalog.search') }}</button></template>
      </form>
      <p v-if="pending" role="status">{{ t('knowledge.loading') }}</p>
      <div v-else-if="error" role="alert" data-testid="unresolved-error"><p>{{ t('projectCatalog.readerError') }} {{ t(operatorDiagnosisKey(error)) }}</p><button class="btn" @click="reader.retry">{{ t('knowledge.retry') }}</button><RouterLink to="/projects/unresolved">{{ t('projectCatalog.review') }}</RouterLink></div>
      <template v-else-if="content">
        <section v-if="content.provenance.length" class="provenance"><h3>{{ t('projectCatalog.provenance') }}</h3><dl><template v-for="(field, index) in content.provenance" :key="index"><dt>{{ field.label }}</dt><dd>{{ field.value }}</dd></template></dl></section>
        <p v-if="!content.items.length">{{ t('projectCatalog.noContent') }}</p>
        <p v-if="kind !== 'code' && (id || route.query.path) && !selected">{{ t('projectCatalog.notOnPage') }} <code>{{ id || route.query.path }}</code></p>
        <article v-for="(item, index) in content.items" :key="`${item.id}-${index}`" :data-testid="`unresolved-content-${item.id}`" :class="{ selected: selected?.id === item.id }">
          <h3>{{ item.title }}</h3><div v-if="item.content" class="body">{{ item.content }}</div>
          <details v-if="kind === 'code'"><summary>{{ t('knowledge.technical') }}</summary><dl><template v-for="field in item.fields" :key="field.label"><dt>{{ field.label }}</dt><dd>{{ field.value }}</dd></template></dl></details>
          <template v-else><dl><template v-for="field in item.fields" :key="field.label"><dt>{{ field.label }}</dt><dd>{{ field.value }}</dd></template></dl><button class="btn" @click="reader.open(item)">{{ t('projectCatalog.history') }}</button></template>
        </article>
        <nav class="paging"><button v-if="route.query.cursor" class="btn" @click="reader.update({ cursor: undefined })">{{ t('projectCatalog.firstPage') }}</button><button v-if="content.nextCursor" class="btn" @click="reader.update({ cursor: content.nextCursor || undefined })">{{ t('projectCatalog.more') }}</button></nav>
      </template>
      <section v-if="kind !== 'code' && (historyID || historyError)" class="history" data-testid="unresolved-history">
        <h3>{{ t('projectCatalog.history') }} · {{ historyID }}</h3>
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
      </section>
    </section>
  </div>
</template>

<style scoped>
.unresolved-page { display:grid; gap:24px; max-width:1100px; min-width:0; } h1 { font-size:var(--text-xl); margin:0 0 8px; } h2 { font-size:var(--text-lg); } h3,h4 { margin:0 0 12px; } p,dt { color:var(--muted); } .retained-list { display:grid; gap:12px; } .retained-list a { display:grid; gap:6px; padding:14px; border:1px solid var(--border); color:var(--fg); } .retained-list [aria-current=page],article.selected { border-color:var(--accent); } .retained-list span { color:var(--muted); } .reader { display:grid; gap:16px; min-width:0; } .filters,.paging { display:flex; align-items:end; gap:12px; flex-wrap:wrap; } label { display:grid; gap:6px; } input,select,.btn { min-height:44px; padding:8px 12px; color:var(--fg); background:var(--surface); border:1px solid var(--border); border-radius:var(--r-sm); } .btn { cursor:pointer; } article,.provenance { padding:18px; border:1px solid var(--border); border-radius:var(--r-sm); min-width:0; } .body { white-space:pre-wrap; overflow-wrap:anywhere; line-height:1.6; } dl { display:grid; grid-template-columns:minmax(100px,180px) minmax(0,1fr); gap:8px 12px; } dt,dd,code { overflow-wrap:anywhere; } dd { margin:0; white-space:pre-wrap; } summary { cursor:pointer; min-height:44px; } .history { border-top:1px solid var(--border); padding-top:20px; display:grid; gap:16px; } @media(max-width:760px) { dl { grid-template-columns:1fr; } dt { margin-top:8px; } .filters label,.filters input { width:100%; } }
</style>
