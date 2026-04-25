import { createRouter, createWebHashHistory } from 'vue-router'
import { useAuth } from '@/composables/useAuth'

const routes = [
  {
    path: '/setup',
    name: 'setup',
    component: () => import('@/views/SetupView.vue'),
    meta: { public: true },
  },
  {
    path: '/register',
    name: 'register',
    component: () => import('@/views/RegisterView.vue'),
    meta: { public: true },
  },
  {
    path: '/login',
    name: 'login',
    component: () => import('@/views/LoginView.vue'),
    meta: { public: true },
  },
  {
    path: '/',
    name: 'home',
    component: () => import('@/views/HomeView.vue'),
  },
  {
    path: '/vault',
    name: 'vault',
    component: () => import('@/views/VaultView.vue'),
  },
  {
    path: '/issues',
    name: 'issues',
    component: () => import('@/views/IssuesView.vue'),
  },
  {
    path: '/issues/:id',
    name: 'issue-detail',
    component: () => import('@/views/IssueDetailView.vue'),
  },
  {
    path: '/tokens',
    name: 'tokens',
    component: () => import('@/views/TokensView.vue'),
  },
  {
    path: '/system',
    name: 'system',
    component: () => import('@/views/SystemView.vue'),
  },
  {
    path: '/admin',
    name: 'admin',
    component: () => import('@/views/AdminView.vue'),
    meta: { requiresAdmin: true },
  },
  {
    path: '/:pathMatch(.*)*',
    name: 'not-found',
    component: () => import('@/views/HomeView.vue'),
    meta: { public: false },
  },
]

const router = createRouter({
  history: createWebHashHistory(),
  routes,
})

// Navigation guard: check setup-needed, then enforce auth
router.beforeEach(async (to) => {
  const { authenticated, loading, checkAuth, checkSetupNeeded, isAdmin } = useAuth()

  if (loading.value) {
    await checkAuth()
  }

  // Always allow /setup itself to prevent redirect loops
  if (to.name === 'setup') {
    return
  }

  // Check if first-time setup is required
  const setupNeeded = await checkSetupNeeded()
  if (setupNeeded) {
    return { name: 'setup' }
  }

  // Redirect unauthenticated users to login (skip public routes)
  if (!to.meta.public && !authenticated.value) {
    return { name: 'login' }
  }

  // Redirect already-authenticated users away from login/register
  if ((to.name === 'login' || to.name === 'register') && authenticated.value) {
    return { name: 'home' }
  }

  // Admin-only routes require admin role
  if (to.meta.requiresAdmin && !isAdmin.value) {
    return { name: 'home' }
  }
})

export default router
