<template>
  <BaseDialog
    :open="open"
    :title="
      title ?? `Tool access${subjectLabel ? ' — ' + subjectLabel : ''}`
    "
    @close="emit('close')"
  >
    <p v-if="note" class="text-sm opacity-80">
      {{ note }}
    </p>
    <form class="flex flex-wrap items-end gap-2" @submit.prevent="run">
      <div class="flex flex-col gap-1.5 flex-1 min-w-[14rem]">
        <label
          class="flex items-center justify-between gap-2 py-1 text-sm"
          for="explain-tool"
          ><span class="text-sm font-medium">Tool</span></label
        >
        <Input
          id="explain-tool"
          v-model.trim="toolInput"
          list="explain-tool-options"
          class="w-full font-mono"
          placeholder="server:tool"
          autocomplete="off"
        />
        <datalist id="explain-tool-options">
          <option v-for="name in toolNames" :key="name" :value="name" />
        </datalist>
      </div>
      <Button
        variant="outline"
        type="submit"
        :disabled="busy || !toolInput"
      >
        <Spinner data-icon="inline-start" v-if="busy" />
        Explain
      </Button>
    </form>

    <div
      v-if="busy && !result"
      class="flex flex-col gap-2"
      aria-busy="true"
      v-show="showSkeleton"
    >
      <Skeleton class="h-20 w-full" />
    </div>
    <Alert
      v-if="error"
      role="alert"
      variant="destructive"
      >{{ error }}</Alert
    >

    <div
      v-if="result"
      class="flex flex-col gap-3"
      aria-live="polite"
    >
      <Alert :variant="result.available ? 'default' : 'destructive'">
        <component :is="result.available ? CircleCheck : CircleX" aria-hidden="true" />
        <AlertTitle>{{ result.available ? 'Available' : 'Unavailable' }}</AlertTitle>
        <AlertDescription v-if="result.issues.length">
          <p v-if="result.issues.length === 1">{{ result.issues[0]!.message }}</p>
          <ul v-else class="list-disc space-y-1 pl-4">
            <li v-for="issue in result.issues" :key="issue.code">{{ issue.message }}</li>
          </ul>
        </AlertDescription>
      </Alert>
    </div>
    <template #actions>
      <Button
        size="sm"
        variant="ghost"
        type="button"
        @click="emit('close')"
        >Close</Button
      >
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { Spinner } from "@/components/ui/spinner";
import { useDelayedLoading } from "@/composables/useDelayedLoading";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Alert, AlertTitle, AlertDescription } from "@/components/ui/alert";
import { CircleCheck, CircleX } from "@lucide/vue";
import { computed, ref, watch } from "vue";
import BaseDialog from "@/components/BaseDialog.vue";
import api from "@/services/api";
import { useProfilesStore } from "@/stores/profiles";
import { useClientsStore } from "@/stores/clients";
import { describeError } from "@/utils/profiles";
import type { AccessExplanation, ExplainSubjectQuery } from "@/types/api";

// Explain current tool availability for one client or profile and tool.
const props = defineProps<{
  open: boolean;
  subject: {
    kind: "client" | "profile";
    name?: string;
  };
  tool?: string;
  // An entry point (a blocked Activity row) can retitle the
  // dialog and say what the verdict is evaluated against; the explainer is live,
  // never a replay of the moment of the call.
  title?: string;
  note?: string;
}>();
const emit = defineEmits<{ (e: "close"): void }>();
const profiles = useProfilesStore();
const clients = useClientsStore();

const toolInput = ref("");
const toolNames = ref<string[]>([]);
const busy = ref(false);
const error = ref("");
const result = ref<AccessExplanation | null>(null);
const showSkeleton = useDelayedLoading(() => busy.value && !result.value);

function subjectName(subject: { kind: string; name?: string }) {
  return subject.kind === 'client' ? clients.labelFor(subject.name) : profiles.labelFor(subject.name);
}
const subjectLabel = computed(() => subjectName(props.subject));
// Every explain takes a ticket and only the latest applies: closing and
// reopening the still-mounted dialog during a pending request must not let the
// old answer repopulate the reset dialog.
let runTicket = 0;

watch(
  () => props.open,
  (open) => {
    runTicket++;
    busy.value = false;
    if (!open) return;
    toolInput.value = props.tool ?? "";
    result.value = null;
    error.value = "";
    void clients.ensureLoaded();
    void profiles.ensureLoaded();
    void loadTools();
    if (props.tool) void run();
  },
);

async function loadTools() {
  try {
    const { tools } = await api.tools.list();
    toolNames.value = (tools ?? [])
      .map((tool) => `${tool.server_name}:${tool.name}`)
      .sort();
  } catch {
    toolNames.value = [];
  }
}

async function run() {
  if (!toolInput.value) return;
  const ticket = ++runTicket;
  busy.value = true;
  error.value = "";
  const query: ExplainSubjectQuery = { tool: toolInput.value };
  if (props.subject.kind === "client") query.client = props.subject.name;
  else if (props.subject.kind === "profile") query.profile = props.subject.name;
  try {
    const explained = await api.access.explain(query);
    if (ticket !== runTicket) return;
    result.value = explained;
  } catch (err) {
    if (ticket !== runTicket) return;
    result.value = null;
    error.value = describeError(err);
  } finally {
    if (ticket === runTicket) busy.value = false;
  }
}

</script>
