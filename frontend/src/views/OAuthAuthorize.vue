<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { useRoute } from "vue-router";
import { ShieldAlert } from "@lucide/vue";
import { Alert } from "@/components/ui/alert";
import { Button, buttonVariants } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Spinner } from "@/components/ui/spinner";
import api, { type OAuthConsent } from "@/services/api";
import { cn } from "@/lib/utils";
import logoUrl from "@/assets/logo.svg";

const route = useRoute();
const id = computed(() => String(route.query.request ?? ""));
const consent = ref<OAuthConsent | null>(null);
const selected = ref("");
const loading = ref(true);
const busy = ref<"" | "approve" | "deny">("");
const error = ref("");
const finished = ref(false);

const isPeer = computed(() => consent.value?.kind === "peer");
const appName = computed(() =>
  isPeer.value ? "An MCPProxy peer" : consent.value?.app_name || "An MCP client",
);
const heading = computed(() =>
  isPeer.value ? "Authenticate a peer" : `Connect to ${consent.value?.host_name ?? "MCPProxy"}`,
);

onMounted(async () => {
  if (!id.value) {
    error.value = "This sign-in link is incomplete. Start the sign-in again from your MCP client or peer.";
    loading.value = false;
    return;
  }
  try {
    consent.value = await api.oauth.request(id.value);
    if (consent.value.options.length === 1) selected.value = consent.value.options[0].id;
  } catch (err) {
    error.value = err instanceof Error ? err.message : "Could not load the sign-in request";
  } finally {
    loading.value = false;
  }
});

async function decide(approve: boolean) {
  busy.value = approve ? "approve" : "deny";
  error.value = "";
  try {
    const { redirect_url } = await api.oauth.decide(id.value, { approve, subject: approve ? selected.value : undefined });
    finished.value = true;
    window.location.assign(redirect_url);
  } catch (err) {
    error.value = err instanceof Error ? err.message : "Could not complete the sign-in";
  } finally {
    busy.value = "";
  }
}
</script>

<template>
  <main class="mx-auto flex min-h-[calc(100svh-4rem)] w-full max-w-md flex-col justify-center gap-6">
    <div class="flex items-center gap-2 text-sm font-medium">
      <img :src="logoUrl" alt="" class="size-5" />
      <span>MCPProxy</span>
    </div>
    <Card>
      <template v-if="loading">
        <CardHeader><Skeleton class="h-6 w-48" /><Skeleton class="h-4 w-64" /></CardHeader>
        <CardContent class="flex flex-col gap-2"><Skeleton class="h-12 w-full" /><Skeleton class="h-12 w-full" /></CardContent>
      </template>
      <template v-else-if="finished">
        <CardHeader>
          <CardTitle>Returning to {{ isPeer ? "the peer" : appName }}…</CardTitle>
          <CardDescription>You can close this tab once it has finished.</CardDescription>
        </CardHeader>
      </template>
      <template v-else-if="consent">
        <CardHeader>
          <CardTitle class="text-lg">{{ heading }}</CardTitle>
          <CardDescription>
            <template v-if="isPeer">
              A peer on this machine wants to connect. Choose which peer it is. It will receive that peer’s
              server configuration and credentials.
            </template>
            <template v-else>
              <span class="font-medium text-foreground">{{ appName }}</span> wants to use MCPProxy. Choose the
              client it connects as; it gets that client’s profile and permissions.
            </template>
          </CardDescription>
        </CardHeader>
        <CardContent class="flex flex-col gap-4">
          <Alert v-if="!consent.loopback" class="flex gap-2">
            <ShieldAlert class="mt-0.5 size-4 shrink-0" />
            <span>This app is not on this computer. Only continue if you started this sign-in yourself.</span>
          </Alert>
          <fieldset v-if="consent.options.length" class="flex flex-col gap-2">
            <legend class="mb-2 text-sm font-medium">{{ isPeer ? "Peer" : `Clients on ${consent.host_name}` }}</legend>
            <label
              v-for="option in consent.options"
              :key="option.id"
              :class="cn(
                'flex cursor-pointer items-center gap-3 rounded-2xl border px-3 py-2.5 transition-colors hover:bg-muted/50',
                selected === option.id && 'border-primary bg-muted/50',
              )"
            >
              <input v-model="selected" type="radio" name="subject" :value="option.id" class="accent-primary" />
              <span class="flex min-w-0 flex-col">
                <span class="truncate font-medium">{{ option.name }}</span>
                <span v-if="option.detail" class="truncate text-xs text-muted-foreground">{{ option.detail }}</span>
              </span>
            </label>
          </fieldset>
          <p v-else class="text-sm text-muted-foreground">
            There are no {{ isPeer ? "peers" : `clients on ${consent.host_name}` }} yet.
            <RouterLink to="/clients" class="underline underline-offset-4">Add one on Clients</RouterLink>, then start the
            sign-in again.
          </p>
          <Alert v-if="error" variant="destructive">{{ error }}</Alert>
          <div class="flex justify-end gap-2">
            <Button variant="outline" :disabled="!!busy" @click="decide(false)">
              <Spinner v-if="busy === 'deny'" data-icon="inline-start" />Deny
            </Button>
            <Button :disabled="!!busy || !selected" @click="decide(true)">
              <Spinner v-if="busy === 'approve'" data-icon="inline-start" />Allow
            </Button>
          </div>
        </CardContent>
      </template>
      <template v-else>
        <CardHeader>
          <CardTitle>Sign-in unavailable</CardTitle>
          <CardDescription>{{ error }}</CardDescription>
        </CardHeader>
        <CardContent>
          <RouterLink to="/servers" :class="buttonVariants({ variant: 'outline' })">Go to dashboard</RouterLink>
        </CardContent>
      </template>
    </Card>
  </main>
</template>
