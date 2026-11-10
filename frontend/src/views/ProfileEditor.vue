<template>
  <div class="flex min-w-0 flex-col gap-6">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div class="flex min-w-0 items-center gap-2">
        <Button variant="ghost" size="icon" as-child
          ><RouterLink to="/profiles" aria-label="Back to profiles"
            ><ArrowLeft /></RouterLink
        ></Button>
        <h1 class="truncate text-2xl font-medium tracking-tight">
          {{ saved?.name ?? profiles.byId.get(id)?.name ?? "Profile" }}
        </h1>
        <Button
          variant="ghost"
          size="icon-sm"
          class="shrink-0"
          aria-label="Rename profile"
          title="Rename profile"
          :disabled="!saved || saving || renaming"
          @click="renameOpen = true"
          ><Pencil aria-hidden="true"
        /></Button>
      </div>
      <div class="flex items-center gap-2">
        <span v-if="dirty" class="text-xs text-muted-foreground"
          >Unsaved changes</span
        ><Button
          v-if="dirty"
          variant="ghost"
          :disabled="saving"
          @click="discard"
          >Discard</Button
        ><Button
          :disabled="!dirty || saving || !saved"
          @click="save"
          ><Spinner v-if="saving" data-icon="inline-start" /><Save
            v-else
            data-icon="inline-start"
          />Save</Button
        ><Button
          variant="outline"
          class="text-destructive hover:text-destructive"
          :disabled="!saved || saving || renaming"
          @click="deleteOpen = true"
          ><Trash2 data-icon="inline-start" aria-hidden="true" />Delete</Button
        >
      </div>
    </div>
    <div
      v-if="loading && !saved"
      v-show="showSkeleton"
      class="flex flex-col gap-4"
      aria-busy="true"
    >
      <Skeleton v-for="n in 3" :key="n" class="h-24" />
    </div>
    <Alert v-else-if="notFound" variant="destructive">Profile not found.</Alert
    ><Alert v-else-if="loadError" variant="destructive"
      >{{ loadError
      }}<Button variant="outline" size="sm" @click="load()"
        >Retry</Button
      ></Alert
    >
    <template v-else-if="saved">
      <Alert v-if="changedElsewhere"
        >This profile changed elsewhere.<Button
          variant="outline"
          size="sm"
          @click="reloadFromServer"
          >Reload</Button
        ></Alert
      >
      <Alert
        v-if="saveError"
        ref="refusalEl"
        variant="destructive"
        >{{ saveError }}</Alert
      >
      <div class="flex min-w-0 flex-col gap-6">
        <Alert v-if="toolsError" variant="destructive">{{ toolsError }}</Alert>
        <FieldError v-if="fieldErrors.selected_tools || fieldErrors.servers">{{
          fieldErrors.selected_tools || fieldErrors.servers
        }}</FieldError>
        <ProfilePermissions
          :servers="serversStore.items"
          :tools="tools?.tools ?? []"
          :selected-servers="draft.servers"
          :selected-tools="draft.selected_tools"
          :loading="toolsLoading"
          :disabled="saving || toolsLoading || !!toolsError"
          :focus="focusKey"
          @preset="setPermissionPreset"
          @server="setServerPermission"
          @tool="setToolPermission"
        >
          <template #aside>
            <Card class="min-w-0">
              <CardHeader><CardTitle>Clients</CardTitle></CardHeader>
              <CardContent>
                <Alert v-if="clientsError" variant="destructive">
                  {{ clientsError }}
                  <Button variant="outline" size="sm" @click="clientsStore.ensureLoaded()">Retry</Button>
                </Alert>
                <div v-if="clientsLoading && !profileClients.length" v-show="showClientsSkeleton" class="space-y-4" aria-busy="true">
                  <div v-for="n in 3" :key="n" class="flex items-center gap-3"><Skeleton class="size-4" /><Skeleton class="h-4 w-28" /></div>
                </div>
                <FieldGroup v-else-if="profileClients.length" class="gap-3">
                  <Field v-for="client in profileClients" :key="client.id" orientation="horizontal">
                    <Checkbox
                      :id="`profile-client-${client.id}`"
                      :model-value="clientSelected(client.id)"
                      :disabled="saving || clientsLoading || !!clientsError"
                      @update:model-value="selectClient(client.id, $event === true)"
                    />
                    <FieldLabel :for="`profile-client-${client.id}`" class="min-w-0 flex-1">
                      <span class="truncate">{{ client.display_name }}</span>
                    </FieldLabel>
                    <span v-if="client.profile && client.profile !== id && !clientSelected(client.id)" class="min-w-0 truncate text-xs text-muted-foreground">{{ profiles.labelFor(client.profile) }}</span>
                  </Field>
                </FieldGroup>
                <p v-else-if="!clientsError" class="text-sm text-muted-foreground">No clients yet.</p>
              </CardContent>
            </Card>
          </template>
        </ProfilePermissions>
        <Card v-if="switchChoices.length"
          ><CardHeader
            ><CardTitle>Profile switching</CardTitle
            ><CardDescription
              >Profiles clients may switch to when Allow switching is
              enabled.</CardDescription
            ></CardHeader
          ><CardContent
            ><FieldGroup
              ><Field
                v-for="other in switchChoices"
                :key="other.id"
                orientation="horizontal"
                ><Checkbox
                  :id="`switch-to-${other.id}`"
                  :model-value="draft.switchable_to.includes(other.id)"
                  @update:model-value="
                    setSwitchTarget(other.id, $event === true)
                  "
                /><FieldLabel :for="`switch-to-${other.id}`">{{
                  other.name
                }}</FieldLabel></Field
              ><FieldError v-if="fieldErrors.switchable_to">{{
                fieldErrors.switchable_to
              }}</FieldError></FieldGroup
            ></CardContent
          ></Card
        >
      </div>
      <ProfileRenameDialog
        :open="renameOpen"
        :profile="saved"
        @close="renameOpen = false"
        @renaming="renaming = true"
        @rename-failed="renaming = false"
        @renamed="onRenamed"
      /><ProfileDeleteDialog
        :open="deleteOpen"
        :profile="saved"
        @close="deleteOpen = false"
        @deleting="onDeleting"
        @delete-failed="deleting = false"
        @deleted="onDeleted"
      />
    </template>
  </div>
