<script setup lang="ts">
import { Skeleton } from '@/components/ui/skeleton'
import { useDelayedLoading } from '@/composables/useDelayedLoading'
withDefaults(defineProps<{ label?: string; rows?: number; chart?: boolean }>(), {
  label: 'Loading', rows: 3, chart: false,
})
const visible = useDelayedLoading(() => true)
</script>

<template>
  <div v-show="visible" role="status" :aria-label="label" class="flex flex-col gap-3">
    <span class="sr-only">{{ label }}</span>
    <div v-if="chart" class="grid gap-4 sm:grid-cols-2" aria-hidden="true">
      <Skeleton v-for="panel in 2" :key="panel" class="h-64 rounded-lg" />
    </div>
    <div v-else class="overflow-hidden rounded-lg border border-border bg-card" aria-hidden="true">
      <div v-for="row in rows" :key="row" class="flex items-center justify-between gap-4 border-b border-border p-4 last:border-b-0">
        <div class="flex-1 flex flex-col gap-2">
          <Skeleton class="h-4 w-36 max-w-full" />
          <Skeleton class="h-3 w-64 max-w-full" />
        </div>
        <Skeleton class="h-6 w-16 rounded-md" />
      </div>
    </div>
  </div>
</template>
