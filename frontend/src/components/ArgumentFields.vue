<script setup lang="ts">
import { Plus, X } from '@lucide/vue'
import { InputGroup, InputGroupInput, InputGroupAddon, InputGroupButton } from '@/components/ui/input-group'
import { Button } from '@/components/ui/button'
const props = defineProps<{ modelValue: string[]; disabled?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: string[]] }>()
function update(index: number, value: string | number) {
  const args = [...props.modelValue]
  args[index] = String(value)
  emit('update:modelValue', args)
}
function remove(index: number) {
  emit('update:modelValue', props.modelValue.filter((_, i) => i !== index))
}
</script>

<template>
  <div class="flex flex-col gap-2" role="group" aria-label="Arguments">
    <InputGroup v-for="(arg, index) in modelValue" :key="index">
      <InputGroupInput :model-value="arg" :aria-label="`Argument ${index + 1}`" class="font-mono" :disabled="disabled" @update:model-value="update(index, $event)" />
      <InputGroupAddon align="inline-end"><InputGroupButton size="icon-xs" :aria-label="`Remove argument ${index + 1}`" :disabled="disabled" @click="remove(index)"><X data-icon="inline-start" /></InputGroupButton></InputGroupAddon>
    </InputGroup>
    <Button type="button" size="sm" variant="outline" class="self-start" :disabled="disabled" @click="emit('update:modelValue', [...modelValue, ''])"><Plus data-icon="inline-start" />Add argument</Button>
  </div>
</template>
