<template>
  <div
    class="min-w-0 [&_canvas]:max-w-full"
  >
    <div class="flex items-center justify-between mb-1">
      <h3 class="font-semibold text-sm">Response size</h3>
      <span class="text-xs text-muted-foreground">bytes</span>
    </div>
    <p class="text-xs text-muted-foreground mb-2">
      Total response bytes per tool.
    </p>
    <div
      v-if="ranked.length === 0"
      class="text-sm text-muted-foreground py-8 text-center"
    >
      No sized responses in this window.
    </div>
    <div v-else class="relative" :style="{ height: chartHeight }">
      <Bar :data="chartData" :options="chartOptions" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { Bar } from "vue-chartjs";
import {
  Chart as ChartJS,
  BarElement,
  CategoryScale,
  LinearScale,
  Tooltip,
  Legend,
} from "chart.js";
import type { ChartOptions } from "chart.js";
import type { UsageToolStat } from "@/types";
import {
  formatBytes,
  partitionUsageTools,
  toolLabel,
  paletteColor,
  themeColor,
} from "@/utils/usageFormat";

ChartJS.register(BarElement, CategoryScale, LinearScale, Tooltip, Legend);
ChartJS.defaults.font.family = '"Geist Variable", sans-serif';
ChartJS.defaults.color = themeColor("muted-foreground");
ChartJS.defaults.borderColor = themeColor("border");

const props = defineProps<{ tools: UsageToolStat[] }>();

// Rank by total response bytes descending; drop tools with no sized output, and
// tools that never completed a call. An error message is a response with bytes,
// so without the second filter a name that only ever failed — an agent's typo,
// an unreachable server — ranked as a "token sink".
const ranked = computed(() =>
  partitionUsageTools(props.tools)
    .completed.filter((t) => t.total_resp_bytes > 0)
    .sort((a, b) => b.total_resp_bytes - a.total_resp_bytes),
);

const chartHeight = computed(
  () => `${Math.max(160, ranked.value.length * 28 + 40)}px`,
);

const chartData = computed(() => ({
  labels: ranked.value.map((t) => toolLabel(t.server, t.tool)),
  datasets: [
    {
      label: "Total response bytes",
      data: ranked.value.map((t) => t.total_resp_bytes),
      backgroundColor: ranked.value.map((_, i) => paletteColor(i)),
      borderWidth: 0,
      borderRadius: 3,
    },
  ],
}));

const chartOptions = computed<ChartOptions<"bar">>(() => ({
  indexAxis: "y",
  responsive: true,
  maintainAspectRatio: false,
  plugins: {
    legend: { display: false },
    tooltip: {
      callbacks: {
        label: (ctx) => {
          const t = ranked.value[ctx.dataIndex];
          if (!t) return "";
          const avg =
            t.avg_resp_bytes != null
              ? ` · avg ${formatBytes(t.avg_resp_bytes)}`
              : "";
          return `${formatBytes(t.total_resp_bytes)} total${avg}`;
        },
      },
    },
  },
  scales: {
    x: {
      beginAtZero: true,
      title: { display: true, text: "Total response bytes" },
      ticks: { callback: (v) => formatBytes(Number(v)) },
    },
    y: { ticks: { autoSkip: false, font: { size: 11 } } },
  },
}));
</script>
