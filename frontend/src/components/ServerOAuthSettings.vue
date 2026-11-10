<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { Eye, EyeOff, Plus, RotateCcw, Trash2 } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import api from '@/services/api'
import type { OAuthDiscovered, OAuthOverrides } from '@/types/oauth'

// OAuth override fields for the Connection card's editor. The card owns
// saving: it reads `patch` and `validate()` and sends one PATCH.
const props = defineProps<{ serverName: string; overrides: OAuthOverrides | null; disabled?: boolean }>()
const loading = ref(false)
const saving = computed(() => !!props.disabled)
const error = ref('')
const settings = ref<OAuthDiscovered | null>(null)
const secretVisible = ref(false)
// A discovered value is shown as text until clicked; clicking opens it as an
// override seeded with that value. Fields with nothing discovered are inputs.
const draft = ref<Record<string, string>>({})
const editing = ref(new Set<string>())
type ParameterRow = { name: string; value: string }
const parameters = ref<ParameterRow[]>([])
const paramsOverridden = ref(false)
let loadVersion = 0
const fields = [
  { key: 'client_id', label: 'Client ID', placeholder: 'Client ID issued by the provider' },
  { key: 'client_secret', label: 'Client secret', placeholder: 'Only for a confidential client' },
  { key: 'scopes', label: 'Scopes', placeholder: 'Automatic' },
  { key: 'auth_server_metadata_url', label: 'Metadata URL', placeholder: 'Automatic discovery' },
  { key: 'authorization_endpoint', label: 'Authorization URL', placeholder: 'Automatic discovery' },
  { key: 'token_endpoint', label: 'Token URL', placeholder: 'Automatic discovery' },
  { key: 'registration_endpoint', label: 'Registration URL', placeholder: 'Automatic discovery' },
  { key: 'redirect_uri', label: 'Redirect URI', placeholder: 'Allocated when signing in' },
] as const

function display(value: unknown): string {
  return Array.isArray(value) ? value.join(' ') : typeof value === 'string' ? value : ''
}
function automatic(key: string): string {
  return display(settings.value?.automatic[key as keyof OAuthOverrides])
}
// Text shown for a field that has an automatic source but no value yet.
const automaticLabels: Record<string, string> = { client_id: 'Registered automatically' }
function automaticText(key: string): string {
  return automatic(key) || automaticLabels[key] || ''
}
// Dynamic registration registers a public client, so a secret only belongs
// with a client ID entered by hand.
const visibleFields = computed(() => fields.filter(field =>
  field.key !== 'client_secret' || editing.value.has('client_id') || !!draft.value.client_secret))
