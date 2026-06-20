<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Database, RefreshCw } from 'lucide-vue-next'
import { useUiI18n } from '@/composables/useUiI18n'
import {
  fetchProjects,
  fetchSessions,
  type SDKSession,
} from '@/utils/api'
import { safeAbsoluteDate, truncate } from '@/utils/formatters'
import EmptyState from '@/components/layout/EmptyState.vue'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

interface ProjectRow {
  id: string
  sessionCountInWindow: number
  activeCount: number
  lastActivityEpoch: number
}

const projects = ref<string[]>([])
const sessions = ref<SDKSession[]>([])
const sessionTotal = ref(0)
const activeTab = ref('projects')
const sessionProjectFilter = ref('all')
const loading = ref(false)
const bootLoading = ref(true)
const error = ref<string | null>(null)
const { t } = useUiI18n()

function readNullableString(value: { String: string; Valid: boolean } | null | undefined): string {
  return value?.Valid ? value.String : ''
}

function sessionTimestamp(session: SDKSession): number {
  if (typeof session.started_at_epoch === 'number' && session.started_at_epoch > 0) {
    return session.started_at_epoch > 1_000_000_000_000
      ? session.started_at_epoch
      : session.started_at_epoch * 1000
  }
  const parsed = Date.parse(session.started_at)
  return Number.isNaN(parsed) ? 0 : parsed
}

