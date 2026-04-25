<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { Plus, Loader2, MessageSquare } from 'lucide-vue-next'
import { useIssues } from '@/composables/useIssues'
import { createIssue, fetchTrackedProjects } from '@/utils/api'
import { formatRelativeTime } from '@/utils/formatters'
import EmptyState from '@/components/layout/EmptyState.vue'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
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
import { cn } from '@/lib/utils'

const router = useRouter()
const { issues, total, loading, error, statusFilter, sourceProjectFilter, typeFilter, projectNames, load } = useIssues()
const myIssuesOnly = ref(false)

function toggleMyIssues() {
  myIssuesOnly.value = !myIssuesOnly.value
  sourceProjectFilter.value = myIssuesOnly.value ? 'dashboard' : ''
}

// --- New Issue modal state ---
const showNewIssue = ref(false)
const newTitle = ref('')
const newBody = ref('')
const newType = ref<'bug' | 'feature' | 'improvement' | 'task'>('task')
const newPriority = ref<'critical' | 'high' | 'medium' | 'low'>('medium')
const newTargetProject = ref('')
const trackedProjects = ref<string[]>([])
const creating = ref(false)
const createError = ref<string | null>(null)

// Restore last-used type and target project from localStorage (priority always resets to 'medium').
const STORAGE_KEY = 'engram:newIssueDefaults'

function loadDefaults() {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return
    const saved = JSON.parse(raw)
    if (saved.type) newType.value = saved.type
    if (saved.targetProject) newTargetProject.value = saved.targetProject
  } catch { /* ignore corrupt localStorage */ }
}

function saveDefaults() {
  localStorage.setItem(STORAGE_KEY, JSON.stringify({
    type: newType.value,
    targetProject: newTargetProject.value,
  }))
}

onMounted(async () => {
  load()
  trackedProjects.value = await fetchTrackedProjects()
  loadDefaults()
  // If saved target project is not in the list, fall back to first
  if (newTargetProject.value && !trackedProjects.value.includes(newTargetProject.value)) {
    newTargetProject.value = trackedProjects.value[0] || ''
  }
})

async function openNewIssue() {
  newTitle.value = ''
  newBody.value = ''
  // Keep type and targetProject from last submission (loaded from localStorage).
  // Priority always resets to 'medium' per issue #61 requirement.
  newPriority.value = 'medium'
  createError.value = null
  showNewIssue.value = true
}

async function submitNewIssue() {
  if (!newTitle.value.trim()) return
  creating.value = true
  createError.value = null
  try {
    await createIssue({
      title: newTitle.value.trim(),
      body: newBody.value.trim() || undefined,
      type: newType.value,
      priority: newPriority.value,
      target_project: newTargetProject.value,
    })
    saveDefaults()
    showNewIssue.value = false
    await load()
  } catch (err: any) {
    createError.value = err.message || 'Failed to create issue'
  } finally {
    creating.value = false
  }
}

// --- Display helpers ---
function statusBadgeClass(status: string): string {
  switch (status) {
    case 'open': return 'bg-blue-100 text-blue-800 dark:bg-blue-900/40 dark:text-blue-300 border-transparent'
    case 'acknowledged': return 'bg-purple-100 text-purple-800 dark:bg-purple-900/40 dark:text-purple-300 border-transparent'
    case 'resolved': return 'bg-green-100 text-green-800 dark:bg-green-900/40 dark:text-green-300 border-transparent'
    case 'reopened': return 'bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-300 border-transparent'
    case 'closed': return 'bg-muted text-muted-foreground border-transparent'
    case 'rejected': return 'bg-destructive/10 text-destructive border-transparent'
    default: return 'bg-muted text-muted-foreground border-transparent'
  }
}

function priorityBadgeClass(priority: string): string {
  switch (priority) {
    case 'critical': return 'bg-destructive/10 text-destructive border-transparent'
    case 'high': return 'bg-orange-100 text-orange-800 dark:bg-orange-900/30 dark:text-orange-400 border-transparent'
    case 'medium': return 'bg-yellow-100 text-yellow-800 dark:bg-yellow-900/30 dark:text-yellow-400 border-transparent'
    case 'low': return 'bg-secondary text-secondary-foreground border-transparent'
    default: return 'bg-secondary text-secondary-foreground border-transparent'
  }
}

