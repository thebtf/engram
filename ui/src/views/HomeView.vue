<script setup lang="ts">
import { useStats } from '@/composables'
import StatsCards from '@/components/StatsCards.vue'

const { stats } = useStats()
</script>

<template>
  <div>
    <StatsCards :stats="stats" />
  </div>
</template>