const automaticParams = computed(() => Object.entries(settings.value?.automatic.extra_params ?? {}))
function resetDraft() {
  if (!settings.value) return
  const current = { overrides: props.overrides }
  editing.value = new Set()
  for (const field of fields) {
    draft.value[field.key] = display(current.overrides?.[field.key])
    if (draft.value[field.key]) editing.value.add(field.key)
  }
  const overrides = current.overrides?.extra_params
  paramsOverridden.value = !!overrides
  parameters.value = Object.entries(overrides ?? {}).map(([name, value]) => ({ name, value }))
}
// isInput: an override in progress, or nothing discovered to show as text.
function isInput(key: string): boolean {
  return editing.value.has(key) || !automaticText(key)
}
async function override(key: string) {
  draft.value[key] = automatic(key)
  editing.value.add(key)
  await nextTick()
  document.getElementById(`oauth-${key}`)?.focus()
}
function resetField(key: string) {
  draft.value[key] = ''
  editing.value.delete(key)
  // The secret belongs to the hand-entered client ID.
  if (key === 'client_id') draft.value.client_secret = ''
}
const paramsAsInputs = computed(() => paramsOverridden.value || automaticParams.value.length === 0)
function overrideParameters() {
  parameters.value = automaticParams.value.map(([name, value]) => ({ name, value }))
  paramsOverridden.value = true
}
function resetParameters() {
  paramsOverridden.value = false
  parameters.value = []
}
function effectiveParameters() {
  return parameters.value.map(row => ({ name: row.name.trim(), value: row.value })).filter(row => row.name)
}
function desiredOverrides(): OAuthOverrides {
  const result: Record<string, unknown> = {}
  for (const field of fields) {
    const text = (draft.value[field.key] ?? '').trim()
    if (!text) continue
    result[field.key] = field.key === 'scopes' ? text.split(/\s+/) : text
  }
  const values = Object.fromEntries(effectiveParameters().map(row => [row.name, row.value]))
  if (paramsAsInputs.value && Object.keys(values).length) result.extra_params = values
  return result as OAuthOverrides
}
function canonical(value: unknown): string {
  if (value && typeof value === 'object' && !Array.isArray(value)) {
    return JSON.stringify(Object.fromEntries(Object.entries(value).sort(([a], [b]) => a.localeCompare(b))))
  }
  return JSON.stringify(value)
}
const patch = computed(() => {
  const desired = desiredOverrides()
  const original = props.overrides ?? {}
  const changes: Record<string, unknown> = {}
  for (const key of [...fields.map(field => field.key), 'extra_params'] as Array<keyof OAuthOverrides>) {
    if (canonical(desired[key]) !== canonical(original[key])) changes[key] = desired[key] ?? null
  }
  return changes
})
async function load() {
  const version = ++loadVersion
  const name = props.serverName
  loading.value = settings.value === null
  error.value = ''
  try {
    const discovered = await api.servers.oauthDiscovered(name)
    if (version !== loadVersion || name !== props.serverName) return
    settings.value = discovered
    resetDraft()
  } catch (cause) {
    if (version === loadVersion) error.value = cause instanceof Error ? cause.message : 'Could not load OAuth settings'
  } finally {
    if (version === loadVersion) loading.value = false
  }
}
// validate reports a problem the backend would refuse, or ''.
// required: an opened override replaces an automatic value, so it must not
// be empty. Without discovery, sign-in needs the authorization and token
// endpoints (a metadata URL supplies them), and registration needs its
// endpoint unless a client ID is entered.
function filled(key: string): boolean {
  return !!(draft.value[key] ?? '').trim()
}
function required(key: string): boolean {
  if (editing.value.has(key)) return true
  if (automaticText(key) || filled('auth_server_metadata_url')) return false
  if (key === 'authorization_endpoint' || key === 'token_endpoint') return true
  return key === 'registration_endpoint' && !filled('client_id')
}
const checked = ref(false)
function missing(key: string): boolean {
  return checked.value && required(key) && !filled(key)
}
function missingName(row: ParameterRow): boolean {
  return checked.value && !row.name.trim() && !!row.value.trim()
}
function validate(): string {
  checked.value = true
  const empty = visibleFields.value.find(field => missing(field.key))
  if (empty) {
    document.getElementById(`oauth-${empty.key}`)?.focus()
    return editing.value.has(empty.key) ? `${empty.label} is required, or reset it to automatic` : `${empty.label} is required`
  }
  if (parameters.value.some(missingName)) return 'Parameter name is required'
  const names = effectiveParameters().map(row => row.name)
  if (new Set(names).size !== names.length) return 'Parameter names must be unique'
  return ''
}
defineExpose({ patch, validate })
onMounted(() => { void load() })
watch(() => props.overrides, () => resetDraft())
watch(() => props.serverName, () => {
  loadVersion++
  settings.value = null
  secretVisible.value = false
  void load()
})
</script>

