<template>
  <section class="rounded-lg border border-gray-200 p-3 dark:border-dark-600" data-testid="account-quality-panel">
    <div class="flex items-center justify-between gap-3 text-sm">
      <h3 class="font-medium">{{ t('admin.accounts.quality.title') }}</h3>
      <span class="badge badge-primary text-xs">
        {{ t(`admin.accounts.quality.${quality.strategy === 'health_first' ? 'healthFirst' : 'legacy'}`) }}
      </span>
    </div>
    <div v-if="quality.auto_pause" class="mt-2 text-xs text-red-600 dark:text-red-400">
      <p>{{ quality.auto_pause.message }}</p>
      <p>{{ t('admin.accounts.quality.manualResume') }}</p>
    </div>
    <p v-if="!quality.models?.length" class="mt-2 text-xs text-gray-500">{{ t('admin.accounts.quality.empty') }}</p>
    <div v-else class="mt-3 max-h-64 overflow-auto">
      <table class="w-full text-left text-xs">
        <thead class="text-gray-500">
          <tr>
            <th class="p-2">{{ t('admin.accounts.quality.model') }}</th>
            <th class="p-2">{{ t('admin.accounts.quality.state') }}</th>
            <th class="p-2">{{ t('admin.accounts.quality.samples') }}</th>
            <th class="p-2">{{ t('admin.accounts.quality.errorRate') }}</th>
            <th class="p-2">{{ t('admin.accounts.quality.latency') }}</th>
            <th class="p-2">{{ t('admin.accounts.quality.updated') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(model, index) in quality.models" :key="index" class="border-t border-gray-100 dark:border-dark-600">
            <td class="p-2">
              <p class="break-all">{{ model.model || t('admin.accounts.quality.account') }}</p>
              <p class="text-gray-500">{{ [model.capability, model.transport, model.effort].filter(Boolean).join(' · ') }}</p>
            </td>
            <td class="whitespace-nowrap p-2">{{ t(`admin.accounts.quality.${model.state}`) }}</td>
            <td class="p-2">{{ model.samples }}<span class="block whitespace-nowrap text-gray-500">{{ t(`admin.accounts.quality.${model.window}`) }}</span></td>
            <td class="p-2">{{ model.error_rate == null ? t('admin.accounts.quality.unknown') : `${(model.error_rate * 100).toFixed(1)}%` }}</td>
            <td class="whitespace-nowrap p-2">{{ latency(model) }}</td>
            <td class="whitespace-nowrap p-2">{{ model.updated_at ? new Date(model.updated_at).toLocaleString() : '—' }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { AccountQualityScheduling, AccountQualitySummary } from '@/types'

defineProps<{ quality: AccountQualityScheduling }>()
const { t } = useI18n()
const latency = (model: AccountQualitySummary): string => {
  if (model.p50_ms == null || model.p95_ms == null) return t('admin.accounts.quality.noLatency')
  return `${(model.p50_ms / 1000).toFixed(2)}s / ${(model.p95_ms / 1000).toFixed(2)}s`
}
</script>
