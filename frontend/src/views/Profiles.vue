<template>
  <div class="flex flex-col gap-6">
    <div class="flex min-h-9 items-center justify-between gap-3">
      <h1 class="text-2xl font-medium tracking-tight">Profiles</h1>
      <Button
        variant="outline"
        @click="createOpen = true"
        ><Plus data-icon="inline-start" />Add profile</Button
      >
    </div>
    <Alert v-if="store.error && !store.loaded" variant="destructive"
      >{{ store.error.message
      }}<Button variant="outline" size="sm" @click="store.ensureLoaded()"
        >Retry</Button
      ></Alert
    >
    <div
      v-if="!store.loaded && !store.error"
      v-show="showSkeleton"
      class="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4"
      aria-busy="true"
    >
      <Card v-for="n in 4" :key="n" size="sm"
        ><CardHeader
          ><Skeleton class="h-5 w-32" /><Skeleton
            class="h-4 w-40" /></CardHeader
      ></Card>
    </div>
    <div
      v-else
      class="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4"
    >
      <ProfileCard
        v-for="profile in store.items"
        :key="profile.id"
        :profile="profile"
      />
      <Card size="sm" class="relative justify-center border border-dashed bg-transparent shadow-none ring-0">
        <CardHeader>
          <button type="button" class="flex h-10 w-full cursor-pointer items-center justify-center gap-2 text-sm text-muted-foreground after:absolute after:inset-0 after:rounded-4xl hover:text-foreground focus-visible:outline-none focus-visible:after:ring-2 focus-visible:after:ring-ring" @click="createOpen = true">
            <Plus class="size-4" />Add a profile
          </button>
        </CardHeader>
      </Card>
    </div>
    <ProfileCreateDialog
      :open="createOpen"
      @close="createOpen = false"
      @created="created"
    />
  </div>
</template>
<script setup lang="ts">
import { onMounted, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { Plus } from "@lucide/vue";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardHeader } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import ProfileCard from "@/components/profiles/ProfileCard.vue";
import ProfileCreateDialog from "@/components/profiles/ProfileCreateDialog.vue";
import { useDelayedLoading } from "@/composables/useDelayedLoading";
import { useProfilesStore } from "@/stores/profiles";
const store = useProfilesStore(),
  route = useRoute(),
  router = useRouter();
const createOpen = ref(false);
const showSkeleton = useDelayedLoading(() => !store.loaded && !store.error);
function created(id: string) {
  createOpen.value = false;
  void router.push({
    name: "profile-editor",
    params: { id },
  });
}
watch(
  () => route.query.create,
  (value) => {
    if (value === "1") {
      createOpen.value = true;
      const query = { ...route.query };
      delete query.create;
      void router.replace({ query });
    }
  },
  { immediate: true },
);
onMounted(() => {
  void store.ensureLoaded();
});
</script>
