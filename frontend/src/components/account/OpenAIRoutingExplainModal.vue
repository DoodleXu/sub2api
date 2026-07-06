<template>
  <BaseDialog :show="show" :title="modalTitle" width="wide" @close="$emit('close')">
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
            <template v-if="isStrictPriority">
              <div class="text-[11px] text-gray-500">{{ t('admin.accounts.routingPriority.strict.priorityLabel') }}</div>
              <div class="text-sm font-semibold">{{ t('admin.accounts.routingPriority.strict.priority', { priority: explain.account.priority }) }}</div>
            </template>
            <template v-else>
              <div class="text-[11px] text-gray-500">{{ t('admin.accounts.routingPriority.score.total') }}</div>
              <div class="text-sm font-semibold">{{ format(explain.account.score.total) }}</div>
            </template>
          </div>
        </div>
      </section>

      <section v-if="isStrictPriority">
        <div class="mb-2 text-xs font-semibold text-gray-700 dark:text-gray-200">{{ t('admin.accounts.routingPriority.sections.selectionBasis') }}</div>
        <div class="grid grid-cols-2 gap-2 md:grid-cols-3">
          <div v-for="item in strictBasisItems" :key="item.key" class="rounded-md border border-gray-200 px-3 py-2 dark:border-dark-600">
            <div class="text-[11px] text-gray-500">{{ item.label }}</div>
            <div class="mt-1 text-sm font-semibold">{{ item.value }}</div>
          </div>
        </div>
      </section>

      <section v-else>
        <div class="mb-2 text-xs font-semibold text-gray-700 dark:text-gray-200">{{ t('admin.accounts.routingPriority.sections.score') }}</div>
        <div class="grid grid-cols-2 gap-2 md:grid-cols-4">
          <div v-for="item in scoreItems" :key="item.key" class="rounded-md border border-gray-200 px-3 py-2 dark:border-dark-600">
            <div class="text-[11px] text-gray-500">{{ item.label }}</div>
            <div class="mt-1 text-sm font-semibold">{{ format(item.value) }}</div>
          </div>
        </div>
      </section>

      <section v-if="priceSourceItems.length">
        <div class="mb-2 text-xs font-semibold text-gray-700 dark:text-gray-200">{{ t('admin.accounts.routingPriority.sections.priceSource') }}</div>
        <div class="grid grid-cols-1 gap-2 md:grid-cols-3">
          <div v-for="item in priceSourceItems" :key="item.key" class="rounded-md border border-gray-200 px-3 py-2 dark:border-dark-600">
            <div class="text-[11px] text-gray-500">{{ item.label }}</div>
            <div class="mt-1 text-sm font-semibold">{{ item.value }}</div>
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

      <section v-if="explain.strict_priority?.enabled">
        <div class="mb-2 text-xs font-semibold text-gray-700 dark:text-gray-200">{{ t('admin.accounts.routingPriority.sections.strictPriority') }}</div>
        <div class="rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-900 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-100">
          <div class="font-medium">
            {{ strictCurrentPriorityText }}
          </div>
          <div class="mt-1 text-amber-800 dark:text-amber-200">
            {{ t('admin.accounts.routingPriority.strict.candidateCount', { count: explain.strict_priority.candidate_count }) }}
          </div>
        </div>
        <div v-if="strictExcludedAccounts.length" class="mt-2 space-y-2">
          <div v-for="row in strictExcludedAccounts" :key="row.account_id" class="flex items-center justify-between gap-3 rounded-md border border-gray-200 px-3 py-2 text-xs dark:border-dark-600">
            <div class="min-w-0">
              <div class="truncate font-medium text-gray-900 dark:text-white">{{ row.account_name }}</div>
              <div class="mt-0.5 text-gray-500 dark:text-gray-400">
                {{ t('admin.accounts.routingPriority.strict.excludedReason', { priority: row.priority, current: row.current_priority }) }}
              </div>
            </div>
            <span class="shrink-0 rounded bg-amber-100 px-2 py-1 text-[11px] font-medium text-amber-800 dark:bg-amber-500/20 dark:text-amber-200">
              {{ t('admin.accounts.routingPriority.strict.priority', { priority: row.priority }) }}
            </span>
          </div>
        </div>
      </section>

      <section v-if="translatedNotes.length">
        <div class="mb-2 text-xs font-semibold text-gray-700 dark:text-gray-200">{{ t('admin.accounts.routingPriority.sections.notes') }}</div>
        <div class="space-y-1 text-xs text-gray-600 dark:text-gray-300">
          <div v-for="note in translatedNotes" :key="note">{{ note }}</div>
        </div>
      </section>

      <section v-if="explain.top.length">
        <div class="mb-2 text-xs font-semibold text-gray-700 dark:text-gray-200">{{ t('admin.accounts.routingPriority.sections.topCandidates') }}</div>
        <div class="space-y-2">
          <div v-for="row in explain.top" :key="row.account_id" class="flex items-center justify-between rounded-md border border-gray-200 px-3 py-2 text-xs dark:border-dark-600">
            <span class="truncate">{{ row.rank ? `#${row.rank}` : '-' }} {{ row.account_name }}</span>
            <span class="font-mono">{{ topCandidateValue(row) }}</span>
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