</template>
<script setup lang="ts">
import { ArrowLeft, Save, Pencil, Trash2 } from "@lucide/vue";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Field,
  FieldError,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field";
import { Spinner } from "@/components/ui/spinner";
import { useDelayedLoading } from "@/composables/useDelayedLoading";
import { Skeleton } from "@/components/ui/skeleton";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  computed,
  nextTick,
  onBeforeUnmount,
  onMounted,
  reactive,
  ref,
  watch,
} from "vue";
import { useRoute, useRouter } from "vue-router";
import ProfilePermissions from "@/components/profiles/ProfilePermissions.vue";
import ProfileRenameDialog from "@/components/profiles/ProfileRenameDialog.vue";
import ProfileDeleteDialog from "@/components/profiles/ProfileDeleteDialog.vue";
import api from "@/services/api";
import { onLive } from "@/services/events";
import { useProfilesStore } from "@/stores/profiles";
import { useClientsStore } from "@/stores/clients";
import { useServersStore } from "@/stores/servers";
import { describeError } from "@/utils/profiles";
import type {
  EffectiveToolsResult,
  ProfileConfig,
  ProfileView,
} from "@/types/api";
import type { ApiError } from "@/services/api";

// The profile policy editor. Edits change a
// local DRAFT; Save PUTs the whole ProfileConfig. The per-tool table is the
// server's evaluation of the SAVED profile; Try it evaluates the draft.
const props = defineProps<{ id: string }>();
const route = useRoute();
const router = useRouter();
const profiles = useProfilesStore();
const serversStore = useServersStore();
const clientsStore = useClientsStore();
const clientEdits = reactive<Record<string, boolean>>({});
const clientsLoading = computed(() => !clientsStore.loaded && !clientsStore.error);
const clientsError = computed(() =>
  !clientsStore.loaded && clientsStore.error ? describeError(clientsStore.error, "Could not load clients") : "",
);
const showClientsSkeleton = useDelayedLoading(() => clientsLoading.value);
const profileClients = computed(() => clientsStore.items);
const clientChanges = computed(() => profileClients.value.filter(client =>
  clientEdits[client.id] !== undefined && clientEdits[client.id] !== (client.profile === props.id),
));
function clientSelected(id: string): boolean {
  return clientEdits[id] ?? (clientsStore.byId.get(id)?.profile === props.id);
}
function selectClient(id: string, checked: boolean) {
  if (checked === (clientsStore.byId.get(id)?.profile === props.id)) delete clientEdits[id];
  else clientEdits[id] = checked;
}
function clearClientEdits() { for (const id of Object.keys(clientEdits)) delete clientEdits[id]; }
const switchChoices = computed(() => {
  const choices = new Map(
    profiles.items
      .filter((p) => p.id !== props.id)
      .map((p) => [p.id, { id: p.id, name: p.name }]),
  );
  for (const name of draft.switchable_to)
    if (!choices.has(name)) choices.set(name, { id: name, name: "Profile missing" });
  return [...choices.values()];
});
function setSwitchTarget(name: string, checked: boolean) {
  draft.switchable_to = checked
    ? [...draft.switchable_to, name]
    : draft.switchable_to.filter((value) => value !== name);
  draft.switch_mode = draft.switchable_to.length ? "list" : "none";
}

