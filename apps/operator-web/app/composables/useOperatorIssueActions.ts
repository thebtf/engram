import type { Issue } from '~/composables/useOperatorApi'

export type IssueStatus = Issue['status']
export type IssuePriority = Issue['priority']
export type IssueType = Issue['type']

export interface IssueUpdateFields {
  status?: IssueStatus
  comment?: string
  source_project?: string
  source_agent?: string
  title?: string
  body?: string
  priority?: IssuePriority
  type?: IssueType
  labels?: string[]
}

export const operatorIssueActor = {
  source_project: 'dashboard',
  source_agent: 'human',
} as const

function apiBase(): string {
  return useRuntimeConfig().public.apiBase
}

async function apiPatch<T>(path: string, body: unknown): Promise<T> {
  return $fetch<T>(`${apiBase()}${path}`, {
    method: 'PATCH',
    body,
    credentials: 'include',
  })
}

async function apiPost<T>(path: string, body: unknown): Promise<T> {
  return $fetch<T>(`${apiBase()}${path}`, {
    method: 'POST',
    body,
    credentials: 'include',
  })
}

async function apiDelete<T>(path: string): Promise<T> {
  return $fetch<T>(`${apiBase()}${path}`, {
    method: 'DELETE',
    credentials: 'include',
  })
}

export async function updateIssue(
  id: number,
  fields: IssueUpdateFields,
): Promise<{ message: string }> {
  return apiPatch<{ message: string }>(`/issues/${id}`, fields)
}

export async function deleteIssue(id: number): Promise<void> {
  await apiDelete<Record<string, unknown>>(`/issues/${id}`)
}

export async function acknowledgeIssues(ids: number[]): Promise<{ acknowledged: number }> {
  return apiPost<{ acknowledged: number }>('/issues/acknowledge', { ids })
}
