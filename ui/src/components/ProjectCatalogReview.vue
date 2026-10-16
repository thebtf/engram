<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { RetainedItem, RetainedKind, RetainedPage, UnresolvedProject } from '../utils/projectCatalog'

const props = defineProps<{
  records: UnresolvedProject[]
  pending?: boolean
  error?: string | null
  reference?: string
  labels: Record<'unresolvedTitle' | 'review' | 'kind' | 'memory' | 'document' | 'issue' | 'code' | 'metadataOnly' | 'readerKindsHelp' | 'history' | 'more' | 'back' | 'loading' | 'search' | 'unknownProject', string>
  read: (reference: string, kind: RetainedKind, id?: string | number, cursor?: string, q?: string) => Promise<RetainedPage>
}>()
const emit = defineEmits<{ select: [reference: string] }>()
const selected = ref('')
const kind = ref<RetainedKind | ''>('')
const retained = computed(() => props.records.find(record => record.reference === selected.value))
const availableKinds = computed(() => retained.value?.reader_kinds || [])
const query = ref('')
const items = ref<RetainedItem[]>([])
const history = ref<RetainedItem[]>([])
const historyId = ref<string | number | undefined>()
const nextCursor = ref<string | null>(null)
const provenance = ref<unknown>()
const busy = ref(false)
const readError = ref('')
let generation = 0

async function load(more = false) {
  const current = ++generation
  if (!selected.value || !kind.value || !availableKinds.value.includes(kind.value) || props.pending || props.error) {
    items.value = []; history.value = []; provenance.value = undefined; nextCursor.value = null; readError.value = ''; busy.value = false
    return
  }
  busy.value = true
  readError.value = ''
  if (!more) {
    nextCursor.value = null
    if (historyId.value === undefined) { items.value = []; provenance.value = undefined }
    else history.value = []
  }
  try {
    const page = await props.read(selected.value, kind.value, historyId.value, more ? nextCursor.value ?? undefined : undefined, query.value)
    if (current !== generation) return
    if (more && page.next_cursor && page.next_cursor === nextCursor.value) throw new Error('Retained continuation did not advance')
    if (historyId.value === undefined) {
      items.value = more ? [...items.value, ...page.items] : page.items
      provenance.value = page.provenance
    } else history.value = more ? [...history.value, ...page.items] : page.items
    nextCursor.value = page.next_cursor
  } catch (error) {
    if (current === generation) readError.value = error instanceof Error ? error.message : props.labels.unknownProject
  } finally {
    if (current === generation) busy.value = false
  }
}

function review(reference: string) {
  selected.value = reference
  historyId.value = undefined
  history.value = []
  emit('select', reference)
  kind.value = availableKinds.value.includes('memory') ? 'memory' : availableKinds.value[0] || ''
}
function openHistory(id: string | number) {
  historyId.value = id
  void load()
}
watch(() => props.reference, reference => {
  if (reference && reference !== selected.value) review(reference)
}, { immediate: true })
watch(availableKinds, choices => {
  if (!kind.value || !choices.includes(kind.value)) kind.value = choices.includes('memory') ? 'memory' : choices[0] || ''
})
watch([selected, kind, () => props.pending, () => props.error], () => { historyId.value = undefined; history.value = []; void load() }, { immediate: true })
</script>

<template>
  <div v-if="pending" role="status" class="text-sm">{{ labels.loading }}</div>
  <div v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</div>
  <details v-if="records.length || selected" :open="Boolean(selected)" class="rounded-lg border p-4 space-y-3">
    <summary class="cursor-pointer font-medium">{{ labels.unresolvedTitle }} ({{ records.length }})</summary>
    <ul class="space-y-2">
      <li v-for="record in records" :key="record.reference">
        <button type="button" :disabled="busy" class="underline" @click="review(record.reference)">{{ record.name }} · {{ labels.review }}</button>
        <p class="text-sm">{{ record.reason }}</p>
        <p v-if="!record.reader_kinds.length" class="text-sm">{{ labels.metadataOnly }}</p>
      </li>
    </ul>
    <div v-if="selected" class="space-y-3">
      <p v-if="retained && !availableKinds.length" role="status">{{ labels.metadataOnly }}</p>
      <p v-else-if="retained" class="text-sm">{{ labels.readerKindsHelp }}</p>
      <p v-else role="alert">{{ labels.unknownProject }}</p>
      <label v-if="availableKinds.length" class="block text-sm">{{ labels.kind }}
        <select v-model="kind" :disabled="busy" class="ml-2 rounded border p-2">
          <option v-for="choice in availableKinds" :key="choice" :value="choice">{{ labels[choice] }}</option>
        </select>
      </label>
      <form v-if="kind && kind !== 'code' && historyId === undefined" class="flex gap-2" @submit.prevent="load()">
        <input v-model="query" :aria-label="labels.search" class="rounded border p-2" />
        <button type="submit" :disabled="busy" class="underline">{{ labels.search }}</button>
      </form>
      <button v-else-if="historyId !== undefined" type="button" :disabled="busy" class="underline" @click="historyId = undefined; history = []; load()">{{ labels.back }}</button>
      <p v-if="busy" role="status">{{ labels.loading }}</p>
      <p v-if="readError" role="alert" class="text-red-600">{{ readError }}</p>
      <article v-for="(item, index) in historyId === undefined ? items : history" :key="`${item.id}:${index}`" class="rounded border p-3">
        <h3 class="font-medium">{{ item.title }} <span class="text-xs">#{{ item.id }}</span></h3>
        <p class="text-xs">{{ item.date }}</p>
        <p class="whitespace-pre-wrap text-sm">{{ item.content }}</p>
        <dl class="text-xs space-y-1"><div v-for="field in item.fields" :key="field.label"><dt class="inline font-medium">{{ field.label }}: </dt><dd class="inline whitespace-pre-wrap">{{ field.value }}</dd></div></dl>
        <button v-if="kind !== 'code' && historyId === undefined" type="button" :disabled="busy" class="underline text-sm" @click="openHistory(item.id)">{{ labels.history }}</button>
      </article>
      <pre v-if="historyId === undefined && provenance !== undefined" class="whitespace-pre-wrap break-words text-xs">{{ JSON.stringify(provenance, null, 2) }}</pre>
      <button v-if="nextCursor" type="button" :disabled="busy" class="underline" @click="load(true)">{{ labels.more }}</button>
    </div>
  </details>
</template>
