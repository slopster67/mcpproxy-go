<template>
  <div class="flex min-w-0 flex-col gap-6">
    <div
      class="flex min-h-9 flex-wrap items-center justify-between gap-3"
    >
      <h1 class="text-2xl font-medium tracking-tight">Clients</h1>
      <div class="flex flex-wrap items-center gap-2">
        <Button
          variant="outline"
          @click="peerDialog?.show()"
          ><Plus data-icon="inline-start" />Add peer</Button
        >
        <Button
          v-if="!peers.hasPeers"
          variant="outline"
          @click="addClient()"
          ><Plus data-icon="inline-start" />Add client</Button
        >
      </div>
    </div>
    <Button
      v-if="scopeQuery.state.client"
      variant="ghost"
      class="self-start"
      size="sm"
      @click="scopeQuery.set({ client: undefined })"
      >Show all clients</Button
    >
    <Alert v-if="loadError" variant="destructive" class="flex items-center justify-between gap-3">
      <span>{{ loadError }}</span>
      <Button variant="outline" size="sm" @click="retry">Try again</Button>
    </Alert>
    <div
      v-if="!store.loaded && !store.error"
      class="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4"
      aria-busy="true"
      v-show="showSkeleton"
    >
      <Card v-for="n in 4" :key="n" size="sm"
        ><CardHeader
          ><Skeleton class="h-5 w-32" />
          <div class="flex h-4 items-center justify-between gap-3">
            <Skeleton class="h-2.5 w-20" /><Skeleton
              class="h-3 w-16"
            /></div></CardHeader
      ></Card>
    </div>
    <div v-else class="flex flex-col gap-8">
      <section
        v-for="group in groups"
        :key="group.id"
        class="flex min-w-0 flex-col gap-3"
        :data-host-id="group.id"
      >
        <header
          v-if="peers.hasPeers"
          class="flex flex-wrap items-center justify-between gap-3"
        >
          <div class="flex min-w-0 items-center gap-2">
            <span
              v-if="group.peer"
              class="size-2 shrink-0 rounded-full"
              :class="
                group.peer && !group.peer.online
                  ? 'bg-white'
                  : 'bg-[var(--server-status-green)]'
              "
              :title="group.peer && !group.peer.online ? 'Offline' : 'Online'"
            />
            <h2 class="truncate text-base font-medium">{{ group.name }}</h2>
            <span
              v-if="group.peer && !group.peer.online && group.peer.last_seen"
              class="text-xs text-muted-foreground"
              >last seen {{ formatRelative(group.peer.last_seen) }}</span
            >
          </div>
          <div class="flex items-center gap-2">
            <Button
              v-if="group.peer"
              variant="ghost"
              size="sm"
              :aria-label="`Manage ${group.name}`"
              @click="peerDialog?.show(group.peer)"
              ><Settings2 data-icon="inline-start" />Manage peer</Button
            >
            <Button
              variant="outline"
              size="sm"
              @click="addClient(group.id)"
              ><Plus data-icon="inline-start" />Add client</Button
            >
          </div>
        </header>
        <div
          class="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4"
        >
          <ClientCard
            v-for="client in group.clients"
            :key="client.id"
            :client="client"
            :profile-title="profiles.labelFor(client.profile)"
            :class="route.query.focus === client.id && 'ring-ring'"
            @edit="editing = $event"
          />
          <Card size="sm" class="relative justify-center border border-dashed bg-transparent shadow-none ring-0">
            <CardHeader>
              <button type="button" class="flex h-10 w-full cursor-pointer items-center justify-center gap-2 text-sm text-muted-foreground after:absolute after:inset-0 after:rounded-4xl hover:text-foreground focus-visible:outline-none focus-visible:after:ring-2 focus-visible:after:ring-ring" @click="addClient(group.id)">
                <Plus class="size-4" />{{ peers.hasPeers ? `Add a ${group.name} client` : 'Add a client' }}
              </button>
            </CardHeader>
          </Card>
        </div>
      </section>
    </div>
    <PeerDialog ref="peerDialog" />
    <CustomClientDialog
      :open="customOpen"
      :initial-host="newClientHost"
      @close="customOpen = false"
      @created="created"
    />
    <ClientEditDialog
      v-if="editing"
      :open="true"
      :client="editing"
      @close="editing = null"
      @saved="editing = null"
      @deleted="deleted"
    />
    <CredentialOnceDialog
      :open="!!secret"
      :credential="secret?.credential ?? ''"
      :snippet="secret?.snippet"
      :title="secret?.title"
      @close="secret = null"
    />
  </div>
