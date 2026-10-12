<script setup lang="ts">
import { ref, watch } from 'vue'
import type { RetainedItem, RetainedKind, RetainedPage, UnresolvedProject } from '../utils/projectCatalog'

const props = defineProps<{
  records: UnresolvedProject[]
  pending?: boolean
  error?: string | null
  reference?: string
  labels: Record<'unresolvedTitle' | 'review' | 'kind' | 'memory' | 'document' | 'issue' | 'history' | 'more' | 'back' | 'loading' | 'search' | 'unknownProject', string>
  read: (reference: string, kind: RetainedKind, id?: string | number, cursor?: string, q?: string) => Promise<RetainedPage>
}>()
const emit = defineEmits<{ select: [reference: string] }>()
const selected = ref('')
const kind = ref<RetainedKind>('memory')
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
  if (!selected.value) return
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
  void load()
}
function openHistory(id: string | number) {
  historyId.value = id
  void load()
}
watch(() => props.reference, reference => {
  if (reference && reference !== selected.value) review(reference)
}, { immediate: true })
watch(kind, () => { historyId.value = undefined; history.value = []; void load() })
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
      </li>
    </ul>
    <div v-if="selected" class="space-y-3">
      <label class="block text-sm">{{ labels.kind }}
        <select v-model="kind" :disabled="busy" class="ml-2 rounded border p-2">
          <option value="memory">{{ labels.memory }}</option>
          <option value="document">{{ labels.document }}</option>
          <option value="issue">{{ labels.issue }}</option>
        </select>
      </label>
      <form v-if="historyId === undefined" class="flex gap-2" @submit.prevent="load()">
        <input v-model="query" :aria-label="labels.search" class="rounded border p-2" />
        <button type="submit" :disabled="busy" class="underline">{{ labels.search }}</button>
      </form>
      <button v-else type="button" :disabled="busy" class="underline" @click="historyId = undefined; history = []; load()">{{ labels.back }}</button>
      <p v-if="busy" role="status">{{ labels.loading }}</p>
      <p v-if="readError" role="alert" class="text-red-600">{{ readError }}</p>
      <article v-for="(item, index) in historyId === undefined ? items : history" :key="`${item.id}:${index}`" class="rounded border p-3">
        <h3 class="font-medium">{{ item.title }} <span class="text-xs">#{{ item.id }}</span></h3>
        <p class="text-xs">{{ item.date }}</p>
        <p class="whitespace-pre-wrap text-sm">{{ item.content }}</p>
        <dl class="text-xs space-y-1"><div v-for="field in item.fields" :key="field.label"><dt class="inline font-medium">{{ field.label }}: </dt><dd class="inline whitespace-pre-wrap">{{ field.value }}</dd></div></dl>
        <button v-if="historyId === undefined" type="button" :disabled="busy" class="underline text-sm" @click="openHistory(item.id)">{{ labels.history }}</button>
      </article>
      <pre v-if="historyId === undefined && provenance !== undefined" class="whitespace-pre-wrap break-words text-xs">{{ JSON.stringify(provenance, null, 2) }}</pre>
      <button v-if="nextCursor" type="button" :disabled="busy" class="underline" @click="load(true)">{{ labels.more }}</button>
    </div>
  </details>
</template>
