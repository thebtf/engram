<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { CircleAlert, Lock, Key, Settings, Monitor, Sun, Moon, LogOut } from 'lucide-vue-next'
import { useColorMode } from '@/composables/useColorMode'
import { useAuth } from '@/composables/useAuth'
import { useSSE } from '@/composables/useSSE'
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
const { preference, cycleColorMode } = useColorMode()

interface NavItem {
  name: string
  label: string
  icon: typeof CircleAlert
  path: string
}

const allNavItems: NavItem[] = [
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
  <Sidebar collapsible="icon" variant="inset">
    <SidebarHeader>
      <!-- Logo (links to home) -->
      <router-link to="/" class="flex items-center gap-3 px-1 py-1 rounded-md hover:bg-sidebar-accent transition-colors">
        <div
          class="w-8 h-8 flex-shrink-0 rounded-lg bg-gradient-to-br from-primary to-primary/70 flex items-center justify-center"
        >
          <span class="text-primary-foreground font-bold text-sm">E</span>
        </div>
        <span class="text-sm font-bold truncate group-data-[collapsible=icon]:hidden text-primary">
          engram
        </span>
      </router-link>

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

    <SidebarContent class="px-2">
      <!-- Main navigation -->
      <SidebarMenu class="gap-1">
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
        <!-- System -->
        <SidebarMenuItem>
          <SidebarMenuButton
            as-child
            :is-active="route.path === '/system'"
            tooltip="System"
          >
            <router-link to="/system">
              <Monitor />
              <span>System</span>
            </router-link>
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

        <!-- Logout + Theme icon row -->
        <SidebarMenuItem>
          <div class="flex items-center justify-between px-2 py-1">
            <SidebarMenuButton
              tooltip="Logout"
              class="text-sidebar-foreground/70 hover:text-red-400 flex-1"
              @click="handleLogout"
            >
              <LogOut />
              <span>Logout</span>
            </SidebarMenuButton>
            <button
              class="p-1.5 rounded-md text-sidebar-foreground/50 hover:text-sidebar-foreground hover:bg-sidebar-accent transition-colors group-data-[collapsible=icon]:hidden"
              :title="`Theme: ${preference}`"
              @click="cycleColorMode"
            >
              <Sun v-if="preference === 'light'" :size="14" />
              <Moon v-if="preference === 'dark'" :size="14" />
              <Monitor v-if="preference === 'auto'" :size="14" />
            </button>
          </div>
        </SidebarMenuItem>
      </SidebarMenu>
    </SidebarFooter>

    <SidebarRail />
  </Sidebar>
</template>
