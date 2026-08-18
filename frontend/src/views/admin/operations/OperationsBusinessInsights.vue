<template>
  <div class="space-y-4">
    <div
      v-if="error"
      class="rounded-lg border border-red-200 bg-red-50 p-3 text-sm text-red-600 dark:border-red-900/60 dark:bg-red-950/30 dark:text-red-300"
    >
      {{ t('admin.operations.businessLoadFailed') }}
    </div>
    <div
      v-else-if="!dataAvailable"
      class="rounded-lg border border-amber-200 bg-amber-50 p-3 text-sm text-amber-700 dark:border-amber-900/60 dark:bg-amber-950/30 dark:text-amber-300"
    >
      {{ t('admin.operations.businessDataUnavailable') }}
    </div>
    <div
      v-else-if="!aggregationComplete"
      class="rounded-lg border border-blue-200 bg-blue-50 p-3 text-sm text-blue-700 dark:border-blue-900/60 dark:bg-blue-950/30 dark:text-blue-300"
    >
      {{ t('admin.operations.businessDataPartial') }}
    </div>

    <div class="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
      <MetricCard :label="t('admin.operations.totalUsers')" :value="formatCount(stats?.total_users)" />
      <MetricCard :label="t('admin.operations.todayNewUsers')" :value="formatCount(stats?.today_new_users)" />
      <MetricCard :label="t('admin.operations.todayActiveUsers')" :value="formatCount(stats?.active_users)" />
      <MetricCard :label="t('admin.operations.periodRequestUsers')" :value="formatCount(periodRequestUsers)" />
    </div>

    <div class="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
      <MetricCard :label="t('admin.operations.d1Retention')" :value="formatPercent(retention?.summary.d1_rate)" />
      <MetricCard :label="t('admin.operations.d7Retention')" :value="formatPercent(retention?.summary.d7_rate)" />
      <MetricCard :label="t('admin.operations.d30Retention')" :value="formatPercent(retention?.summary.d30_rate)" />
      <MetricCard :label="t('admin.operations.averageActiveDays')" :value="formatDecimal(retention?.summary.average_active_days)" />
    </div>

    <div class="grid grid-cols-1 gap-4 xl:grid-cols-2">
      <section class="rounded-lg border border-gray-100 bg-white p-5 dark:border-dark-700 dark:bg-dark-900">
        <div class="flex items-start justify-between gap-3">
          <div>
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.operations.revenueAndMargin') }}</h2>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ rangeStart }} ~ {{ rangeEnd }}</p>
          </div>
          <span v-if="loading" class="text-xs text-gray-400">{{ t('common.loading') }}</span>
        </div>
        <div class="mt-4 grid grid-cols-2 gap-3 text-sm">
          <div class="text-gray-500 dark:text-gray-400">{{ t('admin.operations.paymentRevenue') }}</div>
          <div class="text-right font-medium text-gray-900 dark:text-white">{{ formatCurrencyAmounts(payment?.total_amount) }}</div>
          <div class="text-gray-500 dark:text-gray-400">{{ t('admin.operations.adminRechargeCredits') }}</div>
          <div class="text-right font-medium text-gray-900 dark:text-white">{{ formatUSD(adminRechargeCredits) }}</div>
          <div class="text-gray-500 dark:text-gray-400">{{ t('admin.operations.paidOrders') }}</div>
          <div class="text-right font-medium text-gray-900 dark:text-white">{{ formatCount(payment?.total_count) }}</div>
          <div class="text-gray-500 dark:text-gray-400">{{ t('admin.operations.actualCharges') }}</div>
          <div class="text-right font-medium text-gray-900 dark:text-white">{{ formatBusinessUSD(actualCharges) }}</div>
          <div class="text-gray-500 dark:text-gray-400">{{ t('admin.operations.upstreamCost') }}</div>
          <div class="text-right font-medium text-gray-900 dark:text-white">{{ formatBusinessCNY(realUpstreamCostCNY) }}</div>
          <div class="text-gray-500 dark:text-gray-400">{{ t('admin.operations.accountCostUsdBasis') }}</div>
          <div class="text-right font-medium text-gray-900 dark:text-white">{{ formatBusinessUSD(accountCostUSD) }}</div>
          <div class="text-gray-500 dark:text-gray-400">{{ t('admin.operations.rewardCost') }}</div>
          <div class="text-right font-medium text-gray-900 dark:text-white">{{ formatUSD(rewardCost) }}</div>
          <div class="text-gray-500 dark:text-gray-400">{{ t('admin.operations.contributionMargin') }}</div>
          <div class="text-right font-semibold" :class="dataAvailable ? (contributionMargin >= 0 ? 'text-emerald-600 dark:text-emerald-400' : 'text-red-600 dark:text-red-400') : 'text-gray-400'">
            {{ dataAvailable ? `${formatUSD(contributionMargin)} / ${formatPercent(contributionMarginRate)}` : '-' }}
          </div>
        </div>
        <p class="mt-4 text-xs text-gray-400 dark:text-gray-500">{{ t('admin.operations.marginDefinition') }}</p>
      </section>

      <section class="rounded-lg border border-gray-100 bg-white p-5 dark:border-dark-700 dark:bg-dark-900">
        <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.operations.productUsage') }}</h2>
        <div class="mt-4 grid grid-cols-2 gap-3 text-sm">
          <div class="text-gray-500 dark:text-gray-400">{{ t('admin.operations.periodRequests') }}</div>
          <div class="text-right font-medium text-gray-900 dark:text-white">{{ formatBusinessCount(ranking?.total_requests) }}</div>
          <div class="text-gray-500 dark:text-gray-400">{{ t('admin.operations.periodTokens') }}</div>
          <div class="text-right font-medium text-gray-900 dark:text-white">{{ formatBusinessCount(ranking?.total_tokens) }}</div>
          <div class="text-gray-500 dark:text-gray-400">{{ t('admin.operations.averageChargePerUser') }}</div>
          <div class="text-right font-medium text-gray-900 dark:text-white">{{ formatBusinessUSD(averageChargePerUser) }}</div>
        </div>
      </section>
    </div>

    <div class="grid grid-cols-1 gap-4 xl:grid-cols-2">
      <RankingList
        :title="t('admin.operations.topModels')"
        :empty-label="t('admin.operations.noBusinessData')"
        :items="topModels.map((item) => ({ label: item.model, value: `${formatCount(item.requests)} / ${formatUSD(item.actual_cost)}` }))"
      />
      <RankingList
        :title="t('admin.operations.topGroups')"
        :empty-label="groupDetailsAvailable ? t('admin.operations.noBusinessData') : t('admin.operations.groupDetailsRangeLimited')"
        :items="topGroups.map((item) => ({ label: item.group_name || `#${item.group_id}`, value: `${formatCount(item.requests)} / ${formatUSD(item.actual_cost)}` }))"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, defineComponent, h } from 'vue'
