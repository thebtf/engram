<script setup lang="ts">
const { t } = useI18n()
</script>

<template>
  <SectionStub
    cls="dormant"
    :title="t('deferred.queue.title')"
    evidence="VNEXT_F"
    :lead="t('deferred.queue.lead')"
    :next="t('deferred.queue.next')"
  />
</template>
