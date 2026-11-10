<template>
  <FieldGroup class="min-w-0">
    <Field class="min-w-0"
      ><FieldLabel for="credential-once-secret">Token</FieldLabel
      ><InputGroup class="min-w-0"
        ><InputGroupInput
          id="credential-once-secret"
          readonly
          :value="credential"
          class="min-w-0 font-mono"
          @focus="
            ($event.target as HTMLInputElement).select()
          " /><InputGroupAddon align="inline-end"
          ><InputGroupButton
            :aria-label="copied === 'secret' ? 'Token copied' : 'Copy token'"
            @click="copy('secret', credential)"
            ><Check v-if="copied === 'secret'" /><Copy
              v-else /></InputGroupButton></InputGroupAddon></InputGroup
    ></Field>
    <Field v-if="snippet" class="min-w-0">
      <div class="flex items-center justify-between gap-3">
        <FieldLabel>Client config</FieldLabel
        ><Button
          variant="ghost"
          size="xs"
          @click="copy('snippet', formattedConfig)"
          ><Check v-if="copied === 'snippet'" data-icon="inline-start" /><Copy
            v-else
            data-icon="inline-start"
          />{{ copied === "snippet" ? "Copied" : "Copy config" }}</Button
        >
      </div>
      <JsonCode
        :data="parsedConfig"
        class="min-w-0 w-full"
      />
    </Field>
  </FieldGroup>
</template>
<script setup lang="ts">
import { copyText } from "@/utils/clipboard";
import { computed, ref } from "vue";
import { Check, Copy } from "@lucide/vue";
import JsonCode from "@/components/JsonCode.vue";
import { Button } from "@/components/ui/button";
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field";
import {
  InputGroup,
  InputGroupAddon,
  InputGroupButton,
  InputGroupInput,
} from "@/components/ui/input-group";
const props = defineProps<{
  credential: string;
  snippet?: { generic_http: string } | null;
}>();
const copied = ref("");
const parsedConfig = computed(() => {
  try {
    return JSON.parse(props.snippet?.generic_http ?? "{}");
  } catch {
    return props.snippet?.generic_http ?? "";
  }
});
const formattedConfig = computed(() =>
  typeof parsedConfig.value === "string"
    ? parsedConfig.value
    : JSON.stringify(parsedConfig.value, null, 2),
);
async function copy(kind: string, value: string) {
  try {
    await copyText(value);
    copied.value = kind;
    setTimeout(() => {
      if (copied.value === kind) copied.value = "";
    }, 2000);
  } catch {
    copied.value = "";
  }
}
</script>
