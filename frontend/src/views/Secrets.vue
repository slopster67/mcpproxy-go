<template>
  <div class="flex flex-col gap-6">
    <!-- Page Header -->
    <div class="flex flex-wrap items-center justify-between gap-3">
      <h1 class="text-2xl font-medium tracking-tight">Secrets</h1>
      <div class="flex gap-2">
        <!-- Always-available add button: a per-row button does not render when
             there are no rows. -->
        <Button
          variant="outline"
          size="default"
          @click="showAddSecretModal()"
        >
          <Plus data-icon="inline-start"  aria-hidden="true" />
          Add secret
        </Button>
      </div>
    </div>

    <Input
      v-model="searchQuery"
      type="search"
      placeholder="Search secrets..."
      aria-label="Search secrets"
      class="w-full sm:w-56"
    />

    <div class="flex flex-col gap-4">
      <!-- Loading State -->
      <Card v-if="!store.loaded && !store.error" class="gap-0 py-0" role="status" aria-label="Loading secrets">
        <div class="divide-y divide-border" aria-hidden="true">
          <div v-for="row in 3" :key="row" class="px-3 py-2">
            <div class="grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center">
              <div class="min-w-0">
                <div class="flex h-5 items-center gap-2">
                  <Skeleton class="size-4 shrink-0" />
                  <Skeleton class="h-3.5 w-36 max-w-full" />
                </div>
              </div>
              <div class="flex items-center gap-1.5">
                <Skeleton class="h-6 w-20 rounded-4xl" />
                <Skeleton class="h-6 w-20 rounded-4xl" />
              </div>
            </div>
          </div>
        </div>
      </Card>

      <!-- Error State -->
      <Alert
        v-else-if="!store.loaded && store.error"
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
          <h3 class="font-bold">Failed to load secrets</h3>
          <div class="text-sm">{{ store.error.message }}</div>
        </div>
        <Button
          size="sm"
          variant="outline"
          @click="store.refetch()"

        >
          Try Again
        </Button>
      </Alert>

      <!-- Empty State -->
      <div v-else-if="filteredItems.length === 0" class="text-center py-12">
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
            d="M12 15v2m-6 4h12a2 2 0 002-2v-6a2 2 0 00-2-2H6a2 2 0 00-2 2v6a2 2 0 002 2zm10-10V7a4 4 0 00-8 0v4h8z"
          />
        </svg>
        <h3 class="text-xl font-semibold mb-2">No secrets found</h3>
        <p v-if="!searchQuery" class="text-muted-foreground mb-4">
          Store an API key or token in your encrypted secret store, then reference it from your config as ${secret:name}.
        </p>
        <div class="flex gap-2 justify-center">
          <Button
            variant="outline"
            v-if="searchQuery"
            @click="searchQuery = ''"
          >
            Clear Search
          </Button>
          <!-- UX audit F8: empty-state CTA -->
          <Button
            variant="outline"
            size="default"
            v-if="!searchQuery"
            @click="showAddSecretModal()"
          >
            <Plus data-icon="inline-start"  aria-hidden="true" />
            Add secret
          </Button>
        </div>
      </div>

      <Card v-else class="gap-0 py-0">
        <TransitionGroup name="secret-list" tag="div" class="divide-y divide-border">
          <div
            v-for="item in filteredItems"
            :key="item.id"
            class="px-3 py-2"
            :data-secret-name="item.name"
            :data-secret-kind="item.kind"
          >
            <div class="grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center">
              <div class="min-w-0 flex-1">
                <CardTitle class="flex items-center gap-2 text-sm">
                  <LockKeyhole v-if="item.kind === 'secret'" class="size-3.5 shrink-0 text-info" aria-label="Secret" role="img" />
                  <DollarSign v-else class="size-3.5 shrink-0 text-warning" aria-label="Environment variable" role="img" />
                  <button type="button" class="cursor-pointer break-all text-left underline-offset-4 hover:underline focus-visible:rounded-sm focus-visible:outline-2 focus-visible:outline-ring" title="Copy reference" :aria-label="`Copy reference for ${item.name}`" @click="copyReference(item.kind, item.name)">{{ item.name }}</button>
                  <span v-if="item.kind === 'env' && item.host_name" class="text-xs text-muted-foreground">{{item.host_name}}</span>
                  <Badge v-if="!item.available" variant="outline">Unavailable</Badge>
                  <Badge v-else-if="!item.is_set" variant="destructive">Missing</Badge>
                </CardTitle>
              </div>
              <div class="flex flex-wrap items-center gap-1.5">
                <em v-if="item.kind === 'env' && !item.is_set && item.available" class="text-xs text-muted-foreground">Add this variable to {{item.host_name ? `${item.host_name}'s` : 'the MCPProxy'}} environment</em>
                <Badge v-if="item.servers.length === 1" variant="secondary" as-child>
                  <RouterLink :to="{ name: 'server-detail', params: { serverName: item.servers[0] }, query: { tab: 'config' } }">{{ item.servers[0] }}</RouterLink>
                </Badge>
                <DropdownMenu v-else-if="item.servers.length > 1">
                  <DropdownMenuTrigger as-child>
                    <Badge variant="secondary" as="button" type="button" class="cursor-pointer">{{ item.servers.length }} servers<ChevronDown class="size-3" /></Badge>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="end">
                    <DropdownMenuItem v-for="name in item.servers" :key="name" as-child>
                      <RouterLink :to="{ name: 'server-detail', params: { serverName: name }, query: { tab: 'config' } }">{{ name }}</RouterLink>
                    </DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenu>
                <template v-if="item.kind === 'secret'">
                  <Button v-if="!item.is_set" size="xs" @click="showAddSecretModal(item.name)"><Plus data-icon="inline-start" />Add Value</Button>
                  <template v-else>
                    <Button size="xs" variant="outline" @click="showAddSecretModal(item.name)"><Pencil data-icon="inline-start" />Update</Button>
                    <Button size="xs" variant="outline" class="text-destructive hover:text-destructive" @click="deleteSecret(item)"><Trash2 data-icon="inline-start" />Remove</Button>
                  </template>
                </template>
              </div>
            </div>
          </div>
        </TransitionGroup>
      </Card>

      <!-- Add secret Modal -->
      <AddSecretModal
        :show="showAddModal"
        :predefinedName="modalPredefinedName"
        @close="
          showAddModal = false;
          modalPredefinedName = undefined;
        "
      />
      <DeleteConfirmDialog
        :open="!!pendingDelete"
        kind="secret"
        location="secret"
        :name="pendingDelete?.name ?? ''"
        :busy="deleting"
        @close="pendingDelete = null"
        @confirm="confirmDeleteSecret"
      >
        <Alert v-if="deleteError" variant="destructive">{{ deleteError }}</Alert>
      </DeleteConfirmDialog>
    </div>
  </div>
