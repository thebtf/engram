<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { CodeCatalogEntry, CodeCatalogState } from '~/composables/useOperatorCode'

const { t } = useI18n()
const props = defineProps<{ catalog: CodeCatalogEntry[]; state: CodeCatalogState; pending: boolean; canRefresh: boolean }>()
const emit = defineEmits<{ refresh: [] }>()
const mode = ref<'repository' | 'worktree'>('repository')
const label = ref('')
const sourceRef = ref('')
const taskField = ref<HTMLTextAreaElement | null>(null)
const notice = ref<'copied' | 'select' | null>(null)
const repositories = computed(() => [...new Map(props.catalog.filter(entry => entry.view !== null).map(entry => [entry.sourceRef, entry])).values()])
const expanded = ref(false)
watch(() => props.state, state => { if (state === 'empty') expanded.value = true }, { immediate: true })
function disclosureChanged(event: Event): void {
  if (event.target instanceof HTMLDetailsElement) expanded.value = event.target.open
}
const selectedRepository = computed(() => repositories.value.find(entry => entry.sourceRef === sourceRef.value) ?? null)
const task = computed(() => {
  const name = label.value.trim()
  if (mode.value === 'repository' ? name === '' : selectedRepository.value === null) return ''
  return t(`workspace.connect.tasks.${mode.value}`, { label: JSON.stringify(name), repository: JSON.stringify(selectedRepository.value?.repository ?? '') })
})
watch(task, () => { notice.value = null })
function selectTask(): void {
  taskField.value?.focus()
  taskField.value?.select()
  notice.value = 'select'
}
async function copyTask(): Promise<void> {
  try {
    await navigator.clipboard.writeText(task.value)
    notice.value = 'copied'
  } catch {
    selectTask()
  }
}
</script>

<template>
  <details class="connect" :open="expanded" data-testid="code-repository-connect" @toggle="disclosureChanged">
    <summary>{{ t('workspace.connect.title') }}</summary>
    <p>{{ t('workspace.connect.boundary') }}</p>
    <ol class="steps">
      <li>{{ t('workspace.connect.openHost') }}</li>
      <li>{{ t('workspace.connect.runTask') }}</li>
      <li>{{ t('workspace.connect.readback') }}</li>
    </ol>
    <div class="fields">
      <label for="code-connect-kind">{{ t('workspace.connect.kind') }}
        <select id="code-connect-kind" v-model="mode">
          <option value="repository">{{ t('workspace.connect.repository') }}</option>
          <option value="worktree" :disabled="repositories.length === 0">{{ t('workspace.connect.worktree') }}</option>
        </select>
      </label>
      <label v-if="mode === 'worktree'" for="code-connect-source">{{ t('workspace.repository') }}
        <select id="code-connect-source" v-model="sourceRef">
          <option value="" disabled>{{ t('codeExplorer.context.chooseRepository') }}</option>
          <option v-for="entry in repositories" :key="entry.sourceRef" :value="entry.sourceRef">{{ entry.repository }} · {{ entry.workingCopy || t('codeExplorer.context.unnamedWorkingCopy') }}</option>
        </select>
      </label>
      <label v-if="mode === 'repository'" for="code-connect-label">{{ t('workspace.connect.label') }}
        <input id="code-connect-label" v-model="label" maxlength="120" autocomplete="off" :placeholder="t('workspace.connect.labelExample')" aria-describedby="code-connect-label-help">
      </label>
    </div>
    <p id="code-connect-label-help">{{ t(mode === 'repository' ? 'workspace.connect.labelHelp' : 'workspace.connect.worktreeHelp') }}</p>
    <template v-if="task !== ''">
      <label for="code-connect-task">{{ t('workspace.connect.taskLabel') }}</label>
      <textarea id="code-connect-task" ref="taskField" :value="task" readonly rows="7" data-testid="code-connect-task" />
      <div class="actions">
        <button class="btn" type="button" @click="copyTask">{{ t('workspace.connect.copy') }}</button>
        <button class="btn" type="button" @click="selectTask">{{ t('workspace.connect.select') }}</button>
      </div>
    </template>
    <p v-if="notice !== null" role="status" aria-live="polite">{{ t(`workspace.connect.notices.${notice}`) }}</p>
    <button class="btn primary" type="button" :disabled="pending || !canRefresh" data-testid="code-connect-readback" @click="emit('refresh')">{{ t('workspace.connect.refresh') }}</button>
    <p>{{ t('workspace.connect.recovery') }}</p>
  </details>
</template>

<style scoped>
.connect { min-width:0; border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:16px; color:var(--fg); font-size:var(--text-sm); }
summary { cursor:pointer; font-weight:700; }
p, .steps { max-width:75ch; color:var(--muted); line-height:1.5; }
.steps { padding-inline-start:24px; }.steps li + li { margin-top:6px; }
.fields { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:12px; margin-top:16px; }
label { display:grid; gap:6px; min-width:0; font-weight:700; }
input, select, textarea { width:100%; min-width:0; min-height:40px; box-sizing:border-box; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--bg); color:var(--fg); padding:8px 10px; font:inherit; }
textarea { margin-top:6px; resize:vertical; line-height:1.5; }
.actions { display:flex; flex-wrap:wrap; gap:8px; margin-top:8px; }
.btn { min-height:40px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface); color:var(--fg); padding:8px 12px; font:inherit; font-weight:700; cursor:pointer; }.btn.primary { border-color:var(--accent); background:var(--accent); color:var(--accent-on); }.btn:disabled { cursor:not-allowed; opacity:.55; }
:is(summary, input, select, textarea, .btn):focus-visible { outline:2px solid var(--accent); outline-offset:2px; }
@media (pointer:coarse) { input, select, .btn { min-height:44px; } }
@media (max-width:720px) { .fields { grid-template-columns:1fr; } }
</style>
