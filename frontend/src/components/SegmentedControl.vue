<script setup lang="ts" generic="T extends string">
import { computed } from 'vue'
import { RadioGroupItem, RadioGroupRoot } from 'reka-ui'

// A horizontal radio group whose selection slides between equal-width options.
const props = defineProps<{ options: ReadonlyArray<{ value: T; label: string }>; disabled?: boolean; ariaLabel?: string }>()
const model = defineModel<T>({ required: true })
const index = computed(() => Math.max(0, props.options.findIndex(option => option.value === model.value)))
</script>

<template>
  <RadioGroupRoot
    v-model="model"
    orientation="horizontal"
    :disabled="disabled"
    :aria-label="ariaLabel"
    class="relative inline-grid h-8 auto-cols-fr grid-flow-col rounded-full bg-muted p-1 text-sm data-disabled:opacity-50"
  >
    <span
      aria-hidden="true"
      class="absolute inset-y-1 left-1 rounded-full bg-background shadow-sm transition-transform duration-200 ease-out motion-reduce:transition-none"
      :style="{ width: `calc((100% - 0.5rem) / ${options.length})`, transform: `translateX(${index * 100}%)` }"
    />
    <RadioGroupItem
      v-for="option in options"
      :key="option.value"
      :value="option.value"
      class="relative z-10 min-w-14 rounded-full px-3 font-medium text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-3 focus-visible:ring-ring/50 data-[state=checked]:text-foreground"
    >
      {{ option.label }}
    </RadioGroupItem>
  </RadioGroupRoot>
</template>
