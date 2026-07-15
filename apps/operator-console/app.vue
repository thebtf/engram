<script setup lang="ts">
import { computed } from 'vue'

const { locale } = useI18n()
useHead(computed(() => ({ htmlAttrs: { lang: locale.value === 'zh' ? 'zh-Hans' : locale.value } })))
</script>

<template>
  <NuxtLayout>
    <NuxtPage />
  </NuxtLayout>
</template>
