<template>
  <Card
    :id="sectionId"
    class="scroll-mt-20 md:scroll-mt-4 gap-0 overflow-clip py-0"
  >
    <header
      v-if="!embedded || (reviews.length && !readOnly)"
      :class="
        embedded
          ? 'contents'
          : 'sticky top-14 z-20 flex flex-wrap items-center justify-between gap-3 border-b border-border bg-card px-4 py-3 md:static'
      "
    >
      <div v-if="!embedded" class="flex min-w-0 items-center gap-2">
        <ServerIcon :name="server" />
        <router-link
          :to="serverDetailPath(server)"
          class="truncate text-sm font-semibold hover:underline"
          >{{ server }}</router-link
        >
        <span v-if="hostLabel" class="truncate text-xs text-muted-foreground">{{hostLabel}}</span>
        <span class="text-xs tabular-nums text-muted-foreground"
          >{{ tools.length }} tools</span
        >
      </div>
      <Teleport
        v-if="!embedded || bulkActionsTarget"
        :to="bulkActionsTarget ?? 'body'"
        :disabled="!embedded"
      >
        <Button
          v-if="reviews.length && !readOnly"
          size="sm"
          :disabled="pending.size > 0 || refreshing"
          @click="decide(reviews, true)"
          >{{
            reviews.length === totalReviews ? "Approve all" : "Approve shown"
          }}
          ({{ reviews.length }})</Button
        >
      </Teleport>
    </header>
    <Alert
      v-if="error"
      class="m-3 w-auto border-destructive/25 text-destructive"
      role="alert"
      >{{ error }}</Alert
    >
    <article
      v-for="tool in tools"
      :key="toolKey(tool)"
      class="border-b border-border last:border-b-0"
      :data-tool-name="tool.name"
    >
      <div class="flex items-start justify-between gap-3 px-4 py-3">
        <div class="min-w-0 flex-1">
          <div class="flex flex-wrap items-center gap-2">
            <button
              type="button"
              class="flex min-w-0 items-center gap-2 text-left"
              :aria-expanded="isOpen(tool)"
              :aria-controls="detailId(tool)"
              @click="toggleDetails(tool)"
            >
              <ChevronRight
                class="size-3.5 shrink-0 text-muted-foreground transition-transform"
                :class="isOpen(tool) && 'rotate-90'"
              />
              <code class="break-all text-xs font-medium">{{ tool.name }}</code>
            </button>
            <DropdownMenu v-if="manualTier(tool) && !readOnly">
              <DropdownMenuTrigger as-child>
                <Badge
                  as="button"
                  type="button"
                  variant="outline"
                  class="cursor-pointer border-dashed border-foreground/60 text-[10px]"
                  :class="tierColor(tool.tier)"
                  :disabled="
                    pending.has(toolKey(tool)) || !tool.classification_hash
                  "
                  :aria-label="`Set tier for ${tool.name}: ${tierLabel(tool.tier)}`"
                >
                  {{ tierLabel(tool.tier) }}
                  <ChevronDown data-icon="inline-end" aria-hidden="true" />
                </Badge>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start">
                <DropdownMenuRadioGroup
                  :model-value="tool.classification || 'unannotated'"
                  @update:model-value="
                    classifyTool(
                      tool,
                      $event === 'unannotated' ? '' : String($event),
                    )
                  "
                >
                  <DropdownMenuRadioItem
                    v-for="tier in [
                      'read',
                      'write',
                      'destructive',
                      'unannotated',
                    ]"
                    :key="tier"
                    :value="tier"
                  >
                    <span :class="tierColor(tier)">{{ tierLabel(tier) }}</span>
                  </DropdownMenuRadioItem>
                </DropdownMenuRadioGroup>
              </DropdownMenuContent>
            </DropdownMenu>
            <Badge
              v-else
              :variant="manualTier(tool) ? 'outline' : 'secondary'"
              class="text-[10px]"
              :class="[
                tierColor(tool.tier),
                manualTier(tool) && 'border-dashed border-foreground/60',
              ]"
              >{{ tierLabel(tool.tier) }}</Badge
            >
            <Badge
              v-if="needsToolReview(tool)"
              variant="secondary"
              class="text-[10px] text-warning"
              >{{ toolApprovalLabel(tool.approval_status) }}</Badge
            >
            <span v-if="tool.disabled" class="text-[11px] text-muted-foreground"
              >disabled</span
            >
          </div>
          <button
            v-if="!isOpen(tool)"
            type="button"
            class="mt-1 block w-full truncate pl-5 text-left text-xs text-muted-foreground"
            @click="toggleDetails(tool)"
          >
            {{ tool.description }}
          </button>
        </div>
        <div v-if="!readOnly" class="flex shrink-0 items-center gap-2">
          <template v-if="tool.review">
            <Button
              size="xs"
              variant="outline"
              :disabled="pending.has(toolKey(tool)) || refreshing"
              @click="decide([tool.review], true)"
              >Approve</Button
            >
            <Button
              size="xs"
              variant="ghost"
              class="text-destructive hover:text-destructive"
              :disabled="pending.has(toolKey(tool)) || refreshing"
              @click="decide([tool.review], false)"
              >Block</Button
            >
          </template>
          <Switch
            v-else-if="!needsToolReview(tool)"
            :model-value="enabledOverrides[toolKey(tool)] ?? !tool.disabled"
            :disabled="pending.has(toolKey(tool)) || refreshing"
            :aria-label="`Enable ${tool.name}`"
            title="Enable tool"
            @update:model-value="setEnabled(tool, $event)"
          />
        </div>
      </div>
      <ToolDetailsFrame
        v-if="isOpen(tool)"
        :id="detailId(tool)"
        class="max-h-[min(65dvh,40rem)] overflow-y-auto overscroll-auto px-4 pb-4 pl-9 flex flex-col gap-4 *:shrink-0 [scrollbar-gutter:stable] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
        tabindex="0"
        role="region"
        :aria-label="`${tool.name} details`"
      >
        <p v-if="tool.cached" class="text-xs text-warning">
          Saved definition; the server is currently unavailable.
        </p>
        <p
          v-if="needsToolReview(tool) && !tool.review && !readOnly"
          class="text-xs text-warning"
        >
          Refresh to load the review snapshot before approving this tool.
        </p>
        <dl
          class="flex flex-wrap items-center gap-x-6 gap-y-2 text-xs"
        >
          <div>
            <dt class="text-muted-foreground">Usage (30d)</dt>
            <dd>{{ tool.usage ?? 0 }} calls</dd>
          </div>
          <div>
            <dt class="text-muted-foreground">Last used</dt>
            <dd>
              {{
                tool.last_used
                  ? new Date(tool.last_used).toLocaleString()
                  : "Never in this window"
              }}
            </dd>
          </div>
          <div class="self-center">
            <dt class="sr-only">Activity</dt>
            <dd>
              <router-link
                :to="{
                  path: '/activity',
                  query: {
                    ...activityScope,
                    view: 'calls',
                    tool: `${server}:${tool.name}`,
                  },
                }"
                :class="buttonVariants({ variant: 'ghost', size: 'xs' })"
              >
                View calls
                <ArrowUpRight data-icon="inline-end" aria-hidden="true" />
              </router-link>
            </dd>
          </div>
        </dl>
        <ToolDefinitionDiff
          v-if="tool.approval_status === 'changed' && tool.review?.previous"
          :tool="tool.review"
        />
        <ToolDefinitionText
          v-if="
            !tool.review?.previous ||
            tool.approval_status !== 'changed' ||
            tool.review.previous.description === tool.description
          "
          :text="tool.description"
        />
        <dl
          v-if="tool.access"
          class="flex flex-wrap gap-x-6 gap-y-2 text-xs"
        >
          <div v-if="tool.access">
            <dt class="text-muted-foreground">Access</dt>
            <dd>
              {{ tool.access.callable ? "Callable" : "Not callable" }} ·
              {{ tool.access.visible ? "Visible" : "Hidden" }}
              <Button size="xs" variant="link" @click="emit('explain', tool)"
                >Why?</Button
              >
            </dd>
          </div>
        </dl>
        <p v-if="tool.held_reason" class="text-xs text-warning">
          {{ tool.held_reason
          }}<span v-if="tool.held_verdict"> · {{ tool.held_verdict }}</span>
        </p>
        <JsonDetails
          v-for="part in definitionParts(tool)"
          :key="part.title"
          :title="part.title"
          :data="part.value"
        />
      </ToolDetailsFrame>
    </article>
  </Card>
