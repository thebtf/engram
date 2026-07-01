<script setup lang="ts">
const { t } = useI18n()
</script>

<template>
  <SectionStub
    cls="mustbuild"
    :title="t('deferred.documents.title')"
    evidence="GET /api/documents"
    :lead="t('deferred.documents.lead')"
    :next="t('deferred.documents.next')"
  />
</template>
