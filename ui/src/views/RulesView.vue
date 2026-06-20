<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { Shield, Plus, RefreshCw, Trash2, PencilLine, AlertTriangle } from 'lucide-vue-next'
import { useAuth } from '@/composables/useAuth'
import { useUiI18n } from '@/composables/useUiI18n'
import {
  createRule,
  deleteRule,
  fetchProjects,
  fetchRules,
  updateRule,
  type Rule,
} from '@/utils/api'
import { safeAbsoluteDate, truncate } from '@/utils/formatters'
import EmptyState from '@/components/layout/EmptyState.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
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

const { user } = useAuth()
const { t } = useUiI18n()

const projects = ref<string[]>([])
const selectedProject = ref('all')
const rules = ref<Rule[]>([])
const selectedRuleId = ref<number | null>(null)
const loading = ref(false)
const bootLoading = ref(true)
const error = ref<string | null>(null)
const actionError = ref<string | null>(null)

const showCreateDialog = ref(false)
const showEditDialog = ref(false)
const showDeleteDialog = ref(false)
const creating = ref(false)
const saving = ref(false)

const createContent = ref('')
const createScope = ref('__global__')
const editContent = ref('')
const deleteTarget = ref<Rule | null>(null)

const selectedRule = computed(() =>
  rules.value.find(rule => rule.id === selectedRuleId.value) ?? null,
)

function scopeLabel(project?: string | null): string {
  return project || t.value.rules.scope.global
}

function nextRulePriority(): number {
  if (rules.value.length === 0) return 0
  const minPriority = Math.min(...rules.value.map(rule => rule.priority))
  return minPriority - 10
}

async function loadRules() {
  loading.value = true
  error.value = null

  try {
    const response = await fetchRules(
      selectedProject.value === 'all'
        ? {}
        : { project: selectedProject.value },
    )
    rules.value = response.rules ?? []
    if (!rules.value.some(rule => rule.id === selectedRuleId.value)) {
      selectedRuleId.value = rules.value[0]?.id ?? null
    }
  } catch (err: any) {
    error.value = err?.message || t.value.rules.loadError
    rules.value = []
    selectedRuleId.value = null
  } finally {
    loading.value = false
    bootLoading.value = false
  }
}

async function loadData() {
  bootLoading.value = true
  actionError.value = null
  try {
    projects.value = await fetchProjects()
    await loadRules()
  } catch (err: any) {
    error.value = err?.message || t.value.rules.loadError
    bootLoading.value = false
  }
}

onMounted(async () => {
  await loadData()
})

watch(selectedProject, async (next, prev) => {
  if (next !== prev) {
    await loadRules()
  }
})

function openCreateDialog() {
  createContent.value = ''
  createScope.value = '__global__'
  actionError.value = null
  showCreateDialog.value = true
}

function openEditDialog(rule: Rule) {
  selectedRuleId.value = rule.id
  editContent.value = rule.content
  actionError.value = null
  showEditDialog.value = true
}

function openDeleteDialog(rule: Rule) {
  deleteTarget.value = rule
  showDeleteDialog.value = true
}

async function handleCreate() {
  const content = createContent.value.trim()
  if (!content) return

  creating.value = true
  actionError.value = null

  try {
    const created = await createRule({
      content,
      project: createScope.value === '__global__' ? undefined : createScope.value,
      priority: nextRulePriority(),
      edited_by: user.value?.email || 'operator',
    })
    showCreateDialog.value = false
    await loadRules()
    selectedRuleId.value = created.id
  } catch (err: any) {
    actionError.value = err?.message || t.value.rules.loadError
  } finally {
    creating.value = false
  }
}

async function handleSave() {
  if (!selectedRule.value) return

  const content = editContent.value.trim()
  if (!content) return

  saving.value = true
  actionError.value = null
  try {
    await updateRule(selectedRule.value.id, {
      content,
      edited_by: user.value?.email || 'operator',
    })
    showEditDialog.value = false
    await loadRules()
  } catch (err: any) {
    actionError.value = err?.message || t.value.rules.loadError
  } finally {
    saving.value = false
  }
}

async function handleDelete() {
  if (!deleteTarget.value) return

  actionError.value = null
  showDeleteDialog.value = false
  try {
    await deleteRule(deleteTarget.value.id)
    if (selectedRuleId.value === deleteTarget.value.id) {
      selectedRuleId.value = null
    }
    deleteTarget.value = null
    await loadRules()
  } catch (err: any) {
    actionError.value = err?.message || t.value.rules.loadError
  }
}
</script>

