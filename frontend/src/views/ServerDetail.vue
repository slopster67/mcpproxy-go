<template>
  <Tabs class="flex flex-col gap-6" v-model="activeTab">
    <!-- Header -->
    <div
      class="flex flex-col lg:flex-row lg:justify-between lg:items-center gap-4"
    >
      <h1 class="flex h-9 min-w-0 items-center gap-2 text-2xl font-medium leading-9 tracking-tight" :title="serverName">
        <span
          class="relative top-px size-2 shrink-0 rounded-full"
          :class="headingStatus ? headingStatus.color : 'invisible'"
          :title="headingStatus?.label"
          :aria-label="headingStatus?.label"
          :aria-hidden="headingStatus ? undefined : true"
          role="img"
        />
        <ServerIconUpload :key="serverName" :name="serverName" :src="server?.icon_url" :uploaded="server?.icon_uploaded" :disabled="!server" />
        <span class="truncate">{{ displayName }}</span>
      </h1>

      <div class="flex h-9 max-w-full shrink-0 items-center gap-2 overflow-x-auto">
        <template v-if="server">
          <Button v-if="server.enabled" variant="outline" :disabled="actionLoading || server.host_online === false" @click="restartServer">
            <RotateCw data-icon="inline-start"  aria-hidden="true" />
            {{ isHttpProtocol ? "Reconnect" : "Restart" }}
          </Button>
          <Button v-if="canSignIn" variant="outline" :disabled="actionLoading || server.host_online === false" @click="triggerOAuth">
            <LogIn data-icon="inline-start"  aria-hidden="true" />
            Sign in
          </Button>
          <Button v-if="canLogout" variant="outline" :disabled="actionLoading || server.host_online === false" @click="triggerLogout"><LogOut data-icon="inline-start"  aria-hidden="true" />Log out</Button>
          <Button variant="outline" class="text-destructive hover:text-destructive" :disabled="actionLoading" @click="deleteDialogOpen = true"><Trash2 data-icon="inline-start"  aria-hidden="true" />Delete</Button>
        </template>
      </div>
    </div>

    <Alert v-if="server?.host_id && server.host_online === false">{{server.host_name}} is offline. Configuration and tool reviews remain available.</Alert>
    <SignInPanel
      v-if="server && signInState && server.host_online !== false"
      :loading="actionLoading"
      @login="triggerOAuth"
    />

    <TabsList aria-label="Server details">
      <TabsTrigger value="config">Configuration</TabsTrigger>
      <TabsTrigger value="logs">Logs</TabsTrigger>
      <TabsTrigger value="tools">Tools
        <Badge
          v-if="toolsToReview > 0"
          variant="outline"
          class="h-5 min-w-5 justify-center border-warning/20 bg-warning/10 px-1.5 text-xs tabular-nums text-warning"
          :aria-label="`${toolsToReview} tools to review`"
        >{{ toolsToReview }}</Badge>
      </TabsTrigger>
    </TabsList>

    <div
      v-if="loading && !server && activeTab !== 'tools'"
      v-show="showSkeleton"
      class="flex flex-col gap-4"
      role="status"
      aria-label="Loading server details"
    >
      <Card v-for="section in 2" :key="section" aria-hidden="true">
        <CardContent class="flex flex-col gap-4">
          <Skeleton class="h-5 w-28" />
          <div
            v-for="row in 4"
            :key="row"
            class="grid grid-cols-[6rem_1fr] gap-5"
          >
            <Skeleton class="h-4 w-20" /><Skeleton class="h-4 w-1/2" />
          </div>
        </CardContent>
      </Card>
    </div>

    <!-- Error State -->
    <Alert
      v-else-if="error"
      class="border-destructive/25 bg-destructive/5 text-destructive"
    >
      <svg
        class="w-6 h-6"
        fill="none"
        stroke="currentColor"
        viewBox="0 0 24 24"
      >
        <path
          stroke-linecap="round"
          stroke-linejoin="round"
          stroke-width="2"
          d="M12 8v4m0 4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
        />
      </svg>
      <div>
        <h3 class="font-bold">Failed to load server details</h3>
        <div class="text-sm">{{ error }}</div>
      </div>
      <Button
        size="sm"
        variant="outline"
        @click="serversStore.refetch()"
      >
        Try Again
      </Button>
    </Alert>

    <!-- Server Not Found -->
    <div v-else-if="!server && !loading" class="text-center py-12">
      <svg
        class="w-16 h-16 mx-auto mb-4 opacity-50"
        fill="none"
        stroke="currentColor"
        viewBox="0 0 24 24"
      >
        <path
          stroke-linecap="round"
          stroke-linejoin="round"
          stroke-width="2"
          d="M5 12h14M5 12a2 2 0 01-2-2V6a2 2 0 012-2h14a2 2 0 012 2v4a2 2 0 01-2 2M5 12a2 2 0 00-2 2v4a2 2 0 002 2h14a2 2 0 002-2v-4a2 2 0 00-2-2"
        />
      </svg>
      <h3 class="text-xl font-semibold mb-2">Server not found</h3>
      <p class="text-muted-foreground mb-4">
        The server "{{ serverName }}" was not found.
      </p>
      <router-link
        to="/servers"
        :class="cn(buttonVariants({ variant: 'default', size: 'default' }), '')"
      >
        Back to Servers
      </router-link>
    </div>

    <!-- Configuration and logs use the cached server while metadata refreshes. -->
    <div v-else-if="server && activeTab !== 'tools'" class="flex flex-col gap-6">
      <ErrorPanel
        v-if="!signInState"
        :diagnostic="server.diagnostic"
        :error="server.last_error"
      />

      <!-- Tab Content -->
      <div>
        <TabsContent value="logs" v-if="activeTab === 'logs'">
          <ServerLogs :key="serverName" :name="serverName" />
        </TabsContent>

        <!-- Configuration -->
        <TabsContent value="config" v-if="activeTab === 'config'">
          <div class="flex flex-col gap-6">
            <div class="grid min-w-0 grid-cols-1 gap-6 xl:grid-cols-2">
            <!-- General -->
            <Card class="min-w-0">
              <CardHeader class="min-h-6 items-center"><CardTitle
                  >General</CardTitle
                ></CardHeader><CardContent >

                <dl
                  class="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-5 gap-y-3 text-sm"
                >
                  <dt class="text-muted-foreground">Enabled</dt>
                  <dd class="flex items-center gap-2">
                    <Switch
                      data-slot="switch"
                      :model-value="server.enabled"
                      @update:model-value="toggleEnabled"
                      :disabled="actionLoading"
                    />
                    <span class="text-muted-foreground">{{
                      server.enabled ? "Yes" : "No"
                    }}</span>
                  </dd>
                  <dt class="text-muted-foreground">
                    <label for="trust-all-tools">Trust all tools</label>
                  </dt>
                  <dd class="flex items-center gap-2">
                    <Switch
                      data-slot="switch"
                      id="trust-all-tools"
                      :model-value="trustAllTools"
                      :disabled="trustAllToolsSaving || actionLoading"
                      @update:model-value="toggleTrustAllTools"
                    />
                    <span class="text-muted-foreground">{{
                      trustAllTools ? "Yes" : "No"
                    }}</span>
                  </dd>
                  <template v-if="peers.hasPeers"><dt class="text-muted-foreground">Run on</dt>
                  <dd><HostSelect :model-value="server.host_id || ''" :disabled="actionLoading || kvPatchInFlight" @update:model-value="patchServerDiff({host_id:$event, available_to_peers:false}, 'Updated execution host')" /></dd></template>
                  <template v-if="!server.host_id && (peers.hasPeers || server.available_to_peers)"><dt class="text-muted-foreground"><label for="share-with-peers">Available to peers</label></dt><dd><Switch id="share-with-peers" :model-value="!!server.available_to_peers" :disabled="actionLoading || kvPatchInFlight" @update:model-value="patchServerDiff({available_to_peers:$event}, 'Updated peer access')" /></dd></template>
                  <dt class="text-muted-foreground">Protocol</dt>
                  <dd class="flex items-center gap-2">
                    <SquareTerminal v-if="server.protocol === 'stdio'" class="size-4" aria-hidden="true" />
                    <Globe v-else class="size-4" aria-hidden="true" />
                    {{ server.protocol === "stdio" ? "stdio" : "Remote" }}
                  </dd>
                </dl>
              </CardContent>
            </Card>

            <!-- Connection (HTTP/SSE). Audit F11: the endpoint is editable here
                 so the "Edit URL" remedy offered on an unresolvable host lands on
                 the control that fixes it, not on a read-only echo of it. -->
            <Card
              v-if="server.url"
              class="min-w-0"
              :class="endpointHighlighted ? 'ring-2 ring-primary/50' : ''"
            >
              <CardHeader class="min-h-6 items-center">
                <CardTitle>Connection</CardTitle>
                <CardAction>
                  <Button v-if="!editingUrl" size="xs" variant="ghost" type="button" @click="startEditUrl">Edit</Button>
                </CardAction>
              </CardHeader>
              <CardContent>
                <form
                  v-if="editingUrl"
                  class="flex flex-col gap-4"
                  @submit.prevent="saveConnection"
                  @keyup.escape="cancelEditUrl"
                >
                  <Field>
                    <FieldLabel for="server-url-input">URL</FieldLabel>
                    <Input
                      id="server-url-input"
                      ref="urlInputRef"
                      v-model="urlDraft"
                      type="url"
                      class="w-full min-w-0 font-mono max-w-xl"
                      :class="urlDraftError ? 'border-destructive' : ''"
                      :disabled="actionLoading"
                    />
                    <p v-if="urlDraftError" class="text-destructive text-xs">{{ urlDraftError }}</p>
                  </Field>
                  <Field>
                    <FieldLabel>OAuth</FieldLabel>
                    <SegmentedControl v-model="oauthModeDraft" :options="oauthModeOptions" :disabled="actionLoading" aria-label="OAuth" class="self-start" />
                  </Field>
                  <ServerOAuthSettings v-if="oauthFieldsShown" ref="oauthFieldsRef" :server-name="server.name" :overrides="configuredOAuth" :disabled="actionLoading" />
                  <p v-if="connectionError" class="text-destructive text-xs" role="alert">{{ connectionError }}</p>
                  <div class="flex gap-2">
                    <Button size="sm" type="submit" :disabled="actionLoading || !urlDraft.trim()">Save</Button>
                    <Button size="sm" variant="ghost" type="button" :disabled="actionLoading" @click="cancelEditUrl">Cancel</Button>
                  </div>
                </form>
                <dl v-else class="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-5 gap-y-3 text-sm">
                  <dt class="text-muted-foreground">URL</dt>
                  <dd><code class="bg-background px-1.5 py-0.5 rounded text-xs break-all">{{ server.url }}</code></dd>
                  <dt class="text-muted-foreground">OAuth</dt>
                  <dd :class="oauthMode === 'auto' && 'italic'" :title="oauthMode === 'auto' ? 'Detected automatically' : undefined">{{ oauthStateLabel }}</dd>
                </dl>
              </CardContent>
            </Card>

            <!-- Process (stdio) -->
            <Card v-if="server.protocol === 'stdio'" class="min-w-0">
              <CardHeader class="min-h-6 items-center"><CardTitle >Process</CardTitle><CardAction><Button v-if="!editingProcess" size="xs" variant="ghost" @click="startEditProcess">Edit</Button></CardAction></CardHeader><CardContent >

                <form v-if="editingProcess" class="flex flex-col gap-4" @submit.prevent="saveProcess">
                  <div class="flex flex-col gap-2">
                    <label for="server-process-command" class="text-sm text-muted-foreground">Command</label>
                    <Input id="server-process-command" ref="processCommandInput" v-model="processCommand" class="font-mono" :disabled="processSaving" required />
                  </div>
                  <div class="grid gap-2">
                    <span class="text-sm text-muted-foreground">Arguments</span>
                    <ArgumentFields v-model="processArgs" :disabled="processSaving" />
                  </div>
                  <p v-if="processError" class="text-xs text-destructive" role="alert">{{ processError }}</p>
                  <div class="flex gap-2">
                    <Button type="submit" size="sm" :disabled="processSaving || !processCommand.trim()">{{ processSaving ? 'Saving…' : 'Save' }}</Button>
                    <Button type="button" size="sm" variant="ghost" :disabled="processSaving" @click="editingProcess = false">Cancel</Button>
                  </div>
                </form>
                <dl v-else class="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-5 gap-y-3 text-sm">
                  <dt class="text-muted-foreground">Command</dt>
                  <dd><code class="bg-background px-1.5 py-0.5 rounded text-xs break-all">{{ server.command }}</code></dd>
                  <dt class="text-muted-foreground">Arguments</dt>
                  <dd>
                    <code v-if="server.args?.length" class="bg-background px-1.5 py-0.5 rounded text-xs break-all">{{ server.args.join(" ") }}</code>
                    <span v-else class="text-muted-foreground">None</span>
                  </dd>
                </dl>
                <dl v-if="server.working_dir" class="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-5 mt-3 text-sm">
                  <dt class="text-muted-foreground">Working directory</dt>
                  <dd><code class="bg-background px-1.5 py-0.5 rounded text-xs break-all">{{ server.working_dir }}</code></dd>
                </dl>
              </CardContent>
            </Card>
            </div>

            <!-- Editors use the administrator-only configured values. -->
            <Card
              v-if="(server.url || hasHeaders) && server.protocol !== 'stdio'"

            >
              <CardHeader class="min-h-6 items-center"><CardTitle
                    >Headers</CardTitle
                  ><CardAction><Button
                    size="xs"
                    variant="ghost"
                    v-if="!addingHeader"

                    @click="startAddingHeader"
                    :disabled="kvPatchInFlight"
                    >+ Add header</Button
                  ></CardAction></CardHeader><CardContent class="@container min-w-0">

                <dl v-if="hasHeaders" class="min-w-0 divide-y divide-border">
                  <div v-for="k in headerKeys" :key="`hdr-${k}`" class="grid min-w-0 grid-cols-1 gap-2 py-2 first:pt-0 last:pb-0 @2xl:grid-cols-[minmax(0,1fr)_minmax(0,2fr)] @2xl:items-center">
                    <dt class="min-w-0 break-all font-mono text-xs" :class="editingKey !== `hdr::${k}` && 'px-1.5 @2xl:px-0'">
                      <Input v-if="editingKey === `hdr::${k}`" v-model="editingName" aria-label="Header name" class="w-full min-w-0 font-mono" :disabled="kvPatchInFlight" @keyup.esc="cancelEdit" />
                      <template v-else>{{ k }}</template>
                    </dt>
                    <dd class="min-w-0">
                      <KVValueCell scope="header" :k="k" :raw-value="serverHeaders[k]" :is-editing="editingKey === `hdr::${k}`" :busy="kvPatchInFlight" @start-edit="startEdit('hdr', k)" @cancel-edit="cancelEdit" @save="(val) => saveEdit('header', k, val)" @delete="deleteKv('header', k)" @convert="openConvertModal('header', k, serverHeaders[k])" />
                    </dd>
                  </div>
                </dl>
                <div v-else-if="!addingHeader" class="text-xs text-muted-foreground">
                  No headers configured.
                </div>
                <form v-if="addingHeader" class="grid min-w-0 grid-cols-1 gap-3 @2xl:grid-cols-[minmax(0,1fr)_minmax(0,2fr)]" :class="hasHeaders && 'mt-3'" @submit.prevent="commitNewHeader">
                  <Field>
                    <FieldLabel for="server-new-header-name">Header name</FieldLabel>
                    <Input id="server-new-header-name" v-model="newHeaderKey" ref="newHeaderKeyInput" class="w-full min-w-0 font-mono" placeholder="Header-Name" :disabled="kvPatchInFlight" />
                  </Field>
                  <Field>
                    <FieldLabel for="server-new-header-value">Value</FieldLabel>
                    <Input id="server-new-header-value" v-model="newHeaderValue" type="text" class="w-full min-w-0 font-mono" placeholder="Literal or ${secret:name}" :disabled="kvPatchInFlight" />
                  </Field>
                  <div class="col-span-full flex items-center justify-end gap-2">
                    <Button size="xs" variant="ghost" type="button" @click="addingHeader = false" :disabled="kvPatchInFlight">Cancel</Button>
                    <Button size="xs" type="submit" :disabled="!newHeaderKey || !newHeaderValue || kvPatchInFlight">Add</Button>
                  </div>
                </form>
              </CardContent>
            </Card>

            <!-- Environment variables share the header value editor. -->
            <Card v-if="hasEnv || server.protocol === 'stdio'" >
              <CardHeader class="min-h-6 items-center"><CardTitle
                    >Environment Variables</CardTitle
                  ><CardAction><Button
                    size="xs"
                    variant="ghost"
                    v-if="!addingEnv"

                    @click="startAddingEnv"
                    :disabled="kvPatchInFlight"
                    >+ Add variable</Button
                  ></CardAction></CardHeader><CardContent class="@container min-w-0">

                <dl v-if="hasEnv" class="min-w-0 divide-y divide-border">
                  <div v-for="k in envKeys" :key="`env-${k}`" class="grid min-w-0 grid-cols-1 gap-2 py-2 first:pt-0 last:pb-0 @2xl:grid-cols-[minmax(0,1fr)_minmax(0,2fr)] @2xl:items-center">
                    <dt class="min-w-0 break-all font-mono text-xs" :class="editingKey !== `env::${k}` && 'px-1.5 @2xl:px-0'">
                      <Input v-if="editingKey === `env::${k}`" v-model="editingName" aria-label="Environment variable name" class="w-full min-w-0 font-mono" :disabled="kvPatchInFlight" @keyup.esc="cancelEdit" />
                      <template v-else>{{ k }}</template>
                    </dt>
                    <dd class="min-w-0">
                      <KVValueCell scope="env" :k="k" :raw-value="serverEnv[k]" :is-editing="editingKey === `env::${k}`" :busy="kvPatchInFlight" @start-edit="startEdit('env', k)" @cancel-edit="cancelEdit" @save="(val) => saveEdit('env', k, val)" @delete="deleteKv('env', k)" @convert="openConvertModal('env', k, serverEnv[k])" />
                    </dd>
                  </div>
                </dl>
                <div v-else-if="!addingEnv" class="text-xs text-muted-foreground">
                  No environment variables configured.
                </div>
                <form v-if="addingEnv" class="grid min-w-0 grid-cols-1 gap-3 @2xl:grid-cols-[minmax(0,1fr)_minmax(0,2fr)]" :class="hasEnv && 'mt-3'" @submit.prevent="commitNewEnv">
                  <Field>
                    <FieldLabel for="server-new-env-name">Variable name</FieldLabel>
                    <Input id="server-new-env-name" v-model="newEnvKey" ref="newEnvKeyInput" class="w-full min-w-0 font-mono" placeholder="VAR_NAME" :disabled="kvPatchInFlight" />
                  </Field>
                  <Field>
                    <FieldLabel for="server-new-env-value">Value</FieldLabel>
                    <Input id="server-new-env-value" v-model="newEnvValue" type="text" class="w-full min-w-0 font-mono" placeholder="Literal or ${secret:name}" :disabled="kvPatchInFlight" />
                  </Field>
                  <div class="col-span-full flex items-center justify-end gap-2">
                    <Button size="xs" variant="ghost" type="button" @click="addingEnv = false" :disabled="kvPatchInFlight">Cancel</Button>
                    <Button size="xs" type="submit" :disabled="!newEnvKey || !newEnvValue || kvPatchInFlight">Add</Button>
                  </div>
                </form>
              </CardContent>
            </Card>
          </div>
        </TabsContent>
      </div>
    </div>
    <!-- Convert-to-secret modal: prompts for a secret secret name, then
         calls POST /api/v1/secrets followed by a PATCH replacing the
         literal value with `${secret:NAME}`. -->
    <div
      v-if="convertModal.open"
      class="fixed inset-0 z-[var(--z-modal)] items-center justify-center p-4 bg-black/75 flex"
    >
      <div
        data-slot="dialog-content"
        class="relative w-full max-w-lg rounded-lg border border-border bg-popover p-6 text-foreground shadow-xl max-h-[90dvh] overflow-y-auto max-w-md"
      >
        <h3 class="text-lg font-semibold tracking-tight">Convert to secret</h3>
        <p class="text-sm text-muted-foreground mt-2">
          Store the value of
          <code class="font-mono">{{ convertModal.key }}</code> in the OS
          secret and replace it with a
          <code class="font-mono">{{ "${secret:NAME}" }}</code> reference. The
          server config will then no longer contain the literal value.
        </p>
        <div class="flex flex-col gap-1.5 mt-4">
          <label class="flex items-center justify-between gap-2 py-1 text-sm"
            ><span class="text-sm font-medium">Secret name</span></label
          >
          <Input
            v-model="convertModal.secretName"
            class="font-mono"
            placeholder="my-server-token"
            @keyup.enter="commitConvert"
          />
          <label class="flex items-center justify-between gap-2 py-1 text-sm">
            <span class="text-xs text-muted-foreground"
              >Will be referenced as
              <code>{{
                "${secret:" + (convertModal.secretName || "NAME") + "}"
              }}</code></span
            >
          </label>
        </div>
        <div class="mt-5 flex flex-wrap items-center justify-end gap-2">
          <Button
            size="sm"
            variant="ghost"

            @click="closeConvertModal"
            :disabled="convertModal.busy"
            >Cancel</Button
          >
          <Button
            size="sm"
            variant="default"

            @click="commitConvert"
            :disabled="!convertModal.secretName || convertModal.busy"
          >
            <Spinner data-icon="inline-start" v-if="convertModal.busy"  />
            Convert
          </Button>
        </div>
      </div>
    </div>
    <DeleteConfirmDialog
      :open="deleteDialogOpen"
      kind="server"
      :name="server?.name ?? serverName"
      :busy="actionLoading"
      @close="deleteDialogOpen = false"
      @confirm="confirmDelete"
    />
    <DeleteConfirmDialog
      :open="!!pendingKvDelete"
      :kind="pendingKvDelete?.scope === 'header' ? 'header' : 'environment variable'"
      :name="pendingKvDelete?.key ?? ''"
      :busy="kvPatchInFlight"
      @close="pendingKvDelete = null"
      @confirm="confirmDeleteKv"
    />
    <TabsContent
      v-if="activeTab === 'tools' && (server || loading) && !error"
      value="tools"
    >
      <ToolInventory :key="serverName" :server-name="serverName" embedded @review-count="inventoryReviewCount = $event" />
    </TabsContent>
  </Tabs>
