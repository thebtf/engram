<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from '#imports'
import StatusBadge from '~/components/surfaces/StatusBadge.vue'
import { navGroups } from '~/utils/navigation'

const route = useRoute()

function isActive(to: string): boolean {
  if (to === '/') return route.path === '/'
  return route.path === to || route.path.startsWith(`${to}/`)
}

const groups = computed(() => navGroups)
</script>

<template>
  <aside class="w-full border-b border-[var(--border)] bg-[var(--surface)] p-4 lg:h-screen lg:w-[264px] lg:border-b-0 lg:border-r">
    <div class="mb-6 flex items-center gap-3">
      <div class="flex h-10 w-10 items-center justify-center rounded-xl border border-[var(--border)] bg-[var(--surface-warm)] text-sm font-bold">
        E
      </div>
      <div>
        <div class="text-sm font-semibold">engram</div>
        <div class="text-xs text-[var(--muted)]">operator web foundation</div>
      </div>
    </div>

    <nav class="space-y-5">
      <section v-for="group in groups" :key="group.label" class="space-y-2">
        <div class="px-2 text-[11px] font-bold uppercase tracking-[0.08em] text-[var(--muted)]">
          {{ group.label }}
        </div>

        <div class="space-y-1">
          <NuxtLink
            v-for="item in group.items"
            :key="item.to"
            :to="item.to"
            class="block rounded-xl border px-3 py-2 transition-colors"
            :class="isActive(item.to)
              ? 'border-[var(--accent)] bg-[color:rgba(76,141,255,0.12)] text-[var(--fg)]'
              : 'border-transparent bg-transparent text-[var(--fg-2)] hover:border-[var(--border)] hover:bg-[var(--surface-warm)] hover:text-[var(--fg)]'"
          >
            <div class="flex items-center justify-between gap-3">
              <span class="text-sm font-medium">{{ item.label }}</span>
              <StatusBadge v-if="item.tone" :tone="item.tone" compact />
            </div>
            <div v-if="item.note" class="mt-1 text-xs text-[var(--muted)]">
              {{ item.note }}
            </div>
          </NuxtLink>
        </div>
      </section>
    </nav>
  </aside>
</template>
