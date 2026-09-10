import type { Page } from '@playwright/test'

export interface TimedOperation<T> {
  completedAt: number
  startedAt: number
  value: T
}

export async function browserUserID(page: Page): Promise<number> {
  const response = await page.evaluate(async () => {
    const result = await fetch('/api/auth/me')
    return { body: await result.json(), status: result.status }
  })
  if (
    response.status !== 200
    || response.body === null || typeof response.body !== 'object' || Array.isArray(response.body)
    || !('user' in response.body) || response.body.user === null || typeof response.body.user !== 'object' || Array.isArray(response.body.user)
    || !('id' in response.body.user) || !Number.isSafeInteger(response.body.user.id) || response.body.user.id <= 0
  ) {
    throw new Error('authenticated browser fixture did not return a persisted user identity')
  }
  return response.body.user.id
}

export async function issueReadOnlyKeycard(page: Page, name: string, userID: number): Promise<string> {
  const response = await page.evaluate(async ({ keycardName, principal }) => {
    const result = await fetch('/api/auth/tokens', {
      body: JSON.stringify({ name: keycardName, principal, principal_kind: 'human', scope: 'read-only' }),
      headers: { 'Content-Type': 'application/json' },
      method: 'POST',
    })
    return { body: await result.json(), status: result.status }
  }, { keycardName: name, principal: `browser-user/${userID}` })
  if (
    response.status !== 200
    || response.body === null || typeof response.body !== 'object' || Array.isArray(response.body)
    || !('token' in response.body) || typeof response.body.token !== 'string' || response.body.token === ''
    || !('scope' in response.body) || response.body.scope !== 'read-only'
  ) {
    throw new Error('browser fixture did not issue a scoped read-only keycard')
  }
  return response.body.token
}

export async function observeOperation<T>(operation: () => Promise<T>): Promise<TimedOperation<T>> {
  const startedAt = Date.now()
  const value = await operation()
  return { completedAt: Date.now(), startedAt, value }
}

export function intervalsOverlap(left: TimedOperation<unknown>, right: TimedOperation<unknown>): boolean {
  return left.startedAt <= right.completedAt && right.startedAt <= left.completedAt
}
