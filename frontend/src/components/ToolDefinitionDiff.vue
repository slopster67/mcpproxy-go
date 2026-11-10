<template>
  <section
    class="flex min-w-0 flex-col gap-4"
    aria-label="Definition changes"
  >
    <div
      v-for="section in sections"
      :key="section.title"
      class="min-w-0 space-y-2"
    >
      <div class="flex items-center gap-3 text-xs">
        <h3 class="font-medium">{{ section.title }}</h3>
        <span
          class="font-mono text-emerald-400"
          :aria-label="`${section.added} added lines`"
          >+{{ section.added }}</span
        >
        <span
          class="font-mono text-red-400"
          :aria-label="`${section.removed} removed lines`"
          >−{{ section.removed }}</span
        >
      </div>
      <div
        class="min-w-0 overflow-hidden rounded-md border border-border bg-black font-mono text-xs leading-relaxed"
      >
        <template v-for="(block, index) in section.blocks" :key="index">
          <Button
            v-if="block.hidden && !expanded.has(`${section.title}:${index}`)"
            variant="ghost"
            size="xs"
            class="h-auto w-full justify-start rounded-none px-3 py-2 font-sans text-muted-foreground"
            @click="expanded.add(`${section.title}:${index}`)"
          >
            <UnfoldVertical data-icon="inline-start" />Show
            {{ block.lines.length }} unchanged
            {{ block.lines.length === 1 ? "line" : "lines" }}
          </Button>
          <template v-else>
            <div
              v-for="(line, n) in block.lines"
              :key="n"
              class="grid min-w-0 grid-cols-[1.75rem_minmax(0,1fr)]"
              :class="
                line.kind === 'removed'
                  ? 'bg-red-500/10'
                  : line.kind === 'added'
                    ? 'bg-emerald-500/10'
                    : ''
              "
              :data-diff-kind="line.kind"
            >
              <span
                class="select-none py-0.5 text-center"
                :class="
                  line.kind === 'removed' ? 'text-red-400' : 'text-emerald-400'
                "
                aria-hidden="true"
                >{{
                  line.kind === "removed"
                    ? "−"
                    : line.kind === "added"
                      ? "+"
                      : ""
                }}</span
              >
              <code
                class="min-w-0 whitespace-pre-wrap break-words py-0.5 pr-3 [overflow-wrap:anywhere]"
                ><span v-if="line.kind !== 'context'" class="sr-only">{{
                  line.kind === "removed" ? "Removed: " : "Added: "
                }}</span
                ><template v-if="line.fragments"
                  ><span
                    v-for="(fragment, f) in line.fragments"
                    :key="f"
                    :class="
                      fragment.changed &&
                      (line.kind === 'removed'
                        ? 'bg-red-500/25 text-red-200'
                        : 'bg-emerald-500/25 text-emerald-200')
                    "
                    >{{ fragment.text }}</span
                  ></template
                ><template v-else-if="section.json"
                  ><span
                    v-for="(token, t) in jsonTokens(line.text)"
                    :key="t"
                    :class="token.color"
                    >{{ token.text }}</span
                  ></template
                ><template v-else>{{ line.text || "\u00a0" }}</template></code
              >
            </div>
          </template>
        </template>
      </div>
    </div>
  </section>
</template>
<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { UnfoldVertical } from "@lucide/vue";
import { Button } from "@/components/ui/button";
import { reviewDiffSections } from "@/utils/toolReviewPresentation";
import { jsonTokens } from "@/utils/jsonHighlight";
import type { ReviewTool } from "@/types/api";
const props = defineProps<{ tool: ReviewTool }>();
const expanded = ref(new Set<string>());
watch(
  () => props.tool.current_hash,
  () => expanded.value.clear(),
);
const sections = computed(() => reviewDiffSections(props.tool));
</script>
