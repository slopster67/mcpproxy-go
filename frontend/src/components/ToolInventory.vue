<template>
  <div class="flex flex-col gap-5">
    <h1 v-if="!embedded" class="text-2xl font-medium tracking-tight">Tools</h1>
    <div class="flex flex-wrap items-center gap-3">
      <Input
        v-model="search"
        type="search"
        aria-label="Search tools"
        placeholder="Search tools…"
        class="w-full sm:w-64"
      />
      <NativeSelect
        v-if="!serverName"
        v-model="serverFilter"
        aria-label="Server"
        class="w-48 max-w-full"
      >
        <option value="">All servers</option>
        <option v-for="name in serverNames" :key="name" :value="name">
          {{ name }}
        </option>
      </NativeSelect>
      <NativeSelect
        v-model="availability"
        aria-label="Availability"
        class="w-36 max-w-full"
      >
        <option value="">All states</option>
        <option value="enabled">Enabled</option>
        <option value="disabled">Disabled</option>
      </NativeSelect>
      <NativeSelect v-model="tier" aria-label="Tier" class="w-36 max-w-full">
        <option value="">All tiers</option>
        <option
          v-for="name in ['read', 'write', 'destructive', 'unannotated']"
          :key="name"
          :value="name"
        >
          {{ tierLabel(name) }}
        </option>
      </NativeSelect>
      <div class="flex flex-wrap items-center gap-2 text-sm">
        <NativeSelect
          :value="viewAsValue"
          aria-label="View as"
          class="w-44 max-w-full"
          @change="pickViewAs(($event.target as HTMLSelectElement).value)"
        >
          <option value="">View as</option>
          <optgroup label="Profiles">
            <option
              v-for="profile in profiles.items"
              :key="profile.id"
              :value="`profile:${profile.id}`"
            >
              {{ profile.name }}
            </option>
          </optgroup>
          <optgroup label="Clients">
            <option
              v-for="client in clients.items"
              :key="client.id"
              :value="`client:${client.id}`"
            >
              {{ client.display_name }}
            </option>
          </optgroup>
          <option v-if="viewAsOrphan" :value="viewAsValue">
            {{ subjectName }}
          </option>
        </NativeSelect>
        <Badge v-if="readOnly" variant="secondary">{{ requestScope.client || requestScope.profile ? 'Read-only' : 'Preview unavailable' }}</Badge>
      </div>
      <label
        v-if="reviewCount > 0"
        class="flex cursor-pointer items-center gap-2 whitespace-nowrap text-sm"
        ><Checkbox v-model="reviewOnly" />Needs
        review<span class="text-xs tabular-nums text-muted-foreground">{{
          reviewCount
        }}</span></label
      >
      <div class="ml-auto flex items-center gap-3">
        <div v-if="embedded" ref="bulkActions" class="contents" />
        <Button
          variant="outline"
          size="default"
          :disabled="loading"
          @click="loadTools()"
          ><RefreshCw
            data-icon="inline-start"
            :class="loading && 'animate-spin'"
          />Refresh</Button
        >
      </div>
    </div>
    <Alert v-if="scopeConflict" class="text-warning"
      >Choose a client or a profile, rather than both.<Button
        size="sm"
        variant="outline"
        @click="clearSubject"
        >Clear view-as</Button
      ></Alert
    >
    <Alert
      v-if="error"
      class="border-destructive/25 text-destructive"
      role="alert"
      >{{ error
      }}<Button
        variant="outline"
        size="sm"
        :disabled="loading"
        @click="loadTools()"
        >Retry</Button
      ></Alert
    >
    <Alert v-if="failedServers.length" class="text-warning text-sm"
      >Some servers could not be read: {{ failedServers.join(", ") }}. Stored
      pending reviews remain available.</Alert
    >
    <div
      v-if="!loaded && !error && !scopeConflict"
      class="flex flex-col gap-5"
      role="status"
      aria-label="Loading tools"
      v-show="showSkeleton"
    >
      <span class="sr-only">Loading tools…</span>
      <Card
        v-for="section in embedded ? 1 : 2"
        :key="section"
        class="gap-0 py-0"
        aria-hidden="true"
      >
        <div
          v-if="!embedded"
          class="flex items-center gap-2 border-b border-border px-4 py-3"
        >
          <Skeleton class="h-4 w-32" />
          <Skeleton class="h-3 w-12" />
        </div>
        <div
          v-for="row in 3"
          :key="row"
          class="flex items-start justify-between gap-3 border-b border-border px-4 py-3 last:border-b-0"
        >
          <div class="min-w-0 flex-1 pl-5 flex flex-col gap-2">
            <Skeleton class="h-3 w-1/3" />
            <Skeleton class="h-3 w-3/4" />
          </div>
          <Skeleton class="h-4 w-8 rounded-full" />
        </div>
      </Card>
    </div>
    <div
      v-else-if="!groups.length && !scopeConflict && !error"
      class="rounded-lg border border-border py-12 text-center"
    >
      <p class="text-sm text-muted-foreground">
        {{
          reviewOnly
            ? "Nothing needs review with these filters."
            : "No tools match these filters."
        }}
      </p>
      <Button
        v-if="hasFilters"
        class="mt-3"
        variant="outline"
        size="sm"
        @click="clearFilters"
        >Clear filters</Button
      >
    </div>
    <ToolSection
      v-for="group in groups"
      :key="
        JSON.stringify([group.server, route.query.client, route.query.profile])
      "
      :server="group.server"
      :embedded="embedded"
      :bulk-actions-target="bulkActions"
      :tools="group.tools"
      :total-reviews="reviewTotals.get(group.server) ?? 0"
      :review-only="reviewOnly"
      :focus="focusTool"
      :activity-scope="requestScope"
      :read-only="readOnly || !!error"
      :refreshing="loading"
      @changed="afterDecision"
      @mutation="trackMutation"
      @refresh="refreshSoon"
      @explain="explainTool"
    />
    <AccessExplainer
      :open="explain !== null"
      :subject="explainSubject"
      :tool="explain ?? undefined"
      @close="explain = null"
    />
  </div>
