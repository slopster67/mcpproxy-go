<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { useServersStore } from "@/stores/servers";

const props = defineProps<{ name: string; src?: string }>();
const servers = useServersStore();
onMounted(() => { if (!props.src) void servers.ensureLoaded(); });
const url = computed(() => props.src ?? servers.byId.get(props.name)?.icon_url);
const failed = ref(false);
watch(url, () => { failed.value = false; });
</script>

<template>
  <img v-if="url && !failed" :src="url" alt="" aria-hidden="true" class="inline-block size-4 shrink-0 object-contain align-middle" @error="failed = true" />
</template>
