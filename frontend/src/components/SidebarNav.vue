<template>
  <Sidebar collapsible="icon" variant="inset">
    <SidebarHeader class="select-none">
      <div class="relative flex h-10 items-center justify-end pr-0.5">
        <RouterLink to="/servers" class="absolute inset-y-0 left-0 right-0 flex min-w-0 items-center gap-2 overflow-hidden px-2 transition-opacity duration-160 delay-200 md:right-10 group-data-[collapsible=icon]:pointer-events-none group-data-[collapsible=icon]:invisible group-data-[collapsible=icon]:opacity-0 group-data-[collapsible=icon]:delay-0" title="MCPProxy">
          <img :src="logoUrl" alt="" class="size-6 shrink-0" draggable="false" />
          <span class="truncate text-sm font-medium">MCPProxy</span>
        </RouterLink>
        <SidebarTrigger class="hidden shrink-0 md:inline-flex" />
      </div>
    </SidebarHeader>
    <SidebarContent class="select-none">
      <SidebarGroup v-for="group in visibleGroups" :key="group.id">
        <SidebarGroupLabel>{{ group.label }}</SidebarGroupLabel>
        <SidebarGroupContent>
          <SidebarMenu>
            <SidebarMenuItem v-for="item in group.items" :key="item.id">
              <SidebarMenuButton as-child :is-active="isActiveRoute(item.path)" :tooltip="item.label" class="group-hover/menu-item:bg-sidebar-accent">
                <RouterLink :to="item.path">
                  <component :is="item.icon" />
                  <span>{{ item.label }}</span>
                </RouterLink>
              </SidebarMenuButton>
              <Transition name="sidebar-review-badge" appear>
                <SidebarMenuBadge v-if="badgeValue(item) > 0" class="pointer-events-auto right-1.5 h-6 min-w-6 p-0">
                  <RouterLink class="flex h-full min-w-6 items-center justify-center rounded-xl bg-warning/10 px-1.5 leading-none text-warning" :to="{ path: '/tools', query: { review: '1' } }" :aria-label="`${badgeValue(item)} tools need review`">
                    {{ badgeValue(item) }}
                  </RouterLink>
                </SidebarMenuBadge>
              </Transition>
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarGroupContent>
      </SidebarGroup>
    </SidebarContent>
    <SidebarFooter>
      <SidebarMenu><SidebarMenuItem><SidebarMenuButton :tooltip="'Sign out'" @click="authStore.logout"><LogOut /><span>Sign out</span></SidebarMenuButton></SidebarMenuItem></SidebarMenu>
    </SidebarFooter>
    <SidebarRail v-if="!isMobile" />
  </Sidebar>
</template>

<script setup lang="ts">
import { LogOut } from "@lucide/vue";
import logoUrl from "@/assets/logo.svg";
import { Sidebar, SidebarFooter, SidebarHeader, SidebarContent, SidebarGroup, SidebarGroupLabel, SidebarGroupContent, SidebarMenu, SidebarMenuItem, SidebarMenuButton, SidebarMenuBadge, SidebarTrigger, SidebarRail, useSidebar } from "@/components/ui/sidebar";
import { computed, onMounted, watch } from "vue";
import { RouterLink, useRoute, useRouter } from "vue-router";
import { useAuthStore } from "@/stores/auth";
import { useServersStore } from "@/stores/servers";
import {
  SIDEBAR_GROUPS,
  type NavItem,
} from "@/navigation/navModel";

const route = useRoute();
const router = useRouter();
// Highlight the initial URL while Vue Router loads its first page component.
function initialNavigationPath() {
  let initial = router.resolve(router.options.history.location);
  for (let redirects = 0; redirects < 10; redirects++) {
    const redirect = initial.matched.at(-1)?.redirect;
    if (!redirect) break;
    initial = router.resolve(
      typeof redirect === "function"
        ? redirect(initial, router.currentRoute.value)
        : redirect,
    );
  }
  return initial.path;
}
const startupPath = initialNavigationPath();
const activePath = computed(() =>
  route.matched.length ? route.path : startupPath,
);

const authStore = useAuthStore();
// The one badge: tools awaiting review.
const serversStore = useServersStore();
// Tools awaiting review, from each server's quarantine counts.
const reviewCount = computed(() =>
  serversStore.items.reduce(
    (sum, server) => sum + (server.quarantine?.pending_count ?? 0) + (server.quarantine?.changed_count ?? 0),
    0,
  ),
);
onMounted(() => {
  void serversStore.ensureLoaded();
});

const { isMobile, setOpenMobile } = useSidebar();
watch(() => route.fullPath, () => setOpenMobile(false));

// An item with `requiresRoutePath` shows only when the router has that exact
// path (Profiles, until /profiles is registered). By path, not name, so
// the owning spec may name its route freely.
function hasRoutePath(path: string): boolean {
  return router.getRoutes().some((r) => r.path === path);
}

const visibleGroups = computed(() =>
  SIDEBAR_GROUPS.map((group) => ({
    ...group,
    items: group.items.filter(
      (item: NavItem) =>
        !item.requiresRoutePath || hasRoutePath(item.requiresRoutePath),
    ),
  })),
);

// Per-item badge value (0 hides it).
function badgeValue(item: NavItem): number {
  return item.badge === "review" ? reviewCount.value : 0;
}

function isActiveRoute(path: string): boolean {
  return activePath.value.startsWith(path);
}
</script>

<style scoped>
.sidebar-review-badge-enter-active,
.sidebar-review-badge-leave-active {
  transition: opacity 160ms ease-out;
}
.sidebar-review-badge-enter-from,
.sidebar-review-badge-leave-to {
  opacity: 0;
}
</style>