function typeBadgeClass(type: string): string {
  switch (type) {
    case 'bug': return 'bg-red-100 text-red-700 dark:bg-red-900/40 dark:text-red-400 border-transparent'
    case 'feature': return 'bg-blue-100 text-blue-700 dark:bg-blue-900/40 dark:text-blue-400 border-transparent'
    case 'improvement': return 'bg-green-100 text-green-700 dark:bg-green-900/40 dark:text-green-400 border-transparent'
    case 'task': return 'bg-secondary text-secondary-foreground border-transparent'
    default: return 'bg-secondary text-secondary-foreground border-transparent'
  }
}

function shortProject(project: string): string {
  if (!project) return '?'
  if (projectNames.value[project]) return projectNames.value[project]
  const parts = project.split('/')
  return parts[parts.length - 1] || project
}

const statusGroups = [
  { value: 'open,acknowledged,resolved,reopened', label: 'Active' },
  { value: 'open', label: 'Open' },
  { value: 'acknowledged', label: 'Acknowledged' },
  { value: 'resolved', label: 'Resolved' },
  { value: 'reopened', label: 'Reopened' },
  { value: 'closed', label: 'Closed' },
  { value: 'rejected', label: 'Rejected' },
  { value: '', label: 'All' },
] as const

const issueTypes = [
  { value: '', label: 'All' },
  { value: 'bug', label: 'Bug' },
  { value: 'feature', label: 'Feature' },
  { value: 'improvement', label: 'Improvement' },
  { value: 'task', label: 'Task' },
] as const
</script>

