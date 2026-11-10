<script setup lang="ts">
import { ref } from "vue";
import { Cable, ArrowUpRight } from "@lucide/vue";
import type { MCPSession } from "@/types/api";
import { useScopeQuery } from "@/composables/useScopeQuery";
import { useClientsStore } from "@/stores/clients";
import { formatDateTime, formatRelative } from "@/utils/datetime";
import BaseDialog from "@/components/BaseDialog.vue";
import JsonCode from "@/components/JsonCode.vue";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import {
  Empty,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
defineProps<{ sessions: MCPSession[] }>();
const clients = useClientsStore();
const scope = useScopeQuery("activity"),
  selected = ref<MCPSession | null>(null);
</script>
<template>
  <Card class="min-w-0 py-0"
    ><CardContent class="px-0">
      <Empty v-if="!sessions.length"
        ><EmptyHeader
          ><EmptyMedia variant="icon"><Cable /></EmptyMedia
          ><EmptyTitle>No sessions found</EmptyTitle></EmptyHeader
        ></Empty
      >
      <Table v-else
        ><TableHeader
          ><TableRow
            ><TableHead class="pl-4">Client</TableHead
            ><TableHead class="hidden md:table-cell">Project</TableHead
            ><TableHead>Status</TableHead
            ><TableHead class="text-right">Calls</TableHead
            ><TableHead class="hidden text-right sm:table-cell"
              >Last used</TableHead
            ><TableHead class="w-12"
              ><span class="sr-only">Activity</span></TableHead
            ></TableRow
          ></TableHeader
        ><TableBody
          ><TableRow v-for="session in sessions" :key="session.id"
            ><TableCell class="max-w-48 whitespace-normal pl-4"
              ><button
                class="max-w-full truncate font-medium hover:underline underline-offset-4"
                @click="selected = session"
              >
                {{
                  clients.labelFor(session.client_id, session.client_name || "Unknown client")
                }}
              </button>
              <div
                v-if="session.client_version"
                class="text-xs text-muted-foreground"
              >
                {{ session.client_version }}
              </div></TableCell
            ><TableCell class="hidden text-muted-foreground md:table-cell">{{
              session.workspace_name || "—"
            }}</TableCell
            ><TableCell
              ><Badge variant="secondary">{{
                session.status === "active" ? "Active" : "Closed"
              }}</Badge></TableCell
            ><TableCell class="text-right tabular-nums">{{
              session.tool_call_count.toLocaleString()
            }}</TableCell
            ><TableCell
              class="hidden text-right text-xs text-muted-foreground sm:table-cell"
              :title="formatDateTime(session.last_activity)"
              >{{ formatRelative(session.last_activity) }}</TableCell
            ><TableCell
              ><Button variant="ghost" size="icon-sm" as-child
                ><RouterLink
                  :to="
                    scope.linkTo('activity', {
                      session: session.work_session_id || session.id,
                      view: 'calls',
                    })
                  "
                  aria-label="View calls"
                  ><ArrowUpRight /></RouterLink></Button></TableCell></TableRow></TableBody
      ></Table> </CardContent
  ></Card>
  <BaseDialog
    :open="!!selected"
    :title="selected ? clients.labelFor(selected.client_id, selected.client_name || 'Session') : 'Session'"
    wide
    @close="selected = null"
    ><JsonCode v-if="selected" :data="selected"
  /></BaseDialog>
</template>