</template>
<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { Plus, Settings2 } from "@lucide/vue";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardHeader } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import PeerDialog from "@/components/clients/PeerDialog.vue";
import { usePeersStore } from "@/stores/peers";
import { formatRelative } from "@/utils/datetime";
import ClientCard from "@/components/clients/ClientCard.vue";
import ClientEditDialog from "@/components/clients/ClientEditDialog.vue";
import CredentialOnceDialog from "@/components/clients/CredentialOnceDialog.vue";
import CustomClientDialog from "@/components/clients/CustomClientDialog.vue";
import { useDelayedLoading } from "@/composables/useDelayedLoading";
import { useScopeQuery } from "@/composables/useScopeQuery";
import { useClientsStore } from "@/stores/clients";
import { useProfilesStore } from "@/stores/profiles";
import type { ClientCredential, ClientPresence, Peer } from "@/types/api";
const route = useRoute(),
  router = useRouter(),
  store = useClientsStore(),
  profiles = useProfilesStore(),
  peers = usePeersStore(),
  scopeQuery = useScopeQuery("clients");
const customOpen = ref(false);
const editing = ref<ClientPresence | null>(null);
const peerDialog = ref<InstanceType<typeof PeerDialog> | null>(null);
const newClientHost = ref("");
function addClient(host = "") {
  newClientHost.value = host;
  customOpen.value = true;
}
const secret = ref<{
  credential: string;
  title: string;
  snippet?: { generic_http: string } | null;
} | null>(null);
const showSkeleton = useDelayedLoading(() => !store.loaded && !store.error);
const loadError = computed(
  () =>
    (!store.loaded && store.error?.message) ||
    (!peers.loaded && peers.error?.message) ||
    "",
);
function retry() {
  void store.ensureLoaded();
  void peers.ensureLoaded();
}
// ?client= narrows the page to one client.
const visibleClients = computed(() =>
  scopeQuery.state.client
    ? store.items.filter((client) => client.id === scopeQuery.state.client)
    : store.items,
);
const groups = computed(() => {
  const hosts: { id: string; name: string; peer?: Peer }[] = [
    { id: "", name: "Primary" },
    ...peers.items.map((peer) => ({ id: peer.id, name: peer.name, peer })),
  ];
  return hosts
    .map((host) => ({
      ...host,
      clients: visibleClients.value.filter(
        (client) => (client.host_id || "") === host.id,
      ),
    }))
    .filter((group) => !scopeQuery.state.client || group.clients.length > 0);
});
function created(result: ClientCredential & { client: ClientPresence }) {
  customOpen.value = false;
  secret.value = {
    credential: result.credential,
    snippet: result.snippet,
    title: `${result.client.display_name || result.client.id} token`,
  };
}
function deleted() {
  editing.value = null;
}
watch(
  () => [route.query.focus, route.query.move, store.items.length],
  () => {
    if (route.query.move === "1" && typeof route.query.focus === "string") {
      const client = store.byId.get(route.query.focus);
      if (client) editing.value = client;
    }
  },
);
onMounted(() => {
  void profiles.ensureLoaded();
  void peers.ensureLoaded();
  void store.ensureLoaded();
  if (route.query.create === "1") {
    customOpen.value = true;
    const query = { ...route.query };
    delete query.create;
    void router.replace({ query });
  }
});
</script>
