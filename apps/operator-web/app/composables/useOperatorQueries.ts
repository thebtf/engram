import { useQuery } from '@tanstack/vue-query'
import type { Ref } from 'vue'
import {
  checkForUpdate,
  fetchCredentials,
  fetchConfig,
  fetchIssue,
  fetchIssues,
  fetchMaintenanceStats,
  fetchMemories,
  fetchProjects,
  fetchRules,
  fetchSessions,
  fetchSelfCheck,
  fetchStats,
  fetchVaultStatus,
} from '~/composables/useOperatorApi'

export function useProjectsQuery() {
  return useQuery({
    queryKey: ['projects'],
    queryFn: () => fetchProjects(),
    staleTime: 30_000,
  })
}

export function useSessionsQuery(selectedProject: Ref<string>, ready: Ref<boolean>) {
  return useQuery(() => ({
    queryKey: ['sessions', selectedProject.value],
    enabled: ready.value && selectedProject.value !== '',
    queryFn: () =>
      fetchSessions({
        project: selectedProject.value === 'all' ? undefined : selectedProject.value,
        limit: 20,
        offset: 0,
      }),
    staleTime: 15_000,
  }))
}

export function useRulesQuery(selectedProject: Ref<string>, ready: Ref<boolean>) {
  return useQuery(() => ({
    queryKey: ['rules', selectedProject.value],
    enabled: ready.value && selectedProject.value !== '',
    queryFn: () =>
      fetchRules({
        project: selectedProject.value === 'all' ? undefined : selectedProject.value,
        limit: 50,
      }),
    staleTime: 15_000,
  }))
}

export function useIssuesQuery(filters: {
  project: Ref<string>
  ready: Ref<boolean>
  status: Ref<string>
  type: Ref<string>
  sourceProject: Ref<string>
  sourceAgent: Ref<string>
}) {
  return useQuery(() => ({
    queryKey: [
      'issues',
      filters.project.value || 'all',
      filters.status.value || 'all',
      filters.type.value || 'all',
      filters.sourceProject.value || 'all',
      filters.sourceAgent.value || 'all',
    ],
    enabled: filters.ready.value && filters.project.value !== '' && (filters.sourceAgent.value !== 'human' || filters.project.value !== 'all'),
    queryFn: () =>
      fetchIssues({
        project: filters.project.value === 'all' || filters.sourceAgent.value === 'human' ? undefined : filters.project.value,
        status: filters.status.value || undefined,
        type: filters.type.value || undefined,
        sourceProject: filters.sourceAgent.value === 'human' ? filters.project.value : filters.sourceProject.value || undefined,
        sourceAgent: filters.sourceAgent.value || undefined,
        limit: 50,
        offset: 0,
      }),
    staleTime: 15_000,
  }))
}

export function useIssueDetailQuery(issueId: Ref<number>) {
  return useQuery(() => ({
    queryKey: ['issue-detail', issueId.value],
    queryFn: () => fetchIssue(issueId.value),
    enabled: issueId.value > 0,
    staleTime: 15_000,
  }))
}


export function useVaultOverviewQuery() {
  return useQuery({
    queryKey: ['vault-overview'],
    queryFn: async () => {
      const [credentials, status] = await Promise.all([
        fetchCredentials(),
        fetchVaultStatus(),
      ])

      return {
        credentials,
        status,
      }
    },
    staleTime: 15_000,
  })
}

export function useStatsQuery(selectedProject?: Ref<string>) {
  return useQuery(() => ({
    queryKey: ['stats', selectedProject?.value || 'all'],
    queryFn: () => fetchStats(selectedProject?.value || null),
    staleTime: 15_000,
  }))
}

export function useHealthQuery() {
  return useQuery({
    queryKey: ['selfcheck'],
    queryFn: () => fetchSelfCheck(),
    staleTime: 30_000,
    refetchInterval: 30_000,
  })
}

export function useMaintenanceStatsQuery() {
  return useQuery({
    queryKey: ['maintenance-stats'],
    queryFn: () => fetchMaintenanceStats(),
    staleTime: 60_000,
  })
}

export function useConfigQuery() {
  return useQuery({
    queryKey: ['config'],
    queryFn: () => fetchConfig(),
    staleTime: 30_000,
  })
}

export function useUpdateCheckQuery() {
  return useQuery({
    queryKey: ['update-check'],
    queryFn: () => checkForUpdate(),
    staleTime: 30 * 60_000,
  })
}

export function useMemoriesQuery(selectedProject: Ref<string>, ready: Ref<boolean>, offset: Ref<number>, query: Ref<string>) {
  return useQuery(() => ({
    queryKey: ['memories', selectedProject.value, offset.value, query.value],
    queryFn: () => fetchMemories({ project: selectedProject.value, offset: offset.value, limit: 100, query: query.value }),
    enabled: ready.value && selectedProject.value !== '' && selectedProject.value !== 'all',
    staleTime: 15_000,
  }))
}
