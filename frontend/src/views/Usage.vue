<template>
  <div class="flex min-w-0 flex-col gap-6">
    <div class="flex min-h-9 items-center justify-between gap-3">
      <h1 class="text-2xl font-medium tracking-tight">Usage</h1>
    </div>
    <div class="flex flex-wrap items-center gap-3">
      <NativeSelect
        :model-value="scopeQuery.state.profile ?? ''"
        class="w-auto"
        aria-label="Filter by profile"
        @update:model-value="
          scopeQuery.set({ profile: String($event) || undefined })
        "
        ><option value="">All profiles</option>
        <option value="-">No profile</option>
        <option
          v-for="profile in profilesStore.items"
          :key="profile.id"
          :value="profile.id"
        >
          {{ profile.name }}
        </option></NativeSelect
      >
      <NativeSelect
        :model-value="scopeQuery.state.client ?? ''"
        class="w-auto"
        aria-label="Filter by client"
        @update:model-value="
          scopeQuery.set({ client: String($event) || undefined })
        "
        ><option value="">All clients</option>
        <option
          v-for="client in clientsStore.items"
          :key="client.id"
          :value="client.id"
        >
          {{ client.display_name }}
        </option></NativeSelect
      >
      <NativeSelect
        v-model="status"
        class="w-auto"
        aria-label="Filter calls by status"
        @change="reload"
        ><option value="">All statuses</option>
        <option value="success">Success</option>
        <option value="error">Errors</option>
        <option value="blocked">Blocked</option>
      </NativeSelect>
      <Badge v-if="usageRangeNotApplied" variant="secondary"
        >Custom range · showing all time</Badge
      >
      <div class="flex flex-wrap gap-2">
        <Badge
          v-for="chip in scopeQuery.chips.value.filter((chip) =>
            ['server', 'tool'].includes(chip.name),
          )"
          :key="chip.name"
          variant="secondary"
          ><ServerIcon v-if="chip.name === 'server'" :name="chip.value" />{{ chip.value
          }}<button
            :aria-label="`Remove ${chip.name} filter`"
            @click="chip.remove"
          >
            <X class="size-3" /></button
        ></Badge>
      </div>
    </div>
    <LoadingSkeleton
      v-if="loading && !data"
      label="Loading usage"
      chart
    />
    <Alert v-else-if="error" variant="destructive"
      >{{ error
      }}<Button variant="outline" size="sm" @click="reload()"
        >Retry</Button
      ></Alert
    >
    <Card v-else-if="filterConflict"
      ><Empty
        ><EmptyHeader
          ><EmptyTitle>Filters don't match</EmptyTitle
          ><EmptyDescription
            >Choose a server and tool that belong together.</EmptyDescription
          ></EmptyHeader
        ><EmptyContent
          ><Button variant="outline" @click="clearScopeConflict"
            >Clear filters</Button
          ></EmptyContent
        ></Empty
      ></Card
    >
    <template v-else-if="data">
      <Card>
        <CardHeader
          ><CardTitle>Overview</CardTitle
          ><CardAction
            ><Tabs
              :model-value="window"
              @update:model-value="setWindow($event as UsageWindow)"
              ><TabsList aria-label="Usage period"
                ><TabsTrigger value="24h">24 hours</TabsTrigger
                ><TabsTrigger value="7d">7 days</TabsTrigger
                ><TabsTrigger value="all">All time</TabsTrigger></TabsList
              ></Tabs
            ></CardAction
          ></CardHeader
        >
        <CardContent class="flex flex-col gap-6">
          <dl class="grid grid-cols-3 gap-4">
            <div>
              <dt class="text-xs text-muted-foreground">Calls</dt>
              <dd class="mt-1 text-2xl font-medium tabular-nums">
                {{ formatNumber(headline.calls) }}
              </dd>
            </div>
            <div>
              <dt class="text-xs text-muted-foreground">Errors</dt>
              <dd class="mt-1 text-2xl font-medium tabular-nums">
                {{ formatNumber(headline.errors) }}
              </dd>
            </div>
            <div>
              <dt class="text-xs text-muted-foreground">Error rate</dt>
              <dd class="mt-1 text-2xl font-medium tabular-nums">
                {{ headline.errorRate }}%
              </dd>
            </div>
          </dl>
          <Empty v-if="isEmpty"
            ><EmptyHeader
              ><EmptyMedia variant="icon"><ChartNoAxesCombined /></EmptyMedia
              ><EmptyTitle>No calls in this period</EmptyTitle></EmptyHeader
            ><EmptyContent v-if="window !== 'all'"
              ><Button variant="outline" @click="resetFilters"
                >Show all time</Button
              ></EmptyContent
            ></Empty
          >
          <Timeline
            v-else
            :buckets="data.timeline"
            :window="window"
            @select-bucket="onSelectBucket"
          />
        </CardContent>
      </Card>
      <Card v-if="!isEmpty">
        <CardHeader
          ><CardTitle>Tools</CardTitle
          ><CardAction
            ><NativeSelect
              v-model="sort"
              class="w-auto"
              aria-label="Sort tools by"
              @change="reload"
              ><option value="calls">Most calls</option>
              <option value="resp_bytes">Largest responses</option>
              <option value="error_rate">Highest error rate</option>
              <option value="p95">Slowest calls</option></NativeSelect
            ></CardAction
          ></CardHeader
        >
        <CardContent class="min-w-0"
          ><Tabs v-model="chartTab" class="gap-6"
            ><TabsList aria-label="Tool usage charts"
              ><TabsTrigger value="calls">Calls</TabsTrigger
              ><TabsTrigger value="responses">Responses</TabsTrigger
              ><TabsTrigger value="errors"
                >Errors & latency</TabsTrigger
              ></TabsList
            ><TabsContent value="calls" class="max-h-[32rem] overflow-y-auto"
              ><CallHistogram
                :tools="data.tools"
                @select-tool="onSelectTool" /></TabsContent
            ><TabsContent
              value="responses"
              class="max-h-[32rem] overflow-y-auto"
              ><ResponseSizeRanking :tools="data.tools" /></TabsContent
            ><TabsContent value="errors" class="max-h-[32rem] overflow-y-auto"
              ><ErrorRateChart :tools="data.tools" /></TabsContent
          ></Tabs>
          <p v-if="data.other" class="mt-4 text-xs text-muted-foreground">
            {{ data.other.tools_folded }} more tools grouped into other.
          </p></CardContent
        >
      </Card>
    </template>
  </div>
