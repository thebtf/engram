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
  type IssueDetailPageOptions,
  type IssueListOptions,
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

export interface IssueQueryFilters {
  project: Ref<string>
  ready: Ref<boolean>
  status: Ref<string>
  type: Ref<string>
  sourceProject: Ref<string>
  sourceAgent: Ref<string>
  q?: Ref<string>
  ownerLabel?: Ref<string>
  recipientLabel?: Ref<string>
  sourceSession?: Ref<string>
  participantSession?: Ref<string>
  targetSession?: Ref<string>
  limit?: Ref<number>
  offset?: Ref<number>
}

export function useIssuesQuery(filters: IssueQueryFilters) {
  return useQuery(() => {
    const project = filters.project.value
    const sourceAgent = filters.sourceAgent.value
    const ownerLabel = filters.ownerLabel?.value
    const recipientLabel = filters.recipientLabel?.value
    const params: IssueListOptions = {
      project: project === 'all' || sourceAgent === 'human' ? undefined : project,
      status: filters.status.value || undefined,
      type: filters.type.value || undefined,
      sourceProject: sourceAgent === 'human' ? project : filters.sourceProject.value || undefined,
      sourceAgent: sourceAgent || undefined,
      q: filters.q?.value,
      ownerLabel: ownerLabel === '' ? undefined : ownerLabel,
      recipientLabel: recipientLabel === '' ? undefined : recipientLabel,
      sourceSession: filters.sourceSession?.value || undefined,
      participantSession: filters.participantSession?.value || undefined,
      targetSession: filters.targetSession?.value || undefined,
      limit: filters.limit === undefined ? 50 : filters.limit.value,
      offset: filters.offset === undefined ? 0 : filters.offset.value,
    }
    return {
      queryKey: ['issues', project, params],
      enabled: filters.ready.value && project !== '' && (sourceAgent !== 'human' || project !== 'all'),
      queryFn: () => fetchIssues(params),
      retry: false,
      staleTime: 15_000,
    }
  })
}

export function useIssueDetailQuery(issueId: Ref<number>, page?: Ref<IssueDetailPageOptions>) {
  return useQuery(() => {
    const id = issueId.value
    const options = page?.value
    const params: Required<IssueDetailPageOptions> = {
      comments_offset: options?.comments_offset === undefined ? 0 : options.comments_offset,
      comments_limit: options?.comments_limit === undefined ? 50 : options.comments_limit,
      history_offset: options?.history_offset === undefined ? 0 : options.history_offset,
      history_limit: options?.history_limit === undefined ? 50 : options.history_limit,
    }
    return {
      queryKey: ['issue-detail', id, 'paged', params],
      queryFn: () => fetchIssue(id, params),
      enabled: id > 0,
      retry: false,
      staleTime: 15_000,
    }
  })
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
