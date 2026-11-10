<template>
  <BaseDialog
    :open="open"
    :title="displayed.title || 'Client token'"
    wide
    @close="emit('close')"
  >
    <ClientCredentialFields
      :credential="displayed.credential"
      :snippet="displayed.snippet"
    />
    <template #actions
      ><Button @click="emit('close')"
        >Done</Button
      ></template
    >
  </BaseDialog>
</template>
<script setup lang="ts">
import { ref, watch } from "vue";
import BaseDialog from "@/components/BaseDialog.vue";
import { Button } from "@/components/ui/button";
import ClientCredentialFields from "./ClientCredentialFields.vue";
const props = defineProps<{
  open: boolean;
  credential: string;
  snippet?: { generic_http: string } | null;
  title?: string;
}>();
const emit = defineEmits<{ close: [] }>();
// Keep the content unchanged during the closing animation.
const displayed = ref({
  credential: props.credential,
  snippet: props.snippet,
  title: props.title,
});
watch(
  () => [props.open, props.credential, props.snippet, props.title],
  () => {
    if (props.open)
      displayed.value = {
        credential: props.credential,
        snippet: props.snippet,
        title: props.title,
      };
  },
);
</script>
