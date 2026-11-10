<template>
  <BaseDialog
    :open="open"
    :title="title"
    :wide="stage === 'token'"
    @close="close"
  >
    <template v-if="stage === 'edit'" #title>
      <div class="flex h-8 min-w-0 items-center gap-1 pr-9">
        <template v-if="renaming">
          <DialogTitle class="sr-only">{{ name }}</DialogTitle>
          <Input
            ref="nameInput"
            v-model="displayName"
            class="h-8"
            aria-label="Client name"
            autocomplete="off"
            maxlength="64"
            :disabled="busy"
            @blur="finishRename"
            @keydown.enter.prevent="finishRename"
            @keydown.esc.stop.prevent="cancelRename"
          />
        </template>
        <template v-else>
          <DialogTitle class="truncate">{{ name }}</DialogTitle>
          <Button
            variant="ghost"
            size="icon-sm"
            class="shrink-0"
            aria-label="Rename client"
            title="Rename client"
            :disabled="busy || client.credential_state !== 'client'"
            @click="startRename"
            ><Pencil aria-hidden="true"
          /></Button>
        </template>
      </div>
    </template>
    <template v-if="stage === 'edit'">
      <form id="edit-client-form" @submit.prevent="save">
        <FieldGroup>
          <Field v-if="peers.hasPeers"><FieldLabel>Connect through</FieldLabel><HostSelect v-model="host" :disabled="busy" /></Field>
          <Field v-if="profiles.items.length || selectedProfile"
            ><FieldLabel for="edit-client-profile">Profile</FieldLabel
            ><NativeSelect
              id="edit-client-profile"
              v-model="selectedProfile"
              :disabled="busy || client.credential_state !== 'client'"
              ><option value="">All servers</option>
              <option
                v-for="profile in profiles.items"
                :key="profile.id"
                :value="profile.id"
              >
                {{ profile.name }}
              </option>
              <option
                v-if="selectedProfile && !profiles.byId.has(selectedProfile)"
                :value="selectedProfile"
              >
                Profile missing
              </option></NativeSelect
            ></Field
          >
          <Field v-if="selectedProfile" orientation="horizontal"
            ><Switch
              id="edit-client-switching"
              v-model="allowSwitching"
              :disabled="busy || client.credential_state !== 'client'"
            /><FieldLabel for="edit-client-switching"
              >Allow switching</FieldLabel
            ></Field
          >
          <Field orientation="horizontal">
            <Switch id="client-code-execution" v-model="allowCodeExecution" :disabled="busy" />
            <FieldLabel for="client-code-execution">Allow code execution</FieldLabel>
          </Field>
          <Field orientation="horizontal">
            <Switch id="client-allow-managing" v-model="allowManaging" :disabled="busy" />
            <FieldLabel for="client-allow-managing">Allow MCPProxy management</FieldLabel>
          </Field>
        </FieldGroup>
      </form>
      <div class="flex flex-wrap gap-2">
        <Button variant="outline" :disabled="busy" as-child
          ><RouterLink
            :to="{
              path: '/activity',
              query: { view: 'calls', client: client.id },
            }"
            @click="close"
            ><History data-icon="inline-start" />View activity</RouterLink
          ></Button
        >
        <Button
          variant="outline"
          :disabled="busy || client.credential_state !== 'client'"
          @click="showStage('rotate')"
          ><RefreshCw data-icon="inline-start" />Rotate token</Button
        >
        <Button
          variant="outline"
          class="text-destructive hover:text-destructive"
          :disabled="busy"
          @click="showStage('delete')"
          ><Trash2 data-icon="inline-start" />Delete</Button
        >
      </div>
    </template>
    <p v-else-if="stage === 'rotate'" class="text-sm text-muted-foreground">
      The current token will be invalidated and replaced with a new one.
    </p>
    <DeleteConfirmationText v-else-if="stage === 'delete'" :name="name" />
    <ClientCredentialFields
      v-else-if="rotated"
      :credential="rotated.credential ?? ''"
      :snippet="rotated.snippet"
    />
    <Alert
      v-if="error"
      variant="destructive"
      >{{ error }}</Alert
    >
    <template #actions>
      <Button
        v-if="stage === 'token'"
        @click="close"
        >Done</Button
      >
      <template v-else>
        <Button variant="outline" :disabled="busy" @click="close"
          >Cancel</Button
        >
        <Button
          v-if="stage === 'edit'"
          type="submit"
          form="edit-client-form"
          :disabled="busy || !dirty || client.credential_state !== 'client'"
          ><Spinner v-if="busy" data-icon="inline-start" />Save</Button
        >
        <Button
          v-else-if="stage === 'rotate'"
          :disabled="busy"
          @click="rotate"
          ><Spinner v-if="busy" data-icon="inline-start" />Rotate token</Button
        >
        <Button
          v-else
          variant="destructive"
          :disabled="busy"
          @click="remove"
          ><Spinner v-if="busy" data-icon="inline-start" />Delete client</Button
        >
      </template>
    </template>
  </BaseDialog>