</template>

<script setup lang="ts">
import { usePeersStore } from "@/stores/peers";
import HostSelect from "@/components/HostSelect.vue";
import ServerIconUpload from "@/components/ServerIconUpload.vue";
import { Spinner } from "@/components/ui/spinner";
import DeleteConfirmDialog from "@/components/DeleteConfirmDialog.vue";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { cn } from "@/lib/utils";
import { Alert } from "@/components/ui/alert";
import { Button, buttonVariants } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Field, FieldLabel } from "@/components/ui/field";
import { Card, CardContent, CardTitle, CardHeader, CardAction } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import ServerOAuthSettings from "@/components/ServerOAuthSettings.vue";
import SegmentedControl from "@/components/SegmentedControl.vue";
import ServerLogs from "@/components/ServerLogs.vue";
import { ref, computed, nextTick, onMounted, watch } from "vue";
import { RotateCw, LogIn, LogOut, Trash2, Globe, SquareTerminal } from "@lucide/vue";
import { useRoute, useRouter } from "vue-router";
import { useServersStore } from "@/stores/servers";
import { useSystemStore } from "@/stores/system";
import ErrorPanel from "@/components/diagnostics/ErrorPanel.vue";
import SignInPanel from "@/components/diagnostics/SignInPanel.vue";
import KVValueCell from "@/components/KVValueCell.vue";
import ToolInventory from "@/components/ToolInventory.vue";
import ArgumentFields from "@/components/ArgumentFields.vue";
import { Skeleton } from "@/components/ui/skeleton";
import { useDelayedLoading } from "@/composables/useDelayedLoading";
import type { Server } from "@/types";
import type { OAuthOverrides } from "@/types/oauth";
import api from "@/services/api";
import { serverDisplayName } from "@/utils/serverRoute";
import { refName } from "@/utils/secretRef";
import { needsOAuthSignIn } from "@/utils/health";
import { serverCardStatus } from "@/utils/serverCardStatus";
import { canStartOAuthLogin } from "@/utils/oauthActions";
import { effectiveTrustMode } from "@/utils/trustMode";