</template>
<script setup lang="ts">
import { useServersStore } from "@/stores/servers";

import ServerIcon from "@/components/ServerIcon.vue";
import { Card } from "@/components/ui/card";
import { computed, ref } from "vue";
import { ArrowUpRight, ChevronDown, ChevronRight } from "@lucide/vue";
import { Button, buttonVariants } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuTrigger,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
} from "@/components/ui/dropdown-menu";
import { Alert } from "@/components/ui/alert";
import { Switch } from "@/components/ui/switch";
import ToolDefinitionText from "@/components/ToolDefinitionText.vue";
import ToolDetailsFrame from "@/components/ToolDetailsFrame.vue";
import JsonDetails from "@/components/JsonDetails.vue";
import ToolDefinitionDiff from "@/components/ToolDefinitionDiff.vue";
import { serverDetailPath } from "@/utils/serverRoute";
import { tierLabel, toolApprovalLabel } from "@/utils/toolQuarantine";
import {
  needsToolReview,
  type InventoryTool,
  type ToolChange,
} from "@/utils/toolInventory";
import { expectedHashes } from "@/utils/reviewPresentation";
import type { ReviewTool } from "@/types/api";
import api, { toolId } from "@/services/api";

const props = defineProps<{
  server: string;
  tools: InventoryTool[];
  totalReviews: number;
  embedded?: boolean;
  bulkActionsTarget?: HTMLElement | null;
  refreshing: boolean;
  readOnly: boolean;
  reviewOnly: boolean;
  focus?: string;
  activityScope?: { client?: string; profile?: string };
}>();
const servers = useServersStore();
const hostLabel = computed(() => { const server = servers.byId.get(props.server); return server?.host_id ? server.host_name : '' });

