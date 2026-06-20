<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { CircleAlert, Lock, Key, Settings, Server, Sun, Moon, Monitor, LogOut, House, Database, Folder, Shield } from 'lucide-vue-next'
import { useColorMode } from '@/composables/useColorMode'
import { useAuth } from '@/composables/useAuth'
import { useSSE } from '@/composables/useSSE'
import { useUiI18n } from '@/composables/useUiI18n'
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarRail,
} from '@/components/ui/sidebar'

const route = useRoute()
const router = useRouter()
const { logout, authDisabled, isAdmin } = useAuth()
const { isConnected } = useSSE()
const { preference, cycleColorMode } = useColorMode()
const { t } = useUiI18n()

interface NavItem {
  name: string
  label: string
  icon: typeof CircleAlert
  path: string
  state?: 'live' | 'support'
}

interface NavSection {
  label: string
  items: NavItem[]
}

const allNavSections = computed<NavSection[]>(() => [
  {
    label: t.value.sidebar.sections.workspace,
    items: [
      { name: 'home', label: t.value.sidebar.items.home, icon: House, path: '/', state: 'live' },
    ],
  },
  {
    label: t.value.sidebar.sections.memory,
    items: [
      { name: 'memories', label: t.value.sidebar.items.memories, icon: Database, path: '/memories', state: 'live' },
    ],
  },
  {
    label: t.value.sidebar.sections.work,
    items: [
      { name: 'rules', label: t.value.sidebar.items.rules, icon: Shield, path: '/rules', state: 'live' },
      { name: 'issues', label: t.value.sidebar.items.issues, icon: CircleAlert, path: '/issues', state: 'live' },
      { name: 'projects', label: t.value.sidebar.items.projects, icon: Folder, path: '/projects', state: 'live' },
    ],
  },
  {
    label: t.value.sidebar.sections.storage,
    items: [
      { name: 'vault', label: t.value.sidebar.items.vault, icon: Lock, path: '/vault', state: 'live' },
      { name: 'tokens', label: t.value.sidebar.items.tokens, icon: Key, path: '/tokens', state: 'support' },
    ],
  },
  {
    label: t.value.sidebar.sections.admin,
    items: [
      { name: 'system', label: t.value.sidebar.items.system, icon: Server, path: '/system', state: 'live' },
      { name: 'settings', label: t.value.sidebar.items.settings, icon: Settings, path: '/settings', state: 'live' },
    ],
  },
])

const navSections = computed(() =>
  allNavSections.value
    .map(section => ({
      ...section,
      items: section.items.filter(item => !(authDisabled.value && item.name === 'tokens')),
    }))
    .filter(section => section.items.length > 0),
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

function stateDotClass(state?: NavItem['state']): string {
  if (state === 'support') return 'bg-amber-500'
  return 'bg-emerald-500'
}
</script>

<template>
  <Sidebar collapsible="icon" variant="inset">
    <SidebarHeader>
      <!-- Logo (links to home) -->
      <router-link to="/" class="flex items-center gap-3 px-1 py-1 rounded-md hover:bg-sidebar-accent transition-colors">
        <img
          src="/branding/favicon-32.svg"
          alt="Engram"
          width="32"
          height="32"
          class="w-8 h-8 flex-shrink-0 rounded-lg"
        />
        <span class="text-sm font-bold truncate group-data-[collapsible=icon]:hidden text-primary">
          engram
        </span>
      </router-link>

      <!-- Auth-disabled warning badge -->
      <div
        v-if="authDisabled"
        class="flex items-center gap-2 px-2 py-1 rounded-md bg-yellow-500/10 border border-yellow-500/30 group-data-[collapsible=icon]:justify-center"
        title="Аутентификация отключена: запросы разрешены без токена"
      >
        <span class="text-yellow-400 flex-shrink-0 text-xs">⚠</span>
        <span class="text-[10px] text-yellow-400 font-medium truncate group-data-[collapsible=icon]:hidden">
          {{ t.sidebar.authDisabled }}
        </span>
      </div>
    </SidebarHeader>

    <SidebarContent class="px-2">
      <div class="space-y-4">
        <section v-for="section in navSections" :key="section.label" class="space-y-1">
          <p class="px-2 text-[10px] uppercase tracking-[0.08em] text-sidebar-foreground/50 group-data-[collapsible=icon]:hidden">
            {{ section.label }}
          </p>
          <SidebarMenu class="gap-1">
            <SidebarMenuItem v-for="item in section.items" :key="item.name">
              <SidebarMenuButton
                as-child
                :is-active="isActiveItem(item)"
                :tooltip="item.label"
              >
                <router-link :to="item.path" class="flex items-center gap-2">
                  <span :class="['h-2 w-2 rounded-full shrink-0 group-data-[collapsible=icon]:hidden', stateDotClass(item.state)]" />
                  <component :is="item.icon" />
                  <span>{{ item.label }}</span>
                </router-link>
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
        </section>

        <section v-if="isAdmin" class="space-y-1">
          <p class="px-2 text-[10px] uppercase tracking-[0.08em] text-sidebar-foreground/50 group-data-[collapsible=icon]:hidden">
            {{ t.sidebar.sections.service }}
          </p>
          <SidebarMenu class="gap-1">
            <SidebarMenuItem>
              <SidebarMenuButton
                as-child
                :is-active="route.path === '/admin'"
                :tooltip="t.sidebar.items.admin"
              >
                <router-link to="/admin" class="flex items-center gap-2">
                  <span class="h-2 w-2 rounded-full bg-sky-500 shrink-0 group-data-[collapsible=icon]:hidden" />
                  <Shield />
                  <span>{{ t.sidebar.items.admin }}</span>
                </router-link>
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
        </section>
      </div>
    </SidebarContent>

    <SidebarFooter>
      <SidebarMenu>
        <!-- Connection status -->
        <SidebarMenuItem>
          <div
            class="flex items-center gap-2 px-2 py-1.5 text-xs text-sidebar-foreground/60"
            :title="isConnected ? t.sidebar.connected : t.sidebar.disconnected"
          >
            <span
              :class="[
                'w-2 h-2 rounded-full flex-shrink-0',
                isConnected ? 'bg-green-500' : 'bg-amber-500',
              ]"
            />
            <span class="truncate group-data-[collapsible=icon]:hidden">
              {{ isConnected ? t.sidebar.connected : t.sidebar.disconnected }}
            </span>
          </div>
        </SidebarMenuItem>

        <!-- Logout + Theme icon row -->
        <SidebarMenuItem>
          <div class="flex items-center justify-between px-2 py-1">
            <SidebarMenuButton
              :tooltip="t.sidebar.logout"
              class="text-sidebar-foreground/70 hover:text-red-400 flex-1"
              @click="handleLogout"
            >
              <LogOut />
              <span>{{ t.sidebar.logout }}</span>
            </SidebarMenuButton>
            <button
              class="p-1.5 rounded-md text-sidebar-foreground/50 hover:text-sidebar-foreground hover:bg-sidebar-accent transition-colors group-data-[collapsible=icon]:hidden"
              :title="`${t.app.themeTitle}: ${preference}`"
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
