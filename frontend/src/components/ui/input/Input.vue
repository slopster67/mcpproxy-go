<script setup lang="ts">
import type { HTMLAttributes } from "vue";
import { ref } from "vue";
import { cn } from "@/lib/utils";
const props = defineProps<{
  modelValue?: string | number | null;
  value?: string | number | null;
  defaultValue?: string | number | null;
  modelModifiers?: { number?: boolean; trim?: boolean };
  type?: string;
  class?: HTMLAttributes["class"];
}>();
const inputEl = ref<HTMLInputElement | null>(null);
defineExpose({ focus: () => inputEl.value?.focus(), blur: () => inputEl.value?.blur(), select: () => inputEl.value?.select() });
const emit = defineEmits<{ "update:modelValue": [value: string | number] }>();
function update(event: Event) {
  let value: string | number = (event.target as HTMLInputElement).value;
  if (props.modelModifiers?.trim) value = value.trim();
  if (props.type === "number" || props.modelModifiers?.number) {
    const number = parseFloat(value);
    if (!Number.isNaN(number)) value = number;
  }
  emit("update:modelValue", value);
}
</script>

<template>
  <input
    ref="inputEl"
    :type="type"
    :value="modelValue ?? value ?? defaultValue ?? ''"
    @input="update"
    data-slot="input"
    :class="cn(
      'bg-input/50 border-transparent focus-visible:border-ring focus-visible:ring-ring/30 aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 aria-invalid:border-destructive dark:aria-invalid:border-destructive/50 h-9 rounded-3xl border px-3 py-1 text-base transition-[color,box-shadow,background-color] file:h-7 file:text-sm file:font-medium focus-visible:ring-3 aria-invalid:ring-3 md:text-sm w-full min-w-0 outline-none file:inline-flex file:border-0 file:bg-transparent file:text-foreground placeholder:text-muted-foreground disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-50',
      props.class,
    )"
  >
</template>
