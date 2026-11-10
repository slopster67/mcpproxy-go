<template>
  <pre
    class="min-w-0 max-w-full max-h-96 overflow-auto whitespace-pre-wrap break-words [overflow-wrap:anywhere] rounded-md bg-black p-3 font-mono text-xs leading-relaxed"
  ><code><span
    v-for="(token, index) in tokens"
    :key="index"
    :class="token.color"
  >{{ token.text }}</span></code></pre>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { jsonTokens } from "@/utils/jsonHighlight";

const props = defineProps<{ data: unknown }>();
const tokens = computed(() =>
  jsonTokens(JSON.stringify(props.data, null, 2) ?? ""),
);
</script>