<template>
  <div class="space-y-4 pt-4">
    <div class="flex items-center justify-between gap-3 flex-wrap">
      <div class="flex items-center gap-3">
        <Shield class="size-5 text-primary" />
        <div>
          <h1 class="text-lg font-semibold">{{ t.rules.title }}</h1>
          <p class="text-sm text-muted-foreground">{{ t.rules.subtitle }}</p>
        </div>
      </div>
      <div class="flex items-center gap-2">
        <Select v-model="selectedProject">
          <SelectTrigger class="w-[220px]">
            <SelectValue :placeholder="t.rules.filterAll" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{{ t.rules.filterAll }}</SelectItem>
            <SelectItem v-for="project in projects" :key="project" :value="project">
              {{ project }}
            </SelectItem>
          </SelectContent>
        </Select>
        <Button variant="outline" size="sm" :disabled="loading" @click="loadRules">
          <RefreshCw :class="['size-4', loading && 'animate-spin']" />
          {{ t.common.refresh }}
        </Button>
        <Button size="sm" @click="openCreateDialog">
          <Plus class="size-4" />
          {{ t.rules.create }}
        </Button>
      </div>
    </div>

    <Card class="border-dashed">
      <CardContent class="space-y-2 pt-6 text-sm text-muted-foreground">
        <p>{{ t.rules.reorderNote }}</p>
        <p>{{ t.rules.honestyNote }}</p>
      </CardContent>
    </Card>

    <div v-if="bootLoading" class="text-sm text-muted-foreground">
      {{ t.rules.loading }}
    </div>

    <div v-else-if="error" class="rounded-lg border border-destructive/30 bg-destructive/10 p-4 text-sm text-destructive">
      {{ error }}
    </div>

    <EmptyState
      v-else-if="rules.length === 0"
      icon="fa-scroll"
      :title="t.rules.emptyTitle"
      :description="t.rules.emptyDescription"
    />

    <div v-else class="grid gap-4 xl:grid-cols-[minmax(0,1.3fr)_minmax(320px,0.9fr)]">
      <Card>
        <CardHeader class="pb-3">
          <CardTitle class="text-sm font-medium">{{ t.rules.listTitle }}</CardTitle>
          <CardDescription>{{ t.rules.listDescription }}</CardDescription>
        </CardHeader>
        <CardContent class="pt-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead class="w-36">{{ t.rules.headers.scope }}</TableHead>
                <TableHead>{{ t.rules.headers.rule }}</TableHead>
                <TableHead class="w-24 text-right">{{ t.rules.headers.priority }}</TableHead>
                <TableHead class="w-24 text-right">{{ t.rules.headers.version }}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow
                v-for="rule in rules"
                :key="rule.id"
                class="cursor-pointer"
                :class="selectedRuleId === rule.id ? 'bg-muted/60' : ''"
                @click="selectedRuleId = rule.id"
              >
                <TableCell>
                  <Badge variant="outline">{{ scopeLabel(rule.project) }}</Badge>
                </TableCell>
                <TableCell class="text-sm text-foreground">
                  <div class="space-y-2">
                    <p>{{ truncate(rule.content, 120) }}</p>
                    <div class="flex items-center gap-2">
                      <Button variant="outline" size="xs" @click.stop="openEditDialog(rule)">
                        <PencilLine class="size-3.5" />
                        {{ t.rules.actions.edit }}
                      </Button>
                      <Button variant="outline" size="xs" class="text-destructive" @click.stop="openDeleteDialog(rule)">
                        <Trash2 class="size-3.5" />
                        {{ t.rules.actions.delete }}
                      </Button>
                    </div>
                  </div>
                </TableCell>
                <TableCell class="text-right font-mono">{{ rule.priority }}</TableCell>
                <TableCell class="text-right font-mono">v{{ rule.version }}</TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      <Card>
        <CardHeader class="pb-3">
          <CardTitle class="text-sm font-medium">{{ t.rules.detailTitle }}</CardTitle>
          <CardDescription v-if="selectedRule">
            #{{ selectedRule.id }} · v{{ selectedRule.version }}
          </CardDescription>
        </CardHeader>
        <CardContent v-if="selectedRule" class="space-y-4">
          <div v-if="actionError" class="flex items-start gap-2 rounded-lg border border-destructive/30 bg-destructive/10 p-3 text-sm text-destructive">
            <AlertTriangle class="size-4 mt-0.5 shrink-0" />
            <span>{{ actionError }}</span>
          </div>

          <div class="space-y-2">
            <p class="text-xs uppercase tracking-wide text-muted-foreground">{{ t.rules.headers.rule }}</p>
            <div class="rounded-md border bg-muted/30 p-3 text-sm leading-6 whitespace-pre-wrap">
              {{ selectedRule.content }}
            </div>
          </div>

          <div class="grid gap-3 sm:grid-cols-2">
            <div>
              <p class="text-xs uppercase tracking-wide text-muted-foreground">{{ t.rules.labels.scope }}</p>
              <p class="text-sm">{{ scopeLabel(selectedRule.project) }}</p>
            </div>
            <div>
              <p class="text-xs uppercase tracking-wide text-muted-foreground">{{ t.rules.labels.priority }}</p>
              <p class="text-sm font-mono">{{ selectedRule.priority }}</p>
            </div>
            <div>
              <p class="text-xs uppercase tracking-wide text-muted-foreground">{{ t.rules.labels.version }}</p>
              <p class="text-sm font-mono">v{{ selectedRule.version }}</p>
            </div>
            <div>
              <p class="text-xs uppercase tracking-wide text-muted-foreground">{{ t.rules.labels.editedBy }}</p>
              <p class="text-sm">{{ selectedRule.edited_by || t.common.none }}</p>
            </div>
            <div>
              <p class="text-xs uppercase tracking-wide text-muted-foreground">{{ t.rules.labels.created }}</p>
              <p class="text-sm">{{ safeAbsoluteDate(selectedRule.created_at) }}</p>
            </div>
            <div>
              <p class="text-xs uppercase tracking-wide text-muted-foreground">{{ t.rules.labels.updated }}</p>
              <p class="text-sm">{{ safeAbsoluteDate(selectedRule.updated_at) }}</p>
            </div>
          </div>

          <div class="rounded-lg border border-amber-500/20 bg-amber-500/10 p-3 text-sm text-amber-700 dark:text-amber-300">
            {{ t.rules.immutableScopeNote }}
          </div>
        </CardContent>

        <CardContent v-else class="text-sm text-muted-foreground">
          {{ t.rules.detailEmpty }}
        </CardContent>
      </Card>
    </div>

    <Dialog :open="showCreateDialog" @update:open="showCreateDialog = $event">
      <DialogContent class="max-w-xl">
        <DialogHeader>
          <DialogTitle>{{ t.rules.dialogs.createTitle }}</DialogTitle>
        </DialogHeader>
        <div class="space-y-4">
          <div v-if="actionError" class="text-sm text-destructive">{{ actionError }}</div>
          <div class="space-y-2">
            <label class="text-sm font-medium">{{ t.rules.dialogs.contentLabel }}</label>
            <Textarea
              v-model="createContent"
              :placeholder="t.rules.dialogs.contentPlaceholder"
              class="min-h-[140px]"
            />
          </div>
          <div class="space-y-2">
            <label class="text-sm font-medium">{{ t.rules.dialogs.scopeLabel }}</label>
            <Select v-model="createScope">
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="__global__">{{ t.rules.scope.global }}</SelectItem>
                <SelectItem v-for="project in projects" :key="project" :value="project">
                  {{ project }}
                </SelectItem>
              </SelectContent>
            </Select>
          </div>
        </div>
        <DialogFooter>
          <Button variant="secondary" @click="showCreateDialog = false">{{ t.rules.actions.cancel }}</Button>
          <Button :disabled="creating || !createContent.trim()" @click="handleCreate">
            {{ creating ? t.rules.actions.creating : t.rules.actions.create }}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>

    <Dialog :open="showEditDialog" @update:open="showEditDialog = $event">
      <DialogContent class="max-w-xl">
        <DialogHeader>
          <DialogTitle>{{ t.rules.dialogs.editTitle }}</DialogTitle>
        </DialogHeader>
        <div class="space-y-4">
          <div v-if="actionError" class="text-sm text-destructive">{{ actionError }}</div>
          <div class="space-y-2">
            <label class="text-sm font-medium">{{ t.rules.dialogs.contentLabel }}</label>
            <Textarea
              v-model="editContent"
              :placeholder="t.rules.dialogs.contentPlaceholder"
              class="min-h-[140px]"
            />
          </div>
          <div class="rounded-lg border border-amber-500/20 bg-amber-500/10 p-3 text-sm text-amber-700 dark:text-amber-300">
            {{ t.rules.immutableScopeNote }}
          </div>
        </div>
        <DialogFooter>
          <Button variant="secondary" @click="showEditDialog = false">{{ t.rules.actions.cancel }}</Button>
          <Button :disabled="saving || !editContent.trim()" @click="handleSave">
            {{ t.rules.actions.save }}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>

    <AlertDialog :open="showDeleteDialog" @update:open="showDeleteDialog = $event">
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{{ t.rules.dialogs.deleteTitle }}</AlertDialogTitle>
          <AlertDialogDescription>
            {{ t.rules.dialogs.deleteDescription }}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>{{ t.rules.actions.cancel }}</AlertDialogCancel>
          <AlertDialogAction class="bg-destructive text-destructive-foreground hover:bg-destructive/90" @click="handleDelete">
            {{ t.rules.actions.delete }}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  </div>
</template>