const emit = defineEmits<{
  changed: [change: ToolChange];
  mutation: [completion: Promise<void>];
  refresh: [];
  explain: [tool: InventoryTool];
}>();
const sectionId = computed(() => `tools-${props.server}`);
const pending = ref(new Set<string>()),
  enabledOverrides = ref<Record<string, boolean>>({}),
  error = ref(""),
  expanded = ref(new Set<string>()),
  collapsed = ref(new Set<string>());
const manualTier = (tool: InventoryTool) =>
  !tool.tier || tool.tier === "unannotated" || !!tool.classification;
const tierColor = (tier?: string) =>
  tier === "write"
    ? "text-warning"
    : tier === "destructive"
      ? "text-destructive"
      : "";
const toolKey = (tool: InventoryTool) =>
  JSON.stringify([tool.name, tool.server_incarnation]);
const reviews = computed(() =>
  props.tools.flatMap((tool) => (tool.review ? [tool.review] : [])),
);
const detailId = (tool: InventoryTool) =>
  `tool-${encodeURIComponent(props.server)}-${encodeURIComponent(tool.name)}`;
const isOpen = (tool: InventoryTool) =>
  expanded.value.has(toolKey(tool)) ||
  (!collapsed.value.has(toolKey(tool)) &&
    ((props.reviewOnly && needsToolReview(tool)) || props.focus === tool.name));
