<script setup lang="ts">
import { ref } from "vue";
import { Upload, Trash2 } from "@lucide/vue";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Spinner } from "@/components/ui/spinner";
import { useSystemStore } from "@/stores/system";
import { Button } from "@/components/ui/button";
import ServerIcon from "@/components/ServerIcon.vue";
import { useServersStore } from "@/stores/servers";

const props = defineProps<{ name: string; src?: string; uploaded?: boolean; disabled?: boolean }>();
const servers = useServersStore();
const input = ref<HTMLInputElement>();
const saving = ref(false);
const system = useSystemStore();
function showError(message: string) { system.addToast({ type: "error", title: message }); }
function pickFile() { input.value?.click(); }

async function save(icon: string) {
  const name = props.name;
  saving.value = true;
  try {
    await servers.patch(name, { icon });
  } catch (err) {
    showError(err instanceof Error ? err.message : "Could not update icon");
  } finally {
    saving.value = false;
    if (input.value) input.value.value = "";
  }
}

async function upload(event: Event) {
  const file = (event.target as HTMLInputElement).files?.[0];
  if (!file) return;
  if (file.size > 64 * 1024) { showError("Icon must be 64 KB or smaller"); if (input.value) input.value.value = ""; return; }
  const png = file.type === "image/png" || /\.png$/i.test(file.name);
  const svg = file.type === "image/svg+xml" || /\.svg$/i.test(file.name);
  if (!png && !svg) { showError("Choose an SVG or PNG image"); if (input.value) input.value.value = ""; return; }
  try {
    const icon = png ? await new Promise<string>((resolve, reject) => {
      const reader = new FileReader();
      reader.onload = () => resolve(String(reader.result));
      reader.onerror = () => reject(reader.error);
      reader.readAsDataURL(new Blob([file], { type: "image/png" }));
    }) : await file.text();
    await save(icon);
  } catch { showError("Could not read image"); }
  finally { if (input.value) input.value.value = ""; }
}
</script>

<template>
  <span class="inline-flex shrink-0 items-center">
    <input ref="input" type="file" accept=".svg,.png,image/svg+xml,image/png" class="hidden" aria-label="Upload server icon" :disabled="disabled || saving" @change="upload" />
    <DropdownMenu v-if="src">
      <DropdownMenuTrigger as-child>
        <Button type="button" variant="ghost" size="icon-sm" class="size-7 rounded-md" :disabled="disabled || saving" aria-label="Edit server icon">
          <Spinner v-if="saving" class="size-5" />
          <ServerIcon v-else :name="name" :src="src" class="size-5" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start">
        <DropdownMenuItem @select="pickFile"><Upload />Replace</DropdownMenuItem>
        <DropdownMenuItem v-if="uploaded" variant="destructive" @select="save('')"><Trash2 />Remove</DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
    <Button v-else type="button" variant="ghost" size="icon-sm" class="size-7 rounded-md" :disabled="disabled || saving" aria-label="Upload server icon" @click="pickFile">
      <Spinner v-if="saving" class="size-5" />
      <span v-else class="size-5 rounded-sm border border-dashed border-muted-foreground/60" aria-hidden="true" />
    </Button>
  </span>
</template>
