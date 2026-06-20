<script setup lang="ts">
/** Issues — list via EntityRow with issue-chip tones. Seam: useIssues() → GET /api/issues. */
import { useIssues } from '../composables/useMockData'
const issues = useIssues()
const tone: Record<string,string> = { open:'var(--state-warn)', acknowledged:'var(--accent)', reopened:'var(--accent)', resolved:'var(--class-live)', closed:'var(--muted)' }
const open = useState<number|null>('issueOpen', () => null)
</script>
<template>
  <div>
    <header class="head"><h1>Задачи</h1><p>Кросс-проектный трекер: баги, фичи, передачи между агентами.</p></header>
    <div class="grid">
      <EntityRow v-for="is in issues" :key="is.id"
        :preview="`#${is.id} · ${is.title}`"
        :meta="[is.priority, is.type, `${is.comments} комм.`, is.age]"
        :status="is.status==='resolved'||is.status==='closed' ? 'live' : 'warn'"
        :open="open===is.id" @open="open = open===is.id ? null : is.id">
        <template #side><span class="chip" :style="{ color: tone[is.status] }">{{ is.status }}</span></template>
      </EntityRow>
    </div>
  </div>
</template>
<style scoped>
.head h1 { margin:0 0 4px; font-size:var(--text-xl); font-weight:700; }
.head p { margin:0 0 16px; font-size:var(--text-sm); color:var(--muted); }
.grid { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); overflow:hidden; }
.chip { font-size:10px; font-weight:800; text-transform:uppercase; letter-spacing:.03em; padding:2px 8px; border-radius:6px; background:color-mix(in oklab,currentColor,transparent 88%); }
</style>
