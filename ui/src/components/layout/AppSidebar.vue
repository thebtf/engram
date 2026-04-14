<script setup lang="ts">
import { ref, computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuth } from '@/composables/useAuth'
import { useSSE } from '@/composables/useSSE'
import { useStats } from '@/composables/useStats'
import { useHealth } from '@/composables/useHealth'

const route = useRoute()
const router = useRouter()
const { logout, authDisabled, isAdmin } = useAuth()
const { isConnected } = useSSE()
const { stats } = useStats()
const { health } = useHealth()

const collapsed = ref(localStorage.getItem('nav-sidebar-collapsed') === 'true')

interface NavItem {
  name: string
  label: string
  icon: string
  path: string
}

const allNavItems: NavItem[] = [
  { name: 'home', label: 'Home', icon: 'fa-house', path: '/' },
  { name: 'observations', label: 'Observations', icon: 'fa-brain', path: '/observations' },
  { name: 'search', label: 'Search', icon: 'fa-magnifying-glass', path: '/search' },
  { name: 'vault', label: 'Vault', icon: 'fa-vault', path: '/vault' },
  { name: 'monitor', label: 'Monitor', icon: 'fa-display', path: '/monitor' },
  { name: 'graph', label: 'Graph', icon: 'fa-diagram-project', path: '/graph' },
  { name: 'patterns', label: 'Patterns', icon: 'fa-puzzle-piece', path: '/patterns' },
  { name: 'issues', label: 'Issues', icon: 'fa-circle-exclamation', path: '/issues' },
  { name: 'sessions', label: 'Sessions', icon: 'fa-clock-rotate-left', path: '/sessions' },
  { name: 'analytics', label: 'Analytics', icon: 'fa-chart-line', path: '/analytics' },
  { name: 'learning', label: 'Learning', icon: 'fa-graduation-cap', path: '/learning' },
  { name: 'system', label: 'System', icon: 'fa-server', path: '/system' },
  { name: 'tokens', label: 'Tokens', icon: 'fa-key', path: '/tokens' },
]

const navItems = computed(() =>
  authDisabled.value ? allNavItems.filter(item => item.name !== 'tokens') : allNavItems
)

const sidebarWidth = computed(() => (collapsed.value ? 'w-14' : 'w-60'))

function toggleCollapse() {
  collapsed.value = !collapsed.value
  localStorage.setItem('nav-sidebar-collapsed', String(collapsed.value))
}

function isActive(item: NavItem): boolean {
  if (item.path === '/') {
    return route.path === '/'
  }
  return route.path === item.path || route.path.startsWith(item.path + '/')
}

async function handleLogout() {
  try {
    await logout()
  } finally {
    router.push({ name: 'login' })
  }
}
</script>