const isStrictPriority = computed(() => props.explain?.scheduler_strategy === 'strict_priority' || Boolean(props.explain?.strict_priority?.enabled))
const modalTitle = computed(() => {
  if (isStrictPriority.value) return t('admin.accounts.routingPriority.modal.strictTitle')
  if (props.explain?.scheduler_strategy === 'experimental_scheduler') return t('admin.accounts.routingPriority.modal.experimentalTitle')
  return t('admin.accounts.routingPriority.modal.title')
})
const translatedNotes = computed(() => (props.explain?.notes ?? []).map((note) => translate('admin.accounts.routingPriority.notes', note)))
const priceSourceItems = computed(() => {
  const priceSource = props.explain?.account.price_source
  if (!priceSource) return []
  const items = [
    {
      key: 'source',
      label: t('admin.accounts.routingPriority.priceSource.source'),
      value: translate('admin.accounts.routingPriority.priceSource.values', priceSource.source),
    },
    {
      key: 'rate_multiplier',
      label: t('admin.accounts.routingPriority.priceSource.rateMultiplier'),
      value: t('admin.accounts.routingPriority.priceSource.rateValue', { rate: formatRateMultiplier(priceSource.rate_multiplier) }),
    },
  ]
  if (priceSource.fallback) {
    items.push({
      key: 'fallback',
      label: t('admin.accounts.routingPriority.priceSource.fallback'),
      value: translate('admin.accounts.routingPriority.priceSource.fallbackReasons', priceSource.fallback_reason || 'account_rate_fallback'),
    })
  }
  return items
})
const strictBasisItems = computed(() => {
  const account = props.explain?.account
  if (!account) return []
  return [
    {
      key: 'priority',
      label: t('admin.accounts.routingPriority.strict.priorityLabel'),
      value: t('admin.accounts.routingPriority.strict.priority', { priority: account.priority }),
    },
    {
      key: 'last_used',
      label: t('admin.accounts.routingPriority.strict.lastUsed'),
      value: formatLastUsed(account.last_used_at),
    },
    {
      key: 'status',
      label: t('admin.accounts.routingPriority.strict.currentLayer'),
      value: translate('admin.accounts.routingPriority.summary', account.summary_reason),
    },
  ]
})
const strictExcludedAccounts = computed(() => props.explain?.strict_priority?.excluded_accounts ?? [])
const strictCurrentPriorityText = computed(() => {
  const priority = props.explain?.strict_priority?.current_available_priority
  if (typeof priority === 'number') {
    return t('admin.accounts.routingPriority.strict.currentPriority', { priority })
  }
  return t('admin.accounts.routingPriority.strict.noCurrentPriority')
})

const format = (value: number) => Number.isFinite(value) ? value.toFixed(3) : '-'
const formatRateMultiplier = (value: number) => Number.isFinite(value) ? value.toFixed(4).replace(/\.?0+$/, '') : '-'
const formatLastUsed = (value?: string | null) => {
  if (!value) return t('admin.accounts.routingPriority.strict.neverUsed')
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '-'
  return date.toLocaleString()
}
const topCandidateValue = (row: OpenAIRoutingAccountExplain['top'][number]) => {
  if (!isStrictPriority.value) return format(row.score.total)
  return `${t('admin.accounts.routingPriority.strict.priority', { priority: row.priority })} · ${formatLastUsed(row.last_used_at)}`
}
</script>
