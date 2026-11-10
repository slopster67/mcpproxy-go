<script setup lang="ts">
import ServerIcon from "@/components/ServerIcon.vue";
import { computed, ref, watch } from "vue";
import { useDelayedLoading } from "@/composables/useDelayedLoading";
import { ChevronRight, Search, ListChecks } from "@lucide/vue";
import type { EffectiveTool, Server } from "@/types/api";
import { Card, CardContent } from "@/components/ui/card";
import { Separator } from "@/components/ui/separator";
import { Checkbox } from "@/components/ui/checkbox";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Badge } from "@/components/ui/badge";
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
} from "@/components/ui/input-group";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import { Empty, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { Skeleton } from "@/components/ui/skeleton";
import { tierLabel } from "@/utils/toolQuarantine";
const props = defineProps<{
  servers: Server[];
  tools: EffectiveTool[];
  selectedServers: string[];
  selectedTools: Record<string, string[]>;
  loading: boolean;
  disabled?: boolean;
  focus?: string;
}>();
const emit = defineEmits<{
  preset: [kind: "read" | "write" | "all" | "none"];
  server: [name: string, all: boolean];
  tool: [server: string, tool: string, enabled: boolean];
}>();
const showSkeleton = useDelayedLoading(
  () => props.loading && !props.tools.length,
);
const search = ref(""),
  open = ref(new Set<string>());
const groups = computed(() => {
  const names = new Set([
    ...props.servers.map((s) => s.name),
    ...props.selectedServers,
    ...props.tools.map((t) => t.server),
  ]);
  const query = search.value.trim().toLowerCase();
  return [...names]
    .sort((a, b) => a.localeCompare(b))
    .map((name) => {
      const tools = new Map(
        props.tools
          .filter((t) => t.server === name)
          .map((t) => [
            t.tool,
            { name: t.tool, tier: t.intrinsic_tier, missing: false },
          ]),
      );
      for (const tool of props.selectedTools[name] ?? [])
        if (!tools.has(tool))
          tools.set(tool, { name: tool, tier: "", missing: true });
      const allTools = [...tools.values()].sort((a, b) =>
        a.name.localeCompare(b.name),
      );
      const shown = name.toLowerCase().includes(query)
        ? allTools
        : allTools.filter((tool) => tool.name.toLowerCase().includes(query));
      const enabled = props.selectedServers.includes(name),
        all = enabled && !Object.hasOwn(props.selectedTools, name);
      const count = all
        ? allTools.length
        : enabled
          ? (props.selectedTools[name]?.length ?? 0)
          : 0;
      return {
        name,
        host:props.servers.find(s => s.name === name)?.host_id ? props.servers.find(s => s.name === name)?.host_name : '',
        tools: shown,
        total: allTools.length,
        all,
        enabled,
        count,
        missing: !props.servers.some((s) => s.name === name),
      };
    })
    .filter(
      (group) =>
        !query ||
        group.name.toLowerCase().includes(query) ||
        group.tools.length,
    );
});
function isChecked(server: string, tool: string) {
  return (
    props.selectedServers.includes(server) &&
    (!Object.hasOwn(props.selectedTools, server) ||
      props.selectedTools[server].includes(tool))
  );
}
function expanded(name: string) {
  return open.value.has(name) || !!props.focus?.startsWith(name + ":");
}
function setOpen(name: string, value: boolean) {
  const next = new Set(open.value);
  if (value) next.add(name);
  else next.delete(name);
  open.value = next;
}
watch(search, () => {
  open.value = search.value.trim()
    ? new Set(groups.value.map((group) => group.name))
    : new Set();
});
</script>
<template>
  <div class="flex min-w-0 flex-col gap-4">
    <div class="flex flex-col gap-3">
      <div class="flex flex-wrap items-center gap-3">
        <InputGroup class="sm:max-w-sm"
          ><InputGroupAddon><Search /></InputGroupAddon
          ><InputGroupInput
            v-model="search"
            placeholder="Find a server or tool…"
            aria-label="Find a server or tool"
        /></InputGroup>
        <DropdownMenu
          ><DropdownMenuTrigger as-child
            ><Button
              variant="outline"
              :disabled="disabled"
              ><ListChecks data-icon="inline-start" />Quick select</Button
            ></DropdownMenuTrigger
          ><DropdownMenuContent align="start"
            ><DropdownMenuGroup
              ><DropdownMenuItem @select="emit('preset', 'read')"
                >Read-only tools</DropdownMenuItem
              ><DropdownMenuItem @select="emit('preset', 'write')"
                >Read + write tools</DropdownMenuItem
              ><DropdownMenuItem @select="emit('preset', 'all')"
                >All listed servers</DropdownMenuItem
              ></DropdownMenuGroup
            ><DropdownMenuSeparator /><DropdownMenuGroup
              ><DropdownMenuItem @select="emit('preset', 'none')"
                >Clear selection</DropdownMenuItem
              ></DropdownMenuGroup
            ></DropdownMenuContent
          ></DropdownMenu
        >
      </div>
      <p class="text-sm text-muted-foreground">
        Select a server for all current and future approved tools. Select
        individual tools for limited access.
      </p>
    </div>
    <div class="grid items-start gap-6" :class="$slots.aside && 'lg:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]'">
      <div class="min-w-0">
    <Card
      v-if="loading && !tools.length"
      v-show="showSkeleton"
      class="gap-0 py-0"
      role="status"
      aria-label="Loading permissions"
      ><CardContent class="px-0"
        ><template v-for="n in 5" :key="n"
          ><div class="flex items-center gap-3 px-4 py-3">
            <Skeleton class="size-4 shrink-0" /><Skeleton class="h-5 w-40" />
          </div>
          <Separator v-if="n < 5" /></template></CardContent
    ></Card>
    <template v-else>
      <Card v-if="groups.length" class="gap-0 py-0"
        ><CardContent class="px-0">
          <template v-for="(group, index) in groups" :key="group.name">
            <Collapsible
              :open="expanded(group.name)"
              @update:open="setOpen(group.name, $event)"
            >
              <div class="flex min-w-0 items-center gap-3 px-4 py-3">
                <Checkbox
                  :model-value="
                    group.all ? true : group.enabled ? 'indeterminate' : false
                  "
                  :disabled="disabled"
                  :aria-label="`All tools from ${group.name}`"
                  @update:model-value="
                    emit('server', group.name, $event === true)
                  "
                />
                <CollapsibleTrigger as-child
                  ><button
                    class="flex min-w-0 flex-1 cursor-pointer items-center gap-2 text-left"
                  >
                    <ChevronRight
                      class="size-4 shrink-0 transition-transform"
                      :class="expanded(group.name) && 'rotate-90'"
                    /><ServerIcon :name="group.name" /><span class="truncate font-medium">{{ group.name }}</span><span v-if="group.host" class="truncate text-xs text-muted-foreground">{{group.host}}</span>
                  </button></CollapsibleTrigger
                >
                <span
                  v-if="group.enabled"
                  class="shrink-0 text-xs text-muted-foreground"
                  >{{
                    group.all ? "All tools" : `${group.count} selected`
                  }}</span
                >
                <Button
                  v-if="group.enabled && !group.all"
                  variant="ghost"
                  size="xs"
                  :disabled="disabled"
                  :aria-label="`Clear permissions for ${group.name}`"
                  @click="emit('server', group.name, false)"
                  >Clear</Button
                >
              </div>
              <CollapsibleContent
                ><div class="flex min-w-0 flex-col gap-3 px-4 pb-4 pl-10">
                  <p v-if="group.missing" class="text-xs text-muted-foreground">
                    Server unavailable.
                  </p>
                  <p
                    v-if="!group.tools.length"
                    class="text-sm text-muted-foreground"
                  >
                    {{
                      search ? "No matching tools." : "No tools discovered yet."
                    }}
                  </p>
                  <div
                    v-for="tool in group.tools"
                    :key="tool.name"
                    class="flex min-w-0 items-start gap-3"
                  >
                    <Checkbox
                      :model-value="isChecked(group.name, tool.name)"
                      :disabled="disabled"
                      :aria-label="`Allow ${group.name}:${tool.name}`"
                      @update:model-value="
                        emit('tool', group.name, tool.name, $event === true)
                      "
                    /><RouterLink
                      target="_blank"
                      rel="noopener noreferrer"
                      class="min-w-0 flex-1 break-words text-sm hover:underline underline-offset-4"
                      :to="{
                        path: '/tools',
                        query: {
                          server: group.name,
                          q: tool.name,
                          focus: tool.name,
                        },
                        hash: `#tool-${encodeURIComponent(group.name)}-${encodeURIComponent(tool.name)}`,
                      }"
                      :title="`View ${tool.name} in Tools (new tab)`"
                      >{{ tool.name }}</RouterLink
                    ><Badge v-if="tool.missing" variant="secondary"
                      >Unavailable</Badge
                    ><span
                      v-else
                      class="shrink-0 text-xs text-muted-foreground"
                      >{{ tierLabel(tool.tier) }}</span
                    >
                  </div>
                  <p
                    v-if="group.enabled && !group.all"
                    class="text-xs text-muted-foreground"
                  >
                    New tools won’t be included automatically.
                  </p>
                </div></CollapsibleContent
              >
            </Collapsible>
            <Separator v-if="index < groups.length - 1" />
          </template> </CardContent
      ></Card>
      <Empty v-if="!groups.length"
        ><EmptyHeader
          ><EmptyTitle>{{
            search ? "No matching servers or tools" : "No servers configured"
          }}</EmptyTitle></EmptyHeader
        ></Empty
      >
    </template>
      </div>
      <slot name="aside" />
    </div>
  </div>
</template>
