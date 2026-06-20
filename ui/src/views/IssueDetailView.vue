<script setup lang="ts">
import { ref, onMounted, computed, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useUiI18n } from '@/composables/useUiI18n'
import {
  ArrowLeft,
  Pencil,
  Trash2,
  Ban,
  Loader2,
} from 'lucide-vue-next'
import { fetchIssue, updateIssue, deleteIssue, type Issue, type IssueComment } from '@/utils/api'
import { formatRelativeTime } from '@/utils/formatters'
import { renderMarkdownAsync } from '@/composables/useMarkdown'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import {
  Card,
  CardContent,
  CardHeader,
} from '@/components/ui/card'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from '@/components/ui/dialog'
import { cn } from '@/lib/utils'

const route = useRoute()
const router = useRouter()
const { locale, t } = useUiI18n()
const statusLabels = computed<Record<string, string>>(() => ({
  open: t.value.issues.filters.open,
  acknowledged: t.value.issues.filters.acknowledged,
  resolved: t.value.issues.filters.resolved,
  reopened: t.value.issues.filters.reopened,
  closed: t.value.issues.filters.closed,
  rejected: t.value.issues.filters.rejected,
}))
const typeLabels = computed<Record<string, string>>(() => ({
  task: t.value.issues.filters.task,
  bug: t.value.issues.filters.bug,
  feature: t.value.issues.filters.feature,
  improvement: t.value.issues.filters.improvement,
}))

const issue = ref<Issue | null>(null)
const comments = ref<IssueComment[]>([])
const loading = ref(true)
const error = ref<string | null>(null)
const sourceProjectDisplayName = ref<string | null>(null)
const targetProjectDisplayName = ref<string | null>(null)

// Edit mode
const editing = ref(false)
const editTitle = ref('')
const editBody = ref('')
const editPriority = ref('')
const editType = ref('')

// Comment form
const newComment = ref('')
const commenting = ref(false)

// Action states
const actionLoading = ref(false)
const showDeleteConfirm = ref(false)
const rejectReason = ref('')
const showRejectDialog = ref(false)

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
  if (issue.value) {
    if (project === issue.value.source_project && sourceProjectDisplayName.value) {
      return sourceProjectDisplayName.value
    }
    if (project === issue.value.target_project && targetProjectDisplayName.value) {
      return targetProjectDisplayName.value
    }
  }
  const parts = project.split('/')
  return parts[parts.length - 1] || project
}

const allStatuses = ['open', 'acknowledged', 'resolved', 'reopened', 'closed', 'rejected']

interface TimelineEvent {
  type: 'created' | 'acknowledged' | 'resolved' | 'reopened' | 'closed' | 'rejected' | 'comment'
  date: string
  project: string
  agent: string
  body?: string
}

const timeline = computed(() => {
  if (!issue.value) return []
  return buildTimeline(issue.value, comments.value)
})

// Async-rendered markdown bodies (shiki highlighted)
const renderedBodies = ref<Record<number, string>>({})

async function renderTimelineBodies() {
  const events = timeline.value
  const results: Record<number, string> = {}
  for (let i = 0; i < events.length; i++) {
    if (events[i].body) {
      results[i] = await renderMarkdownAsync(events[i].body!)
    }
  }
  renderedBodies.value = results
}

watch(timeline, () => { renderTimelineBodies() }, { immediate: true })

function buildTimeline(iss: Issue, cmts: IssueComment[]): TimelineEvent[] {
  const events: TimelineEvent[] = []

  events.push({
    type: 'created',
    date: iss.created_at,
    project: iss.source_project,
    agent: iss.source_agent || '',
    body: iss.body || undefined,
  })

  if (iss.acknowledged_at) {
    events.push({ type: 'acknowledged', date: iss.acknowledged_at, project: iss.target_project, agent: '' })
  }

  for (const c of cmts) {
    events.push({ type: 'comment', date: c.created_at, project: c.author_project, agent: c.author_agent || '', body: c.body })
  }

  if (iss.resolved_at) {
    events.push({ type: 'resolved', date: iss.resolved_at, project: '', agent: '' })
  }

  if (iss.reopened_at) {
    events.push({ type: 'reopened', date: iss.reopened_at, project: '', agent: '' })
  }

  if (iss.closed_at) {
    events.push({ type: 'closed', date: iss.closed_at, project: iss.source_project, agent: '' })
  }

  return events.sort((a, b) => new Date(a.date).getTime() - new Date(b.date).getTime())
}

