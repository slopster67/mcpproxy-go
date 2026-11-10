<template>
  <Alert
    v-if="message"
    variant="destructive"
    class="flex items-start gap-3 border-destructive/30"
    role="alert"
  >
    <span class="mt-0.5 shrink-0" aria-hidden="true"
      ><CircleAlert class="size-4"
    /></span>
    <div class="min-w-0 flex-1 text-sm [overflow-wrap:anywhere]">
      <p
        class="whitespace-pre-wrap break-words font-medium"
      >
        {{ message }}
      </p>
      <p
        v-if="stderr"
        class="mt-1 whitespace-pre-wrap break-words font-normal"
      >
        {{ stderr }}
      </p>
    </div>
  </Alert>
</template>
<script setup lang="ts">
import { computed } from "vue";
import { CircleAlert } from "@lucide/vue";
import { Alert } from "@/components/ui/alert";
import type { Diagnostic } from "@/types";
import { capturedStderr } from "@/utils/processError";
const props = defineProps<{ diagnostic?: Diagnostic | null; error?: string }>();
const processFailed = computed(
  () =>
    props.diagnostic?.code === "MCPX_STDIO_EXIT_BEFORE_INITIALIZE" ||
    props.diagnostic?.code === "MCPX_STDIO_EXIT_NONZERO",
);
const stderr = computed(
  () => capturedStderr(props.error) || capturedStderr(props.diagnostic?.cause),
);
const message = computed(() =>
  processFailed.value
    ? "The process failed"
    : props.diagnostic?.user_message?.trim() ||
      props.error?.trim() ||
      props.diagnostic?.cause?.trim() ||
      "",
);
</script>
