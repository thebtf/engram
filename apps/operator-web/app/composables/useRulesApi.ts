import type { Rule } from '~/composables/useOperatorApi'

export interface CreateRuleInput {
  project?: string
  content: string
  priority: number
}

export interface UpdateRuleInput {
  content: string
  priority: number
}

function apiBase(): string {
  return useRuntimeConfig().public.apiBase
}

async function apiPost<T>(path: string, body: unknown): Promise<T> {
  return $fetch<T>(`${apiBase()}${path}`, {
    method: 'POST',
    body,
    credentials: 'include',
  })
}

async function apiPatch<T>(path: string, body: unknown): Promise<T> {
  return $fetch<T>(`${apiBase()}${path}`, {
    method: 'PATCH',
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

export async function createRule(input: CreateRuleInput): Promise<Rule> {
  const project = input.project?.trim()
  return apiPost<Rule>('/rules', {
    project: project || undefined,
    content: input.content.trim(),
    priority: input.priority,
    edited_by: 'operator-web',
  })
}

export async function updateRule(id: number, input: UpdateRuleInput): Promise<Rule> {
  return apiPatch<Rule>(`/rules/${id}`, {
    content: input.content.trim(),
    priority: input.priority,
    edited_by: 'operator-web',
  })
}

export async function deleteRule(id: number): Promise<void> {
  await apiDelete<Record<string, number>>(`/rules/${id}`)
}
