<template>
  <BaseDialog :show="show" :title="t('admin.accounts.routingPriority.modal.title')" width="wide" @close="$emit('close')">
    <div v-if="loading" class="py-8 text-center text-sm text-gray-500">{{ t('common.loading') }}</div>
    <div v-else-if="!explain" class="py-8 text-center text-sm text-gray-500">{{ t('admin.accounts.routingPriority.modal.empty') }}</div>
    <div v-else class="space-y-4">
      <section class="rounded-lg border border-gray-200 px-4 py-3 dark:border-dark-600">
        <div class="flex items-start justify-between gap-3">
          <div>
            <div class="text-sm font-semibold text-gray-900 dark:text-white">{{ explain.account.account_name }}</div>
            <div class="mt-1 flex flex-wrap gap-2 text-xs text-gray-500">
              <span>#{{ explain.account.account_id }}</span>
              <span>{{ explain.account.quality_grade }}</span>
              <span>{{ explain.account.quality_score }}</span>
              <span>{{ translate('admin.accounts.routingPriority.summary', explain.account.summary_reason) }}</span>
            </div>
          </div>
          <div class="rounded-md bg-gray-50 px-3 py-2 text-right dark:bg-dark-700">
            <div class="text-[11px] text-gray-500">{{ t('admin.accounts.routingPriority.score.total') }}</div>
            <div class="text-sm font-semibold">{{ format(explain.account.score.total) }}</div>
          </div>
        </div>
      </section>

      <section>
        <div class="mb-2 text-xs font-semibold text-gray-700 dark:text-gray-200">{{ t('admin.accounts.routingPriority.sections.score') }}</div>
        <div class="grid grid-cols-2 gap-2 md:grid-cols-4">
          <div v-for="item in scoreItems" :key="item.key" class="rounded-md border border-gray-200 px-3 py-2 dark:border-dark-600">
            <div class="text-[11px] text-gray-500">{{ item.label }}</div>
            <div class="mt-1 text-sm font-semibold">{{ format(item.value) }}</div>
          </div>
        </div>
      </section>

      <section v-if="explain.account.block_reasons?.length">
        <div class="mb-2 text-xs font-semibold text-gray-700 dark:text-gray-200">{{ t('admin.accounts.routingPriority.sections.blockReasons') }}</div>
        <div class="flex flex-wrap gap-2">
          <span v-for="reason in explain.account.block_reasons" :key="reason" class="rounded-md border border-amber-200 bg-amber-50 px-2 py-1 text-xs text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200">
            {{ translate('admin.accounts.routingPriority.summary', reason) }}
          </span>
        </div>
      </section>

      <section v-if="explain.top.length">
        <div class="mb-2 text-xs font-semibold text-gray-700 dark:text-gray-200">{{ t('admin.accounts.routingPriority.sections.topCandidates') }}</div>
        <div class="space-y-2">
          <div v-for="row in explain.top" :key="row.account_id" class="flex items-center justify-between rounded-md border border-gray-200 px-3 py-2 text-xs dark:border-dark-600">
            <span class="truncate">{{ row.rank ? `#${row.rank}` : '-' }} {{ row.account_name }}</span>
            <span class="font-mono">{{ format(row.score.total) }}</span>
          </div>
        </div>
      </section>
    </div>
    <template #footer>
      <button class="btn btn-secondary" type="button" @click="$emit('close')">{{ t('common.close') }}</button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import type { OpenAIRoutingAccountExplain } from '@/types'

const props = defineProps<{ show: boolean; loading?: boolean; explain: OpenAIRoutingAccountExplain | null }>()
defineEmits<{ close: [] }>()
const { t, te } = useI18n()
const scoreKeys = ['total', 'quality', 'price', 'latency', 'error_rate', 'priority', 'load', 'queue'] as const

const translate = (prefix: string, value: string) => {
  const key = `${prefix}.${value}`
  return te(key) ? t(key) : value
}

const scoreItems = computed(() => {
  const score = props.explain?.account.score
  if (!score) return []
  return scoreKeys.map((key) => ({
    key,
    label: t(`admin.accounts.routingPriority.score.${key}`),
    value: score[key],
  }))
})

const format = (value: number) => Number.isFinite(value) ? value.toFixed(3) : '-'
</script>