</template>
<script setup lang="ts">
import { Card } from "@/components/ui/card";
import { useDelayedLoading } from "@/composables/useDelayedLoading";
import { computed, ref, onMounted, onUnmounted, watch, nextTick } from "vue";
import { useRoute, useRouter } from "vue-router";
import { RefreshCw } from "@lucide/vue";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { Checkbox } from "@/components/ui/checkbox";
import { Badge } from "@/components/ui/badge";
import { Alert } from "@/components/ui/alert";
import { Skeleton } from "@/components/ui/skeleton";
import ToolSection from "@/components/ToolSection.vue";
import { warmToolPresentation } from "@/utils/warmToolPresentation";
import AccessExplainer from "@/components/AccessExplainer.vue";
import { useScopeQuery } from "@/composables/useScopeQuery";
import { useServersStore } from "@/stores/servers";
import { useProfilesStore } from "@/stores/profiles";
import { useClientsStore } from "@/stores/clients";
import { needsToolReview, type InventoryTool, type ToolChange } from "@/utils/toolInventory";
import api from "@/services/api";
import { onLive } from "@/services/events";
import { tierLabel } from "@/utils/toolQuarantine";

const props = defineProps<{ serverName?: string; embedded?: boolean }>();
const emit = defineEmits<{ reviewCount: [count: number] }>();
const bulkActions = ref<HTMLElement | null>(null);

const route = useRoute(),
  router = useRouter(),
  scope = useScopeQuery("tools");
const servers = useServersStore(),
  profiles = useProfilesStore(),
  clients = useClientsStore();
const inventory = ref<InventoryTool[]>([]),
  loading = ref(false),
  loaded = ref(false),
  error = ref(""),
  failedServers = ref<string[]>([]);
const str = (value: unknown) => (typeof value === "string" ? value : "");
function setQuery(values: Record<string, string | undefined>) {
  const query = { ...route.query, ...values };
  for (const key of Object.keys(query))
    if (query[key] === undefined || query[key] === "") delete query[key];
  void router.replace({ query, hash: "" });
}
function queryField(name: string) {
  return computed({
    get: () => str(route.query[name]),
    set: (value: string) => setQuery({ [name]: value }),
  });
}
const search = queryField("q"),
  serverFilter = computed({
    get: () => props.serverName || str(route.query.server),
    set: (value: string) => {
      if (!props.serverName) setQuery({ server: value });
    },
  }),
  availability = queryField("status"),
  tier = computed({
    get: () => str(route.query.tier),
    set: (value: string) => setQuery({ tier: value }),
  });
