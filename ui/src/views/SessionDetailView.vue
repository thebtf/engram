<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { formatRelativeTime } from '@/utils/formatters'
import Badge from '@/components/Badge.vue'

const route = useRoute()
const router = useRouter()
const sessionId = computed(() => String(route.params.id))

const session = ref<Record<string, any> | null>(null)
const injections = ref<any[]>([])
const loading = ref(true)
const error = ref<string | null>(null)

let abortController: AbortController | null = null

function outcomeBadge(outcome: string | null): { label: string; variant: string } {
  if (!outcome) return { label: 'Pending', variant: 'default' }
  switch (outcome) {
    case 'success': return { label: 'Success', variant: 'success' }
    case 'failure': return { label: 'Failure', variant: 'danger' }
    case 'partial': return { label: 'Partial', variant: 'warning' }
    case 'abandoned': return { label: 'Abandoned', variant: 'muted' }
    default: return { label: outcome, variant: 'default' }
  }
}

function formatDuration(startEpoch: number, endEpoch: number | null): string {
  if (!startEpoch) return 'Unknown'
  const end = endEpoch || Date.now()
  const diffMs = end - startEpoch
  const minutes = Math.floor(diffMs / 60000)
  if (minutes < 60) return `${minutes}m`
  const hours = Math.floor(minutes / 60)
  const remaining = minutes % 60
  return `${hours}h ${remaining}m`
}

function shortProject(project: string): string {
  const parts = project.split('/')
  return parts[parts.length - 1] || project
}

async function loadSession() {
  abortController?.abort()
  abortController = new AbortController()
  loading.value = true
  error.value = null

  try {
    const resp = await fetch(`/api/sessions?claudeSessionId=${encodeURIComponent(sessionId.value)}`, {
      signal: abortController.signal,
    })
    if (!resp.ok) throw new Error(`HTTP ${resp.status}: ${resp.statusText}`)
    session.value = await resp.json()
  } catch (err) {
    if (err instanceof Error && err.name === 'AbortError') return
    error.value = err instanceof Error ? err.message : 'Failed to load session'
  } finally {
    loading.value = false
  }
}

async function loadInjections() {
  try {
    const resp = await fetch(`/api/sessions/${encodeURIComponent(sessionId.value)}/injections`)
    if (resp.ok) {
      const data = await resp.json()
      injections.value = data.injections || []
    }
  } catch {
    // Non-critical
  }
}

onMounted(() => {
  loadSession()
  loadInjections()
})

onBeforeUnmount(() => {
  abortController?.abort()
})
</script>

