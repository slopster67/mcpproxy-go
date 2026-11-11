<script setup lang="ts">
import ServerIcon from "@/components/ServerIcon.vue";
import HostGroupedOptions from "@/components/HostGroupedOptions.vue";
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { usePeersStore } from "@/stores/peers";
import { Activity, ChevronLeft, ChevronRight, ListFilter, Search, X } from "@lucide/vue";
import api from "@/services/api";
import { onLive } from "@/services/events";
import type { ActivityRecord } from "@/types/api";
import { useScopeQuery } from "@/composables/useScopeQuery";
import { useScopeLabels } from "@/composables/useScopeLabels";
import { useServersStore } from "@/stores/servers";
import { useProfilesStore } from "@/stores/profiles";
import { useClientsStore } from "@/stores/clients";
import { useSessionsStore } from "@/stores/sessions";
import { ACTIVITY_TYPE_LABELS, ACTIVITY_VIEW_LABELS, activityViewTypes, formatStatus, formatType } from "@/utils/activity";
import { formatDateTime, formatRelative } from "@/utils/datetime";
import BaseDialog from "@/components/BaseDialog.vue";
import JsonCode from "@/components/JsonCode.vue";
import LoadingSkeleton from "@/components/LoadingSkeleton.vue";
import SessionsPanel from "@/components/activity/SessionsPanel.vue";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Empty, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import { NativeSelect } from "@/components/ui/native-select";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";

const peers = usePeersStore();
const route = useRoute(),
  router = useRouter(),
  scope = useScopeQuery("activity");
const servers = useServersStore(),
  profiles = useProfilesStore(),
  clients = useClientsStore(),
  sessions = useSessionsStore();
const { valueLabel } = useScopeLabels();
// "(peer)" after a client or server chip, only when peers exist.
function chipHost(name: string, value: string) {
  if (!peers.hasPeers) return "";
  const entity =
    name === "client" ? clients.byId.get(value)
    : name === "server" ? servers.byId.get(value)
    : undefined;
  return entity ? ` (${peers.label(entity.host_id)})` : "";
}
function clientName(row: { client_id?: string; client_name?: string }) {
  return clients.labelFor(row.client_id, row.client_name || (row.client_id ? 'Deleted client' : '—'));
}
const view = computed({
  get: () => String(route.query.view || "calls"),
  set: (value) => scope.set({ view: value, type: undefined }),
});
const search = ref(""),
  records = ref<ActivityRecord[]>([]);
const total = ref(0),
  offset = ref(0),
  limit = 50,
  recordsLoading = ref(true),
  refreshing = ref(false),
  recordsError = ref("");
const sessionsView = computed(() => view.value === "sessions");
const loading = computed(() =>
  sessionsView.value
    ? !sessions.loaded && !sessions.error
    : recordsLoading.value,
);
const error = computed(() =>
  sessionsView.value ? (sessions.error?.message ?? "") : recordsError.value,
);
const selected = ref<ActivityRecord | null>(null),
  detailLoading = ref(false),
  detailError = ref(""),
  detailTab = ref("input");
let request = 0,
  detailRequest = 0;
const queryValue = (key: string) =>
  typeof route.query[key] === "string" ? String(route.query[key]) : undefined;
const conflict = computed(() => scope.toRest() === null);
const filterParams = computed(() => {
  const params = { ...scope.toRest() };
  const types = activityViewTypes(view.value);
  if (!params.type && types)
    params.type = [
      ...types,
      ...(view.value === "calls" && params.status === "blocked"
        ? ["policy_decision"]
        : []),
    ].join(",");
  if (queryValue("parent_id")) params.parent_id = queryValue("parent_id")!;
  if (queryValue("request_id")) params.request_id = queryValue("request_id")!;
  return params;
});
const visibleRecords = computed(() =>
  records.value.filter((row) => {
    return (
      !search.value ||
      [
        row.tool_name,
        row.server_name,
        clientName(row),
        row.error_message,
        formatType(row.type),
      ].some((value) =>
        value?.toLowerCase().includes(search.value.toLowerCase()),
      )
    );
  }),
);
// The sessions collection holds the latest sessions; the scope filters match
// the REST ones (`-` selects sessions with none).
const matchesScope = (value: string | undefined, filter: string | undefined) =>
  !filter || (filter === "-" ? !value : value === filter);