function toggleDetails(tool: InventoryTool) {
  const key = toolKey(tool);
  if (isOpen(tool)) {
    expanded.value.delete(key);
    collapsed.value.add(key);
  } else {
    collapsed.value.delete(key);
    expanded.value.add(key);
  }
}
function definitionParts(tool: InventoryTool) {
  const parts = [
    {
      title: "Input schema",
      value: tool.review ? tool.review.input_schema : tool.schema,
    },
    {
      title: "Output schema",
      value: tool.review ? tool.review.output_schema : tool.output_schema,
    },
    {
      title: "Hold evidence",
      value: tool.held_signals?.length
        ? {
            reason: tool.held_reason,
            verdict: tool.held_verdict,
            signals: tool.held_signals,
          }
        : undefined,
    },
  ];
  return parts.filter(
    (part) => part.value !== undefined && part.value !== null,
  );
}
function startMutation() {
  let finish!: () => void;
  const completion = new Promise<void>((resolve) => {
    finish = resolve;
  });
  emit("mutation", completion);
  return finish;
}
async function classifyTool(tool: InventoryTool, tier: string) {
  const key = toolKey(tool);
  if (props.readOnly || pending.value.has(key) || !tool.classification_hash)
    return;
  pending.value.add(key);
  error.value = "";
  const finish = startMutation();
  try {
    const result = await api.tools.patch(toolId(props.server, tool.name), {
      classification: tier,
      expected_hash: tool.classification_hash,
      expected_incarnation: tool.server_incarnation || undefined,
    });
    emit("changed", {
      server: props.server,
      incarnation: tool.server_incarnation,
      names: [tool.name],
      kind: "classification",
      tier: result.tier,
      classification: (result.classification ||
        undefined) as InventoryTool["classification"],
    });
  } catch (err) {
    error.value =
      err instanceof Error ? err.message : "Could not classify tool";
  } finally {
    pending.value.delete(key);
    finish();
  }
}
async function decide(tools: ReviewTool[], approve: boolean) {
  if (props.refreshing || props.readOnly || !tools.length) return;
  const matching = props.tools.filter(
    (tool) => tool.review && tools.includes(tool.review),
  );
  const incarnation = matching[0]?.server_incarnation;
  if (
    matching.length !== tools.length ||
    matching.some((tool) => tool.server_incarnation !== incarnation)
  ) {
    error.value = "Server changed. Refresh before reviewing.";
    emit("refresh");
    return;
  }
  const keys = matching.map(toolKey);
  if (keys.some((key) => pending.value.has(key))) return;
  for (const key of keys) pending.value.add(key);
  const finish = startMutation();
  error.value = "";
  try {
    const names = tools.map((tool) => tool.name);
    if (approve) {
      const result = await api.servers.approveTools(props.server, {
        tools: names,
        expected_hashes: expectedHashes(tools),
        expected_incarnation: incarnation || undefined,
      });
      if (result.approved !== names.length) {
        emit("refresh");
        return;
      }
    } else {
      const result = await api.servers.blockTools(props.server, {
        tools: names,
        expected_incarnation: incarnation || undefined,
      });
      if (result.blocked !== names.length) {
        emit("refresh");
        return;
      }
    }
    emit("changed", {
      server: props.server,
      incarnation,
      names,
      kind: approve ? "approve" : "block",
    });
  } catch (err) {
    error.value = err instanceof Error ? err.message : "Tool decision failed";
  } finally {
    for (const key of keys) pending.value.delete(key);
    finish();
  }
}
async function setEnabled(tool: InventoryTool, enabled: boolean) {
  const key = toolKey(tool);
  if (
    pending.value.has(key) ||
    props.refreshing ||
    props.readOnly ||
    needsToolReview(tool)
  )
    return;
  pending.value.add(key);
  enabledOverrides.value[key] = enabled;
  const finish = startMutation();
  error.value = "";
  try {
    const updated = await api.tools.patch(toolId(props.server, tool.name), {
      enabled,
      expected_incarnation: tool.server_incarnation || undefined,
    });
    emit("changed", {
      server: props.server,
      incarnation: tool.server_incarnation,
      names: [tool.name],
      kind: "availability",
      enabled: !updated.disabled,
    });
  } catch (err) {
    error.value =
      err instanceof Error ? err.message : "Could not change tool availability";
  } finally {
    pending.value.delete(key);
    delete enabledOverrides.value[key];
    finish();
  }
}
</script>
