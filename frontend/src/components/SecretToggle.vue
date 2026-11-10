<template>
  <InputGroup :data-disabled="disabled">
    <InputGroupInput :id="inputId" :aria-label="inputLabel || `${kind === 'env' ? 'Environment variable' : 'Header'} ${name} value`" :type="masked ? 'password' : 'text'" class="font-mono" :model-value="modelValue" placeholder="Value" :disabled="disabled" autocomplete="off" spellcheck="false" data-1p-ignore data-lpignore="true" @update:model-value="emit('update:modelValue', String($event))" />
    <InputGroupAddon align="inline-end">
      <InputGroupButton v-if="sensitive" size="icon-xs" :aria-pressed="revealed" :aria-label="`${revealed ? 'Hide' : 'Show'} ${name} value`" :title="revealed ? 'Hide value' : 'Show value'" :disabled="disabled" @click="revealed = !revealed"><EyeOff data-icon="inline-start" v-if="revealed" /><Eye data-icon="inline-start" v-else /></InputGroupButton>
      <span class="inline-flex" :title="storageTitle">
        <InputGroupButton size="icon-xs" :variant="mode === 'secret' ? 'secondary' : 'ghost'" :aria-pressed="mode === 'secret'" :aria-label="`Store ${name} in secret`" :disabled="disabled" @click="setMode(mode === 'secret' ? 'value' : 'secret')"><LockKeyhole data-icon="inline-start" v-if="mode === 'secret'" /><LockOpen data-icon="inline-start" v-else /></InputGroupButton>
      </span>
    </InputGroupAddon>
  </InputGroup>
</template>

<script setup lang="ts">
import { Eye, EyeOff, LockKeyhole, LockOpen } from "@lucide/vue";
import { InputGroup, InputGroupInput, InputGroupAddon, InputGroupButton } from "@/components/ui/input-group";
import { computed, ref, useId } from "vue";
import { looksSecret } from "@/utils/secretLike";

// Compact value, reveal and secret-storage controls for an env/header row.
// The parent owns the value and storage mode; this component owns reveal state.
interface Props {
  name: string;
  kind: "env" | "header";
  modelValue: string;
  mode: "value" | "secret";
  disabled?: boolean;
  // Row-unique accessible name for the value input (e.g. "Header 2 value"),
  // so duplicate or empty names never leave two inputs indistinguishable.
  inputLabel?: string;
}
const props = defineProps<Props>();
const inputId = `secret-toggle-${useId()}`;
const storageTitle = computed(() => props.mode === "secret"
  ? "Stored encrypted. Click to store in configuration instead."
  : "Stored in configuration. Click to store encrypted instead.");
const emit = defineEmits<{
  "update:modelValue": [value: string];
  "update:mode": [mode: "value" | "secret"];
}>();

// Masking is presentation only a field in
// Secret mode, or whose name looks secret-like (the same D13 rule that picks
// the Secret default), is a password input with a Show/Hide toggle. Value vs
// Secret still decides storage, so Value mode keeps storing plain config.
const revealed = ref(false);
const sensitive = computed(
  () => props.mode === "secret" || looksSecret(props.name),
);
const masked = computed(() => sensitive.value && !revealed.value);

function setMode(mode: "value" | "secret") {
  if (props.disabled) return;
  emit("update:mode", mode);
}

</script>