</template>
<script setup lang="ts">
import { usePeersStore } from "@/stores/peers";
import HostSelect from "@/components/HostSelect.vue";
import { computed, nextTick, onMounted, ref, watch } from "vue";
import { History, Pencil, RefreshCw, Trash2 } from "@lucide/vue";
import { DialogTitle } from "@/components/ui/dialog";
import BaseDialog from "@/components/BaseDialog.vue";
import DeleteConfirmationText from "@/components/DeleteConfirmationText.vue";
import ClientCredentialFields from "./ClientCredentialFields.vue";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { useProfilesStore } from "@/stores/profiles";
import { useClientsStore } from "@/stores/clients";
import { describeError } from "@/utils/profiles";
import type { ClientCredential, ClientPresence } from "@/types/api";
const peers = usePeersStore();
onMounted(peers.ensureLoaded);
const props = defineProps<{ open: boolean; client: ClientPresence }>();
const emit = defineEmits<{
  close: [];
  saved: [];
  deleted: [];
}>();
const profiles = useProfilesStore(),
  clients = useClientsStore();
const allowCodeExecution = ref(props.client.allow_code_execution ?? false);
const allowManaging = ref(props.client.allow_managing ?? false);
const host = ref(props.client.host_id ?? "");
const displayName = ref(props.client.display_name || props.client.id);
const renaming = ref(false);
const nameInput = ref<{ $el?: HTMLInputElement } | HTMLInputElement | null>(null);
const selectedProfile = ref(props.client.profile ?? ""),
  allowSwitching = ref(props.client.profile_mode === "switchable"),
  busy = ref(false),
  error = ref("");
type Stage = "edit" | "rotate" | "delete" | "token";
const stage = ref<Stage>("edit");
const rotated = ref<ClientCredential | null>(null);
const name = computed(() => props.client.display_name || props.client.id);
const title = computed(() =>
  stage.value === "rotate"
    ? `Rotate token for ${name.value}?`
    : stage.value === "delete"
      ? "Delete client"
      : stage.value === "token"
        ? `${name.value} token`
        : name.value,
);
const mode = computed(() =>
  !selectedProfile.value || allowSwitching.value ? "switchable" : "locked",
);
const dirty = computed(
  () =>
    displayName.value.trim() !== (props.client.display_name || props.client.id) ||
    allowCodeExecution.value !== (props.client.allow_code_execution ?? false) ||
    allowManaging.value !== (props.client.allow_managing ?? false) ||
    host.value !== (props.client.host_id ?? "") ||
    selectedProfile.value !== (props.client.profile ?? "") ||
    mode.value !==
      (!props.client.profile || props.client.profile_mode === "switchable"
        ? "switchable"
        : "locked"),
);
function startRename() {
  renaming.value = true;
  void nextTick(() => {
    const input = nameInput.value;
    const el = input instanceof HTMLInputElement ? input : input?.$el;
    el?.focus();
    el?.select();
  });
}
// Leaving the field restores the title only when the name is unchanged (or
// emptied); a changed name stays editable and is applied by Save.
function finishRename() {
  const current = props.client.display_name || props.client.id;
  if (!displayName.value.trim()) displayName.value = current;
  if (displayName.value.trim() === current) renaming.value = false;
}
function cancelRename() {
  displayName.value = props.client.display_name || props.client.id;
  renaming.value = false;
}
function showStage(next: Stage) {
  if (busy.value) return;
  error.value = "";
  stage.value = next;
}
watch(selectedProfile, (profile, previous) => {
  if (!profile || !previous) allowSwitching.value = false;
});
function close() {
  if (!busy.value) emit("close");
}
async function save() {
  if (busy.value || !dirty.value || props.client.credential_state !== "client")
    return;
  if (!displayName.value.trim()) {
    error.value = "Name is required";
    return;
  }
  busy.value = true;
  error.value = "";
  try {
    await clients.patch(props.client.id, {
      display_name: displayName.value.trim(),
      profile: selectedProfile.value,
      allow_managing: allowManaging.value,
      allow_code_execution: allowCodeExecution.value,
      host_id:host.value,
      mode: mode.value,
    });
    emit("saved");
  } catch (err) {
    error.value = describeError(err);
  } finally {
    busy.value = false;
  }
}
async function rotate() {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  try {
    rotated.value = await clients.rotate(props.client.id);
    stage.value = "token";
  } catch (err) {
    error.value = describeError(err);
  } finally {
    busy.value = false;
  }
}
async function remove() {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  try {
    await clients.remove(props.client.id);
    emit("deleted");
  } catch (err) {
    error.value = describeError(err);
  } finally {
    busy.value = false;
  }
}
</script>