interface Draft {
  selected_tools: Record<string, string[]>;
  servers: string[];
  switch_mode: "unset" | "none" | "list";
  switchable_to: string[];
}

function copySelections(
  value: Record<string, string[]> | undefined,
): Record<string, string[]> {
  return Object.fromEntries(
    Object.entries(value ?? {}).map(([server, tools]) => [server, [...tools]]),
  );
}
function draftFrom(view: ProfileView): Draft {
  return {
    selected_tools: copySelections(view.selected_tools),
    servers: [...(view.servers ?? [])],
    switch_mode:
      view.switchable_to === undefined
        ? "unset"
        : view.switchable_to.length === 0
          ? "none"
          : "list",
    switchable_to: [...(view.switchable_to ?? [])],
  };
}

// switchable_to "Not set" leaves
// it out and "None" sends []; empty strings and empty rule sets are left out.
function toConfig(d: Draft): ProfileConfig {
  const cfg: ProfileConfig = { id: props.id, name: saved.value?.name ?? "", servers: [...d.servers] };
  const selected = Object.fromEntries(
    Object.entries(d.selected_tools)
      .filter(([server]) => d.servers.includes(server))
      .map(([server, tools]) => [server, [...new Set(tools)].sort()]),
  );
  if (Object.keys(selected).length) cfg.selected_tools = selected;
  if (d.switch_mode === "none") cfg.switchable_to = [];
  else if (d.switch_mode === "list") cfg.switchable_to = [...d.switchable_to];
  return cfg;
}

const saved = ref<ProfileView | null>(null);
const draft = reactive<Draft>(
  draftFrom({ id: props.id, name: "", servers: [] } as unknown as ProfileView),
);
const loading = ref(true);
const showSkeleton = useDelayedLoading(() => loading.value && !saved.value);
const notFound = ref(false);
const loadError = ref("");
const tools = ref<EffectiveToolsResult | null>(null);
const toolsLoading = ref(false);
const toolsError = ref("");
function setPermissionPreset(kind: "read" | "write" | "all" | "none") {
  if (kind === "all") {
    draft.servers = serversStore.items.map((server) => server.name).sort();
    draft.selected_tools = {};
    return;
  }
  const selected: Record<string, string[]> = {};
  if (kind !== "none")
    for (const tool of tools.value?.tools ?? []) {
      if (
        tool.intrinsic_tier === "read" ||
        (kind === "write" && tool.intrinsic_tier === "write")
      )
        (selected[tool.server] ??= []).push(tool.tool);
    }
  draft.servers = Object.keys(selected).sort();
  draft.selected_tools = selected;
}
function setServerPermission(server: string, all: boolean) {
  if (all) {
    if (!draft.servers.includes(server)) draft.servers.push(server);
  } else draft.servers = draft.servers.filter((name) => name !== server);
  const selected = { ...draft.selected_tools };
  delete selected[server];
  draft.selected_tools = selected;
}
function setToolPermission(server: string, tool: string, enabled: boolean) {
  const previousAll =
    draft.servers.includes(server) &&
    !Object.hasOwn(draft.selected_tools, server);
  if (!draft.servers.includes(server)) draft.servers.push(server);
  const selected = new Set(
    previousAll
      ? (tools.value?.tools ?? [])
          .filter((row) => row.server === server)
          .map((row) => row.tool)
      : (draft.selected_tools[server] ?? []),
  );
  if (enabled) selected.add(tool);
  else selected.delete(tool);
  draft.selected_tools = {
    ...draft.selected_tools,
    [server]: [...selected].sort(),
  };
}

