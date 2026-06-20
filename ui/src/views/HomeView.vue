<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { useStats } from '@/composables'
import { useHealth } from '@/composables'
import { useUiI18n } from '@/composables/useUiI18n'
import { fetchIssues, type Issue } from '@/utils/api'
import { formatUptime, formatRelativeTime, truncate } from '@/utils/formatters'
import { cn } from '@/lib/utils'

import { Card, CardHeader, CardTitle, CardContent } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

import { Activity, Users, Search, Zap, ArrowRight, Circle } from 'lucide-vue-next'

const router = useRouter()
const { stats } = useStats()
const { health } = useHealth()
const { locale, t } = useUiI18n()

// Recent issues — fetched once on mount (top 5 open)
const recentIssues = ref<Issue[]>([])
const issuesLoading = ref(false)

onMounted(async () => {
  issuesLoading.value = true
  try {
    const result = await fetchIssues(undefined, 'open,acknowledged', 5, 0)
    recentIssues.value = result.issues ?? []
  } catch {
    recentIssues.value = []
  } finally {
    issuesLoading.value = false
  }
})

// ---- helpers ----

function statusColor(status: 'healthy' | 'degraded' | 'unhealthy' | undefined): string {
  if (status === 'healthy') return 'text-green-500'
  if (status === 'degraded') return 'text-yellow-500'
  return 'text-red-500'
}

function statusBadgeVariant(status: 'healthy' | 'degraded' | 'unhealthy'): 'default' | 'secondary' | 'destructive' | 'outline' {
  if (status === 'healthy') return 'secondary'
  if (status === 'degraded') return 'outline'
  return 'destructive'
}

function priorityVariant(priority: Issue['priority']): 'default' | 'secondary' | 'destructive' | 'outline' {
  if (priority === 'critical') return 'destructive'
  if (priority === 'high') return 'default'
  if (priority === 'medium') return 'secondary'
  return 'outline'
}

function typeVariant(_type: Issue['type']): 'default' | 'secondary' | 'destructive' | 'outline' {
  return 'outline'
}

const uptimeDisplay = computed(() => {
  const uptime = stats.value?.uptime ?? health.value?.uptime
  if (!uptime) return '—'
  return formatUptime(uptime)
})

const overallStatus = computed(() => health.value?.overall ?? undefined)
const versionDisplay = computed(() => health.value?.version ?? '—')

function navigateToIssue(id: number) {
  router.push(`/issues/${id}`)
}
</script>

