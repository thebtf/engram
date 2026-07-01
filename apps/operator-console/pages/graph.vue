<script setup lang="ts">
const { t } = useI18n()
</script>

<template>
  <SectionStub
    cls="mustbuild"
    :title="t('deferred.graph.title')"
    evidence="GET /api/graph"
    :lead="t('deferred.graph.lead')"
    :next="t('deferred.graph.next')"
  />
</template>
