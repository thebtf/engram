<script setup lang="ts">
import { safeNavigation } from '../composables/useOperatorProjectContext'

const { openSettingsModal } = useSettingsModal()
const router = useRouter()

onMounted(async () => {
  const route = router.currentRoute.value
  if (route.path === '/settings') {
    await router.replace({ path: '/', query: safeNavigation(route.query, route.fullPath) })
  }
  openSettingsModal('general')
})
</script>

<template>
  <div class="settings-redirect" aria-hidden="true" />
</template>

<style scoped>
.settings-redirect { display: none; }
</style>