interface Props {
  // vue-router decodes the percent-encoded ':serverName' param, so
  // this is the plain server name (which may contain '/', e.g.
  // "io.github.owner/repo") and matches the stored config `name` directly.
  serverName: string;
}

const peers = usePeersStore();
onMounted(peers.ensureLoaded);
const props = defineProps<Props>();
const route = useRoute();
const router = useRouter();

const serversStore = useServersStore();
const systemStore = useSystemStore();

// State
const deleteDialogOpen = ref(false);
const restartingServer = ref("");
watch(
  () => props.serverName,
  () => {
    deleteDialogOpen.value = false;
    restartingServer.value = "";
  },
);
const server = computed<Server | null>(() => serversStore.byId.get(props.serverName) ?? null);
const loading = computed(() => !serversStore.loaded && !serversStore.error);
const error = computed<string | null>(() => {
  if (!serversStore.loaded) return serversStore.error?.message ?? null;
  return server.value ? null : `Server "${props.serverName}" not found`;
});
watch(
  () => [server.value?.connected, server.value?.connecting, server.value?.last_error],
  () => {
    if (restartingServer.value !== server.value?.name) return;
    if (server.value.connected) {
      restartingServer.value = "";
      systemStore.addToast({ type: "success", title: "Server restarted", message: "" });
    } else if (!server.value.connecting && server.value.last_error) {
      restartingServer.value = "";
      systemStore.addToast({ type: "error", title: "Restart failed", message: server.value.last_error });
    }
  },
);