import { useI18n } from 'vue-i18n'
import type { DashboardStats, GroupStat, ModelStat, UserSpendingRankingResponse } from '@/types'
import type { CurrencyAmounts, DashboardStats as PaymentDashboardStats } from '@/types/payment'
import type { OperationsRetentionResponse } from '@/api/admin/operations'

const props = defineProps<{
  loading: boolean
  error: boolean
  stats: DashboardStats | null
  payment: PaymentDashboardStats | null
  ranking: UserSpendingRankingResponse | null
  models: ModelStat[]
  groups: GroupStat[]
  dataAvailable: boolean
  aggregationComplete: boolean
  groupDetailsAvailable: boolean
  rewardCost: number
  retention: OperationsRetentionResponse | null
  periodRequestUsers: number
  rangeStart: string
  rangeEnd: string
}>()

const { t } = useI18n()

const MetricCard = defineComponent({
  props: { label: String, value: String },
  setup(cardProps) {
    return () => h('div', { class: 'rounded-lg border border-gray-100 bg-white p-4 dark:border-dark-700 dark:bg-dark-900' }, [
      h('p', { class: 'text-xs text-gray-500 dark:text-gray-400' }, cardProps.label),
      h('p', { class: 'mt-2 text-xl font-semibold text-gray-900 dark:text-white' }, cardProps.value),
    ])
  },
})