<template>
  <div class="space-y-4">
    <!-- Header -->
    <div class="flex items-center justify-between">
      <h1 class="text-xl font-semibold">Issues</h1>
      <div class="flex items-center gap-3">
        <span class="text-sm text-muted-foreground">{{ total }} total</span>
        <Button
          @click="toggleMyIssues"
          :variant="myIssuesOnly ? 'default' : 'secondary'"
          size="sm"
        >
          My Issues
        </Button>
        <Button @click="openNewIssue" size="sm">
          <Plus class="w-4 h-4 mr-1" />
          New Issue
        </Button>
      </div>
    </div>

    <!-- Status filter pills -->
    <div class="flex gap-2 flex-wrap">
      <button
        v-for="s in statusGroups"
        :key="s.value"
        @click="statusFilter = s.value"
        :class="cn(
          'px-3 py-1 text-sm rounded-full border transition-colors',
          statusFilter === s.value
            ? 'bg-primary text-primary-foreground border-primary'
            : 'bg-background text-foreground border-border hover:bg-accent hover:text-accent-foreground'
        )"
      >
        {{ s.label }}
      </button>
    </div>

    <!-- Type filter pills -->
    <div class="flex gap-2 flex-wrap">
      <button
        v-for="t in issueTypes"
        :key="t.value"
        @click="typeFilter = t.value"
        :class="cn(
          'px-3 py-1 text-sm rounded-full border transition-colors',
          typeFilter === t.value
            ? 'bg-primary text-primary-foreground border-primary'
            : 'bg-background text-foreground border-border hover:bg-accent hover:text-accent-foreground'
        )"
      >
        {{ t.label }}
      </button>
    </div>

    <!-- Loading -->
    <div v-if="loading" class="flex items-center justify-center py-8 text-muted-foreground gap-2">
      <Loader2 class="w-4 h-4 animate-spin" />
      <span>Loading issues...</span>
    </div>

    <!-- Error -->
    <div v-else-if="error" class="text-center py-8 text-destructive">{{ error }}</div>

    <!-- Empty state -->
    <EmptyState
      v-else-if="issues.length === 0"
      icon="fa-circle-exclamation"
      title="No issues found"
      description="Issues are created by AI agents to communicate across projects. Use the MCP 'issues' tool to create one."
    />

    <!-- Issue table -->
    <div v-else class="rounded-md border">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead class="w-16">#</TableHead>
            <TableHead class="w-24">Priority</TableHead>
            <TableHead class="w-28">Type</TableHead>
            <TableHead>Title</TableHead>
            <TableHead class="w-40">Project</TableHead>
            <TableHead class="w-32">Created</TableHead>
            <TableHead class="w-20">Comments</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          <TableRow
            v-for="issue in issues"
            :key="issue.id"
            class="cursor-pointer"
            :class="(issue.status === 'closed' || issue.status === 'rejected') ? 'opacity-50' : ''"
            @click="router.push(`/issues/${issue.id}`)"
          >
            <TableCell class="text-muted-foreground text-xs font-mono">
              #{{ issue.id }}
            </TableCell>
            <TableCell>
              <Badge :class="priorityBadgeClass(issue.priority)" class="uppercase text-xs">
                {{ issue.priority }}
              </Badge>
            </TableCell>
            <TableCell>
              <Badge v-if="issue.type" :class="typeBadgeClass(issue.type)">
                {{ issue.type }}
              </Badge>
            </TableCell>
            <TableCell>
              <div class="flex items-center gap-2">
                <span class="font-medium truncate max-w-[320px]">{{ issue.title }}</span>
                <Badge :class="statusBadgeClass(issue.status)" class="shrink-0">
                  {{ issue.status }}
                </Badge>
              </div>
            </TableCell>
            <TableCell class="text-xs text-muted-foreground">
              {{ shortProject(issue.source_project) }} &rarr; {{ shortProject(issue.target_project) }}
            </TableCell>
            <TableCell class="text-xs text-muted-foreground">
              {{ formatRelativeTime(issue.created_at) }}
            </TableCell>
            <TableCell class="text-xs text-muted-foreground">
              <span v-if="issue.comment_count" class="flex items-center gap-1">
                <MessageSquare class="w-3 h-3" />
                {{ issue.comment_count }}
              </span>
            </TableCell>
          </TableRow>
        </TableBody>
      </Table>
    </div>
  </div>

  <!-- New Issue Dialog -->
  <Dialog :open="showNewIssue" @update:open="showNewIssue = $event">
    <DialogContent class="max-w-lg">
      <DialogHeader>
        <DialogTitle>New Issue</DialogTitle>
      </DialogHeader>

      <div class="space-y-4">
        <div v-if="createError" class="text-sm text-destructive">{{ createError }}</div>

        <!-- Title -->
        <div class="space-y-1.5">
          <label class="text-sm font-medium">Title <span class="text-destructive">*</span></label>
          <Input
            v-model="newTitle"
            placeholder="Brief description of the issue"
          />
        </div>

        <!-- Body -->
        <div class="space-y-1.5">
          <label class="text-sm font-medium">Description</label>
          <Textarea
            v-model="newBody"
            placeholder="Detailed description (optional)"
            class="min-h-[100px] resize-y"
          />
        </div>

        <!-- Type + Priority row -->
        <div class="flex gap-3">
          <div class="flex-1 space-y-1.5">
            <label class="text-sm font-medium">Type</label>
            <Select v-model="newType">
              <SelectTrigger>
                <SelectValue placeholder="Select type" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="task">Task</SelectItem>
                <SelectItem value="bug">Bug</SelectItem>
                <SelectItem value="feature">Feature</SelectItem>
                <SelectItem value="improvement">Improvement</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div class="flex-1 space-y-1.5">
            <label class="text-sm font-medium">Priority</label>
            <Select v-model="newPriority">
              <SelectTrigger>
                <SelectValue placeholder="Select priority" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="medium">Medium</SelectItem>
                <SelectItem value="low">Low</SelectItem>
                <SelectItem value="high">High</SelectItem>
                <SelectItem value="critical">Critical</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </div>

        <!-- Target project -->
        <div v-if="trackedProjects.length > 0" class="space-y-1.5">
          <label class="text-sm font-medium">Target Project</label>
          <Select v-model="newTargetProject">
            <SelectTrigger>
              <SelectValue placeholder="— none —" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="">— none —</SelectItem>
              <SelectItem v-for="p in trackedProjects" :key="p" :value="p">{{ p }}</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </div>

      <DialogFooter>
        <Button variant="secondary" @click="showNewIssue = false">Cancel</Button>
        <Button
          @click="submitNewIssue"
          :disabled="creating || !newTitle.trim()"
        >
          <Loader2 v-if="creating" class="w-4 h-4 mr-2 animate-spin" />
          {{ creating ? 'Creating...' : 'Create Issue' }}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