const inventoryReviewCount = ref<number | null>(null);
const toolsToReview = computed(() => inventoryReviewCount.value ??
  ((server.value?.quarantine?.pending_count ?? 0) + (server.value?.quarantine?.changed_count ?? 0)));
const headingStatus = computed(() => server.value ? serverCardStatus({
  ...server.value,
  quarantine: {
    pending_count: toolsToReview.value,
    changed_count: 0,
    blocked_count: server.value.quarantine?.blocked_count ?? 0,
  },
}) : null);
watch(
  [() => props.serverName, () => server.value?.quarantine?.pending_count, () => server.value?.quarantine?.changed_count],
  () => { inventoryReviewCount.value = null; },
);

// Prefer the registry-provided human-friendly title over the raw
// reverse-DNS `name` identifier for display. Falls back to the name (or the
// route param before the server loads).
const displayName = computed(() =>
  server.value ? serverDisplayName(server.value) : props.serverName,
);
type ServerTab = "config" | "tools" | "logs";
const activeTab = computed<ServerTab>({
  get: () => {
    const tab = route.query.tab;
    if (tab === "config" || tab === "tools" || tab === "logs") return tab;
    return typeof route.query.tool === "string" ? "tools" : "config";
  },
  set: (tab) => {
    void router.replace({ query: { ...route.query, tab } });
  },
});
const showSkeleton = useDelayedLoading(
  () => loading.value && !server.value && activeTab.value !== "tools",
);
const actionLoading = ref(false);

