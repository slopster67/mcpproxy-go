<template>
  <Card
    size="sm"
    class="relative cursor-pointer"
    :data-client-id="client.id"
  >
    <CardHeader>
      <CardTitle class="flex min-w-0 items-center gap-2">
        <span
          class="size-2 shrink-0 rounded-full"
          :class="client.profile_missing ? 'bg-[var(--server-status-red)]' : connected ? 'bg-[var(--server-status-green)]' : 'bg-white'"
          role="img"
          :aria-label="client.profile_missing ? 'Profile missing' : connected ? 'Connected' : 'Not connected'"
          :title="client.profile_missing ? 'Profile missing' : connected ? 'Connected' : 'Not connected'"
        />
        <button
          class="block min-w-0 flex-1 cursor-pointer truncate text-left after:absolute after:inset-0 after:rounded-4xl focus-visible:outline-none focus-visible:after:ring-2 focus-visible:after:ring-ring"
          :title="client.display_name"
          @click="emit('edit', client)"
        >
          {{ client.display_name || client.id }}
        </button>
        <span
          v-if="client.profile && !client.profile_missing"
          class="ml-auto min-w-0 truncate text-xs font-normal text-muted-foreground"
          :title="profileTitle || client.profile"
        >{{ profileTitle || client.profile }}</span>
      </CardTitle>
      <CardDescription class="flex h-4 min-w-0 items-center justify-between gap-3 leading-4">
        <span
          class="shrink-0 text-[11px] text-muted-foreground"
          :title="
            client.last_seen
              ? new Date(client.last_seen).toLocaleString()
              : undefined
          "
        >{{ client.last_seen ? formatRelative(client.last_seen) : "never used" }}</span>
        <span v-if="client.profile_missing" class="ml-auto shrink-0 text-xs text-destructive">Profile missing</span>
      </CardDescription>
    </CardHeader>

  </Card>
</template>
<script setup lang="ts">
import { computed, onMounted } from "vue";
import {
  Card,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { formatRelative } from "@/utils/datetime";
import { usePeersStore } from "@/stores/peers";
import type { ClientPresence } from "@/types/api";
const props = defineProps<{ client: ClientPresence; profileTitle?: string }>();
const peers = usePeersStore();
onMounted(() => void peers.ensureLoaded());
const connected = computed(() => (props.client.active_sessions ?? 0) > 0 && peers.online(props.client.host_id));
const emit = defineEmits<{
  edit: [client: ClientPresence];
}>();
</script>
