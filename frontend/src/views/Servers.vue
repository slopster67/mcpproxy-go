<template>
  <div class="flex flex-col gap-6">
    <!-- Page Header -->
    <div class="flex flex-wrap items-center justify-between gap-3">
      <h1 class="text-2xl font-medium tracking-tight">Servers</h1>
      <Button variant="outline" size="default" @click="addServerOpen = true">
        <Plus data-icon="inline-start"  aria-hidden="true" />Add server
      </Button>
    </div>
    <div class="flex min-w-0 flex-wrap items-center gap-3">
      <NativeSelect
        v-if="profilesStore.hasProfiles"
        data-slot="native-select"
        id="servers-profile-select"
        class="w-40 shrink-0 max-w-full"
        aria-label="Profile"
        :value="profileValue"
        @change="pickProfile($event)"
      >
        <option value="">All profiles</option>
        <option
          v-for="profile in profilesStore.items"
          :key="profile.id"
          :value="profile.id"
        >
          {{ profile.name }}
        </option>
        <option v-if="profileOrphan" :value="profileValue">
          {{ profileValue }}
        </option>
      </NativeSelect>
      <Input
        v-model="searchQuery"
        type="search"
        placeholder="Search servers..."
        aria-label="Search servers"
        class="w-full sm:w-64"
      />
    </div>

    <ScopeChips
      page="servers"
      :scope-query="scopeQuery"
      :exclude="['profile']"
    />

    <div class="flex flex-col gap-4">
      <!-- A refresh failed but we still hold a list. Say so without throwing the
         servers away — the cached grid is still the most useful thing on the
         page, and the failure is about freshness, not about the servers. This
         is additive, so it sits outside the state chain below. -->
      <Alert
        v-if="hasServers && serversStore.error"
        class="border-warning/25 bg-warning/5 text-warning"
      >
        <svg
          class="w-6 h-6"
          fill="none"
          stroke="currentColor"
          viewBox="0 0 24 24"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            stroke-width="2"
            d="M12 9v2m0 4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
          />
        </svg>
        <div class="flex-1 text-sm">
          Couldn't refresh the server list — showing the last known state.
          <span class="opacity-70">{{ serversStore.error.message }}</span>
        </div>
        <Button
          size="sm"
          variant="outline"
          @click="serversStore.refetch()"

          >Retry</Button
        >
      </Alert>

      <div
        v-if="scopedBusy || (!serversStore.loaded && !serversStore.error)"
        class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4 gap-3"
        role="status"
        aria-label="Loading servers"
        v-show="showSkeleton"
      >
        <span class="sr-only">Loading servers…</span>
        <Card
          v-for="row in 6"
          :key="row"
          aria-hidden="true"
          class="py-0"
        >
          <CardContent class="flex flex-col gap-2 p-4">
            <div class="flex h-5 items-center gap-2">
              <Skeleton class="size-2 rounded-full" />
              <div class="flex h-full flex-1 items-center">
                <Skeleton class="h-3 w-1/2" />
              </div>
              <Skeleton class="size-4" />
            </div>
            <div
              class="mt-auto flex h-4 items-center justify-between gap-3 leading-4"
            >
              <Skeleton class="h-2.5 w-20" />
              <Skeleton class="h-2.5 w-24" />
            </div>
          </CardContent>
        </Card>
      </div>

      <!-- The viewed profile does not exist (404) or the scoped list
         failed. An inline message with a way out, never a blanked page. -->
      <Alert
        v-else-if="profileActive && scopedNotFound"
        class="border-warning/25 bg-warning/5 text-warning"
        role="alert"
      >
        <div class="flex-1 text-sm">Profile not found</div>
        <Button
          size="sm"
          variant="outline"
          type="button"

          @click="clearProfile"
          >Clear</Button
        >
      </Alert>
      <Alert
        v-else-if="profileActive && scopedError"
        class="border-destructive/25 bg-destructive/5 text-destructive"
        role="alert"
      >
        <div class="flex-1 text-sm">{{ scopedError }}</div>
        <Button
          size="sm"
          variant="outline"
          type="button"

          @click="loadScoped(false)"
          >Try Again</Button
        >
      </Alert>

      <!-- Error State: the load failed and we have nothing to fall back on.
         Audit F28: also suppressed while the Authentication Required modal is
         up — "Failed to load servers" is that modal's cause restated, and it
         has no fix of its own to offer. -->
      <Alert
        v-else-if="
          serversStore.error && !hasServers && !systemStore.authRequired
        "
        class="border-destructive/25 bg-destructive/5 text-destructive"
      >
        <svg
          class="w-6 h-6"
          fill="none"
          stroke="currentColor"
          viewBox="0 0 24 24"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            stroke-width="2"
            d="M12 8v4m0 4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
          />
        </svg>
        <div>
          <h3 class="font-bold">Failed to load servers</h3>
          <div class="text-sm">{{ serversStore.error.message }}</div>
        </div>
        <Button
          size="sm"
          variant="outline"
          @click="serversStore.refetch()"

        >
          Try Again
        </Button>
      </Alert>

      <!-- First-run empty state: no servers configured at all. This is the page a
         new user lands on after closing the setup wizard, so it has to offer
         the next action rather than restate that the list is empty. Kept
         distinct from the filter/search empty state below — "you have nothing
         yet" and "nothing matched" need different answers. -->
      <div
        v-else-if="isFirstRun"
        class="text-center py-12"
      >
        <svg
          class="w-24 h-24 mx-auto mb-4 opacity-50"
          fill="none"
          stroke="currentColor"
          viewBox="0 0 24 24"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            stroke-width="2"
            d="M5 12h14M5 12a2 2 0 01-2-2V6a2 2 0 012-2h14a2 2 0 012 2v4a2 2 0 01-2 2M5 12a2 2 0 00-2 2v4a2 2 0 002 2h14a2 2 0 002-2v-4a2 2 0 00-2-2"
          />
        </svg>
        <h3 class="text-xl font-semibold mb-2">No servers yet</h3>
        <p class="text-muted-foreground mb-4 max-w-lg mx-auto">
          Upstream MCP servers are where your tools come from. Add one and
          MCPProxy indexes its tools, so your AI agent can find them without
          loading every schema into its context.
        </p>
        <div class="flex flex-wrap gap-2 justify-center">
          <Button
            variant="default"
            @click="addServerOpen = true"
          >
            <svg data-icon="inline-start"

              fill="none"
              stroke="currentColor"
              viewBox="0 0 24 24"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                stroke-width="2"
                d="M12 4v16m8-8H4"
              />
            </svg>
            Add Server
          </Button>
        </div>
      </div>

      <!-- Filter/search empty state: servers exist, none match the current view. -->
      <div
        v-else-if="filteredServers.length === 0"
        class="text-center py-12"
      >
        <svg
          class="w-24 h-24 mx-auto mb-4 opacity-50"
          fill="none"
          stroke="currentColor"
          viewBox="0 0 24 24"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            stroke-width="2"
            d="M5 12h14M5 12a2 2 0 01-2-2V6a2 2 0 012-2h14a2 2 0 012 2v4a2 2 0 01-2 2M5 12a2 2 0 00-2 2v4a2 2 0 002 2h14a2 2 0 002-2v-4a2 2 0 00-2-2"
          />
        </svg>
        <h3 class="text-xl font-semibold mb-2">No servers found</h3>
        <Button
          variant="outline"
          v-if="profileActive || searchQuery"
          type="button"
          @click="searchQuery = ''; clearProfile()"
        >
          Clear filters
        </Button>
      </div>

      <!-- Servers Grid with Smooth Transitions -->
      <TransitionGroup
        v-else
        name="server-list"
        tag="div"
        class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4 gap-3"
      >
        <ServerCard
          v-for="server in filteredServers"
          :key="server.name"
          :server="server"
        />
      </TransitionGroup>
    </div>
    <AddServerDialog v-if="addServerOpen" @close="addServerOpen = false" @added="serverAdded" />
  </div>
