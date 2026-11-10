<template>
  <CommandDialog v-model:open="open" title="Search MCPProxy" description="Search servers, tools, profiles, clients.">
    <CommandInput v-model="query" :filter="false" placeholder="Search MCPProxy…" aria-label="Search servers, tools, profiles, clients" autocomplete="off" spellcheck="false" />
    <CommandList aria-label="Results">
      <template v-for="(section, index) in sections" :key="section.id">
        <CommandSeparator v-if="index" />
        <CommandGroup :heading="section.label">
          <CommandItem v-for="row in section.rows" :key="row.key" :value="row.key" @select="activate(row)">
            <ServerIcon v-if="row.server && serversStore.byId.get(row.server)?.icon_url" :name="row.server" />
            <component v-else :is="row.icon || sectionIcons[section.id]" />
            <span class="min-w-0 truncate">{{ row.label }}</span>
            <CommandShortcut v-if="row.hint" class="max-w-[45%] truncate"><ServerIcon v-if="row.server && section.id === 'tools'" :name="row.server" class="mr-1.5" />{{ row.hint }}</CommandShortcut>
          </CommandItem>
        </CommandGroup>
      </template>
    </CommandList>
  </CommandDialog>
</template>

<script setup lang="ts">
import ServerIcon from "@/components/ServerIcon.vue";
import { computed, type Component, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useRouter, type RouteLocationRaw } from "vue-router";
import api from "@/services/api";
import { useServersStore } from "@/stores/servers";
import { useProfilesStore } from "@/stores/profiles";
import { useClientsStore } from "@/stores/clients";
import { CommandDialog, CommandGroup, CommandInput, CommandItem, CommandList, CommandSeparator, CommandShortcut } from "@/components/ui/command";
import { File, Search, Zap, Server, Wrench, Layers, Users } from "@lucide/vue";
import { profileEditorLink } from "@/utils/profileRoute";
import { clientLink } from "@/utils/scopeLinks";
import { PALETTE_ACTIONS, SIDEBAR_GROUPS } from "@/navigation/navModel";

type SectionId =
  | "search"
  | "pages"
  | "actions"
  | "servers"
  | "tools"
  | "profiles"
  | "clients";

interface Row {
  key: string;
  label: string;
  hint?: string;
  icon?: Component;
  server?: string;
  run: () => void;
}
interface Section {
  id: SectionId;
  label: string;
  rows: Row[];
}
interface ToolRow {
  name: string;
  server: string;
}

const SEARCH_DEBOUNCE_MS = 150;
const RESULT_LIMIT = 8;

const open = defineModel<boolean>("open", { default: false });
const router = useRouter();
const serversStore = useServersStore();
const profilesStore = useProfilesStore();
const clientsStore = useClientsStore();

const query = ref("");
const toolRows = ref<ToolRow[]>([]);
// A tenant has no admin doors: only their own pages, and
// free text still goes to /tools, which is in the tenant menu.
const isTenant = computed(() => false);
const text = computed(() => query.value.trim());
const needle = computed(() => text.value.toLowerCase());

function hasRoutePath(path: string | undefined): boolean {
  return !path || router.getRoutes().some((r) => r.path === path);
}

function matches(label: string): boolean {
  return !needle.value || label.toLowerCase().includes(needle.value);
}

function go(to: RouteLocationRaw) {
  void router.push(to);
}

const pageSpecs = computed<Row[]>(() => {
  let specs: Row[];
  {
    const items = [
      ...SIDEBAR_GROUPS.flatMap((g) => g.items),
    ];
    specs = items
      .filter((item) => hasRoutePath(item.requiresRoutePath))
      .map((item) => ({
        key: `page:${item.id}`,
        label: item.label,
        icon: item.icon,
        run: () => go(item.path),
      }));
  }
  return specs.filter((s) => matches(s.label));
});

const actionSpecs = computed<Row[]>(() => {
  if (isTenant.value) return [];
  return (
    PALETTE_ACTIONS
      // The server edition has no Clients hub: it can only add a personal server.
      .filter((a) => hasRoutePath(a.requiresRoutePath) && matches(a.label))
      .map((a) => ({
        key: `action:${a.id}`,
        label: a.label,
        run: () => go(a.to!),
      }))
  );
});

const serverSpecs = computed<Row[]>(() => {
  if (isTenant.value) return [];
  return serversStore.items
    .filter((s) => matches(s.name))
    .slice(0, RESULT_LIMIT)
    .map((s) => ({
      key: `server:${s.name}`,
      server: s.name,
      label: s.name,
      run: () => go(`/servers/${encodeURIComponent(s.name)}`),
    }));
});

const toolSpecs = computed<Row[]>(() =>
  isTenant.value
    ? []
    : toolRows.value.map((t) => ({
        key: `tool:${t.server}:${t.name}`,
        label: t.name,
        hint: t.server,
        server: t.server,
        run: () =>
          go({ path: "/tools", query: { server: t.server, q: t.name } }),
      })),
);