// Computed
const isHttpProtocol = computed(() => {
  return (
    server.value?.protocol === "http" ||
    server.value?.protocol === "streamable-http"
  );
});
const signedIn = computed(() => {
  const current = server.value;
  return !!current && !current.user_logged_out &&
    !needsOAuthSignIn(current) &&
    current.oauth_status !== "expired" && current.oauth_status !== "error" &&
    (current.authenticated === true || current.oauth_status === "authenticated");
});
const canSignIn = computed(
  () => !!server.value && !signedIn.value && canStartOAuthLogin(server.value),
);
const canLogout = computed(() => signedIn.value);

// OAuth sign-in state (null when no sign-in is required). Drives the
// SignInPanel.
const signInState = computed(() => {
  return server.value ? needsOAuthSignIn(server.value) : null;
});


async function toggleEnabled() {
  if (!server.value) return;

  actionLoading.value = true;
  try {
    const name = server.value.name;
    const enabled = !server.value.enabled;
    await serversStore.patch(name, { enabled });
    systemStore.addToast({
      type: "success",
      title: enabled ? "Server Enabled" : "Server Disabled",
      message: `${name} has been ${enabled ? "enabled" : "disabled"}`,
    });
  } catch (error) {
    systemStore.addToast({
      type: "error",
      title: "Operation Failed",
      message: error instanceof Error ? error.message : "Unknown error",
    });
  } finally {
    actionLoading.value = false;
  }
}

