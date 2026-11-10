<script setup lang="ts">
import type { ListboxFilterEmits, ListboxFilterProps } from 'reka-ui'

import type { HTMLAttributes } from 'vue'
import { SearchIcon } from '@lucide/vue'
import { reactiveOmit } from '@vueuse/core'
import { computed } from 'vue'
import { cn } from 'cn'
import { ListboxFilter, useForwardProps } from 'reka-ui'
import { InputGroup, InputGroupAddon } from '@/components/ui/input-group'
import { useCommand } from '.'

defineOptions({
  inheritAttrs: false,
})

const props = withDefaults(defineProps<ListboxFilterProps & {
  class?: HTMLAttributes['class']
  filter?: boolean
}>(), { filter: true })
const emit = defineEmits<ListboxFilterEmits>()

const delegatedProps = reactiveOmit(props, 'class', 'filter', 'modelValue')

const forwardedProps = useForwardProps(delegatedProps)

const { filterState } = useCommand()
// Remote results have already been filtered by their source.
const search = computed({
  get: () => props.modelValue ?? filterState.search,
  set: (value: string) => {
    if (props.filter) filterState.search = value
    emit('update:modelValue', value)
  },
})
</script>

<template>
  <div
    data-slot="command-input-wrapper"
    class="p-1 pb-0"
  >
    <InputGroup class="bg-input/50 h-9">
      <ListboxFilter
        v-bind="{ ...forwardedProps, ...$attrs }"
        v-model="search"
        data-slot="command-input"
        auto-focus
        :class="cn('w-full text-sm outline-hidden disabled:cursor-not-allowed disabled:opacity-50', props.class)"
      />
      <InputGroupAddon>
        <SearchIcon class="size-4 shrink-0 opacity-50" />
      </InputGroupAddon>
    </InputGroup>
  </div>
</template>