<template>
  <div>
      <div v-if="loading" class="grid gap-4" aria-label="Loading OAuth settings">
        <div v-for="index in 4" :key="index" class="grid gap-2"><Skeleton class="h-4 w-28" /><Skeleton class="h-9 w-full" /></div>
      </div>
      <div v-else-if="settings" class="grid gap-4">
        <p v-if="settings.discovery_error" class="text-xs text-muted-foreground">{{ settings.discovery_error }}</p>
        <FieldGroup class="gap-4">
          <Field v-for="field in visibleFields" :key="field.key" class="gap-2">
            <div class="flex min-h-6 min-w-0 items-center gap-2">
              <FieldLabel :for="isInput(field.key) ? `oauth-${field.key}` : undefined">{{ field.label }}<span v-if="isInput(field.key) && required(field.key)" class="text-destructive" aria-hidden="true">*</span></FieldLabel>
              <Button v-if="(editing.has(field.key) && automaticText(field.key)) || (!automaticText(field.key) && draft[field.key])" type="button" size="icon-xs" variant="ghost" class="ml-auto" :disabled="saving" :aria-label="`Reset ${field.label} to automatic`" title="Reset to automatic" @click="resetField(field.key)"><RotateCcw class="size-3.5" /></Button>
            </div>
            <div v-if="isInput(field.key)" class="flex min-w-0 items-center gap-2">
              <Input :id="`oauth-${field.key}`" v-model="draft[field.key]" :type="field.key === 'client_secret' && !secretVisible ? 'password' : 'text'" :placeholder="field.placeholder" :aria-invalid="missing(field.key) || undefined" class="min-w-0 font-mono text-xs" :disabled="saving" autocomplete="off" spellcheck="false" data-1p-ignore data-lpignore="true" />
              <Button v-if="field.key === 'client_secret'" type="button" size="icon-sm" variant="ghost" :aria-label="secretVisible ? 'Hide client secret' : 'Show client secret'" @click="secretVisible = !secretVisible"><EyeOff v-if="secretVisible" class="size-4" /><Eye v-else class="size-4" /></Button>
            </div>
            <button v-else type="button" class="self-start cursor-pointer text-left font-mono text-xs break-all underline decoration-dashed decoration-muted-foreground/60 underline-offset-4 hover:decoration-foreground disabled:pointer-events-none" :disabled="saving" :title="`Override ${field.label}`" @click="override(field.key)">{{ automaticText(field.key) }}</button>
          </Field>
          <Field class="gap-2">
            <div class="flex min-h-6 items-center gap-2">
              <FieldLabel>Extra parameters</FieldLabel>
              <Button v-if="paramsOverridden && (automaticParams.length || parameters.length)" type="button" size="icon-xs" variant="ghost" class="ml-auto" :disabled="saving" aria-label="Reset extra parameters to automatic" title="Reset to automatic" @click="resetParameters"><RotateCcw class="size-3.5" /></Button>
            </div>
            <template v-if="paramsAsInputs">
              <div v-for="(row, index) in parameters" :key="index" class="grid min-w-0 grid-cols-[minmax(0,1fr)_auto] gap-2">
                <div class="grid min-w-0 gap-2 sm:grid-cols-2">
                  <Input v-model="row.name" aria-label="Parameter name" placeholder="Name" :aria-invalid="missingName(row) || undefined" class="min-w-0 font-mono text-xs" :disabled="saving" @update:model-value="paramsOverridden = true" />
                  <Input v-model="row.value" aria-label="Parameter value" placeholder="Value" class="min-w-0 font-mono text-xs" :disabled="saving" autocomplete="off" data-1p-ignore @update:model-value="paramsOverridden = true" />
                </div>
                <Button type="button" size="icon-sm" variant="ghost" :disabled="saving" aria-label="Remove parameter" @click="parameters.splice(index, 1); paramsOverridden = true"><Trash2 class="size-4" /></Button>
              </div>
              <Button type="button" size="xs" variant="ghost" class="self-start" :disabled="saving" @click="parameters.push({name: '', value: ''}); paramsOverridden = true"><Plus class="size-3.5" />Add parameter</Button>
            </template>
            <button v-else type="button" class="self-start cursor-pointer text-left font-mono text-xs break-all underline decoration-dashed decoration-muted-foreground/60 underline-offset-4 hover:decoration-foreground disabled:pointer-events-none" :disabled="saving" title="Override extra parameters" @click="overrideParameters">
              <span v-for="([name, value], index) in automaticParams" :key="name" class="block" :class="index > 0 && 'mt-1'">{{ name }}={{ value }}</span>
            </button>
          </Field>
        </FieldGroup>
      </div>
      <p v-else-if="error" class="text-xs text-destructive" role="alert">{{ error }}</p>
  </div>
</template>
