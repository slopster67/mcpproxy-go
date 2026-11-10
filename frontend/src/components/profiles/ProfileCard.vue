<template>
  <Card size="sm" class="relative">
    <CardHeader>
      <CardTitle class="min-w-0 pr-2"
        ><RouterLink
          :to="profileEditorLink(profile.id)"
          class="block truncate after:absolute after:inset-0 after:rounded-4xl focus-visible:outline-none focus-visible:after:ring-2 focus-visible:after:ring-ring"
          :title="profile.name"
          >{{ profile.name }}</RouterLink
        ></CardTitle
      >
      <CardDescription
        >{{ serverCount }} {{ serverCount === 1 ? "server" : "servers"
        }}<template v-if="profile.used_by">
          · {{ profile.used_by.clients.length }}
          {{
            profile.used_by.clients.length === 1 ? "client" : "clients"
          }}</template
        ></CardDescription
      >
    </CardHeader>
  </Card>
</template>
<script setup lang="ts">
import { computed } from "vue";
import {
  Card,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { profileEditorLink } from "@/utils/profileRoute";
import type { ProfileView } from "@/types/api";
const props = defineProps<{ profile: ProfileView }>();
const serverCount = computed(
  () => (props.profile.effective_servers ?? props.profile.servers).length,
);
</script>