const visibleSessions = computed(() =>
  sessions.items.filter(
    (row) =>
      matchesScope(row.profile, scope.state.profile) &&
      matchesScope(row.client_id, scope.state.client) &&
      (!search.value ||
      [clientName(row), row.workspace_name, row.id].some((value) =>
        value?.toLowerCase().includes(search.value.toLowerCase()),
      )),
  ),
);
const extraChips = computed(() =>
  ["parent_id", "request_id"]
    .filter((key) => queryValue(key))
    .map((key) => ({ key, value: queryValue(key) })),
);
const visibleChips = computed(() =>
  scope.chips.value.filter((chip) => chip.name !== "view"),
);
const metadata = computed(() => {
  if (!selected.value) return {};
  const { arguments: input, response, ...rest } = selected.value;
  return rest;
});
function parseResponse(value: unknown) {
  if (typeof value !== "string") return value;
  try {
    return JSON.parse(value);
  } catch {
    return value;
  }
}
function setFilter(key: string, event: Event) {
  scope.set({ [key]: (event.target as HTMLSelectElement).value || undefined });
}
function clearExtra(key: string) {
  const query = { ...route.query };
  delete query[key];
  void router.replace({ query });
}
// The records page is refetched (not patched) when activity is appended:
// the filters and the offset decide which records belong on it.
async function load(silent = false) {
  if (sessionsView.value) {
    void sessions.ensureLoaded();
    return;
  }
  const id = ++request;
  if (conflict.value) {
    records.value = [];
    total.value = 0;
    recordsLoading.value = false;
    return;
  }
  if (!silent) refreshing.value = true;
  try {
    const page = await api.activity.list({
      ...filterParams.value,
      limit,
      offset: offset.value,
    });
    if (id !== request) return;
    records.value = page.activities;
    total.value = page.total;
    recordsError.value = "";
  } catch (reason) {
    if (id === request)
      recordsError.value =
        reason instanceof Error ? reason.message : "Could not load activity";
  } finally {
    if (id === request) {
      recordsLoading.value = false;
      refreshing.value = false;
    }
  }
}
async function openDetail(row: ActivityRecord) {
  const id = ++detailRequest;
  selected.value = row;
  detailTab.value = row.arguments ? "input" : "metadata";
  detailLoading.value = true;
  detailError.value = "";
  try {
    const result = await api.activity.get(row.id);
    if (id !== detailRequest) return;
    selected.value = result;
  } catch (reason) {
    if (id === detailRequest)
      detailError.value =
        reason instanceof Error ? reason.message : "Could not load details";
  } finally {
    if (id === detailRequest) detailLoading.value = false;
  }
}
function closeDetail() {
  detailRequest++;
  selected.value = null;
  detailLoading.value = false;
}
function page(delta: number) {
  offset.value = Math.max(0, offset.value + delta * limit);
  void load();
}
onLive(["activity"], () => {
  if (!sessionsView.value) void load(true);
}, { delay: 400 });
watch(
  () => JSON.stringify([filterParams.value, view.value]),
  () => {
    offset.value = 0;
    void load();
  },
);
onMounted(() => {
  void peers.ensureLoaded();
  void load();
  void servers.ensureLoaded();
  void profiles.ensureLoaded();
  void clients.ensureLoaded();
});
onUnmounted(() => {
  request++;
  detailRequest++;
});
</script>