</template>

<script setup lang="ts">
import AddServerDialog from "@/components/AddServerDialog.vue";
import { useDelayedLoading } from "@/composables/useDelayedLoading";
import { NativeSelect } from "@/components/ui/native-select";
import { Button } from "@/components/ui/button";
import { Plus } from "@lucide/vue";
import { Skeleton } from "@/components/ui/skeleton";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Alert } from "@/components/ui/alert";
import { ref, computed, onMounted, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { useScopeQuery } from "@/composables/useScopeQuery";
import { useServersStore } from "@/stores/servers";
import { useSystemStore } from "@/stores/system";
import { useProfilesStore } from "@/stores/profiles";
import api from "@/services/api";
import { onLive } from "@/services/events";
import type { Server } from "@/types";
import ServerCard from "@/components/ServerCard.vue";
import ScopeChips from "@/components/scope/ScopeChips.vue";
import { serverCardStatus } from "@/utils/serverCardStatus";
import { serverDisplayName } from "@/utils/serverRoute";

const serversStore = useServersStore();
const systemStore = useSystemStore();
const profilesStore = useProfilesStore();
const route = useRoute();
const router = useRouter();
const addServerOpen = computed({
  get: () => route.query.add === "1",
  set: (open: boolean) => {
    const query = { ...route.query };
    if (open) query.add = "1";
    else delete query.add;
    void router.replace({ query });
  },
});
function serverAdded(name: string) {
  addServerOpen.value = false;
  systemStore.addToast({ type: "success", title: "Server added", message: `${name} has been added successfully` });
}

const scopeQuery = useScopeQuery("servers");
const searchQuery = ref(typeof route.query.q === "string" ? route.query.q : "");


function applySearchQuery() {
  const q = route.query.q;
  searchQuery.value = typeof q === "string" ? q : "";
  if (route.query.status !== undefined) {
    const { status, ...query } = route.query;
    void router.replace({ query });
  }
}

// A profile in the URL asks GET /servers?profile=<p> for the servers (and tool
// counts) that profile admits. The result is page-local: the shared servers
// store keeps the full list.
const profileValue = computed(() => scopeQuery.state.profile ?? "");
const profileOrphan = computed(
  () =>
    profileValue.value !== "" &&
    profileValue.value !== "-" &&
    !profilesStore.items.some(
      (profile) => profile.id === profileValue.value,
    ),
);
// The profile the request will carry: `-` (unattributed) applies to Activity and
// Usage only, so it is never sent here.
const appliedProfile = computed(() => {
  const value = scopeQuery.toRest()?.profile ?? "";
  return value === "-" ? "" : value;
});
const profileActive = computed(() => appliedProfile.value !== "");

const scopedServers = ref<Server[] | null>(null);
const scopedLoading = ref(false);
const scopedError = ref("");
const scopedNotFound = ref(false);
const scopedBusy = computed(
  () =>
    profileActive.value &&
    scopedLoading.value &&
    scopedServers.value === null,
);

const showSkeleton = useDelayedLoading(
  () => scopedBusy.value || (!serversStore.loaded && !serversStore.error),
);

let scopedSeq = 0;
async function loadScoped(silent: boolean) {
  const profile = appliedProfile.value;
  const seq = ++scopedSeq;
  if (!profile) {
    scopedServers.value = null;
    scopedLoading.value = false;
    scopedError.value = "";
    scopedNotFound.value = false;
    return;
  }
  if (!silent) {
    scopedServers.value = null;
    scopedLoading.value = true;
  }
  scopedError.value = "";
  scopedNotFound.value = false;
  try {
    const { servers } = await api.servers.list({ profile });
    if (seq !== scopedSeq) return;
    scopedServers.value = servers ?? [];
  } catch (err) {
    if (seq !== scopedSeq) return;
    scopedServers.value = [];
    const status = (err as { status?: number } | null)?.status;
    if (status === 404) scopedNotFound.value = true;
    else
      scopedError.value =
        err instanceof Error ? err.message : "Failed to load servers";
  } finally {
    if (seq === scopedSeq) scopedLoading.value = false;
  }
}

function pickProfile(event: Event) {
  const value = (event.target as HTMLSelectElement).value;
  scopeQuery.set({ profile: value || undefined });
}

function clearProfile() {
  scopeQuery.clear(["profile"]);
}

// The applied profile changed (the select, a chip or a link): refetch under it.
watch(appliedProfile, () => {
  void loadScoped(false);
});

// A scoped list is page-local: refetch it when a server or a profile changes.
onLive(["servers", "profiles"], () => {
  if (profileActive.value) void loadScoped(true);
}, { delay: 300 });

// What the page displays: the profile's list when one is applied, otherwise the
// shared store. Search, sorting and cards read this list.
const rows = computed<Server[]>(() =>
  profileActive.value ? (scopedServers.value ?? []) : serversStore.items,
);
onMounted(async () => {
  applySearchQuery();
  void serversStore.ensureLoaded();
  void profilesStore.ensureLoaded();
  if (profileActive.value) await loadScoped(false);
});
watch(() => [route.query.status, route.query.q], applySearchQuery);

const hasServers = computed(() => rows.value.length > 0);

// Telling the user "you have no servers" is a claim, and only a list that
// actually arrived can support it. `loaded` (set once any successful list has
// been applied) is what distinguishes "none configured" from "we don't know
// yet" — `servers` starts empty either way.
const isFirstRun = computed(
  () => serversStore.loaded && serversStore.items.length === 0,
);

const filteredServers = computed(() => {
  let servers = rows.value;

  // Apply search
  if (searchQuery.value) {
    const query = searchQuery.value.toLowerCase();
    servers = servers.filter(
      (server) =>
        server.name.toLowerCase().includes(query) ||
        server.url?.toLowerCase().includes(query) ||
        server.command?.toLowerCase().includes(query),
    );
  }

  return [...servers].sort(
    (a, b) =>
      serverCardStatus(a).order - serverCardStatus(b).order ||
      serverDisplayName(a).localeCompare(serverDisplayName(b), undefined, {
        sensitivity: "base",
        numeric: true,
      }) ||
      a.name.localeCompare(b.name),
  );
});

</script>
