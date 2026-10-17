<script setup lang="ts">
import { useRoute, useRouter } from 'vue-router'
import { useUiI18n } from '../composables/useUiI18n'
import { fetchRetainedContent } from '../utils/api'
import type { UnresolvedProject } from '../utils/projectCatalog'
import ProjectCatalogReview from './ProjectCatalogReview.vue'

defineProps<{ records: UnresolvedProject[]; pending: boolean; error: string | null; reference: string }>()
const { t } = useUiI18n()
const route = useRoute()
const router = useRouter()
</script>

<template>
  <ProjectCatalogReview v-bind="$props" :labels="t.projectCatalog" :read="fetchRetainedContent" @select="router.replace({ query: { ...route.query, unresolved: $event } })" />
</template>