function dotColor(type: string): string {
  switch (type) {
    case 'created': return 'bg-blue-500'
    case 'acknowledged': return 'bg-purple-500'
    case 'resolved': return 'bg-green-500'
    case 'reopened': return 'bg-amber-500'
    case 'closed': return 'bg-emerald-600'
    case 'rejected': return 'bg-destructive'
    default: return 'bg-muted-foreground'
  }
}

function timelineBadgeClass(type: string): string {
  switch (type) {
    case 'created': return 'bg-blue-100 text-blue-800 dark:bg-blue-900/40 dark:text-blue-300 border-transparent'
    case 'acknowledged': return 'bg-purple-100 text-purple-800 dark:bg-purple-900/40 dark:text-purple-300 border-transparent'
    case 'resolved': return 'bg-green-100 text-green-800 dark:bg-green-900/40 dark:text-green-300 border-transparent'
    case 'reopened': return 'bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-300 border-transparent'
    case 'closed': return 'bg-muted text-muted-foreground border-transparent'
    case 'rejected': return 'bg-destructive/10 text-destructive border-transparent'
    default: return 'bg-muted text-muted-foreground border-transparent'
  }
}

function timelineTypeLabel(type: TimelineEvent['type']): string {
  return t.value.issueDetail.timelineTypes[type]
}

async function loadIssue() {
  const id = Number(route.params.id)
  if (!id) { error.value = t.value.issueDetail.invalidId; loading.value = false; return }

  try {
    const result = await fetchIssue(id)
    issue.value = result.issue
    comments.value = result.comments || []
    sourceProjectDisplayName.value = result.source_project_display_name ?? null
    targetProjectDisplayName.value = result.target_project_display_name ?? null
  } catch (err: any) {
    error.value = err.message || t.value.issueDetail.loadFailed
  } finally {
    loading.value = false
  }
}

function startEdit() {
  if (!issue.value) return
  editTitle.value = issue.value.title
  editBody.value = issue.value.body
  editPriority.value = issue.value.priority
  editType.value = issue.value.type || 'task'
  editing.value = true
}

async function saveEdit() {
  if (!issue.value) return
  actionLoading.value = true
  try {
    const fields: Record<string, string> = {}
    if (editTitle.value !== issue.value.title) fields.title = editTitle.value
    if (editBody.value !== issue.value.body) fields.body = editBody.value
    if (editPriority.value !== issue.value.priority) fields.priority = editPriority.value
    if (editType.value !== (issue.value.type || 'task')) fields.type = editType.value
    if (Object.keys(fields).length > 0) {
      await updateIssue(issue.value.id, fields)
    }
    editing.value = false
    await loadIssue()
  } catch (err: any) {
    error.value = err.message || t.value.issueDetail.loadFailed
  } finally {
    actionLoading.value = false
  }
}

async function changeStatus(newStatus: string) {
  if (!issue.value) return
  actionLoading.value = true
  try {
    await updateIssue(issue.value.id, {
      status: newStatus,
      source_project: 'dashboard',
      source_agent: 'human',
    })
    await loadIssue()
  } catch (err: any) {
    error.value = err.message || t.value.issueDetail.loadFailed
  } finally {
    actionLoading.value = false
  }
}

async function submitComment() {
  if (!issue.value || !newComment.value.trim()) return
  commenting.value = true
  try {
    await updateIssue(issue.value.id, {
      comment: newComment.value,
      source_project: 'dashboard',
      source_agent: 'human',
    })
    newComment.value = ''
    await loadIssue()
  } catch (err: any) {
    error.value = err.message || t.value.issueDetail.loadFailed
  } finally {
    commenting.value = false
  }
}

async function confirmDelete() {
  if (!issue.value) return
  actionLoading.value = true
  try {
    await deleteIssue(issue.value.id)
    router.push('/issues')
  } catch (err: any) {
    error.value = err.message || t.value.issueDetail.loadFailed
  } finally {
    actionLoading.value = false
    showDeleteConfirm.value = false
  }
}

