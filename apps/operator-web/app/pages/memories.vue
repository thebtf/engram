<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useMemoriesQuery, useProjectsQuery } from '~/composables/useOperatorQueries'
import { formatAbsoluteDate } from '~/utils/formatters'

const projectsQuery = useProjectsQuery()
const selectedProject = ref('')
const memoriesQuery = useMemoriesQuery(selectedProject)
const selectedMemoryId = ref<number | null>(null)

const projects = computed(() => projectsQuery.data.value ?? [])
const memories = computed(() => memoriesQuery.data.value ?? [])
const selectedMemory = computed(() =>
  memories.value.find((memory) => memory.id === selectedMemoryId.value) ?? null,
)

watch(projects, (nextProjects) => {
  if (!selectedProject.value && nextProjects.length > 0) {
    selectedProject.value = nextProjects[0]
  }
}, { immediate: true })

watch(memories, (nextMemories) => {
  if (!nextMemories.some((memory) => memory.id === selectedMemoryId.value)) {
    selectedMemoryId.value = nextMemories[0]?.id ?? null
  }
})
</script>

<template>
  <section class="space-y-5">
    <div class="surface-panel p-5">
      <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 class="text-xl font-semibold">Memory Lab</h1>
          <p class="mt-2 text-sm text-[var(--fg-2)]">
            First live memory slice in the new app shell: project picker, memory list and truthful detail panel.
          </p>
        </div>

        <div class="flex items-center gap-2">
          <select
            v-model="selectedProject"
            class="rounded-lg border border-[var(--border)] bg-[var(--surface-warm)] px-3 py-2 text-sm text-[var(--fg)]"
          >
            <option value="" disabled>select project</option>
            <option v-for="project in projects" :key="project" :value="project">{{ project }}</option>
          </select>
          <button
            class="rounded-lg border border-[var(--border)] bg-[var(--surface-warm)] px-4 py-2 text-sm text-[var(--fg-2)]"
            @click="memoriesQuery.refetch()"
          >
            Refresh
          </button>
        </div>
      </div>

      <div v-if="projectsQuery.isPending.value" class="text-sm text-[var(--muted)]">
        Загрузка проектов...
      </div>
      <div v-else-if="projects.length === 0" class="text-sm text-[var(--muted)]">
        Нет проектов для memory slice.
      </div>
      <div v-else-if="memoriesQuery.isPending.value" class="text-sm text-[var(--muted)]">
        Загрузка памяти...
      </div>
      <div v-else-if="memoriesQuery.isError.value" class="text-sm text-[var(--danger)]">
        {{ memoriesQuery.error.value?.message || 'Не удалось загрузить memories' }}
      </div>
      <div v-else-if="memories.length === 0" class="text-sm text-[var(--muted)]">
        Для проекта {{ selectedProject }} пока нет memory rows.
      </div>
      <div v-else class="grid gap-4 xl:grid-cols-[minmax(0,1.25fr),minmax(320px,0.9fr)]">
        <div class="surface-panel-warm p-4">
          <div class="mb-3 text-xs uppercase tracking-[0.08em] text-[var(--muted)]">Memory List</div>
          <div class="space-y-3">
            <button
              v-for="memory in memories"
              :key="memory.id"
              class="block w-full rounded-xl border p-4 text-left transition-colors"
              :class="selectedMemoryId === memory.id
                ? 'border-[var(--accent)] bg-[color:rgba(76,141,255,0.12)]'
                : 'border-[var(--border)] bg-[var(--surface)] hover:border-[var(--accent)]'"
              @click="selectedMemoryId = memory.id"
            >
              <div class="mb-2 flex flex-wrap items-center justify-between gap-3">
                <div class="text-sm font-semibold">#{{ memory.id }}</div>
                <div class="mono-data text-xs text-[var(--muted)]">{{ memory.citation_count ?? 0 }} citations</div>
              </div>
              <div class="mb-2 text-sm leading-6 text-[var(--fg-2)]">
                {{ memory.content }}
              </div>
              <div class="flex flex-wrap gap-2 text-xs text-[var(--muted)]">
                <span>{{ memory.status || 'active' }}</span>
                <span>{{ memory.privacy_scope || 'project' }}</span>
                <span>{{ memory.source_agent || '—' }}</span>
              </div>
            </button>
          </div>
        </div>

        <div class="surface-panel-warm p-4">
          <div class="mb-3 text-xs uppercase tracking-[0.08em] text-[var(--muted)]">Detail</div>
          <div v-if="selectedMemory" class="space-y-4">
            <div>
              <div class="mb-1 text-xs text-[var(--muted)]">Updated</div>
              <div class="mono-data text-sm">{{ formatAbsoluteDate(selectedMemory.updated_at) }}</div>
            </div>
            <div>
              <div class="mb-1 text-xs text-[var(--muted)]">Content</div>
              <div class="rounded-xl border border-[var(--border)] bg-[var(--surface)] p-4 text-sm leading-6 text-[var(--fg-2)]">
                {{ selectedMemory.content }}
              </div>
            </div>
            <div class="grid gap-3 sm:grid-cols-2 text-sm text-[var(--fg-2)]">
              <div>
                <div class="text-xs text-[var(--muted)]">Status</div>
                <div>{{ selectedMemory.status || 'active' }}</div>
              </div>
              <div>
                <div class="text-xs text-[var(--muted)]">Tier</div>
                <div>{{ selectedMemory.tier || '—' }}</div>
              </div>
              <div>
                <div class="text-xs text-[var(--muted)]">Scope</div>
                <div>{{ selectedMemory.privacy_scope || 'project' }}</div>
              </div>
              <div>
                <div class="text-xs text-[var(--muted)]">Confidence</div>
                <div class="mono-data">{{ selectedMemory.confidence ?? '—' }}</div>
              </div>
              <div>
                <div class="text-xs text-[var(--muted)]">Source Agent</div>
                <div>{{ selectedMemory.source_agent || '—' }}</div>
              </div>
              <div>
                <div class="text-xs text-[var(--muted)]">Injection / Access</div>
                <div class="mono-data">{{ selectedMemory.injection_count ?? 0 }} / {{ selectedMemory.access_count ?? 0 }}</div>
              </div>
            </div>
            <div v-if="selectedMemory.tags?.length">
              <div class="mb-2 text-xs text-[var(--muted)]">Tags</div>
              <div class="flex flex-wrap gap-2">
                <span
                  v-for="tag in selectedMemory.tags"
                  :key="tag"
                  class="rounded-full border border-[var(--border)] bg-[var(--surface)] px-2 py-1 text-xs text-[var(--fg-2)]"
                >
                  {{ tag }}
                </span>
              </div>
            </div>
          </div>
          <div v-else class="text-sm text-[var(--muted)]">
            Выбери memory row, чтобы увидеть детали.
          </div>
        </div>
      </div>
    </div>
  </section>
</template>
