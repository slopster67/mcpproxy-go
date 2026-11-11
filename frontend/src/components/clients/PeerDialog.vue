<script setup lang="ts">
import { computed, ref } from "vue";
import { RefreshCw, Trash2, Copy, Check } from "@lucide/vue";
import { Button } from "@/components/ui/button";
import { Alert } from "@/components/ui/alert";
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import BaseDialog from "@/components/BaseDialog.vue";
import DeleteConfirmationText from "@/components/DeleteConfirmationText.vue";
import ClientCredentialFields from "@/components/clients/ClientCredentialFields.vue";
import { usePeersStore } from "@/stores/peers";
import type { Peer } from "@/types/api";
import { formatRelative } from "@/utils/datetime";
import { copyText } from "@/utils/clipboard";
const store = usePeersStore();
const open = ref(false),
  name = ref(""),
  busy = ref(false),
  error = ref(""),
  token = ref(""),
  copied = ref("");
const selected = ref<Peer | null>(null);
const stage = ref<"create" | "edit" | "rotate" | "delete" | "token">("create");
const remoteURL = ref(
  import.meta.env.DEV
    ? `${location.protocol}//${location.hostname}:8085`
    : location.origin,
);
const title = computed(() =>
  stage.value === "create"
    ? "Add peer"
    : stage.value === "delete"
      ? "Delete peer"
      : stage.value === "rotate"
        ? `Rotate token for ${name.value}?`
        : stage.value === "token"
          ? `Connect ${name.value}`
          : name.value,
);
const quote = (value: string) => `'${value.replace(/'/g, `'"'"'`)}'`;
const primary = computed(() => quote(remoteURL.value.replace(/\/$/, "")));
const signinCommand = computed(
  () => `MCPPROXY_PEER_PRIMARY_URL=${primary.value} mcpproxy peer`,
);
const tokenCommand = computed(
  () =>
    `MCPPROXY_PEER_PRIMARY_URL=${primary.value} \\\nMCPPROXY_PEER_TOKEN=${quote(token.value)} \\\nmcpproxy peer`,
);
function show(peer?: Peer) {
  selected.value = peer ?? null;
  name.value = peer?.name ?? "";
  stage.value = peer ? "edit" : "create";
  token.value = "";
  error.value = "";
  open.value = true;
}
function close() {
  if (!busy.value) open.value = false;
}
function change(next: typeof stage.value) {
  error.value = "";
  stage.value = next;
}
async function submit() {
  busy.value = true;
  error.value = "";
  try {
    if (stage.value === "create") {
      const result = await store.create(name.value.trim());
      selected.value = result.peer;
      token.value = result.token;
      stage.value = "token";
    } else if (stage.value === "rotate" && selected.value) {
      token.value = (await store.rotate(selected.value.id)).token;
      stage.value = "token";
    } else if (stage.value === "delete" && selected.value) {
      await store.remove(selected.value.id);
      open.value = false;
    }
  } catch (err) {
    error.value = err instanceof Error ? err.message : "Could not update peer";
  } finally {
    busy.value = false;
  }
}
async function copy(key: string, value: string) {
  try {
    await copyText(value);
    copied.value = key;
    setTimeout(() => {
      copied.value = "";
    }, 2000);
  } catch {
    error.value = "Could not copy command";
  }
}
defineExpose({ show });
</script>
<template>
  <BaseDialog
    :open="open"
    :title="title"
    :wide="stage === 'token'"
    @close="close"
  >
    <form v-if="stage === 'create'" id="peer-form" @submit.prevent="submit">
      <Field
        ><FieldLabel for="peer-name">Name</FieldLabel
        ><Input
          id="peer-name"
          v-model="name"
          placeholder="Laptop"
          autofocus
          maxlength="100"
          :disabled="busy"
      /></Field>
    </form>
    <template v-else-if="stage === 'edit' && selected"
      ><p class="text-sm text-muted-foreground">
        {{ store.online(selected.id) ? "Online" : "Offline" }} ·
        {{
          (selected && store.byId.get(selected.id)?.address) ||
          (selected.last_seen
            ? `last seen ${formatRelative(selected.last_seen)}`
            : "Not connected yet")
        }}
      </p>
      <div class="flex flex-wrap gap-2">
        <Button variant="outline" @click="change('rotate')"
          ><RefreshCw data-icon="inline-start" />Rotate token</Button
        ><Button
          variant="outline"
          class="text-destructive hover:text-destructive"
          @click="change('delete')"
          ><Trash2 data-icon="inline-start" />Delete</Button
        >
      </div></template
    >
    <p v-else-if="stage === 'rotate'" class="text-sm text-muted-foreground">
      The current token will be invalidated and replaced with a new one. A peer
      signed in through the browser is signed out too. Update the token or sign
      the peer in again to reconnect.
    </p>
    <DeleteConfirmationText v-else-if="stage === 'delete'" :name="name" />
    <FieldGroup v-else-if="stage === 'token'">
      <Field
        ><FieldLabel for="primary-url">Primary URL</FieldLabel
        ><Input
          id="primary-url"
          v-model="remoteURL"
          type="url"
          class="font-mono"
      /></Field>
      <Field
        v-for="item in [
          {
            key: 'signin',
            label: 'Sign in from the peer',
            hint: `Run this on the peer, open its address (http://127.0.0.1:8080 by default) in a browser there and choose ${name}.`,
            value: signinCommand,
          },
          {
            key: 'token',
            label: 'Or connect with a token',
            hint: 'For machines without a browser. The token is shown only once.',
            value: tokenCommand,
          },
        ]"
        :key="item.key"
        ><div class="flex items-center justify-between">
          <FieldLabel>{{ item.label }}</FieldLabel
          ><Button size="xs" variant="ghost" @click="copy(item.key, item.value)"
            ><Check v-if="copied === item.key" data-icon="inline-start" /><Copy
              v-else
              data-icon="inline-start"
            />{{ copied === item.key ? "Copied" : "Copy" }}</Button
          >
        </div>
        <p class="text-xs text-muted-foreground">{{ item.hint }}</p>
        <pre
          class="min-w-0 max-w-full overflow-auto whitespace-pre-wrap break-all rounded-md bg-black p-3 font-mono text-xs leading-relaxed"
          >{{ item.value }}</pre>
      </Field>
      <ClientCredentialFields :credential="token" />
    </FieldGroup>
    <Alert v-if="error" variant="destructive">{{ error }}</Alert>
    <template #actions
      ><Button v-if="stage === 'edit' || stage === 'token'" @click="close"
        >Done</Button
      ><template v-else
        ><Button variant="outline" :disabled="busy" @click="close"
          >Cancel</Button
        ><Button
          :variant="stage === 'delete' ? 'destructive' : 'default'"
          :disabled="busy || (stage === 'create' && !name.trim())"
          :type="stage === 'create' ? 'submit' : 'button'"
          :form="stage === 'create' ? 'peer-form' : undefined"
          @click="stage !== 'create' && submit()"
          ><Spinner v-if="busy" data-icon="inline-start" />{{
            stage === "create"
              ? "Add peer"
              : stage === "rotate"
                ? "Rotate token"
                : "Delete peer"
          }}</Button
        ></template
      ></template
    >
  </BaseDialog>
</template>
