<template>
  <BaseDialog
    :open="open"
    title="Add profile"
    @close="close"
  >
    <form id="create-profile-form" @submit.prevent="submit">
      <FieldGroup>
        <Field :data-invalid="!!fieldError"
          ><FieldLabel for="profile-create-name">Name</FieldLabel
          ><Input
            id="profile-create-name"
            v-model.trim="name"
            placeholder="work"
            autocomplete="off"
            :aria-invalid="!!fieldError"
            autofocus
          /><FieldError v-if="fieldError">{{ fieldError }}</FieldError></Field
        >
      </FieldGroup>
    </form>
    <Alert v-if="error" variant="destructive">{{ error }}</Alert>
    <template #actions
      ><Button variant="outline" :disabled="busy" @click="close">Cancel</Button
      ><Button
        type="submit"
        form="create-profile-form"
        :disabled="busy || !name"
        ><Spinner v-if="busy" data-icon="inline-start" />Add profile</Button
      ></template
    >
  </BaseDialog>
</template>
<script setup lang="ts">
import { ref, watch } from "vue";
import BaseDialog from "@/components/BaseDialog.vue";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Field,
  FieldError,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import type { ApiError } from "@/services/api";
import { useProfilesStore } from "@/stores/profiles";
import { describeError } from "@/utils/profiles";
const props = defineProps<{ open: boolean }>(),
  emit = defineEmits<{ close: []; created: [id: string] }>();
const name = ref(""),
  busy = ref(false),
  error = ref(""),
  fieldError = ref("");
watch(
  () => props.open,
  (open) => {
    if (open) {
      name.value = "";
      error.value = "";
      fieldError.value = "";
    }
  },
  { immediate: true },
);
function close() {
  if (!busy.value) emit("close");
}
const profiles = useProfilesStore();
async function submit() {
  if (busy.value || !name.value) return;
  busy.value = true;
  error.value = "";
  fieldError.value = "";
  try {
    const profile = await profiles.create({
      name: name.value,
      servers: [],
    });
    emit("created", profile.id);
  } catch (err) {
    if ((err as ApiError).field === "name")
      fieldError.value = describeError(err);
    else error.value = describeError(err);
  } finally {
    busy.value = false;
  }
}
</script>
