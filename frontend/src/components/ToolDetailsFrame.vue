<template>
  <div ref="element" @focus="reveal">
    <slot v-if="ready" />
    <div v-else class="h-48" aria-hidden="true" />
  </div>
</template>
<script setup lang="ts">
import { onMounted, onUnmounted, ref } from "vue";
import { whenToolDetailsNearViewport } from "@/utils/toolDetailsViewport";
const element = ref<HTMLElement | null>(null),
  ready = ref(false);
let stop = () => {};
function reveal() {
  stop();
  ready.value = true;
}
onMounted(() => {
  if (element.value) stop = whenToolDetailsNearViewport(element.value, reveal);
});
onUnmounted(() => stop());
</script>