const RankingList = defineComponent({
  props: {
    title: String,
    emptyLabel: String,
    items: { type: Array as () => Array<{ label: string; value: string }>, default: () => [] },
  },
  setup(listProps) {
    return () => h('section', { class: 'rounded-lg border border-gray-100 bg-white p-5 dark:border-dark-700 dark:bg-dark-900' }, [
      h('h2', { class: 'text-base font-semibold text-gray-900 dark:text-white' }, listProps.title),
      h('div', { class: 'mt-4 space-y-3' }, listProps.items.length
        ? listProps.items.map((item, index) => h('div', { class: 'flex items-center justify-between gap-3 text-sm' }, [
          h('span', { class: 'min-w-0 truncate text-gray-600 dark:text-gray-300' }, `${index + 1}. ${item.label}`),
          h('span', { class: 'shrink-0 font-medium text-gray-900 dark:text-white' }, item.value),
        ]))
        : [h('p', { class: 'text-sm text-gray-500 dark:text-gray-400' }, listProps.emptyLabel)]),
    ])
  },
})

const actualCharges = computed(() => Number(props.ranking?.total_actual_cost || 0))
const realUpstreamCostCNY = computed(() => props.models.reduce((sum, item) => sum + Number(item.real_cost_cny || 0), 0))
const accountCostUSD = computed(() => props.models.reduce((sum, item) => sum + Number(item.account_cost || 0), 0))
const adminRechargeCredits = computed(() => Number(props.payment?.admin_recharge_amount || 0))
const contributionMargin = computed(() => actualCharges.value - accountCostUSD.value - Number(props.rewardCost || 0))
const contributionMarginRate = computed(() => actualCharges.value > 0 ? contributionMargin.value / actualCharges.value : 0)
const averageChargePerUser = computed(() => {
  const users = Number(props.periodRequestUsers || 0)
  return users > 0 ? actualCharges.value / users : 0
})
const topModels = computed(() => [...props.models].sort((a, b) => b.actual_cost - a.actual_cost).slice(0, 8))
const topGroups = computed(() => [...props.groups].sort((a, b) => b.actual_cost - a.actual_cost).slice(0, 8))

function formatCount(value: number | undefined | null): string {
  return Number(value || 0).toLocaleString()
}

function formatUSD(value: number | undefined | null): string {
  return `USD ${Number(value || 0).toFixed(2)}`
}

function formatBusinessUSD(value: number | undefined | null): string {
  return props.dataAvailable ? formatUSD(value) : '-'
}

function formatBusinessCNY(value: number | undefined | null): string {
  return props.dataAvailable ? `CNY ${Number(value || 0).toFixed(2)}` : '-'
}

function formatBusinessCount(value: number | undefined | null): string {
  return props.dataAvailable ? formatCount(value) : '-'
}

function formatPercent(value: number | undefined | null): string {
  return `${(Number(value || 0) * 100).toFixed(1)}%`
}

function formatDecimal(value: number | undefined | null): string {
  return Number(value || 0).toFixed(1)
}

function formatCurrencyAmounts(amounts: CurrencyAmounts | undefined): string {
  const entries = Object.entries(amounts || {}).filter(([, amount]) => Number(amount) !== 0)
  if (!entries.length) return '-'
  return entries
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([currency, amount]) => `${currency} ${Number(amount).toFixed(2)}`)
    .join(' / ')
}

</script>
