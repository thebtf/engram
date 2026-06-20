<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useMutation, useQueryClient } from '@tanstack/vue-query'
import type { Rule } from '~/composables/useOperatorApi'
import { createRule, deleteRule, updateRule } from '~/composables/useRulesApi'
import { useProjectsQuery, useRulesQuery } from '~/composables/useOperatorQueries'
import { useOperatorI18n } from '~/composables/useOperatorI18n'
import { formatAbsoluteDate } from '~/utils/formatters'

const { t } = useOperatorI18n()
const queryClient = useQueryClient()

const selectedProject = ref('')
const projectsQuery = useProjectsQuery()
const rulesQuery = useRulesQuery(selectedProject)

const projects = computed(() => projectsQuery.data.value ?? [])
const rules = computed(() => rulesQuery.data.value?.rules ?? [])
const total = computed(() => rulesQuery.data.value?.total ?? rules.value.length)

const createProject = ref('')
const createContent = ref('')
const createPriority = ref(0)
const createError = ref('')
const actionError = ref('')
const actionMessage = ref('')

const editingRuleId = ref<number | null>(null)
const editContent = ref('')
const editPriority = ref(0)
const editError = ref('')
const deletingRuleId = ref<number | null>(null)

watch(selectedProject, (project) => {
  createProject.value = project
})

function normalizedPriority(value: unknown): number {
  const parsed = Number(value)
  return Number.isFinite(parsed) ? Math.trunc(parsed) : 0
}

async function invalidateRules() {
  await queryClient.invalidateQueries({ queryKey: ['rules'] })
}

const createRuleMutation = useMutation({
  mutationFn: () =>
    createRule({
      project: createProject.value || undefined,
      content: createContent.value,
      priority: normalizedPriority(createPriority.value),
    }),
  onSuccess: async (rule) => {
    createContent.value = ''
    createPriority.value = 0
    createError.value = ''
    actionError.value = ''
    actionMessage.value = `Rule #${rule.id} created`
    await invalidateRules()
  },
  onError: (error) => {
    createError.value = error instanceof Error ? error.message : 'Не удалось создать rule'
  },
})

const updateRuleMutation = useMutation({
  mutationFn: ({ rule, content, priority }: { rule: Rule; content: string; priority: number }) =>
    updateRule(rule.id, { content, priority }),
  onSuccess: async (rule) => {
    editingRuleId.value = null
    editError.value = ''
    actionError.value = ''
    actionMessage.value = `Rule #${rule.id} updated`
    await invalidateRules()
  },
  onError: (error) => {
    editError.value = error instanceof Error ? error.message : 'Не удалось обновить rule'
  },
})

const deleteRuleMutation = useMutation({
  mutationFn: (rule: Rule) => {
    deletingRuleId.value = rule.id
    return deleteRule(rule.id)
  },
  onSuccess: async (_result, rule) => {
    if (editingRuleId.value === rule.id) {
      cancelEdit()
    }
    actionError.value = ''
    actionMessage.value = `Rule #${rule.id} deleted`
    await invalidateRules()
  },
  onError: (error) => {
    actionError.value = error instanceof Error ? error.message : 'Не удалось удалить rule'
  },
  onSettled: () => {
    deletingRuleId.value = null
  },
})

function submitCreate() {
  createError.value = ''
  actionError.value = ''
  actionMessage.value = ''

  if (!createContent.value.trim()) {
    createError.value = 'Content обязателен'
    return
  }

  createRuleMutation.mutate()
}

function startEdit(rule: Rule) {
  editingRuleId.value = rule.id
  editContent.value = rule.content
  editPriority.value = rule.priority
  editError.value = ''
  actionError.value = ''
  actionMessage.value = ''
}

function cancelEdit() {
  editingRuleId.value = null
  editContent.value = ''
  editPriority.value = 0
  editError.value = ''
}

function submitEdit(rule: Rule) {
  const content = editContent.value.trim()
  const priority = normalizedPriority(editPriority.value)

  editError.value = ''
  actionError.value = ''
  actionMessage.value = ''

  if (!content) {
    editError.value = 'Content обязателен'
    return
  }

  if (content === rule.content && priority === rule.priority) {
    editError.value = 'Нет изменений для сохранения'
    return
  }

  updateRuleMutation.mutate({ rule, content, priority })
}

function confirmDelete(rule: Rule) {
  actionError.value = ''
  actionMessage.value = ''

  if (typeof window !== 'undefined' && !window.confirm(`Delete rule #${rule.id}?`)) {
    return
  }

  deleteRuleMutation.mutate(rule)
}

