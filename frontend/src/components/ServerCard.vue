<template>
  <Card
    class="py-0 relative bg-card transition-colors hover:border-ring/60 server-card"
  >
    <CardContent class="flex flex-col gap-2 p-4">
      <!-- Connection state, server name and transport. -->
      <div class="flex h-5 items-center gap-2">
        <span v-if="server.icon_url" class="relative inline-flex size-4 shrink-0">
          <ServerIcon :name="server.name" :src="server.icon_url" />
          <span
            class="absolute -bottom-0.5 -right-0.5 size-1.5 rounded-full ring-2 ring-card"
            :class="cardStatus.color"
            :title="cardStatus.label"
            role="img"
            :aria-label="cardStatus.label"
          />
        </span>
        <span
          v-else
          class="size-2 shrink-0 rounded-full"
          :class="cardStatus.color"
          :title="cardStatus.label"
          role="img"
          :aria-label="cardStatus.label"
        />
        <CardTitle
          class="text-sm truncate flex-1 min-w-0"
          :title="server.name"
          ><router-link
            :to="serverDetailPath(server.name)"
            class="after:absolute after:inset-0 after:rounded-lg focus-visible:outline-none focus-visible:after:ring-2 focus-visible:after:ring-ring"
            >{{ displayName }}</router-link
          ></CardTitle
        >

        <span
          class="shrink-0 text-muted-foreground"
          :title="
            server.protocol === 'stdio'
              ? 'Local command (stdio)'
              : 'Remote server (' + server.protocol + ')'
          "
          role="img"
          :aria-label="
            server.protocol === 'stdio'
              ? 'Local command (stdio)'
              : 'Remote server (' + server.protocol + ')'
          "
        >
          <SquareTerminal
            v-if="server.protocol === 'stdio'"
            class="size-4"
            aria-hidden="true"
          />
          <Globe v-else class="size-4" aria-hidden="true" />
        </span>
      </div>

      <div
        class="mt-auto flex h-4 items-center justify-between gap-3 leading-4"
      >
        <span
          class="shrink-0 text-[11px] text-muted-foreground"
          :title="
            server.last_used_at
              ? 'Last used: ' + server.last_used_at
              : 'Never used'
          "
          >{{ lastUsedText }}</span
        >

        <span
          v-if="cardStatus.error"
          class="ml-auto min-w-0 truncate text-xs text-destructive"
          :title="cardStatus.label"
          >{{ cardStatus.error }}</span
        >
        <router-link
          v-else-if="approvalCount"
          :to="reviewPath(server.name)"
          class="relative z-10 ml-auto shrink-0 text-xs text-warning hover:underline underline-offset-4"
        >
          Review {{ approvalCount }}
          {{ approvalCount === 1 ? "tool" : "tools" }}
        </router-link>
      </div>
    </CardContent>
  </Card>
</template>

<script setup lang="ts">
import ServerIcon from "@/components/ServerIcon.vue";
import { serverCardStatus } from "@/utils/serverCardStatus";
import { Card, CardContent, CardTitle } from "@/components/ui/card";
import { Globe, SquareTerminal } from "@lucide/vue";
import { ref, computed, onMounted, onUnmounted } from "vue";
import type { Server } from "@/types";
import {
  serverDetailPath,
  serverDisplayName,
  reviewPath,
} from "@/utils/serverRoute";

interface Props {
  server: Server;
}

const props = defineProps<Props>();

// Title-preferring display label. The '/'-safe detail links call
// serverDetailPath() directly in the template.
const displayName = computed(() => serverDisplayName(props.server));

const cardStatus = computed(() => serverCardStatus(props.server));

const approvalCount = computed(
  () =>
    (props.server.quarantine?.pending_count ?? 0) +
    (props.server.quarantine?.changed_count ?? 0),
);

// `nowMs` is a ticking ref that `lastUsedText` depends on, so the relative
// "Xm ago" label keeps updating; refreshed on an interval and torn down with
// the component.
const nowMs = ref(Date.now());
let nowTimer: ReturnType<typeof setInterval> | undefined;
onMounted(() => {
  nowTimer = setInterval(() => {
    nowMs.value = Date.now();
  }, 30_000);
});
onUnmounted(() => {
  if (nowTimer !== undefined) clearInterval(nowTimer);
});

const lastUsedText = computed(() => {
  const iso = props.server.last_used_at;
  if (!iso) return "never used";
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return "never used";
  const diffMs = nowMs.value - then;
  const minutes = Math.floor(diffMs / 60000);
  if (minutes < 1) return "just now";
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  return `${days}d ago`;
});
</script>
