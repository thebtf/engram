<script setup lang="ts">
const { openSettingsModal } = useSettingsModal()
const router = useRouter()

onMounted(() => {
  openSettingsModal('general')
  if (router.currentRoute.value.path === '/settings') {
    void router.replace('/')
  }
})
</script>

<template>
  <div class="settings-redirect" aria-hidden="true" />
</template>

<style scoped>
.settings-redirect { display: none; }
</style>
