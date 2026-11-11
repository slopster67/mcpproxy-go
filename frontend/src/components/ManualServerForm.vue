<template>
  <form @submit.prevent="handleSubmit">
    <FieldGroup>
      <Field :data-disabled="submitting">
        <FieldLabel :for="ids.name">Name</FieldLabel>
        <Input :id="ids.name" v-model="name" required autocomplete="off" :disabled="submitting" />
      </Field>
      <Field :data-disabled="submitting">
        <FieldLabel :for="ids.description">Description</FieldLabel>
        <Input :id="ids.description" v-model="description" maxlength="100" autocomplete="off" placeholder="What agents use it for (optional)" :disabled="submitting" />
      </Field>
      <Field v-if="peers.hasPeers"><FieldLabel>Run on</FieldLabel><HostSelect v-model="host" :disabled="submitting" /></Field>
      <Field v-if="!host && (peers.hasPeers || shared)" orientation="horizontal"><FieldLabel for="server-share">Available to peers</FieldLabel><Switch id="server-share" v-model="shared" :disabled="submitting" /></Field>
      <Field :data-disabled="submitting">
        <FieldLabel :id="ids.type">Server type</FieldLabel>
        <ToggleGroup type="single" variant="outline" :spacing="2" :model-value="protocol" :aria-labelledby="ids.type" :disabled="submitting" @update:model-value="value => { if (value === 'stdio' || value === 'http') protocol = value }">
          <ToggleGroupItem value="stdio" class="flex-1"><SquareTerminal data-icon="inline-start" />stdio</ToggleGroupItem>
          <ToggleGroupItem value="http" class="flex-1"><Globe data-icon="inline-start" />Remote</ToggleGroupItem>
        </ToggleGroup>
      </Field>
      <Field v-if="protocol === 'http'" :data-disabled="submitting">
        <FieldLabel :for="ids.url">URL</FieldLabel>
        <Input :id="ids.url" v-model="url" type="url" placeholder="https://api.example.com/mcp" class="font-mono" required :disabled="submitting" />
      </Field>
      <template v-else>
        <Field :data-disabled="submitting">
          <FieldLabel :for="ids.command">Command</FieldLabel>
          <Input :id="ids.command" v-model="command" placeholder="npx" class="font-mono" required :disabled="submitting" />
        </Field>
        <Field :data-disabled="submitting">
          <FieldLabel>Arguments</FieldLabel>
          <ArgumentFields v-model="args" :disabled="submitting" />
        </Field>
      </template>
      <FieldSet :disabled="submitting">
        <FieldLegend variant="label">{{ protocol === 'http' ? 'Headers' : 'Environment variables' }}</FieldLegend>
        <FieldGroup class="gap-3">
          <div v-for="(row, index) in activeRows" :key="row.id" class="grid grid-cols-[minmax(0,1fr)_2rem] items-start gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,2fr)_2rem]">
            <Input v-model="row.name" class="col-start-1 row-start-1 font-mono" :placeholder="activeKind === 'header' ? 'Header name' : 'VAR_NAME'" :aria-label="`${fieldLabel} name ${index + 1}`" :disabled="submitting" />
            <SecretToggle class="col-span-2 row-start-2 sm:col-span-1 sm:col-start-2 sm:row-start-1" :name="row.name || `${activeKind}-${index}`" :kind="activeKind" :input-label="`${fieldLabel} ${index + 1} value`" v-model="row.value" v-model:mode="row.mode" :disabled="submitting" />
            <Button type="button" size="icon" variant="ghost" class="col-start-2 row-start-1 sm:col-start-3" :aria-label="`Remove ${fieldLabel.toLowerCase()} ${index + 1}`" :disabled="submitting" @click="activeRows.splice(index, 1)"><X data-icon="inline-start" /></Button>
          </div>
          <Button type="button" size="sm" variant="outline" class="self-start" :disabled="submitting" @click="activeRows.push(newRow())"><Plus data-icon="inline-start" />{{ activeKind === 'header' ? 'Add header' : 'Add variable' }}</Button>
        </FieldGroup>
      </FieldSet>
      <Field v-if="props.allowTrustModeSelection" orientation="horizontal" :data-disabled="submitting">
        <FieldLabel :for="ids.trust">Trust all tools</FieldLabel>
        <Switch :id="ids.trust" :model-value="trustMode === 'auto'" :disabled="submitting" @update:model-value="trustMode = $event ? 'auto' : 'manual'" />
      </Field>
      <FieldError v-if="error" ref="errorEl" tabindex="-1" role="alert">{{ error }}</FieldError>
      <div class="flex justify-end gap-2">
        <Button type="button" variant="outline" :disabled="submitting" @click="emit('cancel')">Cancel</Button>
        <Button variant="default" type="submit" :disabled="submitting"><Spinner v-if="submitting" data-icon="inline-start" />{{ submitting ? 'Adding…' : 'Add server' }}</Button>
      </div>
    </FieldGroup>
  </form>