async function confirmReject() {
  if (!issue.value || !rejectReason.value.trim()) return
  actionLoading.value = true
  try {
    await updateIssue(issue.value.id, {
      status: 'rejected',
      comment: rejectReason.value,
      source_project: 'dashboard',
      source_agent: 'human',
    })
    showRejectDialog.value = false
    rejectReason.value = ''
    await loadIssue()
  } catch (err: any) {
    error.value = err.message || t.value.issueDetail.loadFailed
  } finally {
    actionLoading.value = false
  }
}

onMounted(loadIssue)
</script>

<template>
  <div class="space-y-4 pt-4">
    <!-- Back button -->
    <Button variant="ghost" size="sm" @click="router.push('/issues')" class="-ml-2">
      <ArrowLeft class="w-4 h-4 mr-1" />
      {{ t.issueDetail.back }}
    </Button>

    <!-- Loading -->
    <div v-if="loading" class="flex items-center justify-center py-8 text-muted-foreground gap-2">
      <Loader2 class="w-4 h-4 animate-spin" />
      <span>{{ t.issueDetail.loading }}</span>
    </div>

    <!-- Error -->
    <div v-else-if="error && !issue" class="text-center py-8 text-destructive">{{ error }}</div>

    <template v-else-if="issue">
      <!-- Error banner (non-fatal) -->
      <div v-if="error" class="text-sm text-destructive">{{ error }}</div>

      <!-- Header card -->
      <Card>
        <CardHeader class="pb-3">
          <template v-if="!editing">
            <div class="flex items-start justify-between gap-3">
              <div class="flex-1 min-w-0">
                <div class="flex items-center gap-2 mb-2 flex-wrap">
                  <span class="text-muted-foreground text-sm font-mono">#{{ issue.id }}</span>
                  <Badge v-if="issue.type" :class="typeBadgeClass(issue.type)">{{ typeLabels[issue.type] || issue.type }}</Badge>
                  <Badge :class="priorityBadgeClass(issue.priority)" class="uppercase">{{ issue.priority }}</Badge>
                  <Badge :class="statusBadgeClass(issue.status)">{{ statusLabels[issue.status] || issue.status }}</Badge>
                </div>
                <h1 class="text-lg font-semibold">{{ issue.title }}</h1>
                <p class="text-sm text-muted-foreground mt-1">
                  {{ shortProject(issue.source_project) }} &rarr; {{ shortProject(issue.target_project) }}
                  &middot; {{ t.issueDetail.created }} {{ formatRelativeTime(issue.created_at, locale) }}
                </p>
              </div>

              <!-- Operator actions -->
              <div class="flex items-center gap-2 shrink-0">
                <Button variant="secondary" size="sm" @click="startEdit">
                  <Pencil class="w-3.5 h-3.5 mr-1" />
                  {{ t.issueDetail.edit }}
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  class="border-orange-300 text-orange-600 hover:bg-orange-50 dark:border-orange-700 dark:text-orange-400 dark:hover:bg-orange-900/20"
                  @click="showRejectDialog = true"
                >
                  <Ban class="w-3.5 h-3.5 mr-1" />
                  {{ t.issueDetail.reject }}
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  class="border-destructive/50 text-destructive hover:bg-destructive/10"
                  @click="showDeleteConfirm = true"
                >
                  <Trash2 class="w-3.5 h-3.5 mr-1" />
                  {{ t.issueDetail.delete }}
                </Button>
              </div>
            </div>

            <!-- Status override -->
            <div class="mt-3 flex items-center gap-2">
              <span class="text-xs text-muted-foreground">{{ t.issueDetail.statusOverride }}</span>
                <Select
                  :model-value="issue.status"
                  :disabled="actionLoading"
                  @update:model-value="changeStatus($event as string)"
                >
                  <SelectTrigger class="h-7 w-40 text-xs">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem v-for="s in allStatuses" :key="s" :value="s">{{ statusLabels[s] || s }}</SelectItem>
                  </SelectContent>
                </Select>
              </div>
          </template>

          <!-- Edit mode -->
          <template v-else>
            <div class="space-y-3">
              <Input v-model="editTitle" class="text-lg font-semibold" />
              <Textarea v-model="editBody" class="min-h-[100px] resize-y" />
              <div class="flex gap-3">
                <Select v-model="editPriority" class="flex-1">
                  <SelectTrigger>
                    <SelectValue :placeholder="t.issueDetail.placeholders.priority" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem v-for="p in ['critical', 'high', 'medium', 'low']" :key="p" :value="p">{{ p }}</SelectItem>
                  </SelectContent>
                </Select>
                <Select v-model="editType" class="flex-1">
                  <SelectTrigger>
                    <SelectValue :placeholder="t.issueDetail.placeholders.type" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="task">{{ t.issues.filters.task }}</SelectItem>
                    <SelectItem value="bug">{{ t.issues.filters.bug }}</SelectItem>
                    <SelectItem value="feature">{{ t.issues.filters.feature }}</SelectItem>
                    <SelectItem value="improvement">{{ t.issues.filters.improvement }}</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div class="flex gap-2">
                <Button size="sm" @click="saveEdit" :disabled="actionLoading">
                  <Loader2 v-if="actionLoading" class="w-3.5 h-3.5 mr-1 animate-spin" />
                  {{ t.issueDetail.save }}
                </Button>
                <Button variant="secondary" size="sm" @click="editing = false">{{ t.issueDetail.cancel }}</Button>
              </div>
            </div>
          </template>
        </CardHeader>
      </Card>

      <!-- Labels -->
      <div v-if="issue.labels && issue.labels.length > 0" class="flex gap-1 flex-wrap">
        <Badge
          v-for="label in issue.labels"
          :key="label"
          variant="secondary"
        >
          {{ label }}
        </Badge>
      </div>

      <!-- Comment form -->
      <Card>
        <CardContent class="pt-4">
          <h3 class="text-sm font-medium mb-2">{{ t.issueDetail.addComment }}</h3>
          <div class="flex flex-col gap-2">
            <Textarea
              v-model="newComment"
              @keydown.ctrl.enter="submitComment"
              @keydown.meta.enter="submitComment"
              :placeholder="t.issueDetail.commentPlaceholder"
              class="min-h-[80px] max-h-[400px] resize-y font-mono text-sm"
            />
            <div class="flex items-center justify-between">
              <span class="text-xs text-muted-foreground">
                {{ t.issueDetail.markdownHint }}
              </span>
              <Button
                size="sm"
                @click="submitComment"
                :disabled="commenting || !newComment.trim()"
              >
                <Loader2 v-if="commenting" class="w-3.5 h-3.5 mr-1 animate-spin" />
                {{ commenting ? t.issueDetail.sending : t.issueDetail.send }}
              </Button>
            </div>
          </div>
        </CardContent>
      </Card>

      <!-- Timeline -->
      <div class="space-y-3">
        <h2 class="text-xs font-medium text-muted-foreground uppercase tracking-wide">{{ t.issueDetail.timeline }}</h2>

        <div v-for="(event, idx) in timeline" :key="idx" class="flex gap-3">
          <div class="flex flex-col items-center">
            <div :class="cn('w-2.5 h-2.5 rounded-full mt-1.5 shrink-0', dotColor(event.type))" />
            <div v-if="idx < timeline.length - 1" class="w-px flex-1 bg-border mt-1" />
          </div>

          <div class="pb-4 flex-1 min-w-0">
            <div class="flex items-center gap-2 text-sm flex-wrap">
              <Badge :class="timelineBadgeClass(event.type)" class="capitalize">{{ timelineTypeLabel(event.type) }}</Badge>
              <span v-if="event.project" class="text-muted-foreground text-xs">{{ t.issueDetail.by }} {{ shortProject(event.project) }}</span>
              <span v-if="event.agent" class="text-muted-foreground text-xs">({{ event.agent }})</span>
              <span class="text-muted-foreground text-xs">{{ formatRelativeTime(event.date, locale) }}</span>
            </div>
            <div
              v-if="event.body"
              class="markdown-body mt-1.5 text-sm break-words"
              v-html="renderedBodies[idx] || event.body"
            />
          </div>
        </div>
      </div>
    </template>
  </div>

  <!-- Delete confirmation dialog -->
  <Dialog :open="showDeleteConfirm" @update:open="showDeleteConfirm = $event">
    <DialogContent class="max-w-sm">
      <DialogHeader>
        <DialogTitle>{{ t.issueDetail.deleteDialogTitle.replace('{id}', String(issue?.id ?? '')) }}</DialogTitle>
        <DialogDescription>
          {{ t.issueDetail.deleteDialogDescription }}
        </DialogDescription>
      </DialogHeader>
      <DialogFooter>
        <Button variant="secondary" @click="showDeleteConfirm = false">{{ t.issueDetail.cancel }}</Button>
        <Button variant="destructive" @click="confirmDelete" :disabled="actionLoading">
          <Loader2 v-if="actionLoading" class="w-4 h-4 mr-2 animate-spin" />
          {{ t.issueDetail.delete }}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>

  <!-- Reject dialog -->
  <Dialog :open="showRejectDialog" @update:open="showRejectDialog = $event; if (!$event) rejectReason = ''">
    <DialogContent class="max-w-sm">
      <DialogHeader>
        <DialogTitle>{{ t.issueDetail.rejectDialogTitle.replace('{id}', String(issue?.id ?? '')) }}</DialogTitle>
        <DialogDescription>
          {{ t.issueDetail.rejectDialogDescription }}
        </DialogDescription>
      </DialogHeader>
      <div class="py-2">
        <Textarea
          v-model="rejectReason"
          :placeholder="t.issueDetail.rejectPlaceholder"
          class="min-h-[80px] resize-y"
        />
      </div>
      <DialogFooter>
        <Button variant="secondary" @click="showRejectDialog = false; rejectReason = ''">{{ t.issueDetail.cancel }}</Button>
        <Button
          class="bg-orange-600 text-white hover:bg-orange-500"
          @click="confirmReject"
          :disabled="actionLoading || !rejectReason.trim()"
        >
          <Loader2 v-if="actionLoading" class="w-4 h-4 mr-2 animate-spin" />
          {{ t.issueDetail.reject }}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>

