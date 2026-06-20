<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { Database, RefreshCw, Tag } from 'lucide-vue-next'
import { useUiI18n } from '@/composables/useUiI18n'
import { fetchMemories, fetchProjects, type Memory } from '@/utils/api'
import { safeAbsoluteDate, truncate } from '@/utils/formatters'
import EmptyState from '@/components/layout/EmptyState.vue'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

const projects = ref<string[]>([])
const selectedProject = ref('')
const memories = ref<Memory[]>([])
const selectedMemoryId = ref<number | null>(null)
const loading = ref(false)
const bootLoading = ref(true)
const error = ref<string | null>(null)
const { t } = useUiI18n()

let abortController: AbortController | null = null

const selectedMemory = computed(() =>
  memories.value.find(memory => memory.id === selectedMemoryId.value) ?? null,
)

async function loadProjects() {
  projects.value = await fetchProjects()
  if (!selectedProject.value && projects.value.length > 0) {
    selectedProject.value = projects.value[0]
  }
}

async function loadMemories() {
  if (!selectedProject.value) {
    memories.value = []
    selectedMemoryId.value = null
    return
  }

  abortController?.abort()
  abortController = new AbortController()

  loading.value = true
  error.value = null
  try {
    const items = await fetchMemories(selectedProject.value, 100, abortController.signal)
    memories.value = items
    if (!items.some(memory => memory.id === selectedMemoryId.value)) {
      selectedMemoryId.value = items[0]?.id ?? null
    }
  } catch (err: any) {
    if (err?.name !== 'AbortError') {
      error.value = err?.message || t.value.memories.loadMemoriesError
      memories.value = []
      selectedMemoryId.value = null
    }
  } finally {
    loading.value = false
  }
}

onMounted(async () => {
  bootLoading.value = true
  try {
    await loadProjects()
    await loadMemories()
  } catch (err: any) {
    error.value = err?.message || t.value.memories.loadProjectsError
  } finally {
    bootLoading.value = false
  }
})

watch(selectedProject, async (next, prev) => {
  if (next && next !== prev) {
    await loadMemories()
  }
})
</script>

