<script setup lang="ts">
import { onMounted } from "vue";
import { NativeSelect } from "@/components/ui/native-select";
import { usePeersStore } from "@/stores/peers";
const props = defineProps<{ modelValue: string; disabled?: boolean }>();
const emit = defineEmits<{ "update:modelValue": [value: string] }>();
const peers = usePeersStore();
onMounted(peers.ensureLoaded);
</script>
<template>
  <NativeSelect
    :model-value="props.modelValue"
    :disabled="disabled"
    @update:model-value="emit('update:modelValue', String($event))"
  >
    <option value="">Primary</option>
    <option v-for="peer in peers.items" :key="peer.id" :value="peer.id">
      {{ peer.name }}
    </option>
    <option
      v-if="
        modelValue &&
        peers.loaded &&
        !peers.byId.has(modelValue)
      "
      :value="modelValue"
    >
      Unknown peer
    </option>
  </NativeSelect>
</template>
