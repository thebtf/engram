<script setup lang="ts">
const { t } = useI18n()
</script>

<template>
  <SectionStub
    cls="mustbuild"
    :title="t('deferred.books.title')"
    evidence="POST /api/books/ingest"
    :lead="t('deferred.books.lead')"
    :next="t('deferred.books.next')"
  />
</template>