const saving = ref(false);
const saveError = ref("");
const savedNote = ref("");
const fieldErrors = reactive<Record<string, string>>({});
const changedElsewhere = ref(false);
const renameOpen = ref(false);
const deleteOpen = ref(false);
const deleting = ref(false);
const announcement = ref("");
const refusalEl = ref<{ $el: HTMLElement } | null>(null);
// Defer background reloads while a rename is in flight.
const renaming = ref(false);
const profileDirty = computed(
  () =>
    Boolean(saved.value) &&
    JSON.stringify(toConfig(draft)) !==
      JSON.stringify(toConfig(draftFrom(saved.value as ProfileView))),
);

const dirty = computed(() => profileDirty.value || clientChanges.value.length > 0);

const focusKey = computed(() =>
  typeof route.query.focus === "string" ? route.query.focus : "",
);
watch([focusKey, tools], () => {
  if (
    focusKey.value &&
    tools.value?.tools.some(
      (row) => `${row.server}:${row.tool}` === focusKey.value,
    )
  )
    announcement.value = `Focused ${focusKey.value}`;
});

function clearErrors() {
  for (const key of Object.keys(fieldErrors)) delete fieldErrors[key];
  saveError.value = "";
}

// Every load takes a ticket so stale responses cannot overwrite the current profile.
let loadTicket = 0;

// The editor's baseline is a copy: the collection's object changes in place.
function snapshot(view: ProfileView): ProfileView {
  return JSON.parse(JSON.stringify(view));
}

async function loadProfile(ticket: number): Promise<ProfileView | null | undefined> {
  const name = props.id;
  await profiles.ensureLoaded();
  if (ticket !== loadTicket) return;
  if (!profiles.loaded) {
    loadError.value = describeError(profiles.error, "Failed to load the profile");
    return null;
  }
  const view = profiles.byId.get(name);
  if (!view) {
    notFound.value = true;
    return null;
  }
  return snapshot(view);
}

let toolsTicket = 0;
async function loadTools(ticket: number) {
  const request = ++toolsTicket;
  const name = props.id;
  toolsLoading.value = true;
  toolsError.value = "";
  try {
    const result = await api.profiles.effectiveTools(name);
    if (ticket !== loadTicket || request !== toolsTicket) return;
    tools.value = result;
  } catch (err) {
    if (ticket !== loadTicket || request !== toolsTicket) return;
    tools.value = null;
    toolsError.value =
      (err as ApiError).status === 503
        ? "Profile evaluation is unavailable; retry in a moment"
        : describeError(err, "Failed to load the tools");
  } finally {
    if (ticket === loadTicket && request === toolsTicket)
      toolsLoading.value = false;
  }
}

// The draft's content, independent of which profile it is for. Compared before
// and after an await to tell whether the operator edited meanwhile.
function draftSnapshot(): string {
  return JSON.stringify({ ...toConfig(draft), name: "" });
}

// `force` replaces the draft whatever it holds (an explicit reload, or a switch
// to another profile). Otherwise edits made while the request was in flight win:
// the fetched profile becomes the saved baseline and the draft stays dirty.
async function load(force = false) {
  if (force) clearClientEdits();
  const ticket = ++loadTicket;
  const before = draftSnapshot();
  loading.value = true;
  clearErrors();
  const [view] = await Promise.all([
    loadProfile(ticket),
    loadTools(ticket),
  ]);
  if (ticket !== loadTicket || view === undefined) return;
  const replaceDraft = force || !saved.value || draftSnapshot() === before;
  saved.value = view;
  if (view) {
    notFound.value = false;
    loadError.value = "";
    if (replaceDraft) {
      Object.assign(draft, draftFrom(view));
      changedElsewhere.value = false;
    } else {
      changedElsewhere.value = true;
    }
  }
  if (ticket === loadTicket) loading.value = false;
}

