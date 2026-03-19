import { createRouter, createWebHashHistory } from 'vue-router'
import { useAuth } from '@/composables/useAuth'

const routes = [
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
    path: '/observations',
    name: 'observations',
    component: () => import('@/views/ObservationsView.vue'),
  },
  {
    path: '/observations/:id',
    name: 'observation-detail',
    component: () => import('@/views/ObservationDetailView.vue'),
  },
  {
    path: '/search',
    name: 'search',
    component: () => import('@/views/SearchView.vue'),
  },
  {
    path: '/vault',
    name: 'vault',
    component: () => import('@/views/VaultView.vue'),
  },
  {
    path: '/logs',
    name: 'logs',
    component: () => import('@/views/LogsView.vue'),
  },
  {
    path: '/graph',
    name: 'graph',
    component: () => import('@/views/GraphView.vue'),
  },
  {
    path: '/patterns',
    name: 'patterns',
    component: () => import('@/views/PatternsView.vue'),
  },
  {
    path: '/sessions',
    name: 'sessions',
    component: () => import('@/views/SessionsView.vue'),
  },
  {
    path: '/analytics',
    name: 'analytics',
    component: () => import('@/views/AnalyticsView.vue'),
  },
  {
    path: '/system',
    name: 'system',
    component: () => import('@/views/SystemView.vue'),
  },
  {
    path: '/tokens',
    name: 'tokens',
    component: () => import('@/views/TokensView.vue'),
  },
]

const router = createRouter({
  history: createWebHashHistory(),
  routes,
})

// Navigation guard: redirect to login when not authenticated
router.beforeEach(async (to) => {
  const { authenticated, loading, checkAuth } = useAuth()

  if (loading.value) {
    await checkAuth()
  }

  if (!to.meta.public && !authenticated.value) {
    return { name: 'login' }
  }

  if (to.name === 'login' && authenticated.value) {
    return { name: 'home' }
  }
})

export default router
