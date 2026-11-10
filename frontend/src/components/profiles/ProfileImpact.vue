<template>
  <div class="text-sm flex flex-col gap-1">
    <p class="font-medium">{{ heading }}</p>
    <ul class="list-disc list-inside">
      <li
        v-for="client in usedBy.clients"
        :key="`c-${client.id}`"
      >
        {{ clientName(client.id) }}
      </li>

      <li
        v-for="other in referencedBy"
        :key="`s-${other}`"
      >
        {{ profiles.labelFor(other) }} · switching
      </li>
    </ul>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted } from "vue";
import { useClientsStore } from "@/stores/clients";
import { useProfilesStore } from "@/stores/profiles";
import type { ProfileUsedBy } from "@/types/api";

// What a rename or a delete touches, from the profile's used_by
// (administrators only) and from the other profiles' switchable_to lists.
const props = defineProps<{
  profileName: string;
  usedBy: ProfileUsedBy;
  heading: string;
}>();
const profiles = useProfilesStore();
const clients = useClientsStore();
const referencedBy = computed(() =>
  profiles.items
    .filter(
      (profile) =>
        profile.id !== props.profileName &&
        profile.switchable_to?.includes(props.profileName),
    )
    .map((profile) => profile.id),
);
onMounted(() => void clients.ensureLoaded());
function clientName(id: string): string {
  return clients.labelFor(id, id);
}
</script>
