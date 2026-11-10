<template>
  <div
    v-if="items.length > 0"
    class="flex flex-wrap items-center gap-2"
  >
    <Badge
      variant="secondary"
      v-for="item in items"
      :key="item.name"
      class="text-xs gap-1 h-auto min-h-6 py-1 whitespace-normal text-left"
      :class="
        item.note
          ? 'border-transparent bg-muted text-muted-foreground'
          : 'bg-transparent border-current/30'
      "
      :aria-disabled="item.note ? 'true' : undefined"
    >
      <span>{{ item.label }}</span>
      <span v-if="item.note" class="text-xs"
        >&mdash; {{ item.note }}</span
      >
      <Button
        size="xs"
        variant="ghost"
        type="button"
        class="aspect-square px-0 min-h-6 w-6"
        :aria-label="`Remove ${item.label} filter`"
        @click="item.remove()"
      >
        <svg data-icon="inline-start"

          fill="none"
          stroke="currentColor"
          viewBox="0 0 24 24"
          aria-hidden="true"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            stroke-width="2"
            d="M6 18L18 6M6 6l12 12"
          />
        </svg>
      </Button>
    </Badge>
  </div>
</template>

<script setup lang="ts">
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { computed } from "vue";
import { useRoute } from "vue-router";
import { useScopeLabels, type ScopeName } from "@/composables/useScopeLabels";
import {
  scopeParamAppliesToPage,
  type PageId,
  type UseScopeQueryResult,
} from "@/composables/useScopeQuery";

// The removable profile and client chips of a page. A param in the URL that
// this page does not register renders as a "not applicable here" chip instead
// of being silently ignored.
const props = defineProps<{
  page: PageId;
  scopeQuery: UseScopeQueryResult;
  exclude?: ScopeName[];
}>();

const SCOPE_NAMES = ["profile", "client"] as const;

const route = useRoute();
const { chipLabel } = useScopeLabels();

interface Item {
  name: ScopeName;
  label: string;
  note?: string;
  remove: () => void;
}

const items = computed<Item[]>(() => {
  const out: Item[] = [];
  for (const chip of props.scopeQuery.chips.value) {
    if (!(SCOPE_NAMES as readonly string[]).includes(chip.name)) continue;
    const name = chip.name as ScopeName;
    out.push({
      name,
      label: chipLabel(name, chip.value),
      remove: chip.remove,
    });
  }
  for (const name of SCOPE_NAMES) {
    if (out.some((item) => item.name === name)) continue;
    const raw = route.query[name];
    const value = Array.isArray(raw) ? raw[0] : raw;
    if (typeof value !== "string" || value === "") continue;
    if (scopeParamAppliesToPage(name, props.page)) continue;
    out.push({
      name,
      label: chipLabel(name, value),
      note: "not applicable here",
      remove: () => props.scopeQuery.clear([name]),
    });
  }
  return out.filter(item => !props.exclude?.includes(item.name));
});
</script>
