<template>
  <Dialog :open="show" @update:open="open => { if (!open) handleClose() }">
    <DialogContent class="max-h-[calc(100dvh-2rem)] overflow-y-auto" @open-auto-focus="focusInitialField" @close-auto-focus="clearClosedValue">
      <DialogHeader>
        <DialogTitle>{{ fixedName ? "Set Secret Value" : "Add New Secret" }}</DialogTitle>
      </DialogHeader>
      <form class="flex flex-col gap-6" @submit.prevent="handleSubmit">
        <FieldGroup>
          <Field>
            <FieldLabel id="add-secret-name-label" :for="fixedName ? undefined : 'add-secret-name'">Secret Name</FieldLabel>
            <InputGroup v-if="fixedName" id="add-secret-name" aria-labelledby="add-secret-name-label" aria-disabled="true">
              <span class="truncate px-3 text-sm text-muted-foreground">{{ fixedName }}</span>
            </InputGroup>
            <Input v-else id="add-secret-name" v-model="formData.name" placeholder="e.g., my-api-key" required />
            <FieldDescription v-if="!fixedName">Use only letters, numbers, and hyphens</FieldDescription>
          </Field>
          <Field>
            <FieldLabel for="add-secret-value">Secret Value</FieldLabel>
            <InputGroup>
              <InputGroupInput ref="valueInput" id="add-secret-value" :type="valueVisible ? 'text' : 'password'" v-model="formData.value" placeholder="Enter secret value" autocomplete="off" spellcheck="false" required />
              <InputGroupAddon align="inline-end">
                <InputGroupButton size="icon-xs" :aria-label="valueVisible ? 'Hide secret value' : 'Show secret value'" :aria-pressed="valueVisible" @click="valueVisible = !valueVisible">
                  <EyeOff v-if="valueVisible" />
                  <Eye v-else />
                </InputGroupButton>
              </InputGroupAddon>
            </InputGroup>
          </Field>
        </FieldGroup>
        <div v-if="formData.name" class="flex flex-col gap-1 text-sm">
          <span class="text-muted-foreground">Configuration reference</span>
          <code>${secret:{{ formData.name }}}</code>
        </div>
        <Alert v-if="error" variant="destructive" role="alert">{{ error }}</Alert>
        <DialogFooter>
          <Button type="button" variant="outline" @click="handleClose">Cancel</Button>
          <Button type="submit" :disabled="loading || !formData.name || !formData.value">
            <Spinner v-if="loading" data-icon="inline-start" />
            {{ loading ? "Adding..." : "Add secret" }}
          </Button>
        </DialogFooter>
      </form>
    </DialogContent>
  </Dialog>
</template>

<script setup lang="ts">
import { Spinner } from "@/components/ui/spinner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { InputGroup, InputGroupInput, InputGroupAddon, InputGroupButton } from "@/components/ui/input-group";
import { Eye, EyeOff } from "@lucide/vue";
import { Alert } from "@/components/ui/alert";
import { reactive, ref, watch } from "vue";
import { useSecretsStore } from "@/stores/secrets";
import { useSystemStore } from "@/stores/system";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from "@/components/ui/dialog";
import { Field, FieldGroup, FieldLabel, FieldDescription } from "@/components/ui/field";


interface Props {
  show: boolean;
  predefinedName?: string;
}

interface Emits {
  (e: "close"): void;
  (e: "added"): void;
}

const props = defineProps<Props>();
const emit = defineEmits<Emits>();

const systemStore = useSystemStore();
const secrets = useSecretsStore();

const formData = reactive({
  name: "",
  value: "",
});

const loading = ref(false);
const error = ref("");
const fixedName = ref<string | undefined>(undefined);
const valueInput = ref<InstanceType<typeof InputGroupInput> | null>(null);
const valueVisible = ref(false);

function focusInitialField(event: Event) {
  if (!fixedName.value) return;
  event.preventDefault();
  valueInput.value?.$el.focus();
}

// Keep the active form intact during the exit animation. Initialize each new
// opening, rather than changing the form as its parent clears the dialog props.
watch(
  () => [props.show, props.predefinedName] as const,
  ([open, name]) => {
    if (!open) return;
    fixedName.value = name;
    formData.name = name ?? "";
    formData.value = "";
    valueVisible.value = false;
    error.value = "";
  },
  { immediate: true },
);

function clearClosedValue() {
  if (!props.show) {
    formData.value = "";
    valueVisible.value = false;
  }
}

async function handleSubmit() {
  error.value = "";
  loading.value = true;

  try {
    const entry = await secrets.set(formData.name, formData.value);
    systemStore.addToast({
      type: "success",
      title: "Secret Added",
      message: `${formData.name} has been added successfully. Use in config: ${entry.reference}`,
    });
    emit("added");
    handleClose();
  } catch (err) {
    error.value = err instanceof Error ? err.message : "Failed to add secret";
  } finally {
    loading.value = false;
  }
}

function handleClose() {
  emit("close");
}
</script>