const reviewOnly = computed({
  get: () => route.query.review === "1",
  set: (value: boolean | unknown[]) =>
    setQuery({ review: value === true ? "1" : undefined }),
});
const focusTool = computed(
  () => str(route.query.focus) || (props.embedded ? str(route.query.tool) : ""),
);
const readOnly = computed(
  () => !!(str(route.query.profile) || str(route.query.client)),
);
const requestScope = computed(() => {
  const rest = scope.toRest() ?? {};
  return {
    client: rest.client && rest.client !== "-" ? rest.client : undefined,
    profile: rest.profile && rest.profile !== "-" ? rest.profile : undefined,
  };
});
const scopeConflict = computed(
  () => !!(requestScope.value.client && requestScope.value.profile),
);
const showSkeleton = useDelayedLoading(
  () => !loaded.value && !error.value && !scopeConflict.value,
);
const subjectName = computed(
  () => str(route.query.client) ? clients.labelFor(str(route.query.client)) : profiles.labelFor(str(route.query.profile)),
);
const viewAsOrphan = computed(() =>
  !!(str(route.query.client) && !clients.byId.has(str(route.query.client))) ||
  !!(!str(route.query.client) && str(route.query.profile) && !profiles.byId.has(str(route.query.profile))),
);
const viewAsValue = computed(() =>
  route.query.client
    ? `client:${str(route.query.client)}`
    : route.query.profile
      ? `profile:${str(route.query.profile)}`
      : "",
);
function pickViewAs(value: string) {
  const split = value.indexOf(":");
  setQuery(
    value.startsWith("client:")
      ? { client: value.slice(split + 1), profile: undefined }
      : value.startsWith("profile:")
        ? { profile: value.slice(split + 1), client: undefined }
        : { profile: undefined, client: undefined },
  );
}
function clearSubject() {
  setQuery({ client: undefined, profile: undefined });
}
const serverNames = computed(() =>
  [
    ...new Set([
      ...servers.items.map((server) => server.name),
      ...inventory.value.map((tool) => tool.server_name),
      ...(serverFilter.value ? [serverFilter.value] : []),
    ]),
  ].sort(),
);
const reviewCount = computed(
  () =>
    inventory.value.filter(
      (tool) =>
        needsToolReview(tool) &&
        (!serverFilter.value || tool.server_name === serverFilter.value),
    ).length,
);
watch([loaded, reviewCount, reviewOnly], ([isLoaded, count, onlyReviews]) => {
  if (isLoaded && !readOnly.value) emit("reviewCount", count);
  if (isLoaded && count === 0 && onlyReviews) reviewOnly.value = false;
});
const reviewTotals = computed(() => {
  const map = new Map<string, number>();
  for (const tool of inventory.value)
    if (tool.review)
      map.set(tool.server_name, (map.get(tool.server_name) ?? 0) + 1);
  return map;
});
const groups = computed(() => {
  const terms = search.value.toLocaleLowerCase().split(/\s+/).filter(Boolean),
    map = new Map<string, InventoryTool[]>();
  for (const tool of inventory.value) {
    if (serverFilter.value && tool.server_name !== serverFilter.value) continue;
    if (reviewOnly.value && !needsToolReview(tool)) continue;
    if (tier.value && tool.tier !== tier.value) continue;
    if (availability.value === "enabled" && tool.disabled) continue;
    if (availability.value === "disabled" && !tool.disabled) continue;
    const haystack =
      `${tool.name} ${tool.server_name} ${tool.description}`.toLocaleLowerCase();
    if (!terms.every((term) => haystack.includes(term))) continue;
    const rows = map.get(tool.server_name) ?? [];
    rows.push(tool);
    map.set(tool.server_name, rows);
  }
  return [...map]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([server, tools]) => ({
      server,
      tools: tools.sort((a, b) => a.name.localeCompare(b.name)),
    }));
});
const hasFilters = computed(
  () =>
    !!(
      search.value ||
      (!props.serverName && serverFilter.value) ||
      reviewOnly.value ||
      availability.value ||
      tier.value
    ),
);
function clearFilters() {
  setQuery({
    q: undefined,
    server: undefined,
    review: undefined,
    status: undefined,
    tier: undefined,

    approval: undefined,
    focus: undefined,
    tool: undefined,
  });
}
const explain = ref<string | null>(null);
const explainSubject = computed(() =>
  requestScope.value.client
    ? { kind: "client" as const, name: requestScope.value.client }
    : { kind: "profile" as const, name: requestScope.value.profile },
);
function explainTool(tool: InventoryTool) {
  explain.value = `${tool.server_name}:${tool.name}`;
}
let cancelPresentationWarmup = () => {};
let sequence = 0,
  disposed = false,
  started = false,
  activeMutations = 0,
  refreshTimer: ReturnType<typeof setTimeout> | undefined;
