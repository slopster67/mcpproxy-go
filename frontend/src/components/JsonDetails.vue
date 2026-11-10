<template>
  <details
    :open="open"
    @toggle="visible = ($event.target as HTMLDetailsElement).open"
  >
    <summary
      class="cursor-pointer text-xs font-medium text-muted-foreground hover:text-foreground"
    >
      {{ title }}
    </summary>
    <JsonCode v-if="visible" :data="data" class="mt-2" />
  </details>
</template>

<script setup lang="ts">
import { ref, watch } from "vue";
import JsonCode from "@/components/JsonCode.vue";

const props = defineProps<{ title: string; data: unknown; open?: boolean }>();
const visible = ref(props.open ?? false);
watch(
  () => props.open,
  (open) => {
    visible.value = open ?? false;
  },
);
</script>