async function restartServer() {
  if (!server.value) return;

  actionLoading.value = true;
  try {
    const restarted = await serversStore.restart(server.value.name);
    if (restarted.connected) {
      systemStore.addToast({ type: "success", title: "Server restarted", message: "" });
    } else if (!restarted.connecting && restarted.last_error) {
      systemStore.addToast({ type: "error", title: "Restart failed", message: restarted.last_error });
    } else {
      // Still connecting: the server upserts that follow report the outcome.
      restartingServer.value = restarted.name;
    }
  } catch (error) {
    systemStore.addToast({
      type: "error",
      title: "Restart failed",
      message: error instanceof Error ? error.message : "Unknown error",
    });
  } finally {
    actionLoading.value = false;
  }
}

async function triggerOAuth() {
  if (!server.value) return;

  actionLoading.value = true;
  try {
    const result = await serversStore.login(server.value.name);
    systemStore.addToast({
      type: result.browser_opened ? "info" : "warning",
      title: result.browser_opened ? "Sign-in opened" : "Open the sign-in page",
      message: result.browser_opened
        ? `Finish signing in to ${server.value.name} in your browser.`
        : "Allow popups in your browser and try again.",
    });
  } catch (error) {
    systemStore.addToast({
      type: "error",
      title: "OAuth Failed",
      message: error instanceof Error ? error.message : "Unknown error",
    });
  } finally {
    actionLoading.value = false;
  }
}

async function triggerLogout() {
  const name = server.value?.name;
  if (!name) return;
  actionLoading.value = true;
  try {
    await serversStore.logout(name);
    systemStore.addToast({
      type: "success",
      title: "OAuth Logout Successful",
      message: `${name} has been logged out`,
    });
  } catch (error) {
    systemStore.addToast({
      type: "error",
      title: "Logout Failed",
      message: error instanceof Error ? error.message : "Unknown error",
    });
  } finally {
    actionLoading.value = false;
  }
}

async function confirmDelete() {
  const name = server.value?.name;
  if (!name) return;
  actionLoading.value = true;
  try {
    await serversStore.remove(name);
    systemStore.addToast({
      type: "success",
      title: "Server Deleted",
      message: `${name} has been deleted successfully`,
    });
    deleteDialogOpen.value = false;
    await router.push("/servers");
  } catch (error) {
    systemStore.addToast({
      type: "error",
      title: "Delete Failed",
      message: error instanceof Error ? error.message : "Unknown error",
    });
  } finally {
    actionLoading.value = false;
  }
}

// --- Headers / Env display + edit state ---
// Convention: composite key strings prefixed with the scope ("hdr::Name" or
// "env::Name") so a single Set can drive reveal/edit state across both
// cards without collision.

const serverHeaders = computed<Record<string, string>>(() => server.value?.headers ?? {});
const serverEnv = computed<Record<string, string>>(() => server.value?.env ?? {});
const configuredOAuth = computed<OAuthOverrides | null>(() => server.value?.oauth ?? null);
const headerKeys = computed(() => Object.keys(serverHeaders.value).sort());
const envKeys = computed(() => Object.keys(serverEnv.value).sort());
const hasHeaders = computed(() => headerKeys.value.length > 0);
const hasEnv = computed(() => envKeys.value.length > 0);

const editingKey = ref<string | null>(null);
const editingName = ref("");
const kvPatchInFlight = ref(false);

const addingHeader = ref(false);
const newHeaderKey = ref("");
const newHeaderValue = ref("");
const newHeaderKeyInput = ref<HTMLInputElement | null>(null);

const addingEnv = ref(false);
const newEnvKey = ref("");
const newEnvValue = ref("");
const newEnvKeyInput = ref<HTMLInputElement | null>(null);

const convertModal = ref<{
  open: boolean;
  scope: "header" | "env";
  key: string;
  rawValue: string;
  secretName: string;
  busy: boolean;
}>({
  open: false,
  scope: "header",
  key: "",
  rawValue: "",
  secretName: "",
  busy: false,
});

function startEdit(scope: "hdr" | "env", k: string) {
  editingName.value = k;
  editingKey.value = `${scope}::${k}`;
}
function cancelEdit() {
  editingKey.value = null;
}

async function startAddingHeader() {
  addingHeader.value = true;
  newHeaderKey.value = "";
  newHeaderValue.value = "";
  await new Promise((r) => setTimeout(r, 0));
  newHeaderKeyInput.value?.focus();
}
async function startAddingEnv() {
  addingEnv.value = true;
  newEnvKey.value = "";
  newEnvValue.value = "";
  await new Promise((r) => setTimeout(r, 0));
  newEnvKeyInput.value?.focus();
}