<style scoped>
.markdown-body :deep(code) {
  @apply bg-muted px-1.5 py-0.5 rounded text-[13px] font-mono text-foreground;
}
.markdown-body :deep(pre) {
  @apply bg-muted border border-border rounded-lg overflow-x-auto my-2 p-0;
}
.markdown-body :deep(pre code) {
  @apply bg-transparent p-3 block text-[13px] leading-relaxed whitespace-pre;
}
.markdown-body :deep(ul),
.markdown-body :deep(ol) {
  @apply pl-5 my-1;
}
.markdown-body :deep(ul) {
  @apply list-disc;
}
.markdown-body :deep(ol) {
  @apply list-decimal;
}
.markdown-body :deep(a) {
  @apply text-primary hover:underline;
}
.markdown-body :deep(p) {
  @apply my-1;
}
.markdown-body :deep(h1) {
  @apply text-xl font-bold mt-4 mb-2;
}
.markdown-body :deep(h2) {
  @apply text-lg font-bold mt-3 mb-1;
}
.markdown-body :deep(h3) {
  @apply text-base font-bold mt-2 mb-1;
}
.markdown-body :deep(blockquote) {
  @apply border-l-2 border-primary/40 pl-3 my-2 text-muted-foreground italic;
}
.markdown-body :deep(hr) {
  @apply border-border my-3;
}
.markdown-body :deep(table) {
  @apply w-full border-collapse my-2 text-sm;
}
.markdown-body :deep(th),
.markdown-body :deep(td) {
  @apply border border-border px-2 py-1 text-left;
}
.markdown-body :deep(th) {
  @apply bg-muted font-medium;
}

/* Shiki dual-theme: light theme by default, dark theme when .dark class */
.markdown-body :deep(.shiki-block pre) {
  @apply rounded-lg border border-border overflow-x-auto my-2 p-3 text-[13px] leading-relaxed;
}
.markdown-body :deep(.shiki),
.markdown-body :deep(.shiki span) {
  color: var(--shiki-light) !important;
  background-color: var(--shiki-light-bg) !important;
}
:global(.dark) .markdown-body :deep(.shiki),
:global(.dark) .markdown-body :deep(.shiki span) {
  color: var(--shiki-dark) !important;
  background-color: var(--shiki-dark-bg) !important;
}

/* Diff highlighting */
.markdown-body :deep(.diff.add) {
  @apply bg-green-100 dark:bg-green-900/30;
}
.markdown-body :deep(.diff.remove) {
  @apply bg-red-100 dark:bg-red-900/30;
}
</style>
