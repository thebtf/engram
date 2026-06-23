import { computed } from 'vue'
import { unsupportedOperatorAction } from './useOperatorApi'
import { useCreds, useIssuesState, useModelsState, useProjects, useRules } from './useMockData'
import { useOperatorMemoryLab } from './useOperatorMemoryLab'
import { useOperatorQueue } from './useOperatorQueue'
import { useOperatorShellStatus } from './useOperatorShell'

export function useOperatorOverview() {
  const memoryLab = useOperatorMemoryLab()
  const memories = memoryLab.rows
  const issuesState = useIssuesState()
  const issues = issuesState.rows
  const creds = useCreds()
  const rules = useRules()
  const projects = useProjects()
  const modelsState = useModelsState()
  const models = computed(() => modelsState.rows.value)
  const queue = useOperatorQueue()
  const shell = useOperatorShellStatus()

  const memoryActive = computed(() => memories.filter((memory) => !memory.noise).length)
  const memoryNoise = computed(() => memories.filter((memory) => memory.noise).length)
  const openIssues = computed(() => issues.filter((issue) => issue.status === 'open').length)
  const workIssues = computed(() => issues.filter((issue) => ['acknowledged', 'reopened'].includes(issue.status)).length)
  const resolvedIssues = computed(() => issues.filter((issue) => issue.status === 'resolved').length)
  const closedIssues = computed(() => issues.filter((issue) => issue.status === 'closed').length)
  const ruleCount = computed(() => rules.rows.value.length)
  const projectCount = computed(() => projects.rows.value.length)
  const memoryPending = computed(() => memoryLab.pending.value && memories.length === 0)
  const issuesPending = computed(() => issuesState.pending.value && issues.length === 0)
  const rulesPending = computed(() => rules.pending.value && rules.rows.value.length === 0)
  const projectsPending = computed(() => projects.pending.value && projects.rows.value.length === 0)
  const modelsPending = computed(() => modelsState.pending.value && models.value.length === 0)
  const queuePending = computed(() => queue.pending.value && queue.rows.length === 0)
  const queueGated = computed(() => queue.loadState.value.kind === 'gated')
  const queueCount = computed(() => queue.rows.length)
  const modelsError = computed(() => modelsState.error.value)
  const modelOK = computed(() => models.value.filter((model) => model.health === 'ok').length)
  const modelStandby = computed(() => models.value.filter((model) => model.health === 'standby').length)
  const modelDegraded = computed(() => models.value.filter((model) => model.health === 'degraded').length)
  const accessGap = unsupportedOperatorAction(
    'access-summary',
    'GET /api/access/summary',
    'Single-user console does not have the future multi-user access module yet.',
  )
  return {
    memories,
    issues,
    creds,
    rules,
    projects,
    models,
    queue,
    info: shell.info,
    memoryActive,
    memoryNoise,
    openIssues,
    workIssues,
    resolvedIssues,
    closedIssues,
    ruleCount,
    projectCount,
    memoryPending,
    issuesPending,
    rulesPending,
    projectsPending,
    modelsPending,
    queuePending,
    queueGated,
    queueCount,
    modelsError,
    modelOK,
    modelStandby,
    modelDegraded,
    accessGap,
  }
}
