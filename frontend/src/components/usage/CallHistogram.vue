<template>
  <div class="min-w-0 [&_canvas]:max-w-full">
    <div class="flex items-center justify-between mb-2">
      <h3 class="font-semibold text-sm">Calls per tool</h3>
      <span class="text-xs text-muted-foreground"
        >{{ charted.length }} tool{{ charted.length === 1 ? "" : "s" }}</span
      >
    </div>
    <div
      v-if="charted.length === 0"
      class="text-sm text-muted-foreground py-8 text-center"
    >
      No completed tool calls in this window.
    </div>
    <div v-else class="relative" :style="{ height: chartHeight }">
      <Bar :data="chartData" :options="chartOptions" />
    </div>
    <!--
      Names that came from failed calls are not a catalog. They are
      named here, once, so they are not hidden either — the Errors & latency
      chart below still charts them.
    -->
    <!-- This group is titled "Calls to unknown tools" — a
         name that never completed a call is not a tool this proxy can vouch
         for, so it stays out of the ranking above, but the
         calls made to it are still named and counted rather than vanishing
         silently. -->
    <p
      v-if="unresolved.length > 0"
      class="text-xs text-muted-foreground mt-2"
      :title="unresolvedLabels"
    >
      <span class="font-medium">Calls to unknown tools:</span>
      {{ unresolved.length }} name{{ unresolved.length === 1 ? "" : "s" }} never
      completed a call and {{ unresolved.length === 1 ? "is" : "are" }} excluded
      from this ranking — see “Errors &amp; latency”.
    </p>
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
  formatNumber,
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

// Link map "Usage chart bar (tool
// x bucket)" -> the calls behind the bar. Chart.js bars are canvas-rendered,
// not DOM elements a template can put a router-link on, so the click is
// wired through the chart's own onClick and re-emitted as a plain tool
// selection — Usage.vue (which knows the active window/status) turns that
// into the actual `/activity?...` navigation.
const emit = defineEmits<{ (e: "select-tool", tool: UsageToolStat): void }>();

// This chart answers "what do my agents use". A name that has never once
// completed a call answers nothing about use — it is a typo the agent made or a
// server that was never reachable, and charting it invents a tool catalog out
// of failures.
const split = computed(() => partitionUsageTools(props.tools));
const charted = computed(() => split.value.completed);
const unresolved = computed(() => split.value.unresolved);
const unresolvedLabels = computed(() =>
  unresolved.value.map((t) => toolLabel(t.server, t.tool)).join(", "),
);

// Horizontal bars so long server:tool labels stay readable on high cardinality.
const chartHeight = computed(
  () => `${Math.max(160, charted.value.length * 28 + 40)}px`,
);

const chartData = computed(() => ({
  labels: charted.value.map((t) => toolLabel(t.server, t.tool)),
  datasets: [
    {
      label: "Calls",
      data: charted.value.map((t) => t.calls),
      backgroundColor: charted.value.map((_, i) => paletteColor(i)),
      borderWidth: 0,
      borderRadius: 3,
    },
  ],
}));

const chartOptions = computed<ChartOptions<"bar">>(() => ({
  indexAxis: "y",
  responsive: true,
  maintainAspectRatio: false,
  onClick: (_event, elements) => {
    const el = elements[0];
    const tool = el ? charted.value[el.index] : undefined;
    if (tool) emit("select-tool", tool);
  },
  onHover: (event, elements) => {
    const target = event.native?.target as HTMLElement | undefined;
    if (target)
      target.style.cursor = elements.length > 0 ? "pointer" : "default";
  },
  plugins: {
    legend: { display: false },
    tooltip: {
      callbacks: {
        label: (ctx) => {
          const t = charted.value[ctx.dataIndex];
          if (!t) return "";
          const errPart =
            t.errors > 0 ? ` · ${formatNumber(t.errors)} errors` : "";
          return `${formatNumber(t.calls)} calls${errPart}`;
        },
      },
    },
  },
  scales: {
    // Say what the bars measure. An unlabelled axis is guesswork.
    x: {
      beginAtZero: true,
      title: { display: true, text: "Calls (cumulative)" },
      // A call count is never fractional — force integer
      // ticks rather than letting chart.js round a small max into 0.5/1.5s.
      ticks: { precision: 0, callback: (v) => formatNumber(Number(v)) },
    },
    y: { ticks: { autoSkip: false, font: { size: 11 } } },
  },
}));
</script>
