<script setup lang="ts">
import { computed, ref } from 'vue'
import { useMutation, useQueryClient } from '@tanstack/vue-query'
import type { Issue } from '~/composables/useOperatorApi'
import { useIssueDetailQuery } from '~/composables/useOperatorQueries'
import {
  deleteIssue,
  operatorIssueActor,
  updateIssue,
  type IssuePriority,
  type IssueType,
  type IssueUpdateFields,
} from '~/composables/useOperatorIssueActions'
import { formatAbsoluteDate, shortProjectName } from '~/utils/formatters'

const route = useRoute()
const router = useRouter()
const queryClient = useQueryClient()

const issueId = computed(() => Number(route.params.id))
const issueQuery = useIssueDetailQuery(issueId)
const issue = computed(() => issueQuery.data.value?.issue ?? null)
const comments = computed(() => issueQuery.data.value?.comments ?? [])
const sourceProjectName = computed(() =>
  issue.value
    ? (issueQuery.data.value?.source_project_display_name || shortProjectName(issue.value.source_project))
    : '',
)
const targetProjectName = computed(() =>
  issue.value
    ? (issueQuery.data.value?.target_project_display_name || shortProjectName(issue.value.target_project))
    : '',
)

const editing = ref(false)
const editTitle = ref('')
const editBody = ref('')
const editPriority = ref<IssuePriority>('medium')
const editType = ref<IssueType>('task')

const newComment = ref('')
const rejectReason = ref('')
const showRejectPanel = ref(false)
const showDeleteConfirm = ref(false)
const actionError = ref('')
const actionMessage = ref('')

const priorityOptions: IssuePriority[] = ['critical', 'high', 'medium', 'low']
const typeOptions: IssueType[] = ['task', 'bug', 'feature', 'improvement']

const invalidateIssue = async () => {
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: ['issue-detail', issueId.value] }),
    queryClient.invalidateQueries({ queryKey: ['issues'] }),
  ])
}

const issueMutation = useMutation({
  mutationFn: (fields: IssueUpdateFields) => updateIssue(issueId.value, fields),
  onSuccess: async () => {
    await invalidateIssue()
  },
  onError: (error) => {
    actionMessage.value = ''
    actionError.value = error instanceof Error ? error.message : 'Issue update failed'
  },
})

const deleteMutation = useMutation({
  mutationFn: () => deleteIssue(issueId.value),
  onSuccess: async () => {
    await queryClient.invalidateQueries({ queryKey: ['issues'] })
    await router.push('/issues')
  },
  onError: (error) => {
    actionMessage.value = ''
    actionError.value = error instanceof Error ? error.message : 'Issue delete failed'
  },
})

function statusBadgeClass(status: string): string {
  switch (status) {
    case 'open':
      return 'border-[color:rgba(76,141,255,0.4)] bg-[color:rgba(76,141,255,0.12)] text-[var(--accent)]'
    case 'acknowledged':
      return 'border-[color:rgba(167,139,250,0.4)] bg-[color:rgba(167,139,250,0.12)] text-[var(--must-build)]'
    case 'resolved':
      return 'border-[color:rgba(52,211,153,0.4)] bg-[color:rgba(52,211,153,0.12)] text-[var(--success)]'
    case 'reopened':
      return 'border-[color:rgba(251,191,36,0.4)] bg-[color:rgba(251,191,36,0.12)] text-[var(--warn)]'
    case 'closed':
      return 'border-[color:rgba(107,114,128,0.4)] bg-[color:rgba(107,114,128,0.12)] text-[var(--stale)]'
    case 'rejected':
      return 'border-[color:rgba(248,113,113,0.4)] bg-[color:rgba(248,113,113,0.12)] text-[var(--danger)]'
    default:
      return 'border-[color:rgba(107,114,128,0.4)] bg-[color:rgba(107,114,128,0.12)] text-[var(--stale)]'
  }
}

function priorityBadgeClass(priority: string): string {
  switch (priority) {
    case 'critical':
      return 'border-[color:rgba(248,113,113,0.4)] bg-[color:rgba(248,113,113,0.12)] text-[var(--danger)]'
    case 'high':
      return 'border-[color:rgba(251,191,36,0.4)] bg-[color:rgba(251,191,36,0.12)] text-[var(--warn)]'
    case 'medium':
      return 'border-[color:rgba(76,141,255,0.4)] bg-[color:rgba(76,141,255,0.12)] text-[var(--accent)]'
    default:
      return 'border-[color:rgba(107,114,128,0.4)] bg-[color:rgba(107,114,128,0.12)] text-[var(--stale)]'
  }
}