<template>
  <aside
    :class="[
      sidebarWidth,
      'flex-shrink-0 flex flex-col border-r border-slate-800 bg-slate-900/50 transition-all duration-200 ease-in-out h-screen sticky top-0',
    ]"
  >
    <!-- Logo area -->
    <div class="flex items-center gap-3 px-3 py-4 border-b border-slate-800">
      <div
        class="w-8 h-8 flex-shrink-0 rounded-lg bg-gradient-to-br from-claude-500 to-claude-700 flex items-center justify-center"
      >
        <i class="fas fa-brain text-sm text-white" />
      </div>
      <span v-if="!collapsed" class="text-sm font-bold text-white truncate">
        <span class="text-claude-400">Engram</span>
      </span>
    </div>

    <!-- Auth-disabled warning badge -->
    <div
      v-if="authDisabled"
      :class="[
        'flex items-center gap-2 px-3 py-1.5 bg-yellow-500/10 border-b border-yellow-500/30',
        collapsed ? 'justify-center' : '',
      ]"
      title="Authentication is disabled — all requests are allowed without a token"
    >
      <span class="text-yellow-400 flex-shrink-0">⚠</span>
      <span v-if="!collapsed" class="text-[10px] text-yellow-400 font-medium truncate">Auth Disabled</span>
    </div>

    <!-- Navigation -->
    <nav class="flex-1 overflow-y-auto py-2 px-2 space-y-0.5 scrollbar-thin">
      <router-link
        v-for="item in navItems"
        :key="item.name"
        :to="item.path"
        :class="[
          'flex items-center gap-3 px-3 py-2 rounded-lg text-sm transition-colors',
          isActive(item)
            ? 'bg-claude-500/20 text-claude-400 font-medium'
            : 'text-slate-400 hover:text-white hover:bg-slate-800/50',
        ]"
        :title="collapsed ? item.label : undefined"
      >
        <i :class="['fas', item.icon, 'w-4 text-center flex-shrink-0']" />
        <span v-if="!collapsed" class="truncate">{{ item.label }}</span>
      </router-link>

      <!-- Admin link: only visible to admin users -->
      <router-link
        v-if="isAdmin"
        to="/admin"
        :class="[
          'flex items-center gap-3 px-3 py-2 rounded-lg text-sm transition-colors',
          $route.path === '/admin'
            ? 'bg-claude-500/20 text-claude-400 font-medium'
            : 'text-slate-400 hover:text-white hover:bg-slate-800/50',
        ]"
        title="Admin"
      >
        <i class="fas fa-shield-halved w-4 text-center flex-shrink-0" />
        <span v-if="!collapsed" class="truncate">Admin</span>
      </router-link>
    </nav>

    <!-- Stats Panel (only when expanded) -->
    <div v-if="!collapsed" class="border-t border-slate-800 px-3 py-2 space-y-3 overflow-y-auto max-h-[40vh]">
      <!-- System Health -->
      <div v-if="health && health.components && health.components.length" class="space-y-1">
        <div class="text-[10px] text-slate-500 uppercase tracking-wider font-medium">System Health</div>
        <div v-for="comp in health.components" :key="comp.name" class="flex items-center justify-between text-xs">
          <span class="text-slate-400 truncate">{{ comp.name }}</span>
          <span
            :class="[
              'font-medium capitalize',
              comp.status === 'healthy' ? 'text-green-400' : comp.status === 'degraded' ? 'text-yellow-400' : 'text-red-400',
            ]"
          >{{ comp.status }}</span>
        </div>
      </div>

      <!-- Memory Contents -->
      <div class="space-y-1">
        <div class="text-[10px] text-slate-500 uppercase tracking-wider font-medium">Memory</div>
        <div class="flex items-center justify-between text-xs">
          <span class="text-slate-400">Observations</span>
          <span class="text-white font-medium">{{ stats?.observationCount ?? '—' }}</span>
        </div>
        <div class="flex items-center justify-between text-xs">
          <span class="text-slate-400">Sessions Today</span>
          <span class="text-white font-medium">{{ stats?.sessionsToday ?? 0 }}</span>
        </div>
      </div>

      <!-- Retrieval Stats -->
      <div v-if="stats?.retrieval" class="space-y-1">
        <div class="text-[10px] text-slate-500 uppercase tracking-wider font-medium">Retrieval</div>
        <div class="flex items-center justify-between text-xs">
          <span class="text-slate-400">Requests</span>
          <span class="text-white font-medium">{{ stats.retrieval.total_requests ?? 0 }}</span>
        </div>
        <div class="flex items-center justify-between text-xs">
          <span class="text-slate-400">Injections</span>
          <span class="text-white font-medium">{{ stats.retrieval.context_injections ?? 0 }}</span>
        </div>
      </div>
    </div>

    <!-- Bottom section -->
    <div class="border-t border-slate-800 px-2 py-2 space-y-1">
      <!-- SSE connection status -->
      <div
        :class="[
          'flex items-center gap-3 px-3 py-2 text-sm',
          collapsed ? 'justify-center' : '',
        ]"
        :title="isConnected ? 'Connected' : 'Disconnected'"
      >
        <span
          :class="[
            'w-2 h-2 rounded-full flex-shrink-0',
            isConnected ? 'bg-green-500' : 'bg-red-500',
          ]"
        />
        <span v-if="!collapsed" class="text-slate-500 text-xs truncate">
          {{ isConnected ? 'Connected' : 'Disconnected' }}
        </span>
      </div>

      <!-- Logout -->
      <button
        :class="[
          'flex items-center gap-3 px-3 py-2 rounded-lg text-sm text-slate-400 hover:text-red-400 hover:bg-slate-800/50 transition-colors w-full',
          collapsed ? 'justify-center' : '',
        ]"
        title="Logout"
        @click="handleLogout"
      >
        <i class="fas fa-right-from-bracket w-4 text-center flex-shrink-0" />
        <span v-if="!collapsed" class="truncate">Logout</span>
      </button>

      <!-- Collapse toggle -->
      <button
        :class="[
          'flex items-center gap-3 px-3 py-2 rounded-lg text-sm text-slate-500 hover:text-slate-300 hover:bg-slate-800/50 transition-colors w-full',
          collapsed ? 'justify-center' : '',
        ]"
        :title="collapsed ? 'Expand sidebar' : 'Collapse sidebar'"
        @click="toggleCollapse"
      >
        <i
          :class="[
            'fas w-4 text-center flex-shrink-0 transition-transform duration-200',
            collapsed ? 'fa-chevron-right' : 'fa-chevron-left',
          ]"
        />
        <span v-if="!collapsed" class="truncate">Collapse</span>
      </button>
    </div>
  </aside>
</template>
