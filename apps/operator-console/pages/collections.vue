<script setup lang="ts">
const { t } = useI18n()
</script>

<template>
  <SectionStub
    cls="stale"
    :title="t('deferred.collections.title')"
    evidence="search_collection"
    :lead="t('deferred.collections.lead')"
    :next="t('deferred.collections.next')"
  />
</template>
