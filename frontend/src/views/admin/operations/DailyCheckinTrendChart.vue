<template>
  <div class="rounded-lg border border-gray-100 bg-white p-5 dark:border-dark-700 dark:bg-dark-900">
    <div>
      <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.operations.checkinTrend') }}</h2>
      <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.operations.checkinTrendHint') }}</p>
    </div>
    <div class="mt-4 space-y-3">
      <div
        class="flex flex-wrap items-center gap-x-5 gap-y-2 text-xs text-gray-600 dark:text-gray-300"
      >
        <div class="flex items-center gap-2">
          <span class="h-2.5 w-2.5 rounded-full bg-sky-500"></span>
          <span>{{ t('admin.operations.checkinUsers') }}</span>
        </div>
        <div class="flex items-center gap-2">
          <span class="h-0.5 w-5 rounded-full bg-emerald-500"></span>
          <span>{{ t('admin.operations.checkinRate') }}</span>
        </div>
      </div>
      <div class="h-64 min-w-0">
        <div v-if="loading" class="flex h-full items-center justify-center text-sm text-gray-500 dark:text-gray-400">
          {{ t('common.loading') }}
        </div>
        <div v-else-if="!chartData" class="flex h-full items-center justify-center text-sm text-gray-500 dark:text-gray-400">
          {{ t('admin.operations.noTrendData') }}
        </div>
        <Chart v-else type="bar" :data="chartData" :options="chartOptions" />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { BarElement, CategoryScale, Chart as ChartJS, Legend, LineElement, LinearScale, PointElement, Tooltip } from 'chart.js'
import { Chart } from 'vue-chartjs'
import type { DailyCheckinAnalyticsPoint } from '@/api/admin/operations'

ChartJS.register(CategoryScale, LinearScale, BarElement, LineElement, PointElement, Tooltip, Legend)

const props = defineProps<{
  points: DailyCheckinAnalyticsPoint[]
  loading: boolean
}>()

const { t } = useI18n()

const chartData = computed(() => {
  if (!props.points.length || props.points.every((point) => point.checkin_users === 0 && point.qualified_users === 0)) {
    return null
  }
  return {
    labels: props.points.map((point) => point.date.slice(5)),
    datasets: [
      {
        type: 'bar' as const,
        label: t('admin.operations.checkinUsers'),
        data: props.points.map((point) => point.checkin_users),
        backgroundColor: 'rgba(14, 165, 233, 0.46)',
        hoverBackgroundColor: 'rgba(14, 165, 233, 0.66)',
        borderColor: 'rgba(2, 132, 199, 0.75)',
        borderWidth: 1,
        borderRadius: 5,
        borderSkipped: false,
        barPercentage: 0.72,
        categoryPercentage: 0.72,
        maxBarThickness: 28,
        yAxisID: 'y',
      },
      {
        type: 'line' as const,
        label: t('admin.operations.checkinRate'),
        data: props.points.map((point) => Math.round((point.checkin_rate || 0) * 1000) / 10),
        borderColor: '#10b981',
        backgroundColor: 'rgba(16, 185, 129, 0.14)',
        borderWidth: 2.5,
        tension: 0.35,
        pointRadius: 2,
        pointHoverRadius: 4,
        pointBackgroundColor: '#ffffff',
        pointBorderColor: '#10b981',
        pointBorderWidth: 2,
        pointHitRadius: 10,
        clip: false as const,
        yAxisID: 'y1',
      },
    ],
  }
})

const chartOptions = computed(() => ({
  responsive: true,
  maintainAspectRatio: false,
  interaction: { intersect: false, mode: 'index' as const },
  layout: {
    padding: {
      top: 12,
    },
  },
  plugins: {
    legend: {
      display: false,
    },
    tooltip: {
      backgroundColor: 'rgba(15, 23, 42, 0.92)',
      borderColor: 'rgba(148, 163, 184, 0.2)',
      borderWidth: 1,
      padding: 10,
      titleFont: { size: 12, weight: 'bold' as const },
      bodyFont: { size: 12 },
      callbacks: {
        label: (context: any) => {
          const value = Number(context.raw || 0)
          return context.dataset.yAxisID === 'y1'
            ? `${context.dataset.label}: ${value.toFixed(1)}%`
            : `${context.dataset.label}: ${value.toLocaleString()}`
        },
      },
    },
  },
  scales: {
    x: {
      grid: { display: false },
      border: { display: false },
      ticks: {
        color: '#64748b',
        autoSkip: true,
        maxTicksLimit: 12,
        maxRotation: 45,
        minRotation: 0,
      },
    },
    y: {
      beginAtZero: true,
      border: { display: false },
      grid: { color: 'rgba(148, 163, 184, 0.16)' },
      ticks: { precision: 0, color: '#64748b', padding: 8 },
    },
    y1: {
      beginAtZero: true,
      max: 100,
      position: 'right' as const,
      grid: { drawOnChartArea: false },
      border: { display: false },
      ticks: { color: '#64748b', padding: 8, callback: (value: string | number) => `${value}%` },
    },
  },
}))
</script>
