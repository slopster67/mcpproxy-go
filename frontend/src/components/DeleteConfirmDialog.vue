<template>
  <BaseDialog
    :open="open"
    :title="`Delete ${displayed.kind}`"
    @close="close"
  >
    <DeleteConfirmationText
      :name="displayed.name"
      :location="displayed.location"
    />
    <slot />
    <template #actions>
      <Button variant="outline" :disabled="busy" @click="close">Cancel</Button>
      <Button
        variant="destructive"
        :disabled="busy || disabled"
        @click="emit('confirm')"
        ><Spinner v-if="busy" data-icon="inline-start" />Delete
        {{ displayed.kind }}</Button
      >
    </template>
  </BaseDialog>
</template>
<script setup lang="ts">
import { ref, watch } from "vue";
import BaseDialog from "@/components/BaseDialog.vue";
import DeleteConfirmationText from "@/components/DeleteConfirmationText.vue";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
const props = withDefaults(
  defineProps<{
    open: boolean;
    kind: string;
    name: string;
    location?: string;
    busy?: boolean;
    disabled?: boolean;
  }>(),
  { location: "configuration" },
);
const emit = defineEmits<{ close: []; confirm: [] }>();
const displayed = ref({
  kind: props.kind,
  name: props.name,
  location: props.location,
});
watch(
  () => [props.open, props.kind, props.name, props.location],
  () => {
    if (props.open)
      displayed.value = {
        kind: props.kind,
        name: props.name,
        location: props.location,
      };
  },
);
function close() {
  if (!props.busy) emit("close");
}
</script>