</template>

<script setup lang="ts">
import { copyText } from "@/utils/clipboard";
import { Plus, LockKeyhole, DollarSign, Pencil, Trash2, ChevronDown } from "@lucide/vue";
import { RouterLink } from "vue-router";
import { DropdownMenu, DropdownMenuTrigger, DropdownMenuContent, DropdownMenuItem } from "@/components/ui/dropdown-menu";
import { Skeleton } from "@/components/ui/skeleton";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Alert } from "@/components/ui/alert";
import { Card, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { ref, computed, onMounted } from "vue";
import AddSecretModal from "@/components/AddSecretModal.vue";
import DeleteConfirmDialog from "@/components/DeleteConfirmDialog.vue";
import { useSystemStore } from "@/stores/system";
import { useSecretsStore } from "@/stores/secrets";

const systemStore = useSystemStore();
const store = useSecretsStore();

const searchQuery = ref("");
const showAddModal = ref(false);
const modalPredefinedName = ref<string | undefined>(undefined);

async function copyReference(kind: "secret" | "env", name: string) {
  try {
    await copyText(`\${${kind}:${name}}`);
    systemStore.addToast({ type: "success", title: "Reference copied", message: "" });
  } catch {
    systemStore.addToast({ type: "error", title: "Could not copy reference", message: "" });
  }
}

const filteredItems = computed(() => {
  const query = searchQuery.value.toLowerCase();
  return store.items
    .map(entry => ({ ...entry, kind: entry.type === "env" ? "env" as const : "secret" as const }))
    .filter(item =>
      item.name.toLowerCase().includes(query) ||
      item.reference.toLowerCase().includes(query),
    ).sort((a, b) =>
      Number(a.is_set || !a.available) - Number(b.is_set || !b.available) ||
      a.name.localeCompare(b.name, undefined, { sensitivity: 'base', numeric: true }) ||
      a.kind.localeCompare(b.kind),
    );
});

// Called with a name to fill a config-referenced secret, or with no argument
// from the page-level / empty-state "Add secret" CTA (UX audit F8).
const showAddSecretModal = (predefinedName?: string) => {
  modalPredefinedName.value = predefinedName;
  showAddModal.value = true;
};

const pendingDelete = ref<{ name: string; type: string } | null>(null);
const deleting = ref(false);
const deleteError = ref("");
function deleteSecret(secret: { name: string; type: string }) {
  deleteError.value = "";
  pendingDelete.value = { name: secret.name, type: secret.type };
}
async function confirmDeleteSecret() {
  const secret = pendingDelete.value;
  if (!secret || deleting.value) return;
  deleting.value = true;
  deleteError.value = "";
  try {
    await store.remove(secret.name, secret.type);
    pendingDelete.value = null;
    systemStore.addToast({ type: "success", title: "Secret Deleted", message: `Secret "${secret.name}" deleted successfully` });
  } catch (error) {
    deleteError.value = error instanceof Error ? error.message : "Failed to delete secret";
  } finally {
    deleting.value = false;
  }
}

onMounted(() => void store.ensureLoaded());
</script>
