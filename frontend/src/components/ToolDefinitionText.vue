<template>
  <div
    class="min-w-0 rounded-md bg-black px-4 py-3 flex flex-col gap-2"
  >
    <div
      class="prose prose-sm prose-invert max-w-none break-words [overflow-wrap:anywhere] prose-headings:my-3 prose-p:my-2 prose-pre:max-w-full prose-pre:overflow-x-auto prose-pre:bg-zinc-950 prose-code:before:content-none prose-code:after:content-none prose-table:block prose-table:overflow-x-auto"
      v-html="renderedMarkdown"
    />
    <Button
      size="xs"
      variant="link"
      v-if="truncated"
      type="button"
      class="h-auto px-0"
      @click="expanded = !expanded"
    >
      {{ expanded ? "Show less" : "Show all" }}
    </Button>
  </div>
</template>

<script setup lang="ts">
import { Button } from "@/components/ui/button";
import { computed, ref } from "vue";
import {
  initialToolDescription,
  toolDescriptionLimit,
  toolMarkdown,
} from "@/utils/toolMarkdown";

const props = withDefaults(defineProps<{ text?: string; limit?: number }>(), {
  text: "",
  limit: toolDescriptionLimit,
});
const expanded = ref(false);
const truncated = computed(() => props.text.length > props.limit);
const visibleText = computed(() =>
  expanded.value ? props.text : initialToolDescription(props.text, props.limit),
);
const renderedMarkdown = computed(() => toolMarkdown(visibleText.value));
</script>