<template>
  <div class="flex flex-col gap-4 pt-4">

    <!-- Section 1: Server Status Header -->
    <Card>
      <CardContent class="pt-6">
        <div class="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-4">
          <!-- Left: title + version -->
          <div class="flex items-center gap-3">
            <span class="text-2xl font-bold tracking-tight text-foreground">{{ t.home.productName }}</span>
            <Badge variant="outline" class="text-xs font-mono">{{ versionDisplay }}</Badge>
          </div>
          <!-- Right: status + uptime -->
          <div class="flex items-center gap-4">
            <div class="flex items-center gap-1.5">
              <Circle
                :class="cn('size-3 fill-current', statusColor(overallStatus))"
              />
              <span :class="cn('text-sm font-medium capitalize', statusColor(overallStatus))">
                {{ overallStatus ?? t.home.loading }}
              </span>
            </div>
            <div class="text-sm text-muted-foreground">
              {{ t.home.metrics.uptime }}: <span class="font-medium text-foreground">{{ uptimeDisplay }}</span>
            </div>
          </div>
        </div>
      </CardContent>
    </Card>

    <!-- Section 2: Metric Cards -->
    <div class="grid grid-cols-2 lg:grid-cols-4 gap-3">
      <!-- Sessions Today -->
      <Card>
        <CardHeader class="pb-2">
          <div class="flex items-center gap-2 text-muted-foreground">
            <Activity class="size-4" />
            <span class="text-xs font-medium uppercase tracking-wide">{{ t.home.metrics.sessionsToday }}</span>
          </div>
        </CardHeader>
        <CardContent>
          <div class="text-3xl font-bold text-foreground">
            {{ stats?.sessionsToday ?? '—' }}
          </div>
        </CardContent>
      </Card>

      <!-- Connected Clients -->
      <Card>
        <CardHeader class="pb-2">
          <div class="flex items-center gap-2 text-muted-foreground">
            <Users class="size-4" />
            <span class="text-xs font-medium uppercase tracking-wide">{{ t.home.metrics.connectedClients }}</span>
          </div>
        </CardHeader>
        <CardContent>
          <div class="text-3xl font-bold text-foreground">
            {{ stats?.connectedClients ?? '—' }}
          </div>
        </CardContent>
      </Card>

      <!-- Retrieval Requests -->
      <Card>
        <CardHeader class="pb-2">
          <div class="flex items-center gap-2 text-muted-foreground">
            <Search class="size-4" />
            <span class="text-xs font-medium uppercase tracking-wide">{{ t.home.metrics.retrievalRequests }}</span>
          </div>
        </CardHeader>
        <CardContent>
          <div class="text-3xl font-bold text-foreground">
            {{ stats?.retrieval?.total_requests ?? '—' }}
          </div>
        </CardContent>
      </Card>

      <!-- Context Injections -->
      <Card>
        <CardHeader class="pb-2">
          <div class="flex items-center gap-2 text-muted-foreground">
            <Zap class="size-4" />
            <span class="text-xs font-medium uppercase tracking-wide">{{ t.home.metrics.contextInjections }}</span>
          </div>
        </CardHeader>
        <CardContent>
          <div class="text-3xl font-bold text-foreground">
            {{ stats?.retrieval?.context_injections ?? '—' }}
          </div>
        </CardContent>
      </Card>
    </div>

    <!-- Section 3: System Health -->
    <Card>
      <CardHeader>
        <CardTitle>{{ t.home.systemHealth }}</CardTitle>
      </CardHeader>
      <CardContent>
        <div
          v-if="health && health.components && health.components.length > 0"
          class="grid grid-cols-2 lg:grid-cols-4 gap-3"
        >
          <div
            v-for="component in health.components"
            :key="component.name"
            class="flex items-center justify-between border border-border bg-card p-3"
          >
            <span class="text-sm font-medium text-foreground capitalize">{{ component.name }}</span>
            <Badge :variant="statusBadgeVariant(component.status)">
              {{ component.status }}
            </Badge>
          </div>
        </div>
        <div v-else-if="!health" class="text-sm text-muted-foreground">
          {{ t.home.loading }}
        </div>
        <div v-else class="text-sm text-muted-foreground">
          {{ t.home.noHealth }}
        </div>
      </CardContent>
    </Card>

    <!-- Section 4: Recent Issues -->
    <Card>
      <CardHeader>
        <div class="flex items-center justify-between">
          <CardTitle>{{ t.home.recentIssues }}</CardTitle>
          <Button variant="ghost" size="sm" as-child>
            <router-link to="/issues" class="flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
              {{ t.home.viewAll }}
              <ArrowRight class="size-4" />
            </router-link>
          </Button>
        </div>
      </CardHeader>
      <CardContent>
        <div v-if="issuesLoading" class="text-sm text-muted-foreground py-4 text-center">
          {{ t.home.loading }}
        </div>
        <div v-else-if="recentIssues.length === 0" class="text-sm text-muted-foreground py-8 text-center">
          {{ t.home.noOpenIssues }}
        </div>
        <Table v-else>
          <TableHeader>
            <TableRow>
              <TableHead class="w-16">ID</TableHead>
              <TableHead class="w-24">{{ t.issues.table.priority }}</TableHead>
              <TableHead class="w-28">{{ t.issues.table.type }}</TableHead>
              <TableHead>{{ t.issues.table.title }}</TableHead>
              <TableHead class="w-28 text-right">{{ t.issues.table.created }}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow
              v-for="issue in recentIssues"
              :key="issue.id"
              class="cursor-pointer hover:bg-muted/50"
              @click="navigateToIssue(issue.id)"
            >
              <TableCell class="font-mono text-xs text-muted-foreground">#{{ issue.id }}</TableCell>
              <TableCell>
                <Badge :variant="priorityVariant(issue.priority)" class="capitalize">
                  {{ issue.priority }}
                </Badge>
              </TableCell>
              <TableCell>
                <Badge :variant="typeVariant(issue.type)" class="capitalize">
                  {{ issue.type }}
                </Badge>
              </TableCell>
              <TableCell class="text-sm text-foreground">
                {{ truncate(issue.title, 60) }}
              </TableCell>
              <TableCell class="text-xs text-muted-foreground text-right">
                {{ formatRelativeTime(issue.created_at, locale) }}
              </TableCell>
            </TableRow>
          </TableBody>
        </Table>
      </CardContent>
    </Card>

  </div>
</template>