</template>

<script setup lang="ts">
import { usePeersStore } from "@/stores/peers";
import HostSelect from "@/components/HostSelect.vue";
import { Globe, SquareTerminal, Plus, X } from "@lucide/vue";
import { Field, FieldGroup, FieldLabel, FieldSet, FieldLegend, FieldError } from "@/components/ui/field";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import ArgumentFields from "@/components/ArgumentFields.vue";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { ref, reactive, onMounted, nextTick, useId, watch, computed } from "vue";
import { Spinner } from "@/components/ui/spinner";
import SecretToggle from "@/components/SecretToggle.vue";
import { Switch } from "@/components/ui/switch";
import {
  resolveSecretFields,
  rollbackSecrets,
} from "@/composables/useSecretFields";
import { useServersStore } from "@/stores/servers";
import type { TrustMode } from "@/utils/trustMode";

const peers = usePeersStore();
onMounted(peers.ensureLoaded);
const props = withDefaults(
  defineProps<{
    allowTrustModeSelection?: boolean;
  }>(),
  {
    allowTrustModeSelection: true,
  },
);
const emit = defineEmits<{ added: [name: string]; cancel: []; busy: [value: boolean] }>();

const serversStore = useServersStore();

// Keep labels and radio groups unique to this form instance.
const uid = useId();
const ids = ref({
  name: `${uid}-name`,
  description: `${uid}-description`,
  url: `${uid}-url`,
  command: `${uid}-command`,
  type: `${uid}-type`,
  fields: `${uid}-fields`,
  trust: `${uid}-trust`,
});
const errorEl = ref<InstanceType<typeof FieldError> | null>(null);

const name = ref("");
const description = ref("");
const host = ref("");
const shared = ref(false);
const protocol = ref<"stdio" | "http">("stdio");
const trustMode = ref<TrustMode>("manual");
const url = ref("");
const command = ref("");
const args = ref<string[]>([]);
// Each row carries a stable `id` so :key survives removal of an earlier row;
// keying by index would hand a removed row's SecretToggle (and its revealed
// state) to the next row (review F3.1).
type KVRow = {
  id: number;
  name: string;
  value: string;
  mode: "value" | "secret";
};
let rowSeq = 0;
const newRow = (): KVRow => ({
  id: ++rowSeq,
  name: "",
  value: "",
  mode: "value",
});
const headerRows = reactive<KVRow[]>([]);
const envRows = reactive<KVRow[]>([]);
const activeKind = computed<"header" | "env">(() => protocol.value === "http" ? "header" : "env");
const activeRows = computed(() => protocol.value === "http" ? headerRows : envRows);
const fieldLabel = computed(() => protocol.value === "http" ? "Header" : "Environment variable");
const error = ref<string | null>(null);
const submitting = ref(false);
watch(submitting, (value) => emit("busy", value), { flush: "sync" });
async function handleSubmit() {
  if (submitting.value) return;
  error.value = null;
  submitting.value = true;
  // Populated only once resolveSecretFields has returned successfully, so
  // the catch block below never double-rolls-back refs that
  // resolveSecretFields already rolled back itself on a write failure.
  let writtenRefs: string[] = [];
  try {
    const fields = activeRows.value.filter((row) => row.name.trim()).map((row) => ({
      kind: activeKind.value,
      name: row.name.trim(),
      value: row.value,
      mode: row.mode,
    }));
    const resolved = await resolveSecretFields(name.value, fields);
    writtenRefs = resolved.writtenRefs;

    const serverData: Record<string, unknown> = {
      host_id:host.value,
      available_to_peers: !host.value && shared.value,
      name: name.value,
      protocol: protocol.value,
      enabled: true,
      trust_mode: trustMode.value,
    };
    if (description.value.trim()) serverData.description = description.value.trim();
    if (protocol.value === "http") {
      serverData.url = url.value;
      if (Object.keys(resolved.headers).length > 0)
        serverData.headers = resolved.headers;
    } else {
      serverData.command = command.value;
      if (args.value.length > 0) serverData.args = args.value;
      if (Object.keys(resolved.env).length > 0)
        serverData.env = resolved.env;
    }

    await serversStore.create(serverData);
    emit("added", name.value);
  } catch (e) {
    // The secret write (if any) succeeded but something after it failed —
    // don't leave an orphaned secret entry behind, and let a retry reuse
    // the same ref name instead of computing a new -2-suffixed one.
    if (writtenRefs.length > 0) await rollbackSecrets(writtenRefs);
    error.value = e instanceof Error ? e.message : "Failed to add server";
    // Move focus to the failure so keyboard and screen-reader users hear it.
    await nextTick();
    (errorEl.value?.$el as HTMLElement | undefined)?.focus();
  } finally {
    submitting.value = false;
  }
}
</script>
