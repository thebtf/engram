export default defineNuxtRouteMiddleware(async (to) => {
  const { restore, state, checkSetupNeeded } = useOperatorAuth()
  const publicPaths = new Set(['/login', '/setup'])

  if (import.meta.client) {
    await restore()
  }

  const setupNeeded = import.meta.client ? await checkSetupNeeded() : false

  if (setupNeeded && to.path !== '/setup') {
    return navigateTo('/setup')
  }

  if (!setupNeeded && to.path === '/setup') {
    return navigateTo(state.value.phase === 'authenticated' ? '/' : '/login')
  }

  if (!publicPaths.has(to.path) && state.value.phase !== 'authenticated') {
    return navigateTo('/login')
  }

  if (to.path === '/login' && state.value.phase === 'authenticated') {
    return navigateTo('/')
  }
})
