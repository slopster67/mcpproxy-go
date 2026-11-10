<template>
  <div class="min-w-0 [&_canvas]:max-w-full">
    <h3 class="font-semibold text-sm mb-2">Errors &amp; latency</h3>
    <div
      v-if="tools.length === 0"
      class="text-sm text-muted-foreground py-8 text-center"
    >
      No tool calls in this window.
    </div>
    <template v-else>
      <!-- Error rate per tool. The bars and the table below measure DIFFERENT
           things; each says which. -->
      <p class="text-xs text-muted-foreground mb-1">Failed calls per tool</p>
      <div class="relative mb-4" :style="{ height: chartHeight }">
        <Bar :data="chartData" :options="chartOptions" />
      </div>
      <!-- Per-tool p50/p95 latency (FR: tail-latency visibility, T019) -->
      <p class="text-xs text-muted-foreground mb-1">
        p50 and p95 show latency histogram bounds.
      </p>
      <div class="overflow-x-auto">
        <Table
          class="w-full caption-bottom text-sm text-xs [&_td]:py-1.5"
        >
          <TableHeader>
            <TableRow>
              <TableHead>Tool</TableHead>
              <TableHead
                class="text-right"
                title="Median latency, bounded by its histogram bucket"
                >p50</TableHead
              >
              <TableHead
                class="text-right"
                title="95th-percentile latency, bounded by its histogram bucket"
                >p95</TableHead
              >
              <TableHead
                class="text-right"
                title="Share of this tool's calls that failed"
                >Err%</TableHead
              >
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow v-for="t in latencyRows" :key="`${t.server}:${t.tool}`">
              <TableCell class="font-mono text-xs truncate max-w-[12rem]">
                {{ toolLabel(t.server, t.tool) }}
                <!-- A name that never completed a call has no latency to report
                     and is not evidence that such a tool exists. -->
                <span
                  v-if="neverCompleted(t)"
                  class="text-muted-foreground"
                  title="Never completed a call — no successful response was ever recorded for this name"
                >
                  ·&nbsp;unresolved
                </span>
              </TableCell>
              <TableCell class="text-right font-mono text-xs">{{
                formatLatencyBound(t.p50_ms, t.p50_exceeds)
              }}</TableCell>
              <TableCell class="text-right font-mono text-xs">{{
                formatLatencyBound(t.p95_ms, t.p95_exceeds)
              }}</TableCell>
              <TableCell
                class="text-right font-mono text-xs"
                :class="
                  t.error_rate > 0
                    ? 'text-destructive'
                    : 'text-muted-foreground'
                "
              >
                {{ (t.error_rate * 100).toFixed(1) }}%
              </TableCell>
            </TableRow>
          </TableBody>
        </Table>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { themeColor } from "@/utils/usageFormat";
import {
  Table,
  TableHeader,
  TableRow,
  TableHead,
  TableBody,
  TableCell,
} from "@/components/ui/table";
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
  formatLatencyBound,
  neverCompleted,
  toolLabel,
} from "@/utils/usageFormat";

ChartJS.register(BarElement, CategoryScale, LinearScale, Tooltip, Legend);
ChartJS.defaults.font.family = '"Geist Variable", sans-serif';
ChartJS.defaults.color = themeColor("muted-foreground");
ChartJS.defaults.borderColor = themeColor("border");

const props = defineProps<{ tools: UsageToolStat[] }>();

const chartHeight = computed(
  () => `${Math.max(140, props.tools.length * 24 + 40)}px`,
);

// Worst-offenders first in the latency table; cap the visible rows.
const latencyRows = computed(() =>
  [...props.tools].sort((a, b) => b.p95_ms - a.p95_ms).slice(0, 12),
);

const chartData = computed(() => ({
  labels: props.tools.map((t) => toolLabel(t.server, t.tool)),
  datasets: [
    {
      label: "Error rate %",
      data: props.tools.map((t) => +(t.error_rate * 100).toFixed(2)),
      backgroundColor: props.tools.map((t) =>
        t.error_rate > 0 ? themeColor("destructive") : themeColor("chart-3"),
      ),
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
          const t = props.tools[ctx.dataIndex];
          if (!t) return "";
          return `${(t.error_rate * 100).toFixed(1)}% (${formatNumber(t.errors)}/${formatNumber(t.calls)})`;
        },
      },
    },
  },
  scales: {
    // The bars are an error RATE and the table beneath them is a latency; with
    // no axis title the two read as one chart.
    x: {
      beginAtZero: true,
      max: 100,
      title: { display: true, text: "Error rate (% of calls)" },
      ticks: { callback: (v) => `${v}%` },
    },
    y: { ticks: { autoSkip: false, font: { size: 11 } } },
  },
}));
</script>
