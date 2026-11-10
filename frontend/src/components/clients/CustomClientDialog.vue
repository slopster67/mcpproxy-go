<template>
  <BaseDialog
    :open="open"
    title="Add client"
    @close="close"
  >
    <form id="add-client-form" @submit.prevent="submit">
      <FieldGroup>
        <Field
          ><FieldLabel for="custom-client-name">Name</FieldLabel
          ><Input
            id="custom-client-name"
            v-model="name"
            placeholder="Claude Desktop"
            autocomplete="off"
            maxlength="64"
            autofocus
        /></Field>
        <Field v-if="peers.hasPeers"><FieldLabel>Connect through</FieldLabel><HostSelect v-model="host" :disabled="busy" /></Field>
        <Field v-if="profiles.items.length"
          ><FieldLabel for="custom-client-profile">Profile</FieldLabel
          ><NativeSelect
            id="custom-client-profile"
            v-model="selectedProfile"
            ><option value="">All servers</option>
            <option
              v-for="profile in profiles.items"
              :key="profile.id"
              :value="profile.id"
            >
              {{ profile.name }}
            </option></NativeSelect
          ></Field
        >
        <Field v-if="selectedProfile" orientation="horizontal"
          ><Switch
            id="custom-client-switching"
            v-model="allowSwitching"
          /><FieldLabel for="custom-client-switching"
            >Allow switching</FieldLabel
          ></Field
        >
        <Field orientation="horizontal">
            <Switch id="custom-client-code-execution" v-model="allowCodeExecution" :disabled="busy" />
            <FieldLabel for="custom-client-code-execution">Allow code execution</FieldLabel>
          </Field>
          <Field orientation="horizontal">
          <Switch id="custom-client-allow-managing" v-model="allowManaging" :disabled="busy" />
          <FieldLabel for="custom-client-allow-managing">Allow MCPProxy management</FieldLabel>
        </Field>
        </FieldGroup
      >
    </form>
    <Alert v-if="error" variant="destructive">{{
      error
    }}</Alert>
    <template #actions
      ><Button variant="outline" :disabled="busy" @click="close">Cancel</Button
      ><Button
        type="submit"
        form="add-client-form"
        :disabled="busy || !name.trim()"
        ><Spinner v-if="busy" data-icon="inline-start" />Add client</Button
      ></template
    >
  </BaseDialog>
</template>
<script setup lang="ts">
import { usePeersStore } from "@/stores/peers";
import HostSelect from "@/components/HostSelect.vue";
import { onMounted, ref, watch } from "vue";
import BaseDialog from "@/components/BaseDialog.vue";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { Switch } from "@/components/ui/switch";
import { Spinner } from "@/components/ui/spinner";
import { useClientsStore } from "@/stores/clients";
import { useProfilesStore } from "@/stores/profiles";
import { describeError } from "@/utils/profiles";
import type { ClientCredential, ClientPresence } from "@/types/api";
const peers = usePeersStore();
onMounted(peers.ensureLoaded);
const props = defineProps<{ open: boolean; initialProfile?: string; initialHost?: string }>();
const emit = defineEmits<{
  close: [];
  created: [result: ClientCredential & { client: ClientPresence }];
}>();
const profiles = useProfilesStore(),
  clients = useClientsStore();
const allowCodeExecution = ref(false);
const allowManaging = ref(false);
const allowSwitching = ref(false);
const host = ref("");
const name = ref(""),
  selectedProfile = ref(""),
  error = ref(""),
  busy = ref(false);
watch(
  () => props.open,
  (open) => {
    if (open) {
      name.value = "";
      host.value = props.initialHost ?? "";
      allowManaging.value = false;
      allowCodeExecution.value = false;
      allowSwitching.value = false;
      selectedProfile.value = props.initialProfile ?? "";
      error.value = "";
      void profiles.ensureLoaded();
    }
  },
  { immediate: true },
);
watch(selectedProfile, (profile) => {
  if (!profile) allowSwitching.value = false;
});
function close() {
  if (!busy.value) emit("close");
}
async function submit() {
  if (busy.value || !name.value.trim()) return;
  busy.value = true;
  error.value = "";
  try {
    const label = name.value.trim();
    emit(
      "created",
      await clients.create({
        display_name: label,
        allow_managing: allowManaging.value,
      allow_code_execution: allowCodeExecution.value,
        host_id:host.value,
        mode:
          !selectedProfile.value || allowSwitching.value
            ? "switchable"
            : "locked",
        ...(selectedProfile.value ? { profile: selectedProfile.value } : {}),
      }),
    );
  } catch (err) {
    error.value = describeError(err);
  } finally {
    busy.value = false;
  }
}
</script>