function terminalStatus(status: Issue['status']): boolean {
  return status === 'closed' || status === 'rejected'
}

function startEdit() {
  if (!issue.value) return
  editTitle.value = issue.value.title
  editBody.value = issue.value.body || ''
  editPriority.value = issue.value.priority
  editType.value = issue.value.type || 'task'
  editing.value = true
  actionError.value = ''
  actionMessage.value = ''
}

function cancelEdit() {
  editing.value = false
  actionError.value = ''
}

function changedEditFields(currentIssue: Issue): IssueUpdateFields {
  const fields: IssueUpdateFields = {}
  if (editTitle.value.trim() && editTitle.value.trim() !== currentIssue.title) {
    fields.title = editTitle.value.trim()
  }
  if (editBody.value !== (currentIssue.body || '')) {
    fields.body = editBody.value
  }
  if (editPriority.value !== currentIssue.priority) {
    fields.priority = editPriority.value
  }
  if (editType.value !== currentIssue.type) {
    fields.type = editType.value
  }
  return fields
}

function saveEdit() {
  if (!issue.value) return
  const fields = changedEditFields(issue.value)
  if (!fields.title && editTitle.value.trim() === '') {
    actionError.value = 'Title cannot be empty.'
    actionMessage.value = ''
    return
  }
  if (fields.body === '') {
    actionError.value = 'Current server PATCH cannot clear body; replace it with non-empty text or leave it unchanged.'
    actionMessage.value = ''
    return
  }
  if (Object.keys(fields).length === 0) {
    editing.value = false
    return
  }

  actionError.value = ''
  actionMessage.value = ''
  issueMutation.mutate(fields, {
    onSuccess: async () => {
      await invalidateIssue()
      editing.value = false
      actionMessage.value = 'Issue fields updated.'
    },
  })
}

function updateStatus(status: Issue['status'], comment?: string) {
  actionError.value = ''
  actionMessage.value = ''
  issueMutation.mutate({
    ...operatorIssueActor,
    status,
    comment,
  }, {
    onSuccess: async () => {
      await invalidateIssue()
      actionMessage.value = `Issue status changed to ${status}.`
    },
  })
}

function submitComment() {
  const body = newComment.value.trim()
  if (!body) return

  actionError.value = ''
  actionMessage.value = ''
  issueMutation.mutate({
    ...operatorIssueActor,
    comment: body,
  }, {
    onSuccess: async () => {
      await invalidateIssue()
      newComment.value = ''
      actionMessage.value = 'Comment added.'
    },
  })
}

function submitReject() {
  const reason = rejectReason.value.trim()
  if (!reason) {
    actionError.value = 'Reject requires a reason; the backend stores it as a comment.'
    actionMessage.value = ''
    return
  }

  actionError.value = ''
  actionMessage.value = ''
  issueMutation.mutate({
    ...operatorIssueActor,
    status: 'rejected',
    comment: reason,
  }, {
    onSuccess: async () => {
      await invalidateIssue()
      rejectReason.value = ''
      showRejectPanel.value = false
      actionMessage.value = 'Issue rejected with reason.'
    },
  })
}

function confirmDelete() {
  actionError.value = ''
  actionMessage.value = ''
  deleteMutation.mutate()
}
</script>

