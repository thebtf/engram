<script setup lang="ts">
import { ref, computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuth } from '@/composables/useAuth'
import { useSSE } from '@/composables/useSSE'

const route = useRoute()
const router = useRouter()
const { logout } = useAuth()
const { isConnected } = useSSE()

const collapsed = ref(localStorage.getItem('nav-sidebar-collapsed') === 'true')

interface NavItem {
  name: string
  label: string
  icon: string
  path: string
}

const navItems: NavItem[] = [
  { name: 'home', label: 'Home', icon: 'fa-house', path: '/' },
  { name: 'observations', label: 'Observations', icon: 'fa-brain', path: '/observations' },
  { name: 'search', label: 'Search', icon: 'fa-magnifying-glass', path: '/search' },
  { name: 'vault', label: 'Vault', icon: 'fa-vault', path: '/vault' },
  { name: 'logs', label: 'Logs', icon: 'fa-terminal', path: '/logs' },
  { name: 'graph', label: 'Graph', icon: 'fa-diagram-project', path: '/graph' },
  { name: 'patterns', label: 'Patterns', icon: 'fa-puzzle-piece', path: '/patterns' },
  { name: 'sessions', label: 'Sessions', icon: 'fa-clock-rotate-left', path: '/sessions' },
  { name: 'analytics', label: 'Analytics', icon: 'fa-chart-line', path: '/analytics' },
  { name: 'system', label: 'System', icon: 'fa-server', path: '/system' },
  { name: 'tokens', label: 'Tokens', icon: 'fa-key', path: '/tokens' },
]

const sidebarWidth = computed(() => (collapsed.value ? 'w-14' : 'w-60'))

function toggleCollapse() {
  collapsed.value = !collapsed.value
  localStorage.setItem('nav-sidebar-collapsed', String(collapsed.value))
}

function isActive(item: NavItem): boolean {
  if (item.path === '/') {
    return route.path === '/'
  }
  return route.path.startsWith(item.path)
}

async function handleLogout() {
  await logout()
  router.push({ name: 'login' })
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
    </nav>

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
