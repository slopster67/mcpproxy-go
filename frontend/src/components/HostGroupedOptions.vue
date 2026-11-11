<script setup lang="ts" generic="T extends { host_id?: string }">
import { computed, onMounted } from "vue";
import { usePeersStore } from "@/stores/peers";

// <option>s for a native select. With at least one peer they are grouped by the
// host they belong to (Primary first, then each peer); without peers they are a
// plain list.
const props = defineProps<{
  items: readonly T[];
  value: (item: T) => string;
  label: (item: T) => string;
}>();
const peers = usePeersStore();
onMounted(peers.ensureLoaded);

const groups = computed(() => {
  const byHost = new Map<string, T[]>();
  for (const item of props.items) {
    const host = item.host_id || "";
    byHost.set(host, [...(byHost.get(host) ?? []), item]);
  }
  const known = peers.items.map((peer) => peer.id);
  const order = ["", ...known, ...[...byHost.keys()].filter((host) => host && !known.includes(host))];
  return order
    .filter((host) => byHost.has(host))
    .map((host) => ({ host, label: peers.label(host), items: byHost.get(host)! }));
});
</script>
<template>
  <template v-if="peers.hasPeers">
    <optgroup v-for="group in groups" :key="group.host" :label="group.label">
      <option v-for="item in group.items" :key="value(item)" :value="value(item)">{{ label(item) }}</option>
    </optgroup>
  </template>
  <template v-else>
    <option v-for="item in items" :key="value(item)" :value="value(item)">{{ label(item) }}</option>
  </template>
</template>
