<template>
  <AppLayout>
    <div class="space-y-6 pb-12">
      <div class="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div>
          <h1 class="text-2xl font-bold text-gray-900 dark:text-white">{{ t('admin.operations.title') }}</h1>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.operations.description') }}</p>
        </div>
        <button type="button" class="btn btn-secondary btn-sm" :disabled="loading" @click="refreshAll">
          <Icon name="refresh" size="xs" :class="loading ? 'animate-spin' : ''" />
          {{ t('admin.operations.refresh') }}
        </button>
      </div>

      <div class="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-5">
        <div v-for="metric in metrics" :key="metric.label" class="rounded-lg border border-gray-100 bg-white p-4 dark:border-dark-700 dark:bg-dark-900">
          <p class="text-xs text-gray-500 dark:text-gray-400">{{ metric.label }}</p>
          <p class="mt-2 text-xl font-semibold text-gray-900 dark:text-white">{{ metric.value }}</p>
        </div>
      </div>

      <div class="border-b border-gray-200 dark:border-dark-700">
        <nav class="-mb-px flex gap-4 overflow-x-auto">
          <button
            v-for="tab in tabs"
            :key="tab.key"
            type="button"
            class="border-b-2 px-1 py-3 text-sm font-medium"
            :class="activeTab === tab.key ? 'border-blue-500 text-blue-600 dark:text-blue-400' : 'border-transparent text-gray-500 hover:text-gray-800 dark:text-gray-400 dark:hover:text-white'"
            @click="activeTab = tab.key"
          >
            {{ tab.label }}
          </button>
        </nav>
      </div>

      <section v-if="activeTab === 'overview'" class="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <div class="rounded-lg border border-gray-100 bg-white p-5 dark:border-dark-700 dark:bg-dark-900">
          <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.operations.overview') }}</h2>
          <div class="mt-4 grid grid-cols-2 gap-3 text-sm">
            <div class="text-gray-500 dark:text-gray-400">{{ t('admin.operations.todayCheckins') }}</div>
            <div class="text-right font-medium text-gray-900 dark:text-white">{{ formatCount(stats?.today_checkins) }}</div>
            <div class="text-gray-500 dark:text-gray-400">{{ t('admin.operations.todayReward') }}</div>
            <div class="text-right font-medium text-gray-900 dark:text-white">{{ formatUSD(stats?.today_reward_usd) }}</div>
            <div class="text-gray-500 dark:text-gray-400">{{ t('admin.operations.monthReward') }}</div>
            <div class="text-right font-medium text-gray-900 dark:text-white">{{ formatUSD(stats?.month_reward_usd) }}</div>
            <div class="text-gray-500 dark:text-gray-400">{{ t('admin.operations.averageReward') }}</div>
            <div class="text-right font-medium text-gray-900 dark:text-white">{{ formatUSD(stats?.average_reward_usd) }}</div>
          </div>
        </div>
        <div class="rounded-lg border border-gray-100 bg-white p-5 dark:border-dark-700 dark:bg-dark-900">
          <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.operations.rules') }}</h2>
          <div class="mt-4 grid grid-cols-2 gap-3 text-sm">
            <div class="text-gray-500 dark:text-gray-400">{{ t('admin.operations.dailyRemaining') }}</div>
            <div class="text-right font-medium text-gray-900 dark:text-white">{{ formatBudget(stats?.daily_budget_usd, stats?.daily_remaining_usd) }}</div>
            <div class="text-gray-500 dark:text-gray-400">{{ t('admin.operations.monthlyRemaining') }}</div>
            <div class="text-right font-medium text-gray-900 dark:text-white">{{ formatBudget(stats?.monthly_budget_usd, stats?.monthly_remaining_usd) }}</div>
            <div class="text-gray-500 dark:text-gray-400">{{ t('admin.operations.userMonthlyLimit') }}</div>
            <div class="text-right font-medium text-gray-900 dark:text-white">{{ formatUSD(stats?.user_monthly_limit_usd) }}</div>
          </div>
        </div>
      </section>

      <section v-else-if="activeTab === 'records'" class="space-y-4">
        <div class="rounded-lg border border-gray-100 bg-white p-4 dark:border-dark-700 dark:bg-dark-900">
          <div class="grid grid-cols-1 gap-3 md:grid-cols-3 xl:grid-cols-6">
            <input v-model="recordFilters.user" class="input" :placeholder="t('admin.operations.userSearch')" />
            <input v-model="recordFilters.date_from" class="input" type="date" :aria-label="t('admin.operations.dateFrom')" />
            <input v-model="recordFilters.date_to" class="input" type="date" :aria-label="t('admin.operations.dateTo')" />
            <input v-model.number="recordFilters.reward_min" class="input" type="number" min="0" step="0.01" :placeholder="t('admin.operations.rewardMin')" />
            <input v-model.number="recordFilters.reward_max" class="input" type="number" min="0" step="0.01" :placeholder="t('admin.operations.rewardMax')" />
            <Select v-model="recordFilters.crit_hit" :options="critFilterOptions" />
            <input v-model.number="recordFilters.streak_days" class="input md:col-span-1" type="number" min="1" step="1" :placeholder="t('admin.operations.minStreakDays')" />
            <button type="button" class="btn btn-secondary md:w-fit" @click="loadRecords">{{ t('admin.operations.filters') }}</button>
          </div>
        </div>

        <div class="overflow-hidden rounded-lg border border-gray-100 bg-white dark:border-dark-700 dark:bg-dark-900">
          <div class="overflow-x-auto">
            <table class="min-w-full divide-y divide-gray-100 text-sm dark:divide-dark-700">
              <thead class="bg-gray-50 text-left text-xs uppercase text-gray-500 dark:bg-dark-800 dark:text-gray-400">
                <tr>
                  <th class="px-4 py-3">{{ t('admin.operations.user') }}</th>
                  <th class="px-4 py-3">{{ t('admin.operations.date') }}</th>
                  <th class="px-4 py-3">{{ t('admin.operations.finalReward') }}</th>
                  <th class="px-4 py-3">{{ t('admin.operations.baseReward') }}</th>
                  <th class="px-4 py-3">{{ t('admin.operations.streakMultiplier') }}</th>
                  <th class="px-4 py-3">{{ t('admin.operations.critMultiplier') }}</th>
                  <th class="px-4 py-3">{{ t('admin.operations.usage') }}</th>
                  <th class="px-4 py-3">{{ t('admin.operations.createdAt') }}</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
                <tr v-for="record in records" :key="record.id" class="text-gray-700 dark:text-gray-300">
                  <td class="px-4 py-3">
                    <div class="font-medium text-gray-900 dark:text-white">{{ record.username || record.email || `#${record.user_id}` }}</div>
                    <div class="text-xs text-gray-500">#{{ record.user_id }} {{ record.email }}</div>
                  </td>
                  <td class="px-4 py-3">{{ record.date }}</td>
                  <td class="px-4 py-3 font-medium">{{ formatUSD(record.reward_amount) }}</td>
                  <td class="px-4 py-3">{{ formatUSD(record.reward_metadata?.base_reward_amount ?? record.reward_amount) }}</td>
                  <td class="px-4 py-3">{{ formatMultiplier(record.reward_metadata?.streak_multiplier) }}</td>
                  <td class="px-4 py-3">{{ record.reward_metadata?.crit_hit ? formatMultiplier(record.reward_metadata.crit_multiplier) : '-' }}</td>
                  <td class="px-4 py-3">{{ formatUSD(record.qualified_usage_usd) }}</td>
                  <td class="px-4 py-3">{{ formatDateTime(record.created_at) }}</td>
                </tr>
                <tr v-if="records.length === 0">
                  <td colspan="8" class="px-4 py-10 text-center text-gray-500 dark:text-gray-400">{{ t('admin.operations.noRecords') }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <div class="flex items-center justify-between border-t border-gray-100 px-4 py-3 text-sm dark:border-dark-700">
            <span class="text-gray-500">{{ totalRecords }} total</span>
            <div class="flex gap-2">
              <button type="button" class="btn btn-secondary btn-sm" :disabled="recordPage <= 1" @click="recordPage--; loadRecords()">Prev</button>
              <button type="button" class="btn btn-secondary btn-sm" :disabled="recordPage * recordPageSize >= totalRecords" @click="recordPage++; loadRecords()">Next</button>
            </div>
          </div>
        </div>
      </section>

      <section v-else class="space-y-5">
        <div class="rounded-lg border border-gray-100 bg-white p-5 dark:border-dark-700 dark:bg-dark-900">
          <div class="flex items-start justify-between gap-4">
            <div>
              <label class="input-label">{{ t('admin.operations.checkinEnabled') }}</label>
              <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.settings.features.dailyCheckin.enabledHint') }}</p>
            </div>
            <Toggle v-model="form.daily_checkin_enabled" />
          </div>
          <div class="mt-5 grid grid-cols-1 gap-4 md:grid-cols-3">
            <label class="space-y-1">
              <span class="input-label">{{ t('admin.operations.requiredUsage') }}</span>
              <input v-model.number="form.daily_checkin_required_usage_usd" class="input" type="number" min="0" step="0.01" />
            </label>
            <label class="space-y-1">
              <span class="input-label">{{ t('admin.operations.usageScope') }}</span>
              <Select v-model="form.daily_checkin_usage_scope" :options="usageScopeOptions" />
            </label>
            <label class="space-y-1">
              <span class="input-label">{{ t('admin.operations.userMonthlyLimit') }}</span>
              <input v-model.number="form.daily_checkin_user_monthly_limit_usd" class="input" type="number" min="0" step="0.01" />
            </label>
            <label class="space-y-1">
              <span class="input-label">{{ t('admin.operations.dailyBudget') }}</span>
              <input v-model.number="form.daily_checkin_daily_budget_usd" class="input" type="number" min="0" step="0.01" />
            </label>
            <label class="space-y-1">
              <span class="input-label">{{ t('admin.operations.monthlyBudget') }}</span>
              <input v-model.number="form.daily_checkin_monthly_budget_usd" class="input" type="number" min="0" step="0.01" />
            </label>
          </div>
        </div>

        <div class="rounded-lg border border-gray-100 bg-white p-5 dark:border-dark-700 dark:bg-dark-900">
          <div class="flex items-center justify-between gap-3">
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.operations.rewardTiers') }}</h2>
            <button type="button" class="btn btn-secondary btn-sm" @click="addRewardTier">{{ t('admin.operations.addTier') }}</button>
          </div>
          <div class="mt-4 space-y-3">
            <div v-for="(tier, index) in form.daily_checkin_reward_tiers" :key="index" class="grid grid-cols-1 gap-3 md:grid-cols-[1fr_1fr_1fr_auto]">
              <label class="space-y-1">
                <span class="input-label">{{ t('admin.operations.minReward') }}</span>
                <input v-model.number="tier.min_usd" class="input" type="number" min="1" max="100" step="1" :placeholder="t('admin.operations.minReward')" />
              </label>
              <label class="space-y-1">
                <span class="input-label">{{ t('admin.operations.maxReward') }}</span>
                <input v-model.number="tier.max_usd" class="input" type="number" min="1" max="100" step="1" :placeholder="t('admin.operations.maxReward')" />
              </label>
              <label class="space-y-1">
                <span class="input-label">{{ t('admin.operations.probability') }}</span>
                <input v-model.number="tier.probability_percent" class="input" type="number" min="0" max="100" step="0.0001" :placeholder="t('admin.operations.probability')" />
              </label>
              <button type="button" class="btn btn-secondary self-end" @click="removeRewardTier(index)">{{ t('admin.operations.delete') }}</button>
            </div>
          </div>
          <p class="mt-3 text-xs" :class="Math.abs(rewardProbabilityTotal - 100) < 0.000001 ? 'text-gray-500 dark:text-gray-400' : 'text-red-500'">
            {{ t('admin.operations.probabilityTotal') }}: {{ rewardProbabilityTotal.toFixed(4) }}%
          </p>
        </div>

        <div class="rounded-lg border border-gray-100 bg-white p-5 dark:border-dark-700 dark:bg-dark-900">
          <div class="flex items-start justify-between gap-4">
            <div>
              <label class="input-label">{{ t('admin.operations.streakEnabled') }}</label>
              <div class="mt-3 w-56">
                <Select v-model="form.daily_checkin_streak_multiplier_scope" :options="streakScopeOptions" />
              </div>
            </div>
            <Toggle v-model="form.daily_checkin_streak_multiplier_enabled" />
          </div>
          <div class="mt-4 flex items-center justify-between gap-3">
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.operations.streakRules') }}</h2>
            <button type="button" class="btn btn-secondary btn-sm" @click="addStreakRule">{{ t('admin.operations.addStreakRule') }}</button>
          </div>
          <div class="mt-4 space-y-3">
            <div v-for="(rule, index) in form.daily_checkin_streak_multipliers" :key="index" class="grid grid-cols-1 gap-3 md:grid-cols-[1fr_1fr_auto]">
              <label class="space-y-1">
                <span class="input-label">{{ t('admin.operations.streakDays') }}</span>
                <input v-model.number="rule.days" class="input" type="number" min="1" step="1" :placeholder="t('admin.operations.days')" />
              </label>
              <label class="space-y-1">
                <span class="input-label">{{ t('admin.operations.streakMultiplierValue') }}</span>
                <input v-model.number="rule.multiplier" class="input" type="number" min="1" step="0.01" :placeholder="t('admin.operations.multiplier')" />
              </label>
              <button type="button" class="btn btn-secondary self-end" @click="removeStreakRule(index)">{{ t('admin.operations.delete') }}</button>
            </div>
          </div>
        </div>

        <div class="rounded-lg border border-gray-100 bg-white p-5 dark:border-dark-700 dark:bg-dark-900">
          <div class="flex items-start justify-between gap-4">
            <label class="input-label">{{ t('admin.operations.critEnabled') }}</label>
            <Toggle v-model="form.daily_checkin_crit_enabled" />
          </div>
          <div class="mt-5 grid grid-cols-1 gap-4 md:grid-cols-3">
            <label class="space-y-1">
              <span class="input-label">{{ t('admin.operations.critProbability') }}</span>
              <input v-model.number="form.daily_checkin_crit_probability_percent" class="input" type="number" min="0" max="100" step="0.0001" />
            </label>
            <label class="space-y-1">
              <span class="input-label">{{ t('admin.operations.critMultiplier') }}</span>
              <input v-model.number="form.daily_checkin_crit_multiplier" class="input" type="number" min="1" step="0.01" />
            </label>
            <label class="space-y-1">
              <span class="input-label">{{ t('admin.operations.critMaxReward') }}</span>
              <input v-model.number="form.daily_checkin_crit_max_reward_usd" class="input" type="number" min="0" step="0.01" />
              <span class="input-hint">{{ t('admin.operations.critMaxRewardHint') }}</span>
            </label>
          </div>
        </div>

        <div class="flex justify-end">
          <button type="button" class="btn btn-primary" :disabled="saving" @click="saveRules">
            {{ saving ? t('admin.operations.saving') : t('admin.operations.save') }}
          </button>
        </div>
      </section>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import Select from '@/components/common/Select.vue'
import Toggle from '@/components/common/Toggle.vue'
import { useAppStore } from '@/stores'
import settingsAPI, { type DailyCheckinAdminStats, type DailyCheckinRewardTier, type DailyCheckinStreakMultiplier, type UpdateSettingsRequest } from '@/api/admin/settings'
import operationsAPI, { type DailyCheckinAdminRecord } from '@/api/admin/operations'

type TabKey = 'overview' | 'records' | 'rules'

interface CheckinRuleForm {
  daily_checkin_enabled: boolean
  daily_checkin_required_usage_usd: number
  daily_checkin_usage_scope: 'actual_cost' | 'balance_only'
  daily_checkin_reward_min_usd: number
  daily_checkin_reward_max_usd: number
  daily_checkin_daily_budget_usd: number
  daily_checkin_monthly_budget_usd: number
  daily_checkin_user_monthly_limit_usd: number
  daily_checkin_reward_tiers: DailyCheckinRewardTier[]
  daily_checkin_streak_multiplier_enabled: boolean
  daily_checkin_streak_multiplier_scope: 'cross_month' | 'monthly'
  daily_checkin_streak_multipliers: DailyCheckinStreakMultiplier[]
  daily_checkin_crit_enabled: boolean
  daily_checkin_crit_probability_percent: number
  daily_checkin_crit_multiplier: number
  daily_checkin_crit_max_reward_usd: number
}

const { t } = useI18n()
const appStore = useAppStore()

const activeTab = ref<TabKey>('overview')
const loading = ref(false)
const saving = ref(false)
const stats = ref<DailyCheckinAdminStats | null>(null)
const records = ref<DailyCheckinAdminRecord[]>([])
const totalRecords = ref(0)
const recordPage = ref(1)
const recordPageSize = 20

const form = reactive<CheckinRuleForm>({
  daily_checkin_enabled: true,
  daily_checkin_required_usage_usd: 1,
  daily_checkin_usage_scope: 'actual_cost',
  daily_checkin_reward_min_usd: 1,
  daily_checkin_reward_max_usd: 3,
  daily_checkin_daily_budget_usd: 0,
  daily_checkin_monthly_budget_usd: 0,
  daily_checkin_user_monthly_limit_usd: 0,
  daily_checkin_reward_tiers: [{ min_usd: 1, max_usd: 3, probability_percent: 100 }],
  daily_checkin_streak_multiplier_enabled: false,
  daily_checkin_streak_multiplier_scope: 'cross_month',
  daily_checkin_streak_multipliers: [],
  daily_checkin_crit_enabled: false,
  daily_checkin_crit_probability_percent: 0,
  daily_checkin_crit_multiplier: 1,
  daily_checkin_crit_max_reward_usd: 0,
})

const recordFilters = reactive({
  user: '',
  date_from: '',
  date_to: '',
  reward_min: undefined as number | undefined,
  reward_max: undefined as number | undefined,
  crit_hit: '' as boolean | '',
  streak_days: undefined as number | undefined,
})

const tabs = computed(() => [
  { key: 'overview' as const, label: t('admin.operations.overview') },
  { key: 'records' as const, label: t('admin.operations.records') },
  { key: 'rules' as const, label: t('admin.operations.rules') },
])

const usageScopeOptions = computed(() => [
  { value: 'actual_cost', label: t('admin.operations.usageScopeActual') },
  { value: 'balance_only', label: t('admin.operations.usageScopeBalance') },
])

const streakScopeOptions = computed(() => [
  { value: 'cross_month', label: t('admin.operations.streakCrossMonth') },
  { value: 'monthly', label: t('admin.operations.streakMonthly') },
])

const critFilterOptions = computed(() => [
  { value: '', label: t('admin.operations.all') },
  { value: true, label: t('admin.operations.critOnly') },
  { value: false, label: t('admin.operations.nonCritOnly') },
])

const metrics = computed(() => [
  { label: t('admin.operations.todayCheckins'), value: formatCount(stats.value?.today_checkins) },
  { label: t('admin.operations.todayReward'), value: formatUSD(stats.value?.today_reward_usd) },
  { label: t('admin.operations.monthReward'), value: formatUSD(stats.value?.month_reward_usd) },
  { label: t('admin.operations.averageReward'), value: formatUSD(stats.value?.average_reward_usd) },
  { label: t('admin.operations.dailyRemaining'), value: formatBudget(stats.value?.daily_budget_usd, stats.value?.daily_remaining_usd) },
])

const rewardProbabilityTotal = computed(() =>
  form.daily_checkin_reward_tiers.reduce((sum, tier) => sum + (Number(tier.probability_percent) || 0), 0)
)

function formatUSD(value: number | undefined | null): string {
  return `$${Number(value || 0).toFixed(2)}`
}

function formatCount(value: number | undefined | null): string {
  return String(Number(value || 0))
}

function formatMultiplier(value: number | undefined | null): string {
  return value ? `${Number(value).toFixed(2).replace(/\.?0+$/, '')}x` : '-'
}

function formatBudget(limit: number | undefined | null, remaining: number | undefined | null): string {
  return Number(limit || 0) > 0 ? `${formatUSD(remaining)} / ${formatUSD(limit)}` : 'Unlimited'
}

function formatDateTime(value: string): string {
  if (!value) return '-'
  return new Date(value).toLocaleString()
}

function assignSettings(settings: Awaited<ReturnType<typeof settingsAPI.getSettings>>) {
  form.daily_checkin_enabled = settings.daily_checkin_enabled
  form.daily_checkin_required_usage_usd = settings.daily_checkin_required_usage_usd
  form.daily_checkin_usage_scope = settings.daily_checkin_usage_scope
  form.daily_checkin_reward_min_usd = settings.daily_checkin_reward_min_usd
  form.daily_checkin_reward_max_usd = settings.daily_checkin_reward_max_usd
  form.daily_checkin_daily_budget_usd = settings.daily_checkin_daily_budget_usd
  form.daily_checkin_monthly_budget_usd = settings.daily_checkin_monthly_budget_usd
  form.daily_checkin_user_monthly_limit_usd = settings.daily_checkin_user_monthly_limit_usd
  form.daily_checkin_reward_tiers = (settings.daily_checkin_reward_tiers?.length ? settings.daily_checkin_reward_tiers : [{
    min_usd: settings.daily_checkin_reward_min_usd,
    max_usd: settings.daily_checkin_reward_max_usd,
    probability_percent: 100,
  }]).map((tier) => ({ ...tier }))
  form.daily_checkin_streak_multiplier_enabled = settings.daily_checkin_streak_multiplier_enabled ?? false
  form.daily_checkin_streak_multiplier_scope = settings.daily_checkin_streak_multiplier_scope || 'cross_month'
  form.daily_checkin_streak_multipliers = (settings.daily_checkin_streak_multipliers || []).map((rule) => ({ ...rule }))
  form.daily_checkin_crit_enabled = settings.daily_checkin_crit_enabled ?? false
  form.daily_checkin_crit_probability_percent = settings.daily_checkin_crit_probability_percent ?? 0
  form.daily_checkin_crit_multiplier = settings.daily_checkin_crit_multiplier ?? 1
  form.daily_checkin_crit_max_reward_usd = settings.daily_checkin_crit_max_reward_usd ?? 0
}

async function refreshAll() {
  loading.value = true
  try {
    const [settings, nextStats] = await Promise.all([
      settingsAPI.getSettings(),
      operationsAPI.getDailyCheckinStats(),
      loadRecords(),
    ])
    assignSettings(settings)
    stats.value = nextStats
  } catch (error) {
    console.error('Failed to load operations center:', error)
    appStore.showError(t('admin.operations.loadFailed'))
  } finally {
    loading.value = false
  }
}

async function loadRecords() {
  const result = await operationsAPI.listDailyCheckinRecords({
    page: recordPage.value,
    page_size: recordPageSize,
    ...recordFilters,
  })
  records.value = result.items
  totalRecords.value = result.total
}

function addRewardTier() {
  form.daily_checkin_reward_tiers.push({ min_usd: 1, max_usd: 1, probability_percent: 0 })
}

function removeRewardTier(index: number) {
  if (form.daily_checkin_reward_tiers.length <= 1) return
  form.daily_checkin_reward_tiers.splice(index, 1)
}

function addStreakRule() {
  form.daily_checkin_streak_multipliers.push({ days: 1, multiplier: 1 })
}

function removeStreakRule(index: number) {
  form.daily_checkin_streak_multipliers.splice(index, 1)
}

function normalizedRules(): UpdateSettingsRequest {
  const tiers = form.daily_checkin_reward_tiers.map((tier) => {
    const min = Math.max(1, Math.min(100, Math.floor(Number(tier.min_usd) || 1)))
    const max = Math.max(min, Math.min(100, Math.floor(Number(tier.max_usd) || min)))
    return {
      min_usd: min,
      max_usd: max,
      probability_percent: Math.max(0, Math.min(100, Number(tier.probability_percent) || 0)),
    }
  })
  const minReward = Math.min(...tiers.map((tier) => tier.min_usd))
  const maxReward = Math.max(...tiers.map((tier) => tier.max_usd))
  return {
    daily_checkin_enabled: form.daily_checkin_enabled,
    daily_checkin_required_usage_usd: Math.max(0, Number(form.daily_checkin_required_usage_usd) || 0),
    daily_checkin_usage_scope: form.daily_checkin_usage_scope === 'balance_only' ? 'balance_only' : 'actual_cost',
    daily_checkin_reward_min_usd: minReward,
    daily_checkin_reward_max_usd: maxReward,
    daily_checkin_daily_budget_usd: Math.max(0, Number(form.daily_checkin_daily_budget_usd) || 0),
    daily_checkin_monthly_budget_usd: Math.max(0, Number(form.daily_checkin_monthly_budget_usd) || 0),
    daily_checkin_user_monthly_limit_usd: Math.max(0, Number(form.daily_checkin_user_monthly_limit_usd) || 0),
    daily_checkin_reward_tiers: tiers,
    daily_checkin_streak_multiplier_enabled: form.daily_checkin_streak_multiplier_enabled,
    daily_checkin_streak_multiplier_scope: form.daily_checkin_streak_multiplier_scope === 'monthly' ? 'monthly' : 'cross_month',
    daily_checkin_streak_multipliers: form.daily_checkin_streak_multipliers
      .map((rule) => ({
        days: Math.max(1, Math.floor(Number(rule.days) || 1)),
        multiplier: Math.max(1, Number(rule.multiplier) || 1),
      }))
      .sort((a, b) => a.days - b.days),
    daily_checkin_crit_enabled: form.daily_checkin_crit_enabled,
    daily_checkin_crit_probability_percent: Math.max(0, Math.min(100, Number(form.daily_checkin_crit_probability_percent) || 0)),
    daily_checkin_crit_multiplier: Math.max(1, Number(form.daily_checkin_crit_multiplier) || 1),
    daily_checkin_crit_max_reward_usd: Math.max(0, Number(form.daily_checkin_crit_max_reward_usd) || 0),
  }
}

async function saveRules() {
  if (Math.abs(rewardProbabilityTotal.value - 100) >= 0.000001) {
    appStore.showError(`${t('admin.operations.probabilityTotal')}: ${rewardProbabilityTotal.value.toFixed(4)}%`)
    return
  }
  saving.value = true
  try {
    const updated = await settingsAPI.updateSettings(normalizedRules())
    assignSettings(updated)
    stats.value = await operationsAPI.getDailyCheckinStats()
    appStore.showSuccess(t('admin.operations.saved'))
  } catch (error) {
    console.error('Failed to save daily check-in rules:', error)
    appStore.showError(t('admin.operations.saveFailed'))
  } finally {
    saving.value = false
  }
}

onMounted(() => {
  refreshAll()
})
</script>