<template>
  <div>
    <!-- Back nav -->
    <button
      class="mb-4 text-sm text-slate-400 hover:text-white transition-colors flex items-center gap-1.5"
      @click="router.push('/sessions')"
    >
      <i class="fas fa-arrow-left text-xs" />
      Back to Sessions
    </button>

    <!-- Loading -->
    <div v-if="loading" class="flex items-center justify-center py-20">
      <i class="fas fa-circle-notch fa-spin text-claude-400 text-2xl" />
    </div>

    <!-- Error -->
    <div v-else-if="error" class="text-center py-16">
      <i class="fas fa-exclamation-triangle text-red-400 text-3xl mb-3 block" />
      <p class="text-red-400 mb-2">{{ error }}</p>
      <button @click="loadSession()" class="text-sm text-slate-400 hover:text-white transition-colors">
        Try again
      </button>
    </div>

    <!-- Session detail -->
    <template v-else-if="session">
      <!-- Header -->
      <div class="flex items-center gap-3 mb-6">
        <i class="fas fa-clock-rotate-left text-claude-400 text-xl" />
        <h1 class="text-2xl font-bold text-white">Session Detail</h1>
      </div>

      <!-- Metadata card -->
      <div class="p-5 rounded-xl border-2 border-slate-700/50 bg-gradient-to-br from-slate-800/50 to-slate-900/50 mb-6">
        <div class="grid grid-cols-2 md:grid-cols-4 gap-4">
          <!-- Outcome -->
          <div>
            <span class="text-xs text-slate-500 uppercase tracking-wide block mb-1">Outcome</span>
            <Badge :variant="outcomeBadge(session.outcome)?.variant as any">
              {{ outcomeBadge(session.outcome)?.label }}
            </Badge>
          </div>

          <!-- Prompts -->
          <div>
            <span class="text-xs text-slate-500 uppercase tracking-wide block mb-1">Prompts</span>
            <span class="text-lg font-semibold text-white">{{ session.prompt_counter || 0 }}</span>
          </div>

          <!-- Project -->
          <div>
            <span class="text-xs text-slate-500 uppercase tracking-wide block mb-1">Project</span>
            <span class="text-sm text-amber-600/80 font-mono">
              {{ session.project ? shortProject(session.project) : 'Unknown' }}
            </span>
          </div>

          <!-- Duration -->
          <div>
            <span class="text-xs text-slate-500 uppercase tracking-wide block mb-1">Duration</span>
            <span class="text-sm text-slate-200">
              {{ formatDuration(session.started_at_epoch, session.completed_at_epoch) }}
            </span>
          </div>
        </div>

        <!-- Second row -->
        <div class="grid grid-cols-2 md:grid-cols-4 gap-4 mt-4 pt-4 border-t border-slate-700/50">
          <!-- Started -->
          <div>
            <span class="text-xs text-slate-500 uppercase tracking-wide block mb-1">Started</span>
            <span class="text-sm text-slate-300">
              {{ session.started_at ? formatRelativeTime(session.started_at) : 'Unknown' }}
            </span>
          </div>

          <!-- Status -->
          <div>
            <span class="text-xs text-slate-500 uppercase tracking-wide block mb-1">Status</span>
            <span class="text-sm text-slate-300">{{ session.status || 'Unknown' }}</span>
          </div>

          <!-- Claude Session ID -->
          <div class="col-span-2">
            <span class="text-xs text-slate-500 uppercase tracking-wide block mb-1">Session ID</span>
            <code class="text-xs font-mono text-slate-500 break-all">{{ session.claude_session_id }}</code>
          </div>
        </div>

        <!-- User prompt -->
        <div v-if="session.user_prompt" class="mt-4 pt-4 border-t border-slate-700/50">
          <span class="text-xs text-slate-500 uppercase tracking-wide block mb-1">First Prompt</span>
          <p class="text-sm text-slate-300 whitespace-pre-wrap">{{ session.user_prompt }}</p>
        </div>

        <!-- Outcome reason -->
        <div v-if="session.outcome_reason" class="mt-4 pt-4 border-t border-slate-700/50">
          <span class="text-xs text-slate-500 uppercase tracking-wide block mb-1">Outcome Reason</span>
          <p class="text-sm text-slate-400">{{ session.outcome_reason }}</p>
        </div>
      </div>

      <!-- Injections section -->
      <div v-if="injections.length > 0" class="mb-6">
        <h2 class="text-lg font-semibold text-white mb-3 flex items-center gap-2">
          <i class="fas fa-syringe text-claude-400 text-sm" />
          Injection Records
          <span class="text-sm text-slate-500 font-normal">({{ injections.length }})</span>
        </h2>
        <div class="space-y-2">
          <div
            v-for="(inj, idx) in injections"
            :key="idx"
            class="p-3 rounded-lg border border-slate-700/50 bg-slate-800/30"
          >
            <div class="flex items-center justify-between">
              <div class="flex-1 min-w-0">
                <span class="text-sm text-white font-medium truncate block">
                  {{ inj.observation_title || `Observation #${inj.observation_id}` }}
                </span>
                <span class="text-xs text-slate-500">
                  {{ inj.observation_type || 'unknown' }}
                  <template v-if="inj.effectiveness_score != null">
                    &middot; Effectiveness: {{ (inj.effectiveness_score * 100).toFixed(0) }}%
                  </template>
                </span>
              </div>
              <span v-if="inj.injected_at" class="text-xs text-slate-600 flex-shrink-0">
                {{ formatRelativeTime(inj.injected_at) }}
              </span>
            </div>
          </div>
        </div>
      </div>

      <!-- Note about observations -->
      <div class="p-4 rounded-lg bg-slate-800/30 border border-slate-700/50">
        <div class="flex items-center gap-2 text-slate-400 text-sm">
          <i class="fas fa-info-circle text-slate-500" />
          <span>
            Observations for this session can be explored via the
            <router-link to="/observations" class="text-claude-400 hover:text-claude-300 underline">
              Observations
            </router-link>
            view filtered by project.
          </span>
        </div>
      </div>
    </template>
  </div>
</template>