function ruleScope(rule: Rule): string {
  return rule.project || 'global'
}

function isRuleDeleting(rule: Rule): boolean {
  return deleteRuleMutation.isPending.value && deletingRuleId.value === rule.id
}

function isRuleUpdating(rule: Rule): boolean {
  return updateRuleMutation.isPending.value && editingRuleId.value === rule.id
}
</script>

<template>
  <section class="space-y-5">
    <div class="surface-panel p-5">
      <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 class="text-xl font-semibold">Rules</h1>
          <p class="mt-2 text-sm text-[var(--fg-2)]">
            Live-backed Rules MVP slice в новом shell: list, create, edit и delete через текущие REST endpoints.
          </p>
        </div>

        <div class="flex flex-wrap items-center gap-3">
          <div class="mono-data text-sm text-[var(--muted)]">
            {{ total }} rows
          </div>
          <button
            type="button"
            class="rounded-lg border border-[var(--border)] bg-[var(--surface-warm)] px-4 py-2 text-sm text-[var(--fg-2)]"
            @click="rulesQuery.refetch()"
          >
            Refresh
          </button>
        </div>
      </div>

      <div class="mb-4 flex flex-wrap gap-3">
        <label class="text-sm text-[var(--fg-2)]">
          <span class="mr-2">{{ t('common.allProjects') }}</span>
          <select
            v-model="selectedProject"
            class="rounded-lg border border-[var(--border)] bg-[var(--surface-warm)] px-3 py-2 text-sm text-[var(--fg)]"
          >
            <option value="">{{ t('common.allProjects') }}</option>
            <option v-for="project in projects" :key="project" :value="project">
              {{ project }}
            </option>
          </select>
        </label>
      </div>

      <div v-if="actionMessage" class="mb-4 rounded-lg border border-[color:rgba(52,211,153,0.35)] bg-[color:rgba(52,211,153,0.12)] px-3 py-2 text-sm text-[var(--success)]">
        {{ actionMessage }}
      </div>
      <div v-if="actionError" class="mb-4 rounded-lg border border-[color:rgba(248,113,113,0.4)] bg-[color:rgba(248,113,113,0.12)] px-3 py-2 text-sm text-[var(--danger)]">
        {{ actionError }}
      </div>

      <div class="grid gap-4 xl:grid-cols-[minmax(0,1fr),380px]">
        <div class="surface-panel-warm p-4">
          <div class="mb-3 flex flex-wrap items-center justify-between gap-3">
            <div>
              <div class="text-xs uppercase tracking-[0.08em] text-[var(--muted)]">Current Rules</div>
              <div class="mt-1 text-xs text-[var(--fg-2)]">
                Scope is immutable on edit; recreate a rule if scope must change.
              </div>
            </div>
          </div>

          <div v-if="rulesQuery.isPending.value" class="text-sm text-[var(--muted)]">
            {{ t('common.loading') }}
          </div>
          <div v-else-if="rulesQuery.isError.value" class="text-sm text-[var(--danger)]">
            {{ rulesQuery.error.value?.message || 'Не удалось загрузить rules' }}
          </div>
          <div v-else-if="rules.length === 0" class="text-sm text-[var(--muted)]">
            {{ t('common.empty') }}
          </div>
          <div v-else class="space-y-3">
            <article
              v-for="rule in rules"
              :key="rule.id"
              class="rounded-xl border border-[var(--border)] bg-[var(--surface)] p-4"
            >
              <div class="mb-3 flex flex-wrap items-start justify-between gap-3">
                <div>
                  <div class="text-sm font-semibold">
                    {{ ruleScope(rule) }}
                  </div>
                  <div class="mono-data mt-1 text-xs text-[var(--muted)]">
                    #{{ rule.id }} · priority {{ rule.priority }} · v{{ rule.version }}
                  </div>
                </div>
                <div class="flex flex-wrap gap-2">
                  <button
                    type="button"
                    class="rounded-md border border-[var(--border)] bg-[var(--surface-warm)] px-3 py-2 text-xs font-semibold text-[var(--fg-2)] disabled:opacity-60"
                    :disabled="updateRuleMutation.isPending.value || deleteRuleMutation.isPending.value"
                    @click="startEdit(rule)"
                  >
                    Edit
                  </button>
                  <button
                    type="button"
                    class="rounded-md border border-[color:rgba(248,113,113,0.4)] bg-transparent px-3 py-2 text-xs font-semibold text-[var(--danger)] disabled:opacity-60"
                    :disabled="deleteRuleMutation.isPending.value || updateRuleMutation.isPending.value"
                    @click="confirmDelete(rule)"
                  >
                    {{ isRuleDeleting(rule) ? 'Deleting...' : 'Delete' }}
                  </button>
                </div>
              </div>

              <form
                v-if="editingRuleId === rule.id"
                class="space-y-3 rounded-lg border border-[var(--border)] bg-[var(--surface-warm)] p-3"
                @submit.prevent="submitEdit(rule)"
              >
                <div class="rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-xs text-[var(--muted)]">
                  Editing #{{ rule.id }} in read-only scope <span class="font-semibold text-[var(--fg-2)]">{{ ruleScope(rule) }}</span>.
                </div>

                <label class="block">
                  <span class="mb-1 block text-xs text-[var(--muted)]">Content</span>
                  <textarea
                    v-model="editContent"
                    rows="5"
                    class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm leading-6 text-[var(--fg)]"
                  />
                </label>

                <label class="block">
                  <span class="mb-1 block text-xs text-[var(--muted)]">Priority</span>
                  <input
                    v-model.number="editPriority"
                    type="number"
                    class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm text-[var(--fg)]"
                  />
                </label>

                <div v-if="editError" class="rounded-lg border border-[color:rgba(248,113,113,0.4)] bg-[color:rgba(248,113,113,0.12)] px-3 py-2 text-sm text-[var(--danger)]">
                  {{ editError }}
                </div>

                <div class="flex flex-wrap gap-2">
                  <button
                    type="submit"
                    class="rounded-md border border-[var(--accent)] bg-[color:rgba(76,141,255,0.16)] px-3 py-2 text-xs font-semibold text-[var(--fg)] disabled:opacity-60"
                    :disabled="isRuleUpdating(rule)"
                  >
                    {{ isRuleUpdating(rule) ? 'Saving...' : 'Save edit' }}
                  </button>
                  <button
                    type="button"
                    class="rounded-md border border-[var(--border)] bg-transparent px-3 py-2 text-xs font-semibold text-[var(--fg-2)]"
                    @click="cancelEdit"
                  >
                    Cancel
                  </button>
                </div>
              </form>

              <template v-else>
                <p class="whitespace-pre-wrap text-sm leading-6 text-[var(--fg-2)]">
                  {{ rule.content }}
                </p>
                <div class="mt-3 flex flex-wrap items-center justify-between gap-3 text-xs text-[var(--muted)]">
                  <span>updated {{ formatAbsoluteDate(rule.updated_at) }}</span>
                  <span v-if="rule.edited_by">edited by {{ rule.edited_by }}</span>
                </div>
              </template>
            </article>
          </div>
        </div>

        <form class="surface-panel-warm p-4" @submit.prevent="submitCreate">
          <div class="mb-3 text-xs uppercase tracking-[0.08em] text-[var(--muted)]">
            New rule
          </div>

          <div class="space-y-3">
            <label class="block">
              <span class="mb-1 block text-xs text-[var(--muted)]">Scope</span>
              <select
                v-model="createProject"
                class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm text-[var(--fg)]"
              >
                <option value="">global</option>
                <option v-for="project in projects" :key="project" :value="project">
                  {{ project }}
                </option>
              </select>
            </label>

            <label class="block">
              <span class="mb-1 block text-xs text-[var(--muted)]">Content</span>
              <textarea
                v-model="createContent"
                rows="7"
                class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm leading-6 text-[var(--fg)]"
                placeholder="Write the behavioral rule exactly as it should be injected."
              />
            </label>

            <label class="block">
              <span class="mb-1 block text-xs text-[var(--muted)]">Priority</span>
              <input
                v-model.number="createPriority"
                type="number"
                class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm text-[var(--fg)]"
              />
            </label>

            <div class="rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-xs leading-5 text-[var(--muted)]">
              This MVP intentionally exposes only server-backed fields: scope on create, content, priority, and delete. No enable/disable or drag reorder controls are shown.
            </div>

            <div v-if="createError" class="rounded-lg border border-[color:rgba(248,113,113,0.4)] bg-[color:rgba(248,113,113,0.12)] px-3 py-2 text-sm text-[var(--danger)]">
              {{ createError }}
            </div>

            <button
              type="submit"
              class="w-full rounded-lg border border-[var(--accent)] bg-[color:rgba(76,141,255,0.16)] px-4 py-2 text-sm font-semibold text-[var(--fg)] disabled:opacity-60"
              :disabled="createRuleMutation.isPending.value || !createContent.trim()"
            >
              {{ createRuleMutation.isPending.value ? 'Creating...' : 'Create rule' }}
            </button>
          </div>
        </form>
      </div>
    </div>
  </section>
</template>
