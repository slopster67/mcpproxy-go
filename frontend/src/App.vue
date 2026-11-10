<template>
  <div id="app" class="relative">
    <SidebarProvider v-if="authStore.isAuthenticated" class="min-h-svh md:h-svh md:min-h-0 md:overflow-hidden md:[@media(any-pointer:coarse)]:h-auto md:[@media(any-pointer:coarse)]:min-h-svh md:[@media(any-pointer:coarse)]:overflow-visible">
      <SidebarNav v-if="authStore.shellPresented" />
      <MobileSidebarInset class="min-h-svh min-w-0 md:h-[calc(100svh-1rem)] md:min-h-0 md:overflow-hidden md:[@media(any-pointer:coarse)]:h-auto md:[@media(any-pointer:coarse)]:min-h-[calc(100svh-1rem)] md:[@media(any-pointer:coarse)]:overflow-visible">
        <header v-if="authStore.shellPresented" class="sticky top-0 z-30 flex h-14 shrink-0 items-center gap-3 border-b bg-background px-4 md:hidden">
          <SidebarTrigger class="-ml-1" aria-label="Open navigation menu" />
          <RouterLink to="/servers" class="flex items-center gap-2 text-sm font-medium" aria-label="MCPProxy servers">
            <img :src="logoUrl" alt="" class="size-5" />
            <span>MCPProxy</span>
          </RouterLink>
        </header>
        <div class="min-w-0 flex-1 p-4 sm:p-6 md:min-h-0 md:overflow-y-auto md:p-8 md:[@media(any-pointer:coarse)]:overflow-visible">
          <router-view />
        </div>
      </MobileSidebarInset>
    </SidebarProvider>

    <CommandPalette v-if="authStore.shellPresented" />

    <!-- Toast Notifications -->
    <Toaster theme="dark" />

    <!-- Connection Status -->
    <ConnectionStatus />

    <main v-if="authStore.bootstrapError" class="mx-auto max-w-lg p-6">
      <Alert variant="destructive">{{ authStore.bootstrapError }}</Alert>
    </main>
  </div>
</template>

<script setup lang="ts">
import { onMounted, onUnmounted } from 'vue';
import { SidebarProvider, SidebarTrigger } from '@/components/ui/sidebar';
import SidebarNav from '@/components/SidebarNav.vue';
import MobileSidebarInset from '@/components/MobileSidebarInset.vue';
import logoUrl from '@/assets/logo.svg';
import CommandPalette from '@/components/CommandPalette.vue';
import { Toaster } from '@/components/ui/sonner';
import 'vue-sonner/style.css';
import ConnectionStatus from '@/components/ConnectionStatus.vue';
import { Alert } from '@/components/ui/alert';
import { useSystemStore } from '@/stores/system';
import { useAuthStore } from '@/stores/auth';
import { setUnauthorizedHandler } from '@/services/api';

const systemStore = useSystemStore();
const authStore = useAuthStore();
function sessionLost() {
  systemStore.setAuthRequired(true);
  systemStore.disconnectEventSource();
}
async function checkSession() {
  await authStore.checkAuth();
  if (!authStore.isAuthenticated) sessionLost();
}
onMounted(async () => {
  setUnauthorizedHandler(() => {
    authStore.expire();
    authStore.login();
    sessionLost();
  });
  await authStore.checkAuth();
  if (authStore.isAuthenticated) {
    systemStore.setAuthRequired(false);
    systemStore.connectEventSource(() => void checkSession());
  }
});
onUnmounted(() => {
  setUnauthorizedHandler(null);
  systemStore.disconnectEventSource();
});
</script>
