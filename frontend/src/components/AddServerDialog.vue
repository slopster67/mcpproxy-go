<template>
  <BaseDialog :open="true" title="Add server" wide @close="close">
    <ManualServerForm @added="emit('added', $event)" @cancel="close" @busy="busy = $event" />
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import BaseDialog from '@/components/BaseDialog.vue'
import ManualServerForm from '@/components/ManualServerForm.vue'
const emit = defineEmits<{ close: []; added: [name: string] }>()
const busy = ref(false)
function close() { if (!busy.value) emit('close') }
</script>
