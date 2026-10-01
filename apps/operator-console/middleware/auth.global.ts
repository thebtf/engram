import { operatorFetchJson } from '../composables/useOperatorApi'

export default defineNuxtRouteMiddleware(async (to) => {
  if (import.meta.server) return

  let authenticated = false
  try {
    const status = await operatorFetchJson<{ authenticated: boolean; auth_disabled?: boolean }>('/api/auth/me', {}, 'auth-guard')
    authenticated = status.authenticated === true || status.auth_disabled === true
  } catch {
    // The sign-in page remains reachable even when the server is unavailable.
  }

  if (!authenticated && to.path !== '/login') return navigateTo('/login')
  if (authenticated && to.path === '/login') return navigateTo('/')
})
