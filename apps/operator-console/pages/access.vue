<script setup lang="ts">
const { t } = useI18n()
</script>

<template>
  <SectionStub
    cls="mustbuild"
    :title="t('deferred.access.title')"
    evidence="GET /api/access/users"
    :lead="t('deferred.access.lead')"
    :next="t('deferred.access.next')"
  />
</template>