// Send only the changed key; the backend preserves every omitted entry.
async function patchServerDiff(
  patch: Record<string, unknown>,
  action: string,
): Promise<boolean> {
  if (!server.value) return false;
  kvPatchInFlight.value = true;
  try {
    await serversStore.patch(server.value.name, patch);
    systemStore.addToast({ type: "success", title: action, message: "" });
    return true;
  } catch (e: any) {
    systemStore.addToast({
      type: "error",
      title: `${action} failed`,
      message: e?.message || String(e),
    });
    return false;
  } finally {
    kvPatchInFlight.value = false;
  }
}

function scopeKey(scope: "header" | "env"): "headers" | "env" {
  return scope === "header" ? "headers" : "env";
}

const editingProcess = ref(false);
const processCommand = ref("");
const processArgs = ref<string[]>([]);
const processSaving = ref(false);
const processError = ref("");
const processCommandInput = ref<InstanceType<typeof Input> | null>(null);
let originalProcess = { command: "", args: [] as string[] };
function startEditProcess() {
  originalProcess = { command: server.value?.command ?? "", args: [...(server.value?.args ?? [])] };
  processCommand.value = originalProcess.command;
  processArgs.value = [...originalProcess.args];
  processError.value = "";
  editingProcess.value = true;
  void nextTick(() => (processCommandInput.value?.$el as HTMLInputElement | undefined)?.focus());
}
async function saveProcess() {
  if (processSaving.value || !server.value) return;
  const command = processCommand.value.trim();
  if (!command) { processError.value = "Enter a command"; return; }
  const patch: Record<string, unknown> = {};
  if (command !== originalProcess.command) patch.command = command;
  if (JSON.stringify(processArgs.value) !== JSON.stringify(originalProcess.args)) patch.args = [...processArgs.value];
  if (!Object.keys(patch).length) { editingProcess.value = false; return; }
  const target = server.value.name;
  processSaving.value = true;
  processError.value = "";
  try {
    await serversStore.patch(target, patch);
    if (props.serverName === target) editingProcess.value = false;
    systemStore.addToast({ type: "success", title: "Updated process", message: "" });
  } catch (err) {
    if (props.serverName === target) processError.value = err instanceof Error ? err.message : "Could not update process";
  } finally { processSaving.value = false; }
}
watch(() => props.serverName, () => { editingProcess.value = false; processError.value = ""; });

// --- Endpoint editing (audit F11) ---------------------------------------
// A DNS or malformed-URL failure now offers "Edit URL", which deep-links here
// with ?focus=endpoint. The remedy has to land on an editable field, so the
// Connection card's URL row is one.
const editingUrl = ref(false);
const urlDraft = ref("");
const urlDraftError = ref("");
const endpointHighlighted = ref(false);
const urlInputRef = ref<HTMLInputElement | null>(null);

function startEditUrl() {
  urlDraft.value = server.value?.url ?? "";
  oauthModeDraft.value = oauthMode.value;
  urlDraftError.value = "";
  connectionError.value = "";
  editingUrl.value = true;
  void nextTick(() => urlInputRef.value?.focus());
}

function cancelEditUrl() {
  editingUrl.value = false;
  urlDraftError.value = "";
  endpointHighlighted.value = false;
}

type OAuthMode = "auto" | "on" | "off";
const oauthModeOptions = [
  { value: "off", label: "Off" },
  { value: "auto", label: "Auto" },
  { value: "on", label: "On" },
] as const;
const oauthModeDraft = ref<OAuthMode>("auto");
const oauthMode = computed<OAuthMode>(() => server.value?.oauth_mode ?? "auto");
// In Auto, discovery decides: OAuth is in use once the upstream asked for it.
const oauthDetected = computed(() => {
  const current = server.value;
  return !!current && (!!current.oauth_active || !!current.authenticated || needsOAuthSignIn(current));
});
const oauthStateLabel = computed(() => {
  if (oauthMode.value === "auto") return oauthDetected.value ? "On" : "Off";
  return oauthMode.value === "on" ? "On" : "Off";
});
const oauthFieldsShown = computed(() =>
  oauthModeDraft.value === "on" || (oauthModeDraft.value === "auto" && oauthDetected.value));
const connectionError = ref("");
const oauthFieldsRef = ref<InstanceType<typeof ServerOAuthSettings> | null>(null);

async function saveConnection() {
  const next = urlDraft.value.trim();
  if (!next) {
    urlDraftError.value = "Enter a URL";
    return;
  }
  // Reject locally what the backend would reject anyway, so the user sees the
  // problem beside the field instead of as a toast over a closed editor.
  let parsed: URL;
  try {
    parsed = new URL(next);
  } catch {
    urlDraftError.value =
      "Not a valid URL — include the scheme, e.g. https://example.com/mcp";
    return;
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
    urlDraftError.value = "Only http:// and https:// endpoints are supported";
    return;
  }
  const patch: Record<string, unknown> = {};
  if (next !== server.value?.url) patch.url = next;
  const oauth: Record<string, unknown> = {};
  if (oauthFieldsShown.value && oauthFieldsRef.value) {
    const problem = oauthFieldsRef.value.validate();
    if (problem) {
      connectionError.value = problem;
      return;
    }
    Object.assign(oauth, oauthFieldsRef.value.patch);
  }
  if (oauthModeDraft.value !== oauthMode.value) {
    oauth.mode = oauthModeDraft.value === "auto" ? null : oauthModeDraft.value;
  }
  if (Object.keys(oauth).length) patch.oauth = oauth;
  connectionError.value = "";
  if (!Object.keys(patch).length) {
    cancelEditUrl();
    return;
  }
  urlDraftError.value = "";
  const ok = await patchServerDiff(patch, "Updated connection");
  if (ok) cancelEditUrl();
}

