<template>
  <p class="text-sm">
    Delete <strong>{{ name }}</strong> from your {{ location }}? This cannot be
    undone.
  </p>
</template>
<script setup lang="ts">
withDefaults(defineProps<{ name: string; location?: string }>(), {
  location: "configuration",
});
</script>