<template>
  <div class="space-y-4 pt-4">
    <div class="flex items-center justify-between gap-3 flex-wrap">
      <div class="flex items-center gap-3">
        <Database class="size-5 text-primary" />
        <div>
          <h1 class="text-lg font-semibold">{{ t.memories.title }}</h1>
          <p class="text-sm text-muted-foreground">
            {{ t.memories.subtitle }}
          </p>
        </div>
      </div>
      <div class="flex items-center gap-2">
        <Select v-model="selectedProject" :disabled="bootLoading || projects.length === 0">
          <SelectTrigger class="w-[220px]">
            <SelectValue :placeholder="t.memories.projectSelect" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem v-for="project in projects" :key="project" :value="project">
              {{ project }}
            </SelectItem>
          </SelectContent>
        </Select>
        <Button variant="outline" size="sm" :disabled="loading || !selectedProject" @click="loadMemories">
          <RefreshCw :class="['size-4', loading && 'animate-spin']" />
          {{ t.common.refresh }}
        </Button>
      </div>
    </div>

    <div v-if="bootLoading" class="text-sm text-muted-foreground">
      {{ t.memories.loading }}
    </div>

    <div v-else-if="error" class="rounded-lg border border-destructive/30 bg-destructive/10 p-4 text-sm text-destructive">
      {{ error }}
    </div>

    <EmptyState
      v-else-if="!selectedProject"
      icon="fa-project-diagram"
      :title="t.memories.noProjectsTitle"
      :description="t.memories.noProjectsDescription"
    />

    <EmptyState
      v-else-if="!loading && memories.length === 0"
      icon="fa-brain"
      :title="t.memories.emptyTitle"
      :description="t.memories.emptyDescription"
    />

    <div v-else class="grid gap-4 xl:grid-cols-[minmax(0,1.3fr)_minmax(320px,0.9fr)]">
      <Card>
        <CardHeader class="pb-3">
          <CardTitle class="text-sm font-medium">{{ t.memories.listTitle }}</CardTitle>
          <CardDescription>
            {{ t.memories.listDescription }}: <span class="font-mono">{{ selectedProject }}</span> · {{ memories.length }} записей
          </CardDescription>
        </CardHeader>
        <CardContent class="pt-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead class="w-16">{{ t.memories.headers.id }}</TableHead>
                <TableHead>{{ t.memories.headers.content }}</TableHead>
                <TableHead class="w-28">{{ t.memories.headers.status }}</TableHead>
                <TableHead class="w-28">{{ t.memories.headers.scope }}</TableHead>
                <TableHead class="w-24 text-right">{{ t.memories.headers.citations }}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow
                v-for="memory in memories"
                :key="memory.id"
                class="cursor-pointer"
                :class="selectedMemoryId === memory.id ? 'bg-muted/60' : ''"
                @click="selectedMemoryId = memory.id"
              >
                <TableCell class="font-mono text-xs text-muted-foreground">#{{ memory.id }}</TableCell>
                <TableCell class="text-sm text-foreground">
                  {{ truncate(memory.content, 110) }}
                </TableCell>
                <TableCell>
                  <Badge variant="secondary">
                    {{ memory.status || t.memories.defaults.active }}
                  </Badge>
                </TableCell>
                <TableCell>
                  <Badge variant="outline">
                    {{ memory.privacy_scope || t.memories.defaults.project }}
                  </Badge>
                </TableCell>
                <TableCell class="text-right font-mono text-sm">
                  {{ memory.citation_count ?? 0 }}
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      <Card>
        <CardHeader class="pb-3">
          <CardTitle class="text-sm font-medium">{{ t.memories.detailTitle }}</CardTitle>
          <CardDescription v-if="selectedMemory">
            #{{ selectedMemory.id }} · {{ safeAbsoluteDate(selectedMemory.updated_at) }}
          </CardDescription>
        </CardHeader>
        <CardContent v-if="selectedMemory" class="space-y-4">
          <div class="space-y-2">
            <p class="text-xs uppercase tracking-wide text-muted-foreground">{{ t.memories.labels.content }}</p>
            <div class="rounded-md border bg-muted/30 p-3 text-sm leading-6 whitespace-pre-wrap">
              {{ selectedMemory.content }}
            </div>
          </div>

          <div class="grid gap-3 sm:grid-cols-2">
            <div>
              <p class="text-xs uppercase tracking-wide text-muted-foreground">{{ t.memories.labels.status }}</p>
              <p class="text-sm">{{ selectedMemory.status || t.memories.defaults.active }}</p>
            </div>
            <div>
              <p class="text-xs uppercase tracking-wide text-muted-foreground">{{ t.memories.labels.tier }}</p>
              <p class="text-sm">{{ selectedMemory.tier || '—' }}</p>
            </div>
            <div>
              <p class="text-xs uppercase tracking-wide text-muted-foreground">{{ t.memories.labels.scope }}</p>
              <p class="text-sm">{{ selectedMemory.privacy_scope || t.memories.defaults.project }}</p>
            </div>
            <div>
              <p class="text-xs uppercase tracking-wide text-muted-foreground">{{ t.memories.labels.sourceAgent }}</p>
              <p class="text-sm">{{ selectedMemory.source_agent || '—' }}</p>
            </div>
            <div>
              <p class="text-xs uppercase tracking-wide text-muted-foreground">{{ t.memories.labels.confidence }}</p>
              <p class="text-sm font-mono">{{ selectedMemory.confidence ?? '—' }}</p>
            </div>
            <div>
              <p class="text-xs uppercase tracking-wide text-muted-foreground">{{ t.memories.labels.injectionAccess }}</p>
              <p class="text-sm font-mono">{{ selectedMemory.injection_count ?? 0 }} / {{ selectedMemory.access_count ?? 0 }}</p>
            </div>
          </div>

          <div v-if="selectedMemory.tags?.length" class="space-y-2">
            <p class="text-xs uppercase tracking-wide text-muted-foreground">{{ t.memories.labels.tags }}</p>
            <div class="flex flex-wrap gap-2">
              <Badge v-for="tag in selectedMemory.tags" :key="tag" variant="outline" class="gap-1">
                <Tag class="size-3" />
                {{ tag }}
              </Badge>
            </div>
          </div>
        </CardContent>

        <CardContent v-else class="text-sm text-muted-foreground">
          {{ t.memories.detailEmpty }}
        </CardContent>
      </Card>
    </div>
  </div>
</template>