// Profiles and clients (needle-only). A tenant and
// the server edition have no Clients hub, so none of the three exist there.
const hasDirectory = computed(() => !isTenant.value);

const profileSpecs = computed<Row[]>(() => {
  if (!hasDirectory.value || !needle.value || !hasRoutePath("/profiles"))
    return [];
  return profilesStore.items
    .filter((p) => matches(p.name))
    .slice(0, RESULT_LIMIT)
    .map((p) => ({
      key: `profile:${p.id}`,
      label: p.name,
      run: () => go(profileEditorLink(p.id)),
    }));
});

const clientSpecs = computed<Row[]>(() => {
  if (!hasDirectory.value || !needle.value || !hasRoutePath("/clients"))
    return [];
  return clientsStore.items
    .filter((c) => matches(c.display_name || c.id) || matches(c.id))
    .slice(0, RESULT_LIMIT)
    .map((c) => ({
      key: `client:${c.id}`,
      label: c.display_name || c.id,
      run: () => go(clientLink(c.id)),
    }));
});

const sections = computed<Section[]>(() => {
  const defs: Array<[SectionId, string, Row[]]> = [
    [
      "search",
      "Search",
      text.value
        ? [
            {
              key: "search",
              label: `Search tools for “${text.value}”`,
              run: () => go({ path: "/tools", query: { q: text.value } }),
            },
          ]
        : [],
    ],
    ["pages", "Pages", pageSpecs.value],
    ["actions", "Actions", actionSpecs.value],
    ["servers", "Servers", serverSpecs.value],
    ["tools", "Tools", toolSpecs.value],
    ["profiles", "Profiles", profileSpecs.value],
    ["clients", "Clients", clientSpecs.value],
  ];
  return defs
    .filter(([, , specs]) => specs.length > 0)
    .map(([id, label, specs]) => ({
      id,
      label,
      rows: specs,
    }));
});

const sectionIcons: Record<SectionId, Component> = {
  search: Search, pages: File, actions: Zap, servers: Server, tools: Wrench,
  profiles: Layers, clients: Users,
};

// --- tool search: debounced, sequenced, never on empty text ---------------

let timer: ReturnType<typeof setTimeout> | null = null;
let sequence = 0;

function cancelSearch() {
  if (timer) clearTimeout(timer);
  timer = null;
  sequence++; // drops any response still in flight
}

async function runSearch(value: string) {
  const mine = ++sequence;
  try {
    const { results } = await api.index.search(value, RESULT_LIMIT);
    if (mine !== sequence) return;
    toolRows.value = (results ?? []).map((r) => ({
      name: r.tool.name,
      server: r.tool.server_name,
    }));
  } catch {
    if (mine === sequence) toolRows.value = [];
  }
}

// Servers, profiles and clients come from their live collections, loaded on
// first open.
watch(open, (isOpen) => {
  if (!isOpen) return;
  void serversStore.ensureLoaded();
  void profilesStore.ensureLoaded();
  void clientsStore.ensureLoaded();
});

watch([query, open], ([raw, isOpen]) => {
  if (!isOpen) { cancelSearch(); return; }
  const value = raw.trim();
  cancelSearch();
  // Tool rows belong to the query that produced them: drop them at once so a
  // stale row cannot be clicked during the debounce.
  toolRows.value = [];
  if (!value || isTenant.value) return;
  timer = setTimeout(() => {
    timer = null;
    void runSearch(value);
  }, SEARCH_DEBOUNCE_MS);
});

// --- open / close -----------------------------------------------------------

watch(open, (isOpen) => {
  if (!isOpen) cancelSearch();
});

/** Open the palette, optionally seeded with text the user already typed. */
function show(seed = "") {
  if (!open.value) toolRows.value = [];
  query.value = seed;
  open.value = true;
}

function close() {
  open.value = false;
}

function activate(row: Row) {
  close();
  row.run();
}

// --- global hotkeys -----------------------------------------------------------

function isEditable(el: Element | EventTarget | null): boolean {
  if (!(el instanceof HTMLElement)) return false;
  return (
    el.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(el.tagName)
  );
}

function onWindowKeydown(event: KeyboardEvent) {
  if (
    (event.metaKey || event.ctrlKey) &&
    !event.altKey &&
    !event.shiftKey &&
    event.key.toLowerCase() === "k"
  ) {
    // preventDefault so the browser's own Ctrl/Cmd+K (focus address bar) stays out of it.
    event.preventDefault();
    if (open.value) close();
    else show();
    return;
  }
  if (
    event.key === "/" &&
    !event.metaKey &&
    !event.ctrlKey &&
    !event.altKey &&
    !open.value &&
    !isEditable(document.activeElement) &&
    !isEditable(event.target) &&
    !document.querySelector('dialog[open], [role="dialog"][data-state="open"]')
  ) {
    event.preventDefault();
    show();
  }
}

onMounted(() => window.addEventListener("keydown", onWindowKeydown));
onBeforeUnmount(() => {
  window.removeEventListener("keydown", onWindowKeydown);
  cancelSearch();
});

defineExpose({ show, close });
</script>
