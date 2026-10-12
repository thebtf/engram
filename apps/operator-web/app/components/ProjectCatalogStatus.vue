<script setup lang="ts">
import { computed } from 'vue'
import { useOperatorI18n } from '~/composables/useOperatorI18n'
import { fetchRetainedContent } from '~/composables/useOperatorApi'
import type { UnresolvedProject } from '../../../../ui/src/utils/projectCatalog'
import ProjectCatalogReview from '../../../../ui/src/components/ProjectCatalogReview.vue'

defineProps<{ records: UnresolvedProject[]; pending: boolean; error: string | null; reference: string }>()
const { t } = useOperatorI18n()
const route = useRoute()
const router = useRouter()
const labels = computed(() => ({
  unresolvedTitle: t('projectCatalog.unresolvedTitle'), review: t('projectCatalog.review'),
  kind: t('projectCatalog.kind'), memory: t('projectCatalog.memory'), document: t('projectCatalog.document'), issue: t('projectCatalog.issue'),
  history: t('projectCatalog.history'), more: t('projectCatalog.more'), back: t('projectCatalog.back'),
  loading: t('projectCatalog.loading'), search: t('projectCatalog.search'), unknownProject: t('projectCatalog.unknownProject'),
}))
</script>

<template>
  <ProjectCatalogReview v-bind="$props" :labels="labels" :read="fetchRetainedContent" @select="router.replace({ query: { ...route.query, unresolved: $event } })" />
</template>
