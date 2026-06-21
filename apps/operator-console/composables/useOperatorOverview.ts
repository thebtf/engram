import { computed } from 'vue'
import { endpointEvidence, unsupportedOperatorAction } from './useOperatorApi'
import { useCreds, useIssuesState, useProjects, useRules } from './useMockData'
import { useOperatorMemoryLab } from './useOperatorMemoryLab'
import { useOperatorShellStatus } from './useOperatorShell'

export function useOperatorOverview() {
  const memoryLab = useOperatorMemoryLab()
  const memories = memoryLab.rows
  const issuesState = useIssuesState()
  const issues = issuesState.rows
  const creds = useCreds()
  const rules = useRules()
  const projects = useProjects()
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

  const modelHealthGap = unsupportedOperatorAction(
    'model-health',
    'GET /api/model-health',
    'No backend endpoint currently exposes model-level health for the overview chart.',
  )
  const accessGap = unsupportedOperatorAction(
    'access-summary',
    'GET /api/access/summary',
    'Single-user console does not have the future multi-user access module yet.',
  )
  const queueGap = {
    kind: 'dormant' as const,
    action: 'memory-review-queue',
    operable: false,
    evidence: endpointEvidence('GET /api/memory/candidates', 'feature-flag', {
      flag: 'VNEXT_F',
      reason: 'Review queue is gated until the server-side candidate workflow is enabled.',
    }),
  }

  return {
    memories,
    issues,
    creds,
    rules,
    projects,
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
    modelHealthGap,
    accessGap,
    queueGap,
  }
}
