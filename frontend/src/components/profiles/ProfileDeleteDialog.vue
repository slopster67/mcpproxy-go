<template>
  <DeleteConfirmDialog
    :open="open"
    kind="profile"
    :name="profile.name"
    :busy="busy"
    :disabled="needsTarget && !leave && !target"
    @confirm="submit"
    @close="emit('close')"
  >
    <ProfileImpact
      v-if="inUse"
      :profile-name="profile.id"
      :used-by="usedBy"
      heading="This profile is used by:"
    />

    <div v-if="needsTarget" class="flex flex-col gap-1.5">
      <label v-if="targets.length"
        class="flex items-center justify-between gap-2 py-1 text-sm"
        for="profile-delete-target"
        ><span class="text-sm font-medium">Move them to</span></label
      >
      <NativeSelect
        v-if="targets.length"
        data-slot="native-select"
        id="profile-delete-target"
        v-model="target"
        class="w-full"
        :disabled="busy || leave"
      >
        <option value="" disabled>Choose a profile&hellip;</option>
        <option v-for="other in targets" :key="other.id" :value="other.id">
          {{ other.name }}
        </option>
      </NativeSelect>
      <p v-if="!targets.length" class="text-xs text-warning mt-1">
        There is no other profile to move them to.
      </p>
    </div>
    <label
      v-if="needsTarget"
      class="flex items-center justify-between gap-2 py-1 text-sm cursor-pointer justify-start"
    >
      <Checkbox
        data-slot="checkbox"
        v-model="leave"
        :disabled="busy"
        class="size-4"
      />
      <span class="text-sm font-medium"
        >Leave them without a profile (they will be denied all tools)</span
      >
    </label>
    <Alert
      v-if="error"
      role="alert"
      variant="destructive"
      >{{ error }}</Alert
    >
  </DeleteConfirmDialog>
</template>

<script setup lang="ts">
import { NativeSelect } from "@/components/ui/native-select";
import { Checkbox } from "@/components/ui/checkbox";
import { Alert } from "@/components/ui/alert";
import { computed, ref, watch } from "vue";
import DeleteConfirmDialog from "@/components/DeleteConfirmDialog.vue";
import ProfileImpact from "@/components/profiles/ProfileImpact.vue";
import { useProfilesStore } from "@/stores/profiles";
import { describeError } from "@/utils/profiles";
import type { ProfileUsedBy, ProfileView } from "@/types/api";
import type { ApiError } from "@/services/api";

// When anything points at the profile a "Move them
// to" target is REQUIRED (another profile: a locked client cannot hold an empty
// pin). `force` leaves pins dangling (deny-all); it is not offered for the
// anonymous_profile, which the server refuses to delete without a target.
const props = defineProps<{ open: boolean; profile: ProfileView }>();
const emit = defineEmits<{ (e: "close"): void; (e: "deleting"): void; (e: "delete-failed"): void; (e: "deleted"): void }>();
const profiles = useProfilesStore();
const target = ref("");
const leave = ref(false);
const busy = ref(false);
const apiError = ref<ApiError | null>(null);

watch(
  () => props.open,
  (open) => {
    if (!open) return;
    target.value = "";
    leave.value = false;
    busy.value = false;
    apiError.value = null;
    void profiles.ensureLoaded();
  },
  { immediate: true },
);

const usedBy = computed<ProfileUsedBy>(
  () =>
    props.profile.used_by ?? {
      clients: [],
    },
);
const referenced = computed(() =>
  profiles.items.some(
    (other) =>
      other.id !== props.profile.id &&
      other.switchable_to?.includes(props.profile.id),
  ),
);
const needsTarget = computed(() => usedBy.value.clients.length > 0);
const inUse = computed(() => needsTarget.value || referenced.value);
const targets = computed(() =>
  profiles.items.filter((other) => other.id !== props.profile.id),
);
const error = computed(() =>
  apiError.value ? describeError(apiError.value) : "",
);

async function submit() {
  if (busy.value || (needsTarget.value && !leave.value && !target.value))
    return;
  busy.value = true;
  apiError.value = null;
  emit("deleting");
  try {
    const opts: { reassign_to?: string; force?: boolean } = {};
    if (needsTarget.value) {
      if (leave.value) opts.force = true;
      else opts.reassign_to = target.value;
    }
    await profiles.remove(props.profile.id, opts);
    emit("deleted");
  } catch (err) {
    apiError.value = err as ApiError;
    emit("delete-failed");
  } finally {
    busy.value = false;
  }
}
</script>
