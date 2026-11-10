<template>
  <Alert
    variant="destructive"
    class="flex items-center gap-3 border-destructive/30"
    role="alert"
  >
    <span class="shrink-0" aria-hidden="true"><KeyRound class="size-4" /></span>
    <span class="flex-1 text-sm font-medium">OAuth sign-in required</span>
    <Button
      size="sm"
      variant="outline"
      type="button"
      :disabled="loading"
      @click="$emit('login')"
    >
      <Spinner v-if="loading" data-icon="inline-start" />
      Sign in
    </Button>
  </Alert>
</template>

<script setup lang="ts">
import { KeyRound } from "@lucide/vue";
import { Alert } from "@/components/ui/alert";
import { Spinner } from "@/components/ui/spinner";
import { Button } from "@/components/ui/button";

defineProps<{ loading?: boolean }>();
defineEmits<{ login: [] }>();
</script>
