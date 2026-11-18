<script setup lang="ts">
import SegmentedControl from "@/components/SegmentedControl.vue";
import type { ServerConnectionLifetime } from "@/types";

defineProps<{ disabled?: boolean }>();
const mode = defineModel<ServerConnectionLifetime>({ required: true });
</script>

<template>
  <div class="flex flex-col items-start gap-3">
    <SegmentedControl
      aria-label="Keep running"
      :disabled="disabled"
      :model-value="mode === 'always' ? 'always' : 'on_demand'"
      :options="[{ value: 'always', label: 'Always' }, { value: 'on_demand', label: 'On demand' }]"
      @update:model-value="mode = $event === 'always' ? 'always' : 'shared'"
    />
    <div v-if="mode !== 'always'" class="flex flex-col items-start gap-1.5">
      <span class="text-sm text-muted-foreground">Spawn mode</span>
      <SegmentedControl
        aria-label="Spawn mode"
        :disabled="disabled"
        :model-value="mode"
        :options="[{ value: 'shared', label: 'Shared' }, { value: 'per_session', label: 'Per session' }]"
        @update:model-value="mode = $event"
      />
    </div>
  </div>
</template>
