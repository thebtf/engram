<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { LayoutDashboard, CircleAlert, Lock, Key, Settings, Sun, Moon, LogOut } from 'lucide-vue-next'
import { useAuth } from '@/composables/useAuth'
import { useSSE } from '@/composables/useSSE'
import { useColorMode } from '@/composables/useColorMode'
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarRail,
  SidebarSeparator,
} from '@/components/ui/sidebar'

const route = useRoute()
const router = useRouter()
const { logout, authDisabled, isAdmin } = useAuth()
const { isConnected } = useSSE()
const { mode, toggleColorMode } = useColorMode()

interface NavItem {
  name: string
  label: string
  icon: typeof LayoutDashboard
  path: string
}

const allNavItems: NavItem[] = [
  { name: 'home', label: 'Home', icon: LayoutDashboard, path: '/' },
  { name: 'issues', label: 'Issues', icon: CircleAlert, path: '/issues' },
  { name: 'vault', label: 'Vault', icon: Lock, path: '/vault' },
  { name: 'tokens', label: 'Tokens', icon: Key, path: '/tokens' },
]

const navItems = computed(() =>
  authDisabled.value ? allNavItems.filter(item => item.name !== 'tokens') : allNavItems
)

function isActiveItem(item: NavItem): boolean {
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
  <Sidebar collapsible="icon">
    <SidebarHeader>
      <!-- Logo -->
      <div class="flex items-center gap-3 px-1 py-1">
        <div
          class="w-8 h-8 flex-shrink-0 rounded-lg bg-gradient-to-br from-claude-500 to-claude-700 flex items-center justify-center"
        >
          <span class="text-white font-bold text-sm">E</span>
        </div>
        <span class="text-sm font-bold truncate group-data-[collapsible=icon]:hidden">
          <span class="text-claude-400">engram</span>
        </span>
      </div>

      <!-- Auth-disabled warning badge -->
      <div
        v-if="authDisabled"
        class="flex items-center gap-2 px-2 py-1 rounded-md bg-yellow-500/10 border border-yellow-500/30 group-data-[collapsible=icon]:justify-center"
        title="Authentication is disabled — all requests are allowed without a token"
      >
        <span class="text-yellow-400 flex-shrink-0 text-xs">⚠</span>
        <span class="text-[10px] text-yellow-400 font-medium truncate group-data-[collapsible=icon]:hidden">
          Auth Disabled
        </span>
      </div>
    </SidebarHeader>

    <SidebarContent>
      <!-- Main navigation -->
      <SidebarMenu>
        <SidebarMenuItem v-for="item in navItems" :key="item.name">
          <SidebarMenuButton
            as-child
            :is-active="isActiveItem(item)"
            :tooltip="item.label"
          >
            <router-link :to="item.path">
              <component :is="item.icon" />
              <span>{{ item.label }}</span>
            </router-link>
          </SidebarMenuButton>
        </SidebarMenuItem>
      </SidebarMenu>

      <!-- Admin section (only for admin users) -->
      <template v-if="isAdmin">
        <SidebarSeparator />
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton
              as-child
              :is-active="route.path === '/admin'"
              tooltip="Admin"
            >
              <router-link to="/admin">
                <Settings />
                <span>Admin</span>
              </router-link>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </template>
    </SidebarContent>

    <SidebarFooter>
      <SidebarMenu>
        <!-- Theme toggle -->
        <SidebarMenuItem>
          <SidebarMenuButton
            :tooltip="mode === 'dark' ? 'Switch to light mode' : 'Switch to dark mode'"
            @click="toggleColorMode"
          >
            <Sun v-if="mode === 'dark'" />
            <Moon v-else />
            <span class="group-data-[collapsible=icon]:hidden">
              {{ mode === 'dark' ? 'Light mode' : 'Dark mode' }}
            </span>
          </SidebarMenuButton>
        </SidebarMenuItem>

        <!-- Connection status -->
        <SidebarMenuItem>
          <div
            class="flex items-center gap-2 px-2 py-1.5 text-xs text-sidebar-foreground/60"
            :title="isConnected ? 'Connected' : 'Disconnected'"
          >
            <span
              :class="[
                'w-2 h-2 rounded-full flex-shrink-0',
                isConnected ? 'bg-green-500' : 'bg-amber-500',
              ]"
            />
            <span class="truncate group-data-[collapsible=icon]:hidden">
              {{ isConnected ? 'Connected' : 'Disconnected' }}
            </span>
          </div>
        </SidebarMenuItem>

        <!-- Logout -->
        <SidebarMenuItem>
          <SidebarMenuButton
            tooltip="Logout"
            class="text-sidebar-foreground/70 hover:text-red-400"
            @click="handleLogout"
          >
            <LogOut />
            <span>Logout</span>
          </SidebarMenuButton>
        </SidebarMenuItem>
      </SidebarMenu>
    </SidebarFooter>

    <SidebarRail />
  </Sidebar>
</template>
