<script setup lang="ts">
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from '@/components/ui/dialog'
const props = defineProps<{ open: boolean; title: string; wide?: boolean }>()
const emit = defineEmits<{ close: [] }>()
</script>

<template>
  <Dialog :open="open" @update:open="value => { if (!value) emit('close') }">
    <DialogContent :class="wide ? 'sm:max-w-3xl' : undefined" class="max-h-[calc(100dvh-2rem)] overflow-y-auto">
      <DialogHeader><slot name="title"><DialogTitle>{{ title }}</DialogTitle></slot></DialogHeader>
      <slot />
      <DialogFooter v-if="$slots.actions"><slot name="actions" /></DialogFooter>
    </DialogContent>
  </Dialog>
</template>