async function loadTools(background = false) {
  if (activeMutations && (background || loaded.value)) return;
  const seq = ++sequence;
  if (!background) loading.value = true;
  error.value = "";
  try {
    if (scopeConflict.value) {
      inventory.value = [];
      failedServers.value = [];
      return;
    }
    const catalog = await api.tools.list({ ...requestScope.value, server: props.serverName });
    if (disposed || seq !== sequence) return;
    inventory.value = catalog.tools ?? [];
    cancelPresentationWarmup();
    cancelPresentationWarmup = warmToolPresentation(inventory.value);
    failedServers.value = catalog.failed_servers ?? [];
    loaded.value = true;
    await scrollToSection();
  } catch (err) {
    if (disposed || seq !== sequence) return;
    // Never leave another subject's tools on screen after a failed scoped request.
    if (readOnly.value) {
      inventory.value = [];
      failedServers.value = [];
    }
    error.value = err instanceof Error ? err.message : "Could not load tools";
  } finally {
    if (!disposed && seq === sequence) loading.value = false;
  }
}
function afterDecision(change: ToolChange) {
  if (readOnly.value) return;
  const names = new Set(change.names);
  inventory.value = inventory.value.map((tool) => {
    if (
      tool.server_name !== change.server ||
      tool.server_incarnation !== change.incarnation ||
      !names.has(tool.name)
    )
      return tool;
    if (change.kind === "classification")
      return {
        ...tool,
        tier: change.tier,
        classification: change.classification,
      };
    if (change.kind === "availability")
      return { ...tool, disabled: !change.enabled };
    const { review, ...approved } = tool;
    return {
      ...approved,
      approval_status: "approved",
      disabled: change.kind === "block" ? true : tool.disabled,
    };
  });
}
function trackMutation(completion: Promise<void>) {
  activeMutations++;
  // A response captured before this write must not undo the local result.
  sequence++;
  if (refreshTimer) clearTimeout(refreshTimer);
  // Track the request even if filtering removes its section before it finishes.
  void completion.then(() => {
    activeMutations--;
    if (!disposed && !activeMutations) refreshSoon();
  });
}
let lastHash = "";
async function scrollToSection() {
  if (!route.hash || route.hash === lastHash) return;
  await nextTick();
  let id: string;
  try {
    id = decodeURIComponent(route.hash.slice(1));
  } catch {
    return;
  }
  const target = document.getElementById(id);
  if (target) {
    target.scrollIntoView({ block: "start" });
    lastHash = route.hash;
  }
}
function refreshSoon() {
  if (refreshTimer) clearTimeout(refreshTimer);
  if (activeMutations) return;
  refreshTimer = setTimeout(() => void loadTools(true), 400);
}
onMounted(async () => {
  if (disposed) return;
  void servers.ensureLoaded();
  void profiles.ensureLoaded();
  void clients.ensureLoaded();
  started = true;
  void loadTools();
});
// The stream invalidates tools (with their reviews) per server. A view-as
// listing also depends on the viewed profile or client and on server state.
onLive(["tools", "servers", "profiles", "clients"], () => void loadTools(true), {
  delay: 400,
  filter: (frame) => {
    if (frame.entity === "tools")
      return !props.serverName || !frame.id || frame.id === props.serverName;
    if (!readOnly.value) return false;
    if (frame.entity === "servers") return !props.serverName || frame.id === props.serverName;
    if (frame.entity === "profiles") return frame.id === requestScope.value.profile;
    return frame.id === requestScope.value.client;
  },
});
// Watch individual scope values, not a new array on every URL change.
// Search, review, tier, server and availability are entirely local filters.
watch(
  [
    () => route.query.client,
    () => route.query.profile,
    () => props.serverName,
    () => requestScope.value.client,
    () => requestScope.value.profile,
  ],
  () => {
    if (refreshTimer) clearTimeout(refreshTimer);
    cancelPresentationWarmup();
    inventory.value = [];
    failedServers.value = [];
    loaded.value = false;
    if (started) void loadTools();
  },
);
watch(
  () => route.hash,
  () => {
    lastHash = "";
    if (route.hash) void scrollToSection();
  },
);
onUnmounted(() => {
  cancelPresentationWarmup();
  disposed = true;
  sequence++;
  if (refreshTimer) clearTimeout(refreshTimer);
});
</script>
