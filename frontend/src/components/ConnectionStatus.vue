<template>
  <!-- Audit F28: while the Authentication Required modal is up, the SSE stream
       is down *because* there is no key. Repeating that as a second, vaguer
       message ("Connection Lost — Reconnecting…") only competes with the one
       screen that can actually fix it. -->
  <Alert
    v-if="systemStore.connectionLost && !systemStore.authRequired"
    class="fixed bottom-4 left-4 border-warning/25 bg-warning/5 text-warning shadow-lg max-w-sm z-40"
  >
    <svg class="w-6 h-6" fill="none" stroke="currentColor" viewBox="0 0 24 24">
      <path
        stroke-linecap="round"
        stroke-linejoin="round"
        stroke-width="2"
        d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-2.5L13.732 4c-.77-.833-1.732-.833-2.5 0L3.732 16.5c-.77.833.192 2.5 1.732 2.5z"
      />
    </svg>
    <div>
      <h3 class="font-bold">Connection Lost</h3>
      <div class="text-xs">Reconnecting to server...</div>
    </div>
  </Alert>
</template>

<script setup lang="ts">
import { Alert } from "@/components/ui/alert";
import { useSystemStore } from "@/stores/system";

const systemStore = useSystemStore();
</script>