<template>
  <section class="space-y-5">
    <div class="surface-panel p-5">
      <div v-if="issueQuery.isPending.value" class="text-sm text-[var(--muted)]">
        Загрузка issue...
      </div>
      <div v-else-if="issueQuery.isError.value" class="text-sm text-[var(--danger)]">
        {{ issueQuery.error.value?.message || 'Не удалось загрузить issue detail' }}
      </div>
      <div v-else-if="issue" class="space-y-5">
        <NuxtLink to="/issues" class="inline-flex text-sm font-semibold text-[var(--accent)] hover:underline">
          ← Back to issues
        </NuxtLink>

        <div v-if="actionError" class="rounded-xl border border-[color:rgba(248,113,113,0.4)] bg-[color:rgba(248,113,113,0.12)] p-3 text-sm text-[var(--danger)]">
          {{ actionError }}
        </div>
        <div v-else-if="actionMessage" class="rounded-xl border border-[color:rgba(52,211,153,0.4)] bg-[color:rgba(52,211,153,0.12)] p-3 text-sm text-[var(--success)]">
          {{ actionMessage }}
        </div>

        <div class="flex flex-wrap items-start justify-between gap-4">
          <div class="min-w-0 flex-1">
            <div class="mb-2 flex flex-wrap items-center gap-2">
              <span class="mono-data text-xs uppercase tracking-[0.08em] text-[var(--muted)]">
                issue #{{ issue.id }}
              </span>
              <span class="rounded-full border px-2.5 py-1 text-[10px] font-semibold uppercase tracking-[0.04em]" :class="statusBadgeClass(issue.status)">
                {{ issue.status }}
              </span>
              <span class="rounded-full border px-2.5 py-1 text-[10px] font-semibold uppercase tracking-[0.04em]" :class="priorityBadgeClass(issue.priority)">
                {{ issue.priority }}
              </span>
              <span class="rounded-full border border-[var(--border)] bg-[var(--surface-warm)] px-2.5 py-1 text-[10px] font-semibold uppercase tracking-[0.04em] text-[var(--fg-2)]">
                {{ issue.type }}
              </span>
            </div>
            <h1 class="text-2xl font-semibold">{{ issue.title }}</h1>
            <p class="mt-2 text-sm text-[var(--fg-2)]">
              {{ sourceProjectName }} → {{ targetProjectName }} · created {{ formatAbsoluteDate(issue.created_at) }}
            </p>
          </div>

          <div class="flex flex-wrap gap-2">
            <button
              class="rounded-lg border border-[var(--border)] bg-[var(--surface-warm)] px-3 py-2 text-xs font-semibold text-[var(--fg-2)]"
              :disabled="issueMutation.isPending.value || editing"
              @click="startEdit"
            >
              Edit
            </button>
            <button
              v-if="issue.status === 'open'"
              class="rounded-lg border border-[var(--must-build)] bg-[color:rgba(167,139,250,0.12)] px-3 py-2 text-xs font-semibold text-[var(--must-build)]"
              :disabled="issueMutation.isPending.value"
              @click="updateStatus('acknowledged')"
            >
              Acknowledge
            </button>
            <button
              v-if="!terminalStatus(issue.status) && issue.status !== 'resolved'"
              class="rounded-lg border border-[var(--success)] bg-[color:rgba(52,211,153,0.12)] px-3 py-2 text-xs font-semibold text-[var(--success)]"
              :disabled="issueMutation.isPending.value"
              @click="updateStatus('resolved')"
            >
              Mark resolved
            </button>
            <button
              v-if="issue.status === 'resolved'"
              class="rounded-lg border border-[var(--warn)] bg-[color:rgba(251,191,36,0.12)] px-3 py-2 text-xs font-semibold text-[var(--warn)]"
              :disabled="issueMutation.isPending.value"
              @click="updateStatus('reopened', 'Reopened by dashboard operator.')"
            >
              Reopen
            </button>
            <button
              v-if="!terminalStatus(issue.status)"
              class="rounded-lg border border-[var(--stale)] bg-[color:rgba(107,114,128,0.12)] px-3 py-2 text-xs font-semibold text-[var(--stale)]"
              :disabled="issueMutation.isPending.value"
              @click="updateStatus('closed')"
            >
              Close
            </button>
            <button
              v-if="issue.status !== 'rejected'"
              class="rounded-lg border border-[var(--danger)] bg-[color:rgba(248,113,113,0.12)] px-3 py-2 text-xs font-semibold text-[var(--danger)]"
              :disabled="issueMutation.isPending.value"
              @click="showRejectPanel = !showRejectPanel"
            >
              Reject
            </button>
            <button
              class="rounded-lg border border-[var(--danger)] bg-[var(--surface)] px-3 py-2 text-xs font-semibold text-[var(--danger)]"
              :disabled="deleteMutation.isPending.value"
              @click="showDeleteConfirm = !showDeleteConfirm"
            >
              Delete
            </button>
          </div>
        </div>

        <div v-if="editing" class="surface-panel-warm p-4">
          <div class="mb-3 text-xs uppercase tracking-[0.08em] text-[var(--muted)]">
            Edit backed fields
          </div>
          <div class="space-y-3">
            <label class="block">
              <span class="mb-1 block text-xs text-[var(--muted)]">Title</span>
              <input
                v-model="editTitle"
                type="text"
                class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm text-[var(--fg)]"
              />
            </label>

            <label class="block">
              <span class="mb-1 block text-xs text-[var(--muted)]">Description</span>
              <textarea
                v-model="editBody"
                rows="5"
                class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm text-[var(--fg)]"
              />
              <span class="mt-1 block text-xs text-[var(--muted)]">
                Server can replace body with non-empty text; clearing body is not backed by current PATCH behavior.
              </span>
            </label>

            <div class="grid gap-3 sm:grid-cols-2">
              <label class="block">
                <span class="mb-1 block text-xs text-[var(--muted)]">Type</span>
                <select
                  v-model="editType"
                  class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm text-[var(--fg)]"
                >
                  <option v-for="type in typeOptions" :key="type" :value="type">{{ type }}</option>
                </select>
              </label>
              <label class="block">
                <span class="mb-1 block text-xs text-[var(--muted)]">Priority</span>
                <select
                  v-model="editPriority"
                  class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm text-[var(--fg)]"
                >
                  <option v-for="priority in priorityOptions" :key="priority" :value="priority">{{ priority }}</option>
                </select>
              </label>
            </div>

            <div class="flex flex-wrap gap-2">
              <button
                class="rounded-lg border border-[var(--accent)] bg-[var(--accent)] px-4 py-2 text-sm font-semibold text-[var(--accent-on)] disabled:cursor-not-allowed disabled:opacity-60"
                :disabled="issueMutation.isPending.value"
                @click="saveEdit"
              >
                {{ issueMutation.isPending.value ? 'Saving...' : 'Save edit' }}
              </button>
              <button
                class="rounded-lg border border-[var(--border)] bg-[var(--surface)] px-4 py-2 text-sm font-semibold text-[var(--fg-2)]"
                @click="cancelEdit"
              >
                Cancel
              </button>
            </div>
          </div>
        </div>

        <div v-if="showRejectPanel" class="surface-panel-warm p-4">
          <div class="mb-2 text-xs uppercase tracking-[0.08em] text-[var(--danger)]">
            Reject issue
          </div>
          <textarea
            v-model="rejectReason"
            rows="3"
            placeholder="Reason is required and will be stored as a comment."
            class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm text-[var(--fg)]"
          />
          <div class="mt-3 flex flex-wrap gap-2">
            <button
              class="rounded-lg border border-[var(--danger)] bg-[color:rgba(248,113,113,0.12)] px-4 py-2 text-sm font-semibold text-[var(--danger)] disabled:cursor-not-allowed disabled:opacity-60"
              :disabled="issueMutation.isPending.value"
              @click="submitReject"
            >
              {{ issueMutation.isPending.value ? 'Rejecting...' : 'Reject with reason' }}
            </button>
            <button
              class="rounded-lg border border-[var(--border)] bg-[var(--surface)] px-4 py-2 text-sm font-semibold text-[var(--fg-2)]"
              @click="showRejectPanel = false; rejectReason = ''"
            >
              Cancel
            </button>
          </div>
        </div>

        <div v-if="showDeleteConfirm" class="surface-panel-warm border-[color:rgba(248,113,113,0.35)] p-4">
          <div class="mb-2 text-sm font-semibold text-[var(--danger)]">
            Delete issue #{{ issue.id }} permanently?
          </div>
          <p class="text-sm text-[var(--fg-2)]">
            Backed by <span class="mono-data">DELETE /api/issues/{{ issue.id }}</span>. This removes the issue and comments.
          </p>
          <div class="mt-3 flex flex-wrap gap-2">
            <button
              class="rounded-lg border border-[var(--danger)] bg-[color:rgba(248,113,113,0.12)] px-4 py-2 text-sm font-semibold text-[var(--danger)] disabled:cursor-not-allowed disabled:opacity-60"
              :disabled="deleteMutation.isPending.value"
              @click="confirmDelete"
            >
              {{ deleteMutation.isPending.value ? 'Deleting...' : 'Delete permanently' }}
            </button>
            <button
              class="rounded-lg border border-[var(--border)] bg-[var(--surface)] px-4 py-2 text-sm font-semibold text-[var(--fg-2)]"
              @click="showDeleteConfirm = false"
            >
              Cancel
            </button>
          </div>
        </div>

        <div class="surface-panel-warm p-4">
          <div class="mb-2 text-xs uppercase tracking-[0.08em] text-[var(--muted)]">
            Description
          </div>
          <div class="whitespace-pre-wrap text-sm leading-6 text-[var(--fg-2)]">
            {{ issue.body || 'Без описания' }}
          </div>
        </div>

        <div class="grid gap-4 lg:grid-cols-[280px,minmax(0,1fr)]">
          <div class="surface-panel-warm p-4">
            <div class="mb-3 text-xs uppercase tracking-[0.08em] text-[var(--muted)]">
              Meta
            </div>
            <div class="space-y-3 text-sm text-[var(--fg-2)]">
              <div>
                <div class="text-xs text-[var(--muted)]">Type</div>
                <div>{{ issue.type }}</div>
              </div>
              <div>
                <div class="text-xs text-[var(--muted)]">Priority</div>
                <div>{{ issue.priority }}</div>
              </div>
              <div>
                <div class="text-xs text-[var(--muted)]">Source</div>
                <div>{{ sourceProjectName }}</div>
                <div class="mono-data text-xs text-[var(--muted)]">{{ issue.source_project }}</div>
              </div>
              <div>
                <div class="text-xs text-[var(--muted)]">Target</div>
                <div>{{ targetProjectName }}</div>
                <div class="mono-data text-xs text-[var(--muted)]">{{ issue.target_project }}</div>
              </div>
              <div>
                <div class="text-xs text-[var(--muted)]">Updated</div>
                <div class="mono-data">{{ formatAbsoluteDate(issue.updated_at) }}</div>
              </div>
            </div>
          </div>

          <div class="space-y-4">
            <div class="surface-panel-warm p-4">
              <div class="mb-3 text-xs uppercase tracking-[0.08em] text-[var(--muted)]">
                Add comment
              </div>
              <textarea
                v-model="newComment"
                rows="4"
                class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm text-[var(--fg)]"
                placeholder="Write an operator comment..."
                @keydown.ctrl.enter.prevent="submitComment"
                @keydown.meta.enter.prevent="submitComment"
              />
              <div class="mt-3 flex items-center justify-between gap-3">
                <span class="text-xs text-[var(--muted)]">Ctrl/⌘+Enter submits via PATCH comment.</span>
                <button
                  class="rounded-lg border border-[var(--accent)] bg-[var(--accent)] px-4 py-2 text-sm font-semibold text-[var(--accent-on)] disabled:cursor-not-allowed disabled:opacity-60"
                  :disabled="issueMutation.isPending.value || !newComment.trim()"
                  @click="submitComment"
                >
                  {{ issueMutation.isPending.value ? 'Sending...' : 'Send comment' }}
                </button>
              </div>
            </div>

            <div class="surface-panel-warm p-4">
              <div class="mb-3 flex flex-wrap items-center justify-between gap-3">
                <div class="text-xs uppercase tracking-[0.08em] text-[var(--muted)]">
                  Comments
                </div>
                <div class="mono-data text-xs text-[var(--muted)]">
                  {{ comments.length }} total
                </div>
              </div>
              <div v-if="comments.length === 0" class="text-sm text-[var(--muted)]">
                Комментариев пока нет.
              </div>
              <div v-else class="space-y-3">
                <article
                  v-for="comment in comments"
                  :key="comment.id"
                  class="rounded-xl border border-[var(--border)] bg-[var(--surface)] p-4"
                >
                  <div class="mb-2 flex flex-wrap items-center justify-between gap-3">
                    <div class="text-sm font-semibold">
                      {{ comment.author_project }} · {{ comment.author_agent || 'unknown agent' }}
                    </div>
                    <div class="mono-data text-xs text-[var(--muted)]">
                      {{ formatAbsoluteDate(comment.created_at) }}
                    </div>
                  </div>
                  <div class="whitespace-pre-wrap text-sm leading-6 text-[var(--fg-2)]">
                    {{ comment.body }}
                  </div>
                </article>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  </section>
</template>