</template>

<script setup lang="ts">
import ServerIcon from "@/components/ServerIcon.vue";
import { ChartNoAxesCombined, X } from "@lucide/vue";
import { CardAction, CardHeader, CardTitle } from "@/components/ui/card";
import { Empty, EmptyContent, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useClientsStore } from "@/stores/clients";

import LoadingSkeleton from "@/components/LoadingSkeleton.vue";
import { defineAsyncComponent } from "vue";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { NativeSelect } from "@/components/ui/native-select";
import { Alert } from "@/components/ui/alert";
import { Card, CardContent } from "@/components/ui/card";
import { ref, computed, onMounted, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import api from "@/services/api";
import { onLive } from "@/services/events";
import { useScopeQuery, splitScopeTool, usageWindowFor } from "@/composables/useScopeQuery";
import { useProfilesStore } from "@/stores/profiles";
import { pickScopeParams, scopeParamsKey } from "@/utils/scopeParams";
import type { UsageAggregateResponse, UsageWindow, UsageSort, UsageStatus, UsageToolStat } from "@/types";
import { formatNumber, usageHeadline } from "@/utils/usageFormat";
const CallHistogram = defineAsyncComponent(
  () => import("@/components/usage/CallHistogram.vue"),
);
const ResponseSizeRanking = defineAsyncComponent(
  () => import("@/components/usage/ResponseSizeRanking.vue"),
);
const ErrorRateChart = defineAsyncComponent(
  () => import("@/components/usage/ErrorRateChart.vue"),
);
const Timeline = defineAsyncComponent(
  () => import("@/components/usage/Timeline.vue"),
);

const chartTab = ref("calls");
const clientsStore = useClientsStore();
const window = ref<UsageWindow>("24h");
const status = ref<UsageStatus | "">("");
const sort = ref<UsageSort>("resp_bytes");

const data = ref<UsageAggregateResponse | null>(null);
const loading = ref(false);
const error = ref<string | null>(null);
const route = useRoute();
const router = useRouter();
const scopeQuery = useScopeQuery("usage");
const profilesStore = useProfilesStore();

const usageScopeParams = computed(() => pickScopeParams(scopeQuery.toRest()));
const scopeKey = computed(() => scopeParamsKey(usageScopeParams.value));

// `server`, `tool`, `from` and `to` only arrive through incoming links; the
// page has no picker for them, so they are read once on mount.
const filterServer = ref("");
const filterTool = ref("");
// The raw `from`/`to` values drive the "custom range" badge; the request
// always uses `usageWindowFor`.
const rawFrom = ref("");
const rawTo = ref("");
function applyScopeQueryParams(): void {
  const server = route.query.server;
  if (typeof server === "string") filterServer.value = server;
  const tool = route.query.tool;
  if (typeof tool === "string") filterTool.value = tool;
  const from = route.query.from;
  rawFrom.value = typeof from === "string" ? from : "";
  const to = route.query.to;
  rawTo.value = typeof to === "string" ? to : "";
  if (rawFrom.value || rawTo.value) {
    window.value = usageWindowFor(
      rawFrom.value || undefined,
      rawTo.value || undefined,
    ) as UsageWindow;
  }
}
applyScopeQueryParams();

/** A `from`/`to` that is not one of the presets is requested as `window=all`;
 * the badge tells the operator the exact range was not applied. */
const usageRangeNotApplied = computed(() => {
  if (!rawFrom.value && !rawTo.value) return false;
  if (rawTo.value) return true; // an explicit end time is never one of the presets
  return rawFrom.value !== "-24h" && rawFrom.value !== "-7d";
});

const headline = computed(() => usageHeadline(data.value));

const isEmpty = computed(() => {
  if (!data.value) return false;
  return data.value.tools.length === 0 && data.value.timeline.length === 0;
});

// Requests can overlap and are not cancelled; only the newest may write state.
let reloadSeq = 0;

/** `server` and `tool` name different servers, so no request can express
 * both: the page issues none and shows the conflict empty state. */
const filterConflict = computed(
  () =>
    splitScopeTool(
      filterTool.value || undefined,
      filterServer.value || undefined,
    ).conflict === true,
);

function clearScopeConflict(): void {
  filterServer.value = "";
  filterTool.value = "";
  const query = { ...route.query };
  delete query.server;
  delete query.tool;
  router.replace({ query });
  reload();
}

// `live` reloads (usage invalidations) keep the current error until they
// succeed or fail themselves.
async function reload(live = false) {
  if (filterConflict.value) {
    data.value = null;
    error.value = null;
    loading.value = false;
    return;
  }
  const seq = ++reloadSeq;
  loading.value = true;
  if (!live) error.value = null;
  try {
    const split = splitScopeTool(
      filterTool.value || undefined,
      filterServer.value || undefined,
    );
    const next = await api.usage.get({
      ...usageScopeParams.value,
      window: window.value,
      status: status.value || undefined,
      sort: sort.value,
      server: split.server,
      tool: split.tool,
    });
    if (seq !== reloadSeq) return;
    data.value = next;
    error.value = null;
  } catch (e) {
    if (seq !== reloadSeq) return;
    error.value = e instanceof Error ? e.message : "Failed to load usage data";
  } finally {
    // A superseded request must not clear the spinner the newest one owns.
    if (seq === reloadSeq) {
      loading.value = false;
    }
  }
}

function setWindow(w: UsageWindow) {
  window.value = w;
  reload();
}

function resetFilters() {
  window.value = "all";
  status.value = "";
  reload();
}

/** The active window as the `from`/`to` that Activity links carry. */
function windowToRange(w: UsageWindow): { from?: string; to?: string } {
  if (w === "24h") return { from: "-24h" };
  if (w === "7d") return { from: "-7d" };
  return {};
}

/** A CallHistogram bar links to that tool's calls over the active window. */
function onSelectTool(tool: UsageToolStat): void {
  const patch: Record<string, string> = {
    view: "calls",
    tool: `${tool.server}:${tool.tool}`,
  };
  const range = windowToRange(window.value);
  if (range.from) patch.from = range.from;
  if (status.value) patch.status = status.value;
  router.push(scopeQuery.linkTo("activity", patch));
}

/** A Timeline bar links to the calls of one time bucket across every tool. */
function onSelectBucket(range: { start: string; end: string }): void {
  const patch: Record<string, string> = {
    view: "calls",
    from: range.start,
    to: range.end,
  };
  if (status.value) patch.status = status.value;
  router.push(scopeQuery.linkTo("activity", patch));
}

// A changed profile/client scope refetches.
watch(scopeKey, () => void reload());

onMounted(() => {
  void clientsStore.ensureLoaded();
  void profilesStore.ensureLoaded();
  void reload();
});

// `usage` invalidations arrive at most every 2 s.
onLive(["usage"], () => void reload(true), { delay: 500 });
</script>