function reloadFromServer() {
  void load(true);
}

async function save() {
  if (!dirty.value || saving.value) return;
  saving.value = true;
  clearErrors();
  savedNote.value = "";
  // The profile this save is for: if the operator navigates to another profile
  // while the PUT is in flight, its answer must not replace the new page's state.
  const name = props.id;
  const body = toConfig(draft);
  const sent = draftSnapshot();
  const updatePermissions = profileDirty.value;
  const assignments = clientChanges.value.map(client => ({client, checked: clientSelected(client.id)}));
  try {
    if (updatePermissions) {
      const result = await profiles.patch(name, body);
      if (props.id === name) {
        saved.value = snapshot(result);
        if (draftSnapshot() === sent) Object.assign(draft, draftFrom(result));
      }
    }
    for (const {client, checked} of assignments) {
      try {
        await clientsStore.patch(client.id, {profile: checked ? name : ""});
        if (props.id === name) delete clientEdits[client.id];
      } catch (error) {
        throw new Error(`Could not update ${client.display_name}: ${describeError(error)}`);
      }
    }
    if (props.id !== name) return;
    savedNote.value = "Saved";
    if (updatePermissions) await loadTools(loadTicket);
  } catch (err) {
    if (props.id !== name) return;
    const refused = err as ApiError;
    if (refused.field) fieldErrors[refused.field] = refused.message;
    else saveError.value = describeError(err, "Failed to save the profile");
    // Completed writes remain saved; failed client assignments stay selected for retry.
    void revealRefusal();
  } finally {
    saving.value = false;
  }
}

async function revealRefusal() {
  await nextTick();
  const target = refusalEl.value?.$el;
  if (!target) return;
  target.setAttribute("tabindex", "-1");
  target.focus({ preventScroll: true });
  target.scrollIntoView?.({ block: "center", behavior: "smooth" });
}

function discard() {
  if (!saved.value) return;
  clearClientEdits();
  Object.assign(draft, draftFrom(saved.value));
  clearErrors();
  savedNote.value = "";
}

function onRenamed() {
  renameOpen.value = false;
  renaming.value = false;
  void load();
}
function onDeleting() {
  deleting.value = true;
  ++loadTicket;
  loading.value = false;
}
async function onDeleted() {
  await router.replace({ name: "profiles" });
}

// A change made elsewhere never overwrites an unsaved draft. The profile's
// statistics (tool counts, calls, used_by) change often; they refresh the
// baseline without touching the draft.
function configKey(view: ProfileView): string {
  return JSON.stringify({ ...toConfig(draftFrom(view)), name: view.name });
}
watch(
  () => {
    const live = profiles.byId.get(props.id);
    return live ? JSON.stringify(live) : "";
  },
  (next) => {
    if (!next || !saved.value || saving.value || renaming.value || deleting.value) return;
    const live = JSON.parse(next) as ProfileView;
    if (configKey(live) === configKey(saved.value)) {
      saved.value = live;
      return;
    }
    if (dirty.value) changedElsewhere.value = true;
    else void load();
  },
);

// The per-tool table is the server's evaluation: refetch it when tools,
// servers or this profile change.
onLive(["tools", "servers", "profiles"], () => {
  if (!saved.value || loading.value || saving.value || deleting.value) return;
  void loadTools(loadTicket);
}, {
  delay: 300,
  filter: (frame) => frame.entity !== "profiles" || frame.id === props.id,
});
onMounted(() => {
  void load();
  void clientsStore.ensureLoaded();
  void serversStore.ensureLoaded();
});
onBeforeUnmount(() => {
  toolsTicket++;
});
watch(
  () => props.id,
  () => {
    renaming.value = false;
    void load(true);
  },
);
</script>
