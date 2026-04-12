<script setup lang="ts">
import type { SearchResultObservation, ObservationType } from '@/types'
import { TYPE_CONFIG } from '@/types/observation'
import { CONCEPT_CONFIG, type ConceptType } from '@/types/observation'
import { formatRelativeTime } from '@/utils/formatters'
import Badge from '@/components/Badge.vue'
import EmptyState from '@/components/layout/EmptyState.vue'

defineProps<{
  results: SearchResultObservation[]
  loading: boolean
  query: string
  decisionMode?: boolean
  intent?: string
}>()

function getTypeConfig(type: string) {
  return TYPE_CONFIG[type as ObservationType] || TYPE_CONFIG.change
}

function getConceptConfig(concept: string) {
  return CONCEPT_CONFIG[concept as ConceptType] || { icon: 'fa-tag', colorClass: 'text-slate-300', bgClass: 'bg-slate-500/20', borderClass: 'border-slate-500/40' }
}

function shortProject(project: string): string {
  const parts = project.split('/')
  return parts[parts.length - 1] || project
}

function formatSimilarity(score?: number): string {
  if (score === undefined || score === null) return ''
  return `${Math.round(score * 100)}%`
}
</script>

<template>
  <!-- Loading -->
  <div v-if="loading" class="flex items-center justify-center py-16">
    <i class="fas fa-circle-notch fa-spin text-claude-400 text-2xl mr-3" />
    <span class="text-slate-400">Searching...</span>
  </div>

  <!-- No results -->
  <EmptyState
    v-else-if="query && results.length === 0"
    icon="fa-magnifying-glass"
    title="No results found"
    :description="`No observations match &quot;${query}&quot;. Try a different search term.`"
  />

  <!-- Results -->
  <div v-else-if="results.length > 0" class="space-y-3">
    <!-- Intent badge -->
    <div v-if="intent" class="flex items-center gap-2 mb-2">
      <span class="text-xs text-slate-500">Detected intent:</span>
      <Badge bg-class="bg-cyan-500/20" color-class="text-cyan-300" border-class="border-cyan-500/40" icon="fa-brain">
        {{ intent }}
      </Badge>
    </div>

    <!-- Result count -->
    <p class="text-xs text-slate-500">
      {{ results.length }} result{{ results.length !== 1 ? 's' : '' }}
      <span v-if="decisionMode" class="text-yellow-400/70">(decision mode)</span>
    </p>

    <!-- Result cards -->
    <RouterLink
      v-for="obs in results"
      :key="obs.id"
      :to="{ name: 'observation-detail', params: { id: obs.id } }"
      class="group block p-4 rounded-xl border-2 border-slate-700/50 bg-gradient-to-br from-slate-800/50 to-slate-900/50 hover:border-claude-500/30 cursor-pointer transition-all"
    >
      <div class="flex items-start gap-3">
        <!-- Type icon -->
        <div
          class="w-9 h-9 flex items-center justify-center rounded-lg bg-gradient-to-br flex-shrink-0"
          :class="getTypeConfig(obs.type).gradient"
        >
          <i :class="['fas', getTypeConfig(obs.type).icon, 'text-white text-xs']" />
        </div>

        <!-- Content -->
        <div class="flex-1 min-w-0">
          <!-- Header row -->
          <div class="flex items-center gap-2 mb-1 flex-wrap">
            <h3 class="text-sm font-medium text-white group-hover:text-claude-300 transition-colors">
              {{ obs.title || 'Untitled' }}
            </h3>
            <!-- Relevance score -->
            <span
              v-if="obs.similarity"
              class="text-[10px] font-mono px-1.5 py-0.5 rounded bg-green-500/10 text-green-400 border border-green-500/30"
            >
              {{ formatSimilarity(obs.similarity) }} match
            </span>
          </div>

          <!-- Meta -->
          <div class="flex items-center gap-2 text-xs text-slate-500 mb-2">
            <Badge
              :icon="getTypeConfig(obs.type).icon"
              :color-class="getTypeConfig(obs.type).colorClass"
              :bg-class="getTypeConfig(obs.type).bgClass"
              :border-class="getTypeConfig(obs.type).borderClass"
            >
              {{ obs.type }}
            </Badge>
            <span>{{ formatRelativeTime(obs.created_at) }}</span>
            <span v-if="obs.project" class="flex items-center gap-1">
              <span class="text-slate-600">|</span>
              <i class="fas fa-folder text-slate-600 text-[10px]" />
              <span class="text-amber-600/80 font-mono">{{ shortProject(obs.project) }}</span>
            </span>
          </div>

          <!-- Subtitle/narrative preview -->
          <p v-if="obs.subtitle || obs.narrative" class="text-xs text-slate-400 line-clamp-2 mb-2">
            {{ obs.subtitle || obs.narrative }}
          </p>

          <!-- Concepts -->
          <div v-if="obs.concepts?.length > 0" class="flex flex-wrap gap-1 mb-2">
            <Badge
              v-for="concept in obs.concepts.slice(0, 5)"
              :key="concept"
              :icon="getConceptConfig(concept).icon"
              :color-class="getConceptConfig(concept).colorClass"
              :bg-class="getConceptConfig(concept).bgClass"
              :border-class="getConceptConfig(concept).borderClass"
            >
              {{ concept }}
            </Badge>
            <span v-if="obs.concepts.length > 5" class="text-[10px] text-slate-500">
              +{{ obs.concepts.length - 5 }} more
            </span>
          </div>

          <!-- Rejected alternatives (decision mode) -->
          <div v-if="decisionMode && obs.rejected?.length" class="mt-2 p-2 rounded-lg bg-red-500/5 border border-red-500/20">
            <span class="text-[10px] text-red-400/70 uppercase tracking-wide">Rejected:</span>
            <div class="mt-1 space-y-0.5">
              <div v-for="(alt, idx) in obs.rejected.slice(0, 3)" :key="idx" class="flex items-start gap-1.5 text-xs text-slate-400">
                <i class="fas fa-times text-red-400/40 mt-0.5 text-[10px]" />
                <span>{{ alt }}</span>
              </div>
              <span v-if="obs.rejected.length > 3" class="text-[10px] text-slate-500 ml-4">
                +{{ obs.rejected.length - 3 }} more
              </span>
            </div>
          </div>
        </div>
      </div>
    </RouterLink>
  </div>
</template>