<template>
  <div class="flex min-w-0 flex-col gap-6">
    <h1 class="text-2xl font-medium tracking-tight">Activity</h1>
    <Tabs v-model="view"
      ><TabsList aria-label="Activity views"
        ><TabsTrigger
          v-for="(label, key) in ACTIVITY_VIEW_LABELS"
          :key="key"
          :value="key"
          >{{ label }}</TabsTrigger
        ></TabsList
      ></Tabs
    >
    <div class="flex flex-wrap items-center gap-2">
      <InputGroup class="w-full sm:max-w-xs"
        ><InputGroupAddon><Search /></InputGroupAddon
        ><InputGroupInput
          v-model="search"
          placeholder="Search this page"
          aria-label="Search activity"
      /></InputGroup>
      <NativeSelect
        v-if="view !== 'sessions'"
        :model-value="scope.state.server || ''"
        aria-label="Server"
        @change="setFilter('server', $event)"
        ><option value="">All servers</option>
        <HostGroupedOptions :items="servers.items" :value="(server) => server.name" :label="(server) => server.name" /></NativeSelect
      >
      <NativeSelect
        v-if="view !== 'sessions'"
        :model-value="scope.state.status || ''"
        aria-label="Result"
        @change="setFilter('status', $event)"
        ><option value="">Any result</option>
        <option
          v-for="status in ['success', 'error', 'blocked']"
          :key="status"
          :value="status"
        >
          {{ formatStatus(status) }}
        </option></NativeSelect
      >
      <Popover
        ><PopoverTrigger as-child
          ><Button variant="outline"
            ><ListFilter data-icon="inline-start" />Filters</Button
          ></PopoverTrigger
        ><PopoverContent align="start" class="w-80"
          ><FieldGroup>
            <Field
              ><FieldLabel for="activity-profile">Profile</FieldLabel
              ><NativeSelect
                id="activity-profile"
                :model-value="scope.state.profile || ''"
                @change="setFilter('profile', $event)"
                ><option value="">All profiles</option>
                <option value="-">All servers</option>
                <option
                  v-for="profile in profiles.items"
                  :key="profile.id"
                  :value="profile.id"
                >
                  {{ profile.name }}
                </option></NativeSelect
              ></Field
            >
            <Field
              ><FieldLabel for="activity-client">Client</FieldLabel
              ><NativeSelect
                id="activity-client"
                :model-value="scope.state.client || ''"
                @change="setFilter('client', $event)"
                ><option value="">All clients</option>
                <HostGroupedOptions :items="clients.items" :value="(client) => client.id" :label="(client) => client.display_name" /></NativeSelect
              ></Field
            >
            <template v-if="view !== 'sessions'"
              ><Field
                ><FieldLabel for="activity-type">Event</FieldLabel
                ><NativeSelect
                  id="activity-type"
                  :model-value="scope.state.type || ''"
                  @change="setFilter('type', $event)"
                  ><option value="">Any event</option>
                  <option
                    v-for="(label, type) in ACTIVITY_TYPE_LABELS"
                    :key="type"
                    :value="type"
                  >
                    {{ label }}
                  </option></NativeSelect
                ></Field
              ><Field
                ><FieldLabel for="activity-from">From</FieldLabel
                ><Input
                  id="activity-from"
                  :model-value="scope.state.from || ''"
                  placeholder="-24h or ISO date"
                  @change="setFilter('from', $event)" /></Field
              ><Field
                ><FieldLabel for="activity-to">Until</FieldLabel
                ><Input
                  id="activity-to"
                  :model-value="scope.state.to || ''"
                  placeholder="ISO date"
                  @change="setFilter('to', $event)" /></Field
            ></template>
          </FieldGroup></PopoverContent
        ></Popover
      >
    </div>
    <div
      v-if="visibleChips.length || extraChips.length"
      class="flex flex-wrap gap-2"
    >
      <Badge v-for="chip in visibleChips" :key="chip.name" variant="secondary"
        ><ServerIcon v-if="chip.name === 'server'" :name="chip.value" />{{ chip.label }}: {{ chip.name === 'client' || chip.name === 'profile' ? valueLabel(chip.name, chip.value) : chip.value
        }}{{ chipHost(chip.name, chip.value) }}<button :aria-label="`Clear ${chip.label}`" @click="chip.remove">
          <X class="size-3" /></button></Badge
      ><Badge v-for="chip in extraChips" :key="chip.key" variant="secondary"
        >{{ chip.key === "parent_id" ? "Sub-calls" : "Request"
        }}<button
          :aria-label="`Clear ${chip.key}`"
          @click="clearExtra(chip.key)"
        >
          <X class="size-3" /></button
      ></Badge>
    </div>
    <Alert v-if="error" variant="destructive" class="flex items-center justify-between gap-3"
      ><span>{{ error }}</span
      ><Button variant="outline" size="sm" @click="sessionsView ? sessions.refetch() : load()"
        >Try again</Button
      ></Alert
    >
    <LoadingSkeleton v-if="loading" label="Loading activity" :rows="6" />
    <Empty v-else-if="conflict"
      ><EmptyHeader
        ><EmptyTitle>These filters conflict</EmptyTitle></EmptyHeader
      ></Empty
    >
    <SessionsPanel
      v-else-if="view === 'sessions'"
      :sessions="visibleSessions"
    />
    <Card v-else class="min-w-0 py-0"
      ><CardContent class="px-0">
        <Empty v-if="!visibleRecords.length"
          ><EmptyHeader
            ><EmptyMedia variant="icon"><Activity /></EmptyMedia
            ><EmptyTitle>No activity found</EmptyTitle></EmptyHeader
          ></Empty
        >
        <Table v-else
          ><TableHeader
            ><TableRow
              ><TableHead class="pl-4">Event</TableHead
              ><TableHead class="hidden lg:table-cell">Client</TableHead
              ><TableHead>Result</TableHead
              ><TableHead class="hidden md:table-cell">Duration</TableHead
              ><TableHead class="pr-4 text-right">Time</TableHead></TableRow
            ></TableHeader
          ><TableBody
            ><TableRow
              v-for="row in visibleRecords"
              :key="row.id"
              class="cursor-pointer"
              @click="openDetail(row)"
              ><TableCell class="max-w-40 whitespace-normal pl-4"
                ><button
                  class="block max-w-full truncate text-left font-medium hover:underline underline-offset-4"
                  @click.stop="openDetail(row)"
                >
                  {{ row.tool_name || formatType(row.type) }}</button
                ><span
                  v-if="row.server_name"
                  class="flex items-center gap-1.5 text-xs text-muted-foreground"
                  ><ServerIcon :name="row.server_name" />{{ row.server_name }}</span
                ></TableCell
              ><TableCell class="hidden lg:table-cell">{{
                clientName(row)
              }}<span v-if="row.client_host_id" class="block text-xs text-muted-foreground">{{peers.label(row.client_host_id)}}</span></TableCell
              ><TableCell
                ><Badge
                  :variant="
                    row.status === 'error' ? 'destructive' : 'secondary'
                  "
                  >{{ formatStatus(row.status) }}</Badge
                ></TableCell
              ><TableCell class="hidden text-muted-foreground md:table-cell">{{
                row.duration_ms == null
                  ? "—"
                  : `${row.duration_ms.toLocaleString()} ms`
              }}</TableCell
              ><TableCell
                class="whitespace-nowrap pr-4 text-right text-xs text-muted-foreground"
                :title="formatDateTime(row.timestamp)"
                >{{ formatRelative(row.timestamp) }}</TableCell
              ></TableRow
            ></TableBody
          ></Table
        >
      </CardContent></Card
    >
    <div
      v-if="view !== 'sessions' && total > limit"
      class="flex items-center justify-between gap-3 text-sm text-muted-foreground"
    >
      <span
        >{{ offset + 1 }}–{{ Math.min(offset + limit, total) }} of
        {{ total.toLocaleString() }}</span
      >
      <div class="flex gap-2">
        <Button
          variant="outline"
          size="icon"
          aria-label="Previous page"
          :disabled="offset === 0 || refreshing"
          @click="page(-1)"
          ><ChevronLeft /></Button
        ><Button
          variant="outline"
          size="icon"
          aria-label="Next page"
          :disabled="offset + limit >= total || refreshing"
          @click="page(1)"
          ><ChevronRight
        /></Button>
      </div>
    </div>
    <BaseDialog
      :open="!!selected"
      :title="selected?.tool_name || formatType(selected?.type || '')"
      wide
      @close="closeDetail"
      ><template v-if="selected"
        ><div class="flex flex-wrap gap-3 text-sm text-muted-foreground">
          <Badge
            :variant="selected.status === 'error' ? 'destructive' : 'secondary'"
            >{{ formatStatus(selected.status) }}</Badge
          ><span>{{ selected.server_name }}</span
          ><span>{{ formatDateTime(selected.timestamp) }}</span
          ><span v-if="selected.duration_ms != null"
            >{{ selected.duration_ms }} ms</span
          >
        </div>
        <Alert
          v-if="selected.error_message || detailError"
          variant="destructive"
          >{{ detailError || selected.error_message }}</Alert
        ><LoadingSkeleton
          v-if="detailLoading"
          label="Loading event details"
          :rows="2"
        /><Tabs v-else v-model="detailTab" class="min-w-0"
          ><TabsList
            ><TabsTrigger value="input">Input</TabsTrigger
            ><TabsTrigger value="output">Output</TabsTrigger
            ><TabsTrigger value="metadata">Details</TabsTrigger></TabsList
          ><TabsContent value="input"
            ><JsonCode :data="selected.arguments ?? null" /></TabsContent
          ><TabsContent value="output"
            ><JsonCode
              :data="parseResponse(selected.response) ?? null" /></TabsContent
          ><TabsContent value="metadata"
            ><JsonCode :data="metadata" /></TabsContent
        ></Tabs>
        <div class="flex flex-wrap gap-2">
          <Button
            v-if="
              selected.request_id && selected.tool_name === 'code_execution'
            "
            variant="outline"
            @click="
              router.push({
                query: { view: 'calls', parent_id: selected.request_id },
              });
              closeDetail();
            "
            >View sub-calls</Button
          ><Button
            v-if="selected.parent_id"
            variant="outline"
            @click="
              router.push({
                query: { view: 'all', request_id: selected.parent_id },
              });
              closeDetail();
            "
            >View parent</Button
          >
        </div></template
      ></BaseDialog
    >
  </div>
</template>
