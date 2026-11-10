<template>
  <BaseDialog
    :open="open"
    :title="`Rename ${profile.name}`"
    @close="!busy && emit('close')"
  >
    <form id="rename-profile-form" @submit.prevent="submit">
      <FieldGroup
        ><Field :data-invalid="!!fieldError"
          ><FieldLabel for="profile-new-name">Name</FieldLabel
          ><Input
            id="profile-new-name"
            v-model.trim="newName"
            :disabled="busy"
            :aria-invalid="!!fieldError"
            autocomplete="off"
          /><FieldError v-if="fieldError">{{
            fieldError
          }}</FieldError></Field
        ></FieldGroup
      >
    </form>
    <Alert v-if="otherError" variant="destructive">{{
      otherError
    }}</Alert>
    <template #actions
      ><Button variant="outline" :disabled="busy" @click="emit('close')"
        >Cancel</Button
      ><Button
        type="submit"
        form="rename-profile-form"
        :disabled="busy || !newName || newName === profile.name"
        ><Spinner v-if="busy" data-icon="inline-start" />Rename</Button
      ></template
    >
  </BaseDialog>
</template>
<script setup lang="ts">
import {
  Field,
  FieldError,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field";
import { Spinner } from "@/components/ui/spinner";
import { Input } from "@/components/ui/input";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { computed, ref, watch } from "vue";
import BaseDialog from "@/components/BaseDialog.vue";
import { useProfilesStore } from "@/stores/profiles";
import { describeError } from "@/utils/profiles";
import type { ProfileView } from "@/types/api";
import type { ApiError } from "@/services/api";

const props = defineProps<{ open: boolean; profile: ProfileView }>();
const emit = defineEmits<{
  (e: "close"): void;
  (e: "renamed"): void;
  (e: "renaming"): void;
  (e: "rename-failed"): void;
}>();
const newName = ref("");
const busy = ref(false);
const error = ref<ApiError | null>(null);

watch(
  () => props.open,
  (open) => {
    if (!open) return;
    newName.value = props.profile.name;
    error.value = null;
    busy.value = false;
  },
  { immediate: true },
);

const fieldError = computed(() =>
  error.value?.field === "new_name" || error.value?.field === "name"
    ? error.value.message
    : "",
);
const otherError = computed(() =>
  error.value && !fieldError.value
    ? describeError(error.value)
    : "",
);

const profiles = useProfilesStore();
async function submit() {
  busy.value = true;
  error.value = null;
  // Hold background reloads until the rename response arrives.
  emit("renaming");
  try {
    await profiles.patch(props.profile.id, { name: newName.value });
    emit("renamed");
  } catch (err) {
    error.value = err as ApiError;
    emit("rename-failed");
  } finally {
    busy.value = false;
  }
}
</script>
