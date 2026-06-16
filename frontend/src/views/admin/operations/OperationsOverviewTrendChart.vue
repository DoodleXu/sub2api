<template>
  <div class="rounded-lg border border-gray-100 bg-white p-5 dark:border-dark-700 dark:bg-dark-900">
    <div class="flex items-start justify-between gap-3">
      <div>
        <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.operations.apiActivityTrend') }}</h2>
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.operations.apiActivityTrendHint') }}</p>
      </div>
    </div>
    <div class="mt-4 space-y-3">
      <div
        class="flex flex-wrap items-center gap-x-5 gap-y-2 text-xs text-gray-600 dark:text-gray-300"
      >
        <div class="flex items-center gap-2">
          <span class="h-1 w-5 rounded-full bg-sky-500"></span>
          <span>{{ t('admin.operations.apiDau') }}</span>
        </div>
        <div class="flex items-center gap-2">
          <span class="h-1 w-5 rounded-full bg-amber-500"></span>
          <span>{{ t('admin.operations.newUsers') }}</span>
        </div>
      </div>
      <div class="h-64 min-w-0">
        <div v-if="loading" class="flex h-full items-center justify-center text-sm text-gray-500 dark:text-gray-400">
          {{ t('common.loading') }}
        </div>
        <div v-else-if="!chartData" class="flex h-full items-center justify-center text-sm text-gray-500 dark:text-gray-400">
          {{ t('admin.operations.noTrendData') }}
        </div>
        <Line v-else :data="chartData" :options="chartOptions" />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Chart as ChartJS, CategoryScale, Legend, LineElement, LinearScale, PointElement, Tooltip } from 'chart.js'
import { Line } from 'vue-chartjs'
import type { OperationsOverviewPoint } from '@/api/admin/operations'

ChartJS.register(CategoryScale, LinearScale, PointElement, LineElement, Tooltip, Legend)

const props = defineProps<{
  points: OperationsOverviewPoint[]
  loading: boolean
}>()

const { t } = useI18n()

const chartData = computed(() => {
  if (!props.points.length || props.points.every((point) => point.dau === 0 && point.new_users === 0)) {
    return null
  }
  return {
    labels: props.points.map((point) => point.date.slice(5)),
    datasets: [
      {
        label: t('admin.operations.apiDau'),
        data: props.points.map((point) => point.dau),
        borderColor: '#0ea5e9',
        backgroundColor: 'rgba(14, 165, 233, 0.12)',
        borderWidth: 3.5,
        tension: 0.35,
        pointRadius: 0,
        pointHoverRadius: 4,
        pointBackgroundColor: '#ffffff',
        pointBorderColor: '#0ea5e9',
        pointBorderWidth: 2,
        pointHitRadius: 10,
        fill: true,
      },
      {
        label: t('admin.operations.newUsers'),
        data: props.points.map((point) => point.new_users),
        borderColor: '#f59e0b',
        backgroundColor: 'rgba(245, 158, 11, 0.12)',
        borderWidth: 3.5,
        tension: 0.35,
        pointRadius: 0,
        pointHoverRadius: 4,
        pointBackgroundColor: '#ffffff',
        pointBorderColor: '#f59e0b',
        pointBorderWidth: 2,
        pointHitRadius: 10,
      },
    ],
  }
})

const chartOptions = computed(() => ({
  responsive: true,
  maintainAspectRatio: false,
  interaction: { intersect: false, mode: 'index' as const },
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
        label: (context: any) => `${context.dataset.label}: ${Number(context.raw || 0).toLocaleString()}`,
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
  },
}))
</script>