// ?focus=endpoint (set by the Edit URL action) opens the editor and draws
// attention to the card, so the deep link ends on the control, not near it.
function applyEndpointFocus() {
  if (route.query.focus !== "endpoint") return;
  if (activeTab.value !== "config") return;
  if (!server.value?.url) return;
  endpointHighlighted.value = true;
  if (!editingUrl.value) startEditUrl();
}

const trustAllTools = computed(
  () => effectiveTrustMode(server.value?.trust_mode) === "auto",
);
const trustAllToolsSaving = ref(false);

async function toggleTrustAllTools(checked: boolean) {
  const mode = checked ? "auto" : "manual";
  if (!server.value || trustAllToolsSaving.value) return;
  const name = server.value.name;
  trustAllToolsSaving.value = true;
  try {
    await serversStore.patch(name, { trust_mode: mode });
    systemStore.addToast({
      type: "success",
      title: mode === "auto" ? "All tools trusted" : "Tool review required",
      message: "",
    });
  } catch (err) {
    systemStore.addToast({
      type: "error",
      title: "Tool approval setting could not be saved",
      message: err instanceof Error ? err.message : String(err),
    });
  } finally {
    trustAllToolsSaving.value = false;
  }
}

async function saveEdit(scope: "header" | "env", k: string, val: string) {
  const name = editingName.value.trim();
  if (!name) {
    systemStore.addToast({ type: "error", title: "Name is required", message: "" });
    return;
  }
  const keys = scope === "header" ? headerKeys.value : envKeys.value;
  const normalize = (key: string) => scope === "header" ? key.toLowerCase() : key;
  if (keys.some(key => key !== k && normalize(key) === normalize(name))) {
    systemStore.addToast({ type: "error", title: "Name already exists", message: name });
    return;
  }
  const ok = await patchServerDiff(
    { [scopeKey(scope)]: name === k ? { [k]: val } : { [k]: null, [name]: val } },
    `Updated ${name}`,
  );
  if (ok) editingKey.value = null;
}

// Deletion uses JSON Merge Patch (RFC 7396): a null value on the key
// signals "delete this key" to the backend. JSON.stringify emits `null`
// as the literal token, so the patch body becomes `{"headers": {"X-Old":
// null}}` on the wire — symmetric with the MCP `upstream_servers patch`
// tool's `{"X-Old": null}` convention. Note the explicit `null` literal:
// passing `undefined` would be stripped by JSON.stringify (no key in the
// output) and the backend would interpret that as "preserve".
const pendingKvDelete = ref<{ scope: "header" | "env"; key: string } | null>(null);
function deleteKv(scope: "header" | "env", key: string) {
  pendingKvDelete.value = { scope, key };
}
async function confirmDeleteKv() {
  const target = pendingKvDelete.value;
  if (!target || kvPatchInFlight.value) return;
  const ok = await patchServerDiff({ [scopeKey(target.scope)]: { [target.key]: null } }, `Deleted ${target.key}`);
  if (ok) pendingKvDelete.value = null;
}
watch(() => props.serverName, () => { pendingKvDelete.value = null; });

async function commitNewHeader() {
  if (!newHeaderKey.value || !newHeaderValue.value) return;
  const ok = await patchServerDiff(
    { headers: { [newHeaderKey.value]: newHeaderValue.value } },
    `Added ${newHeaderKey.value}`,
  );
  if (ok) addingHeader.value = false;
}

async function commitNewEnv() {
  if (!newEnvKey.value || !newEnvValue.value) return;
  const ok = await patchServerDiff(
    { env: { [newEnvKey.value]: newEnvValue.value } },
    `Added ${newEnvKey.value}`,
  );
  if (ok) addingEnv.value = false;
}

// Suggest a secret name derived from the kv key: the shared
// helper (also used by Paste/Manual/Catalog secret toggles) keeps the field
// KIND (env vs header) in the name, so a header and an env var with the same
// key never collide on one secret entry.
function suggestSecretName(scope: "header" | "env", k: string): string {
  return refName(server.value?.name || "server", scope, k);
}

function openConvertModal(
  scope: "header" | "env",
  k: string,
  rawValue: string,
) {
  convertModal.value = {
    open: true,
    scope,
    key: k,
    rawValue,
    secretName: suggestSecretName(scope, k),
    busy: false,
  };
}
function closeConvertModal() {
  convertModal.value.open = false;
  convertModal.value.busy = false;
}

async function commitConvert() {
  const m = convertModal.value;
  if (!m.secretName || !server.value) return;
  m.busy = true;
  try {
    // Atomic server-side conversion: the backend reads the real value
    // from the loaded config (so we don't need the plaintext on the
    // client — important when the API redacts sensitive headers on the
    // read path), stores it in secret, and rewrites the config field
    // with the ${secret:NAME} reference. Single round-trip, single
    // failure surface.
    const name = server.value.name;
    await api.servers.configToSecret(name, {
      scope: m.scope,
      key: m.key,
      secret_name: m.secretName,
    });
    serversStore.upsertLocal(await api.servers.get(name));
    systemStore.addToast({
      type: "success",
      title: `Converted ${m.key} to secret`,
      message: "",
    });
    closeConvertModal();
  } catch (e: any) {
    systemStore.addToast({
      type: "error",
      title: "Convert failed",
      message: e?.message || String(e),
    });
  } finally {
    convertModal.value.busy = false;
  }
}

onMounted(() => {
  void serversStore.ensureLoaded().then(applyEndpointFocus);
});
</script>
