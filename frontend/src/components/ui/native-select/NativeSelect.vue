<script setup lang="ts">
import { ref, type HTMLAttributes } from 'vue'
import { ChevronDownIcon } from '@lucide/vue'
import { reactiveOmit } from '@vueuse/core'
import { cn } from 'cn'

defineOptions({
  inheritAttrs: false,
})

const props = defineProps<{
  modelValue?: string | number | null
  value?: string | number | null
  defaultValue?: string | number | null
  modelModifiers?: { number?: boolean; trim?: boolean }
  class?: HTMLAttributes['class']
  size?: 'sm' | 'default'
}>()

const selectEl = ref<HTMLSelectElement | null>(null)
defineExpose({ focus: () => selectEl.value?.focus(), scrollIntoView: (options?: ScrollIntoViewOptions) => selectEl.value?.scrollIntoView(options) })

const emit = defineEmits<{
  'update:modelValue': [value: string | number]
}>()

function update(event: Event) {
  let value: string | number = (event.target as HTMLInputElement).value;
  if (props.modelModifiers?.trim) value = value.trim();
  if (props.modelModifiers?.number) {
    const number = parseFloat(value);
    if (!Number.isNaN(number)) value = number;
  }
  emit("update:modelValue", value);
}
const delegatedProps = reactiveOmit(props, 'class', 'size', 'modelValue', 'value', 'defaultValue', 'modelModifiers')
</script>

<template>
  <div
    :class="cn('group/native-select relative w-fit has-[select:disabled]:opacity-50', props.class)"
    data-slot="native-select-wrapper"
    :data-size="props.size ?? 'default'"
  >
    <select
      v-bind="{ ...$attrs, ...delegatedProps }"
      ref="selectEl"
      :value="modelValue ?? value ?? defaultValue ?? ''"
      @change="update"
      data-slot="native-select"
      class="bg-input/50 border-transparent placeholder:text-muted-foreground selection:bg-primary selection:text-primary-foreground focus-visible:border-ring focus-visible:ring-ring/30 aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 aria-invalid:border-destructive dark:aria-invalid:border-destructive/50 h-9 w-full min-w-0 appearance-none rounded-3xl border py-1 pr-8 pl-3 text-sm transition-[color,box-shadow,background-color] select-none focus-visible:ring-3 aria-invalid:ring-3 data-[size=sm]:h-8 outline-none disabled:pointer-events-none disabled:cursor-not-allowed"
      :data-size="props.size ?? 'default'"
    >
      <slot />
    </select>
    <ChevronDownIcon class="text-muted-foreground top-1/2 right-2.5 size-4 -translate-y-1/2 pointer-events-none absolute select-none" aria-hidden="true" data-slot="native-select-icon" />
  </div>
</template>
