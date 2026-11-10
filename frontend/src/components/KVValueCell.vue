<template>
  <div v-if="isEditing" class="flex min-w-0 flex-col gap-2 @lg:flex-row @lg:items-center">
    <Input
      v-model="draftValue"
      type="text"
      class="w-full min-w-0 flex-1 font-mono"
      :placeholder="placeholder"
      ref="editInput"
      @keyup.enter="$emit('save', draftValue)"
      @keyup.esc="$emit('cancel-edit')"
    />
    <div class="flex shrink-0 items-center justify-end gap-2">
    <Button
      size="xs"
      variant="default"

      :disabled="busy || !draftValue"
      @click="$emit('save', draftValue)"
      >Save</Button
    >
    <Button
      size="xs"
      variant="outline"

      :disabled="busy"
      @click="$emit('cancel-edit')"
      >Cancel</Button
    >
    </div>
  </div>
  <div v-else class="grid min-w-0 gap-2 @lg:grid-cols-[minmax(0,1fr)_auto] @lg:items-center">
    <div class="flex min-w-0 flex-wrap items-center gap-2">
      <component :is="isCompound ? 'code' : 'span'" v-if="hasReferences" class="min-w-0 max-w-full whitespace-pre-wrap break-all font-mono text-xs" :class="[scope === 'header' ? 'text-muted-foreground' : 'text-foreground', isCompound && 'bg-background px-1.5 py-0.5 rounded']">
        <template v-for="(part, index) in valueParts" :key="index">
          <span v-if="part.kind === 'text'">{{ part.text }}</span>
          <Badge v-else variant="secondary" class="h-auto min-w-0 max-w-full whitespace-normal align-middle border-transparent text-xs gap-1" :class="part.kind === 'secret' ? 'bg-info/10 text-info' : 'bg-warning/10 text-warning'" :title="part.reference">
            <LockKeyhole v-if="part.kind === 'secret'" class="size-3 shrink-0" aria-hidden="true" />
            <DollarSign v-else class="size-3 shrink-0" aria-hidden="true" />
            <span class="min-w-0 whitespace-normal break-all font-mono text-xs">{{ part.name }}</span>
          </Badge>
        </template>
      </component>
      <template v-else>
        <code
          class="bg-background px-1.5 py-0.5 rounded text-xs font-mono break-all"
          :class="scope === 'header' ? 'text-muted-foreground' : 'text-foreground'"
          >{{ rawValue || '(empty)' }}</code
        >
      </template>
    </div>
    <div class="flex items-center justify-end gap-1.5">
      <Button v-if="!hasReferences" size="xs" variant="outline" title="Convert to secret" aria-label="Convert to secret" @click="$emit('convert')" :disabled="busy">
        <LockKeyhole data-icon="inline-start" />Convert
      </Button>
      <Button size="xs" variant="outline" title="Edit" @click="$emit('start-edit')" :disabled="busy">
        <Pencil data-icon="inline-start" />Edit
      </Button>
      <Button size="xs" variant="outline" class="text-destructive hover:text-destructive" title="Remove" @click="$emit('delete')" :disabled="busy">
        <Trash2 data-icon="inline-start" />Remove
      </Button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { Input } from '@/components/ui/input';
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { DollarSign, LockKeyhole, Pencil, Trash2 } from "@lucide/vue";
import { computed, nextTick, ref, watch } from "vue";

// KVValueCell encapsulates the per-row UI for the Headers and Environment
// Variables cards on ServerDetail. It owns the visual state (edit-in-
// progress draft) and emits intent events; the parent owns the
// persistence (PATCH the server, call config-to-secret, refresh).

const props = defineProps<{
  scope: "header" | "env";
  k: string;
  rawValue: string;
  isEditing: boolean;
  busy?: boolean;
}>();

const emit = defineEmits<{
  (e: "start-edit"): void;
  (e: "cancel-edit"): void;
  (e: "save", value: string): void;
  (e: "delete"): void;
  (e: "convert"): void;
}>();

void emit;

const draftValue = ref(props.rawValue);
const editInput = ref<HTMLInputElement | null>(null);

// Keep the draft in sync with the parent's raw value when entering edit
// mode. We intentionally do NOT mirror raw → draft outside edit mode so
// the user's in-progress text never gets clobbered by a background SSE
// refresh.
watch(
  () => [props.isEditing, props.rawValue] as const,
  ([editing, raw]) => {
    if (editing) {
      draftValue.value = raw;
      nextTick(() => editInput.value?.focus());
    }
  },
  { immediate: true },
);

type ValuePart =
  | { kind: "text"; text: string }
  | { kind: "secret" | "env"; name: string; reference: string };

const valueParts = computed<ValuePart[]>(() => {
  const value = props.rawValue ?? "";
  const parts: ValuePart[] = [];
  let cursor = 0;
  for (const match of value.matchAll(/\$\{(secret|env):([^}]+)\}/g)) {
    if (match.index > cursor) parts.push({ kind: "text", text: value.slice(cursor, match.index) });
    parts.push({ kind: match[1] as "secret" | "env", name: match[2]!, reference: match[0] });
    cursor = match.index + match[0].length;
  }
  if (cursor < value.length) parts.push({ kind: "text", text: value.slice(cursor) });
  return parts;
});
const hasReferences = computed(() => valueParts.value.some(part => part.kind !== "text"));
const isCompound = computed(() => valueParts.value.length > 1);

const placeholder = computed(() =>
  props.scope === "header"
    ? "value (e.g. Bearer abc...)"
    : "value (literal or ${secret:name})",
);
</script>