function formatRelativeMoment(epoch: number): string {
  if (!epoch) return '—'

  const diff = Date.now() - epoch
  if (diff < 60_000) return 'сейчас'

  const minutes = Math.floor(diff / 60_000)
  if (minutes < 60) return `${minutes}м назад`

  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}ч назад`

  const days = Math.floor(hours / 24)
  if (days < 7) return `${days}д назад`

  return new Date(epoch).toLocaleDateString('ru-RU', {
    day: '2-digit',
    month: 'short',
    year: 'numeric',
  })
}

async function loadData() {
  loading.value = true
  error.value = null

  try {
    const [projectList, sessionList] = await Promise.all([
      fetchProjects(),
      fetchSessions({ limit: 200 }),
    ])

    projects.value = projectList
    sessions.value = (sessionList.sessions ?? []).slice().sort((a, b) => sessionTimestamp(b) - sessionTimestamp(a))
    sessionTotal.value = sessionList.total ?? sessions.value.length
  } catch (err: any) {
    error.value = err?.message || t.value.projects.loadError
    projects.value = []
    sessions.value = []
    sessionTotal.value = 0
  } finally {
    loading.value = false
    bootLoading.value = false
  }
}

onMounted(async () => {
  await loadData()
})

const projectRows = computed<ProjectRow[]>(() => {
  const ids = Array.from(new Set([
    ...projects.value,
    ...sessions.value.map(session => session.project).filter(Boolean),
  ]))

  return ids
    .map(id => {
      const projectSessions = sessions.value.filter(session => session.project === id)
      const lastActivityEpoch = projectSessions.reduce((latest, session) => {
        const candidate = sessionTimestamp(session)
        return candidate > latest ? candidate : latest
      }, 0)

      return {
        id,
        sessionCountInWindow: projectSessions.length,
        activeCount: projectSessions.filter(session => session.status === 'active').length,
        lastActivityEpoch,
      }
    })
    .sort((a, b) => {
      if (b.lastActivityEpoch !== a.lastActivityEpoch) {
        return b.lastActivityEpoch - a.lastActivityEpoch
      }
      return a.id.localeCompare(b.id)
    })
})

const filteredSessions = computed(() => {
  if (sessionProjectFilter.value === 'all') {
    return sessions.value
  }
  return sessions.value.filter(session => session.project === sessionProjectFilter.value)
})

const activeSessionsCount = computed(() =>
  sessions.value.filter(session => session.status === 'active').length,
)

const sessionWindowNote = computed(() => {
  if (sessionTotal.value > sessions.value.length) {
    return t.value.projects.notes.windowSlice
      .replace('{shown}', String(sessions.value.length))
      .replace('{total}', String(sessionTotal.value))
  }
  return t.value.projects.notes.windowAll.replace('{shown}', String(sessions.value.length))
})

function statusVariant(status: string): 'default' | 'secondary' | 'destructive' | 'outline' {
  if (status === 'active') return 'default'
  if (status === 'completed') return 'secondary'
  if (status === 'failed') return 'destructive'
  return 'outline'
}
</script>

<template>
  <div class="space-y-4 pt-4">
    <div class="flex items-center justify-between gap-3 flex-wrap">
      <div class="flex items-center gap-3">
        <Database class="size-5 text-primary" />
        <div>
          <h1 class="text-lg font-semibold">{{ t.projects.title }}</h1>
          <p class="text-sm text-muted-foreground">
            {{ t.projects.subtitle }}
          </p>
        </div>
      </div>
      <Button variant="outline" size="sm" :disabled="loading" @click="loadData">
        <RefreshCw :class="['size-4', loading && 'animate-spin']" />
        {{ t.common.refresh }}
      </Button>
    </div>

    <div class="grid gap-3 md:grid-cols-3">
      <Card>
        <CardHeader class="pb-2">
          <CardDescription>{{ t.projects.metrics.projects }}</CardDescription>
          <CardTitle class="text-2xl">{{ projectRows.length }}</CardTitle>
        </CardHeader>
        <CardContent class="text-xs text-muted-foreground">
          `/api/projects`
        </CardContent>
      </Card>

      <Card>
        <CardHeader class="pb-2">
          <CardDescription>{{ t.projects.metrics.sessionsWindow }}</CardDescription>
          <CardTitle class="text-2xl">{{ sessions.length }}</CardTitle>
        </CardHeader>
        <CardContent class="text-xs text-muted-foreground">
          {{ t.projects.metrics.totalServer }}: {{ sessionTotal }}
        </CardContent>
      </Card>

      <Card>
        <CardHeader class="pb-2">
          <CardDescription>{{ t.projects.metrics.activeSessions }}</CardDescription>
          <CardTitle class="text-2xl">{{ activeSessionsCount }}</CardTitle>
        </CardHeader>
        <CardContent class="text-xs text-muted-foreground">
          {{ t.projects.metrics.activeStatus }}
        </CardContent>
      </Card>
    </div>

    <div v-if="bootLoading" class="text-sm text-muted-foreground">
      {{ t.projects.loading }}
    </div>

    <div v-else-if="error" class="rounded-lg border border-destructive/30 bg-destructive/10 p-4 text-sm text-destructive">
      {{ error }}
    </div>

    <EmptyState
      v-else-if="projectRows.length === 0 && sessions.length === 0"
      icon="fa-folder-tree"
      :title="t.projects.emptyTitle"
      :description="t.projects.emptyDescription"
    />

    <Tabs v-else v-model="activeTab" class="space-y-4">
      <TabsList class="grid w-full max-w-md grid-cols-2">
        <TabsTrigger value="projects">{{ t.projects.tabs.projects }}</TabsTrigger>
        <TabsTrigger value="sessions">{{ t.projects.tabs.sessions }}</TabsTrigger>
      </TabsList>

      <TabsContent value="projects">
        <Card>
          <CardHeader class="pb-3">
            <CardTitle class="text-sm font-medium">{{ t.projects.registry.title }}</CardTitle>
            <CardDescription>
              {{ sessionWindowNote }}
            </CardDescription>
          </CardHeader>
          <CardContent class="pt-0">
            <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{{ t.projects.registry.projectId }}</TableHead>
                <TableHead class="w-32 text-right">{{ t.projects.registry.sessionWindow }}</TableHead>
                <TableHead class="w-28 text-right">{{ t.projects.registry.active }}</TableHead>
                <TableHead class="w-40 text-right">{{ t.projects.registry.lastActivity }}</TableHead>
              </TableRow>
            </TableHeader>
              <TableBody>
                <TableRow v-for="project in projectRows" :key="project.id">
                  <TableCell>
                    <div class="space-y-1">
                      <p class="font-medium text-foreground">{{ project.id }}</p>
                      <p class="text-xs text-muted-foreground">
                        {{ t.projects.registry.liveRegistryId }}
                      </p>
                    </div>
                  </TableCell>
                  <TableCell class="text-right font-mono">{{ project.sessionCountInWindow }}</TableCell>
                  <TableCell class="text-right font-mono">{{ project.activeCount }}</TableCell>
                  <TableCell class="text-right text-sm text-muted-foreground">
                    {{ formatRelativeMoment(project.lastActivityEpoch) }}
                  </TableCell>
                </TableRow>
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      </TabsContent>

      <TabsContent value="sessions" class="space-y-4">
        <div class="flex items-center justify-between gap-3 flex-wrap">
          <p class="text-sm text-muted-foreground">
            {{ sessionWindowNote }}
          </p>
          <Select v-model="sessionProjectFilter">
            <SelectTrigger class="w-[220px]">
              <SelectValue :placeholder="t.projects.sessions.project" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{{ t.projects.sessions.filterAll }}</SelectItem>
              <SelectItem v-for="project in projectRows" :key="project.id" :value="project.id">
                {{ project.id }}
              </SelectItem>
            </SelectContent>
          </Select>
        </div>

        <Card v-if="filteredSessions.length > 0">
          <CardHeader class="pb-3">
            <CardTitle class="text-sm font-medium">{{ t.projects.sessions.title }}</CardTitle>
            <CardDescription>
              {{ t.projects.sessions.countInFilter.replace('{count}', String(filteredSessions.length)) }}
            </CardDescription>
          </CardHeader>
          <CardContent class="pt-0">
            <Table>
            <TableHeader>
              <TableRow>
                <TableHead class="w-24">{{ t.projects.sessions.session }}</TableHead>
                <TableHead class="w-36">{{ t.projects.sessions.project }}</TableHead>
                <TableHead>{{ t.projects.sessions.prompt }}</TableHead>
                <TableHead class="w-28">{{ t.projects.sessions.state }}</TableHead>
                <TableHead class="w-28 text-right">{{ t.projects.sessions.promptCount }}</TableHead>
                <TableHead class="w-40 text-right">{{ t.projects.sessions.started }}</TableHead>
              </TableRow>
            </TableHeader>
              <TableBody>
                <TableRow v-for="session in filteredSessions" :key="session.id">
                  <TableCell class="font-mono text-xs text-muted-foreground">
                    #{{ session.id }}
                  </TableCell>
                  <TableCell>
                    <div class="space-y-1">
                      <p class="text-sm text-foreground">{{ session.project }}</p>
                      <p class="text-[11px] text-muted-foreground">
                        {{ truncate(session.claude_session_id, 18) }}
                      </p>
                    </div>
                  </TableCell>
                  <TableCell>
                    <div class="space-y-1">
                      <p class="text-sm text-foreground">
                        {{ truncate(readNullableString(session.user_prompt) || t.projects.sessions.promptMissing, 84) }}
                      </p>
                      <p class="text-[11px] text-muted-foreground">
                        {{ t.projects.sessions.outcomeMode
                          .replace('{outcome}', readNullableString(session.outcome) || '—')
                          .replace('{mode}', readNullableString(session.injection_strategy) || '—') }}
                      </p>
                    </div>
                  </TableCell>
                  <TableCell>
                    <Badge :variant="statusVariant(session.status)">
                      {{ session.status }}
                    </Badge>
                  </TableCell>
                  <TableCell class="text-right font-mono">
                    {{ session.prompt_counter }}
                  </TableCell>
                  <TableCell class="text-right text-sm text-muted-foreground">
                    <div>{{ formatRelativeMoment(sessionTimestamp(session)) }}</div>
                    <div class="text-[11px]">{{ safeAbsoluteDate(session.started_at) }}</div>
                  </TableCell>
                </TableRow>
              </TableBody>
            </Table>
          </CardContent>
        </Card>

        <EmptyState
          v-else
          icon="fa-clock-rotate-left"
          :title="t.projects.sessions.emptyTitle"
          :description="t.projects.sessions.emptyDescription"
        />
      </TabsContent>
    </Tabs>

    <div class="grid gap-3 lg:grid-cols-2">
      <Card class="border-dashed">
        <CardHeader class="pb-3">
          <CardTitle class="text-sm font-medium">{{ t.projects.notes.clientsTitle }}</CardTitle>
          <CardDescription>{{ t.projects.notes.clientsCaption }}</CardDescription>
        </CardHeader>
        <CardContent class="text-sm text-muted-foreground">
          {{ t.projects.notes.clientsDescription }}
        </CardContent>
      </Card>

      <Card class="border-dashed">
        <CardHeader class="pb-3">
          <CardTitle class="text-sm font-medium">{{ t.projects.notes.codeIntelTitle }}</CardTitle>
          <CardDescription>{{ t.projects.notes.codeIntelCaption }}</CardDescription>
        </CardHeader>
        <CardContent class="text-sm text-muted-foreground">
          {{ t.projects.notes.codeIntelDescription }}
        </CardContent>
      </Card>
    </div>
  </div>
</template>
