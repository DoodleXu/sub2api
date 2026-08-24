<template>
  <AppLayout>
    <div class="space-y-6 pb-12">
      <div class="flex flex-col gap-3 xl:flex-row xl:items-start xl:justify-between">
        <div>
          <h1 class="text-2xl font-bold text-gray-900 dark:text-white">{{ t('admin.operations.title') }}</h1>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.operations.description') }}</p>
        </div>
        <div class="flex flex-wrap items-center gap-2">
          <button
            v-for="option in rangeOptions"
            :key="option.days"
            type="button"
            class="btn btn-secondary btn-sm"
            :class="rangeDays === option.days ? 'border-blue-500 text-blue-600 dark:text-blue-400' : ''"
            @click="setRange(option.days)"
          >
            {{ option.label }}
          </button>
          <input v-model="customStartDate" class="input h-9 w-36 text-xs" type="date" :aria-label="t('admin.operations.customDateFrom')" />
          <input v-model="customEndDate" class="input h-9 w-36 text-xs" type="date" :aria-label="t('admin.operations.customDateTo')" />
          <button type="button" class="btn btn-secondary btn-sm" @click="applyCustomRange">{{ t('admin.operations.applyRange') }}</button>
          <button type="button" class="btn btn-secondary btn-sm" :disabled="loading" @click="refreshAll">
            <Icon name="refresh" size="xs" :class="loading ? 'animate-spin' : ''" />
            {{ t('admin.operations.refresh') }}
          </button>
        </div>
      </div>

      <div
        v-if="overview?.meta"
        class="flex flex-wrap items-center gap-x-4 gap-y-1 rounded-lg border px-4 py-2 text-xs"
        :class="overview.meta.data_quality === 'complete' ? 'border-gray-100 bg-white text-gray-500 dark:border-dark-700 dark:bg-dark-900 dark:text-gray-400' : 'border-amber-200 bg-amber-50 text-amber-700 dark:border-amber-900/60 dark:bg-amber-950/30 dark:text-amber-300'"
      >
        <span>{{ t('admin.operations.dataAsOf') }}: {{ formatDateTime(overview.meta.as_of) }}</span>
        <span>{{ t('admin.operations.reportingTimezone') }}: {{ overview.meta.timezone }}</span>
        <span v-if="overview.meta.coverage_start">{{ t('admin.operations.dataCoverage') }}: {{ overview.meta.coverage_start }} ~ {{ overview.meta.coverage_end }}</span>
        <span v-if="overview.meta.data_quality !== 'complete'">{{ t(`admin.operations.dataQuality.${overview.meta.data_quality}`) }}</span>
      </div>

      <div class="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-5">
        <div v-for="metric in metrics" :key="metric.label" class="rounded-lg border border-gray-100 bg-white p-4 dark:border-dark-700 dark:bg-dark-900">
          <p class="text-xs text-gray-500 dark:text-gray-400">{{ metric.label }}</p>
          <p class="mt-2 text-xl font-semibold text-gray-900 dark:text-white">{{ metric.value }}</p>
          <p v-if="metric.hint" class="mt-1 text-xs" :class="metric.tone === 'positive' ? 'text-emerald-500' : metric.tone === 'negative' ? 'text-red-500' : 'text-gray-400 dark:text-gray-500'">{{ metric.hint }}</p>
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
            @click="selectTab(tab.key)"
          >
            {{ tab.label }}
          </button>
        </nav>
      </div>

      <section v-if="activeTab === 'overview'" class="space-y-4">
        <div v-if="overviewError" class="rounded-lg border border-red-200 bg-red-50 p-3 text-sm text-red-600 dark:border-red-900/60 dark:bg-red-950/30 dark:text-red-300">
          {{ t('admin.operations.overviewLoadFailed') }}
        </div>
        <OperationsOverviewTrendChart :points="overview?.points || []" :loading="loadingOverview" />
        <div class="grid grid-cols-1 gap-4 lg:grid-cols-2">
          <div class="rounded-lg border border-gray-100 bg-white p-5 dark:border-dark-700 dark:bg-dark-900">
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.operations.activitySummary') }}</h2>
            <div class="mt-4 grid grid-cols-2 gap-3 text-sm">
              <div class="text-gray-500 dark:text-gray-400">{{ t('admin.operations.apiDau') }}</div>
              <div class="text-right font-medium text-gray-900 dark:text-white">{{ formatCount(overview?.summary.dau) }}</div>
              <div class="text-gray-500 dark:text-gray-400">{{ t('admin.operations.newUsers') }}</div>
              <div class="text-right font-medium text-gray-900 dark:text-white">{{ formatCount(overview?.summary.new_users) }}</div>
              <div class="text-gray-500 dark:text-gray-400">{{ t('admin.operations.requests') }}</div>
              <div class="text-right font-medium text-gray-900 dark:text-white">{{ formatCount(overview?.summary.requests) }}</div>
              <div class="text-gray-500 dark:text-gray-400">{{ t('admin.operations.actualCost') }}</div>
              <div class="text-right font-medium text-gray-900 dark:text-white">{{ formatUSD(overview?.summary.actual_cost) }}</div>
            </div>
          </div>
          <div class="rounded-lg border border-gray-100 bg-white p-5 dark:border-dark-700 dark:bg-dark-900">
            <div class="flex items-center justify-between gap-3">
              <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.operations.exportData') }}</h2>
              <button type="button" class="btn btn-secondary btn-sm" :disabled="exporting" @click="exportDataset('overview_daily')">
                {{ exporting ? t('admin.operations.exporting') : t('admin.operations.exportOverview') }}
              </button>
            </div>
            <p class="mt-3 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.operations.exportOverviewHint') }}</p>
          </div>
        </div>
      </section>

      <section v-else-if="activeTab === 'business'">
        <OperationsBusinessInsights
          :loading="loadingBusiness"
          :error="businessError"
          :stats="businessStats"
          :payment="paymentStats"
          :ranking="businessRanking"
          :models="businessModels"
          :groups="businessGroups"
          :data-available="businessDataAvailable"
          :aggregation-complete="businessAggregationComplete"
          :group-details-available="businessGroupDetailsAvailable"
          :reward-cost="businessRewardCost"
          :retention="businessRetention"
          :period-request-users="overview?.summary.period_request_users || 0"
          :range-start="dateRangeQuery().start_date"
          :range-end="dateRangeQuery().end_date"
        />
      </section>

      <section v-else-if="activeTab === 'checkin'" class="space-y-4">
        <div v-if="analyticsError" class="rounded-lg border border-red-200 bg-red-50 p-3 text-sm text-red-600 dark:border-red-900/60 dark:bg-red-950/30 dark:text-red-300">
          {{ t('admin.operations.analyticsLoadFailed') }}
        </div>
        <div class="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
          <div class="rounded-lg border border-gray-100 bg-white p-4 dark:border-dark-700 dark:bg-dark-900">
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.operations.qualifiedUsers') }}</p>
            <p class="mt-2 text-xl font-semibold text-gray-900 dark:text-white">{{ formatCount(checkinAnalytics?.summary.qualified_users) }}</p>
          </div>
          <div class="rounded-lg border border-gray-100 bg-white p-4 dark:border-dark-700 dark:bg-dark-900">
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.operations.checkinUsers') }}</p>
            <p class="mt-2 text-xl font-semibold text-gray-900 dark:text-white">{{ formatCount(checkinAnalytics?.summary.checkin_users) }}</p>
          </div>
          <div class="rounded-lg border border-gray-100 bg-white p-4 dark:border-dark-700 dark:bg-dark-900">
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.operations.checkinRate') }}</p>
            <p class="mt-2 text-xl font-semibold text-gray-900 dark:text-white">{{ formatPercent(checkinAnalytics?.summary.checkin_opportunity_rate) }}</p>
          </div>
          <div class="rounded-lg border border-gray-100 bg-white p-4 dark:border-dark-700 dark:bg-dark-900">
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.operations.projectedBudgetDays') }}</p>
            <p class="mt-2 text-xl font-semibold text-gray-900 dark:text-white">{{ formatDays(checkinAnalytics?.summary.projected_budget_days) }}</p>
          </div>
        </div>

        <DailyCheckinTrendChart :points="checkinAnalytics?.points || []" :loading="loadingAnalytics" />

        <div class="grid grid-cols-1 gap-4 xl:grid-cols-3">
          <div class="rounded-lg border border-gray-100 bg-white p-5 dark:border-dark-700 dark:bg-dark-900">
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.operations.checkinFunnel') }}</h2>
            <div class="mt-4 space-y-3">
              <div v-for="item in funnelItems" :key="item.label">
                <div class="mb-1 flex justify-between text-sm">
                  <span class="text-gray-500 dark:text-gray-400">{{ item.label }}</span>
                  <span class="font-medium text-gray-900 dark:text-white">{{ item.value }}</span>
                </div>
                <div class="h-2 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700">
                  <div class="h-full rounded-full bg-blue-500" :style="{ width: item.width }"></div>
                </div>
              </div>
            </div>
          </div>
          <div class="rounded-lg border border-gray-100 bg-white p-5 dark:border-dark-700 dark:bg-dark-900">
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.operations.rewardBudget') }}</h2>
            <div class="mt-4 grid grid-cols-2 gap-3 text-sm">
              <div class="text-gray-500 dark:text-gray-400">{{ t('admin.operations.todayRewardPerCheckinUser') }}</div>
              <div class="text-right font-medium text-gray-900 dark:text-white">{{ formatRewardPerCheckinUser(stats?.today_reward_usd, stats?.today_checkins) }}</div>
              <div class="text-gray-500 dark:text-gray-400">{{ t('admin.operations.dailyRemaining') }}</div>
              <div class="text-right font-medium text-gray-900 dark:text-white">{{ formatBudget(stats?.daily_budget_usd, stats?.daily_remaining_usd) }}</div>
              <div class="text-gray-500 dark:text-gray-400">{{ t('admin.operations.estimatedRemainingCheckins') }}</div>
              <div class="text-right font-medium text-gray-900 dark:text-white">{{ formatEstimatedCheckins(checkinAnalytics?.summary.estimated_remaining_checkins) }}</div>
              <div class="text-gray-500 dark:text-gray-400">{{ t('admin.operations.monthReward') }}</div>
              <div class="text-right font-medium text-gray-900 dark:text-white">{{ formatUSD(stats?.month_reward_usd) }}</div>
              <div class="text-gray-500 dark:text-gray-400">{{ t('admin.operations.monthlyRemaining') }}</div>
              <div class="text-right font-medium text-gray-900 dark:text-white">{{ formatBudget(stats?.monthly_budget_usd, stats?.monthly_remaining_usd) }}</div>
            </div>
          </div>
          <div class="rounded-lg border border-gray-100 bg-white p-5 dark:border-dark-700 dark:bg-dark-900">
            <div class="flex items-center justify-between gap-3">
              <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.operations.ruleEffect') }}</h2>
              <button type="button" class="btn btn-secondary btn-sm" :disabled="exporting" @click="exportDataset('daily_checkin_summary')">
                {{ t('admin.operations.exportSummary') }}
              </button>
            </div>
            <div class="mt-4 grid grid-cols-2 gap-3 text-sm">
              <div class="text-gray-500 dark:text-gray-400">{{ t('admin.operations.averageReward') }}</div>
              <div class="text-right font-medium text-gray-900 dark:text-white">{{ formatUSD(checkinAnalytics?.summary.avg_reward_usd) }}</div>
              <div class="text-gray-500 dark:text-gray-400">{{ t('admin.operations.fallbackRate') }}</div>
              <div class="text-right font-medium text-gray-900 dark:text-white">{{ formatPercent(checkinAnalytics?.summary.fallback_rate) }}</div>
              <div class="text-gray-500 dark:text-gray-400">{{ t('admin.operations.critRate') }}</div>
              <div class="text-right font-medium text-gray-900 dark:text-white">{{ formatPercent(checkinAnalytics?.summary.crit_rate) }}</div>
              <div class="text-gray-500 dark:text-gray-400">{{ t('admin.operations.streakUserRate') }}</div>
              <div class="text-right font-medium text-gray-900 dark:text-white">{{ formatPercent(checkinAnalytics?.summary.streak_user_rate) }}</div>
            </div>
          </div>
        </div>

        <div class="rounded-lg border border-gray-100 bg-white p-5 dark:border-dark-700 dark:bg-dark-900">
          <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.operations.rewardDistribution') }}</h2>
          <div class="mt-4 grid grid-cols-1 gap-3 md:grid-cols-4">
            <div v-for="item in checkinAnalytics?.reward_distribution || []" :key="item.label" class="border-l-2 border-blue-500 pl-3">
              <p class="text-xs text-gray-500 dark:text-gray-400">{{ item.label }}</p>
              <p class="mt-1 text-sm font-medium text-gray-900 dark:text-white">{{ formatCount(item.count) }} / {{ formatUSD(item.reward_usd) }}</p>
            </div>
            <p v-if="!(checkinAnalytics?.reward_distribution || []).length" class="text-sm text-gray-500 dark:text-gray-400">{{ t('admin.operations.noTrendData') }}</p>
          </div>
        </div>
      </section>

      <section v-else-if="activeTab === 'records'" class="space-y-4">
        <div v-if="recordsError" class="rounded-lg border border-red-200 bg-red-50 p-3 text-sm text-red-600 dark:border-red-900/60 dark:bg-red-950/30 dark:text-red-300">
          {{ t('admin.operations.recordsLoadFailed') }}
        </div>
        <div class="rounded-lg border border-gray-100 bg-white p-4 dark:border-dark-700 dark:bg-dark-900">
          <div class="grid grid-cols-1 gap-3 md:grid-cols-3 xl:grid-cols-7">
            <input v-model="recordFilters.user" class="input" :placeholder="t('admin.operations.userSearch')" />
            <input v-model="recordFilters.date_from" class="input" type="date" :aria-label="t('admin.operations.dateFrom')" />
            <input v-model="recordFilters.date_to" class="input" type="date" :aria-label="t('admin.operations.dateTo')" />
            <input v-model.number="recordFilters.reward_min" class="input" type="number" min="0" step="0.01" :placeholder="t('admin.operations.rewardMin')" />
            <input v-model.number="recordFilters.reward_max" class="input" type="number" min="0" step="0.01" :placeholder="t('admin.operations.rewardMax')" />
            <Select v-model="recordFilters.crit_hit" :options="critFilterOptions" />
            <input v-model.number="recordFilters.streak_days" class="input" type="number" min="1" step="1" :placeholder="t('admin.operations.minStreakDays')" />
            <div class="flex flex-wrap items-center gap-2 md:col-span-3 xl:col-span-7">
              <button type="button" class="btn btn-secondary" @click="applyRecordFilters">{{ t('admin.operations.filters') }}</button>
              <button type="button" class="btn btn-secondary" :disabled="exporting" @click="exportDataset('daily_checkin_records')">
                {{ exporting ? t('admin.operations.exporting') : t('admin.operations.exportRecords') }}
              </button>
            </div>
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
            <span class="text-gray-500">{{ t('admin.operations.totalRecords', { count: totalRecords }) }}</span>
            <div class="flex gap-2">
              <button type="button" class="btn btn-secondary btn-sm" :disabled="recordPage <= 1 || loadingRecords" @click="changeRecordPage(-1)">{{ t('admin.operations.prevPage') }}</button>
              <button type="button" class="btn btn-secondary btn-sm" :disabled="recordPage * recordPageSize >= totalRecords || loadingRecords" @click="changeRecordPage(1)">{{ t('admin.operations.nextPage') }}</button>
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
            <label class="space-y-1">
              <span class="input-label">{{ t('admin.operations.budgetFallbackReward') }}</span>
              <input v-model.number="form.daily_checkin_budget_fallback_reward_usd" class="input" type="number" min="0.01" max="100" step="0.01" />
            </label>
            <label class="space-y-1 md:col-span-2">
              <span class="input-label">{{ t('admin.operations.budgetFallbackMessage') }}</span>
              <input v-model="form.daily_checkin_budget_fallback_message" class="input" type="text" maxlength="120" />
              <span class="input-hint">{{ t('admin.operations.budgetFallbackHint') }}</span>
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
                <input v-model.number="tier.min_usd" class="input" type="number" min="0.01" max="100" step="0.01" :placeholder="t('admin.operations.minReward')" />
              </label>
              <label class="space-y-1">
                <span class="input-label">{{ t('admin.operations.maxReward') }}</span>
                <input v-model.number="tier.max_usd" class="input" type="number" min="0.01" max="100" step="0.01" :placeholder="t('admin.operations.maxReward')" />
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
    <TotpStepUpDialog :controller="operationsExportStepUp" />
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
import settingsAPI, { type DailyCheckinRewardTier, type DailyCheckinStreakMultiplier } from '@/api/admin/settings'
import operationsAPI, {
  type DailyCheckinAdminRecord,
  type DailyCheckinAnalyticsResponse,
  type DailyCheckinSettingsUpdateRequest,
  type OperationsExportDataset,
  type OperationsOverviewResponse,
  type OperationsRetentionResponse,
} from '@/api/admin/operations'
import dashboardAPI from '@/api/admin/dashboard'
import { adminPaymentAPI } from '@/api/admin/payment'
import type { DashboardStats, GroupStat, ModelStat, UserSpendingRankingResponse } from '@/types'
import type { DashboardStats as PaymentDashboardStats } from '@/types/payment'
import OperationsOverviewTrendChart from './operations/OperationsOverviewTrendChart.vue'
import DailyCheckinTrendChart from './operations/DailyCheckinTrendChart.vue'
import OperationsBusinessInsights from './operations/OperationsBusinessInsights.vue'
import { useStepUp, isStepUpBlocked, isStepUpCancelled, stepUpBlockReason } from '@/composables/useStepUp'
import TotpStepUpDialog from '@/components/auth/TotpStepUpDialog.vue'

type TabKey = 'overview' | 'business' | 'checkin' | 'records' | 'rules'

interface CheckinRuleForm {
  daily_checkin_enabled: boolean
  daily_checkin_required_usage_usd: number
  daily_checkin_usage_scope: 'actual_cost' | 'balance_only'
  daily_checkin_reward_min_usd: number
  daily_checkin_reward_max_usd: number
  daily_checkin_daily_budget_usd: number
  daily_checkin_monthly_budget_usd: number
  daily_checkin_user_monthly_limit_usd: number
  daily_checkin_budget_fallback_reward_usd: number
  daily_checkin_budget_fallback_message: string
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
const operationsExportStepUp = useStepUp()
const appStore = useAppStore()

const activeTab = ref<TabKey>('overview')
const loading = ref(false)
const loadingOverview = ref(false)
const loadingAnalytics = ref(false)
const loadingRecords = ref(false)
const loadingBusiness = ref(false)
const exporting = ref(false)
const saving = ref(false)
const rangeDays = ref(30)
const customStartDate = ref('')
const customEndDate = ref('')
const overview = ref<OperationsOverviewResponse | null>(null)
const previousOverview = ref<OperationsOverviewResponse | null>(null)
const checkinAnalytics = ref<DailyCheckinAnalyticsResponse | null>(null)
const businessStats = ref<DashboardStats | null>(null)
const paymentStats = ref<PaymentDashboardStats | null>(null)
const businessRanking = ref<UserSpendingRankingResponse | null>(null)
const businessModels = ref<ModelStat[]>([])
const businessGroups = ref<GroupStat[]>([])
const businessDataAvailable = ref(true)
const businessAggregationComplete = ref(true)
const businessGroupDetailsAvailable = ref(true)
const businessRewardCost = ref(0)
const businessRetention = ref<OperationsRetentionResponse | null>(null)
const stats = ref<Awaited<ReturnType<typeof operationsAPI.getDailyCheckinStats>> | null>(null)
const records = ref<DailyCheckinAdminRecord[]>([])
const overviewError = ref(false)
const analyticsError = ref(false)
const recordsError = ref(false)
const businessError = ref(false)
const settingsLoaded = ref(false)
const analyticsLoaded = ref(false)
const recordsLoaded = ref(false)
const businessLoaded = ref(false)
const totalRecords = ref(0)
const recordPage = ref(1)
const recordPageSize = 20

// A generation protects data assignment, while a per-loader token protects
// the loading flag itself.  An older request may finish after a newer range or
// tab request; it must not clear the newer request's spinner, nor leave a
// spinner stuck when the newer generation does not load that section.
let overviewRequestToken = 0
let analyticsRequestToken = 0
let recordsRequestToken = 0
let businessRequestToken = 0
let rangeRequestGeneration = 0

const form = reactive<CheckinRuleForm>({
  daily_checkin_enabled: true,
  daily_checkin_required_usage_usd: 1,
  daily_checkin_usage_scope: 'actual_cost',
  daily_checkin_reward_min_usd: 1,
  daily_checkin_reward_max_usd: 3,
  daily_checkin_daily_budget_usd: 0,
  daily_checkin_monthly_budget_usd: 0,
  daily_checkin_user_monthly_limit_usd: 0,
  daily_checkin_budget_fallback_reward_usd: 0.01,
  daily_checkin_budget_fallback_message: '今日签到预算已用完哦～奖励0.01',
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
  { key: 'business' as const, label: t('admin.operations.businessInsights') },
  { key: 'checkin' as const, label: t('admin.operations.checkinAnalysis') },
  { key: 'records' as const, label: t('admin.operations.records') },
  { key: 'rules' as const, label: t('admin.operations.rules') },
])

const rangeOptions = computed(() => [
  { days: 7, label: t('admin.operations.last7Days') },
  { days: 30, label: t('admin.operations.last30Days') },
  { days: 90, label: t('admin.operations.last90Days') },
  { days: 370, label: t('admin.operations.last370Days') },
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
  comparisonMetric(t('admin.operations.apiDau'), overview.value?.summary.last_day_dau, previousOverview.value?.summary.last_day_dau, formatCount, t('admin.operations.apiDauHint')),
  comparisonMetric(t('admin.operations.newUsers'), overview.value?.summary.new_users, previousOverview.value?.summary.new_users, formatCount),
  comparisonMetric(t('admin.operations.requestUsers'), overview.value?.summary.period_request_users, previousOverview.value?.summary.period_request_users, formatCount),
  comparisonMetric(t('admin.operations.requests'), overview.value?.summary.requests, previousOverview.value?.summary.requests, formatCount),
  comparisonMetric(t('admin.operations.actualCost'), overview.value?.summary.actual_cost, previousOverview.value?.summary.actual_cost, formatUSD),
])

const funnelItems = computed(() => {
  const summary = checkinAnalytics.value?.summary
  const qualified = Number(summary?.qualified_users || 0)
  const checked = Number(summary?.checkin_users || 0)
  const streak = Number(summary?.streak_users || 0)
  const max = Math.max(qualified, checked, streak, 1)
  return [
    { label: t('admin.operations.qualifiedUsers'), value: formatCount(qualified), width: `${Math.round((qualified / max) * 100)}%` },
    { label: t('admin.operations.checkinUsers'), value: formatCount(checked), width: `${Math.round((checked / max) * 100)}%` },
    { label: t('admin.operations.streakUsers'), value: formatCount(streak), width: `${Math.round((streak / max) * 100)}%` },
  ]
})

const rewardProbabilityTotal = computed(() =>
  form.daily_checkin_reward_tiers.reduce((sum, tier) => sum + (Number(tier.probability_percent) || 0), 0)
)

function formatUSD(value: number | undefined | null): string {
  return `$${Number(value || 0).toFixed(2)}`
}

function formatCount(value: number | undefined | null): string {
  return Number(value || 0).toLocaleString()
}

function formatPercent(value: number | undefined | null): string {
  return `${(Number(value || 0) * 100).toFixed(1)}%`
}

function formatDays(value: number | undefined | null): string {
  return value !== undefined && value !== null && value >= 0 ? t('admin.operations.daysValue', { count: value }) : '-'
}

function formatEstimatedCheckins(value: number | undefined | null): string {
  return value === undefined || value === null ? '-' : t('admin.operations.checkinsValue', { count: value })
}

function comparisonMetric(
  label: string,
  current: number | undefined,
  previous: number | undefined,
  formatter: (value: number | undefined | null) => string,
  baseHint = '',
) {
  const hint = comparisonHint(current, previous)
  return {
    label,
    value: formatter(current),
    hint: [baseHint, hint].filter(Boolean).join(' · '),
    tone: comparisonTone(current, previous),
  }
}

function comparisonHint(current: number | undefined, previous: number | undefined): string {
  if (current === undefined || previous === undefined) return ''
  if (previous === 0) return current === 0 ? t('admin.operations.noChange') : t('admin.operations.newFromZero')
  const delta = ((current - previous) / Math.abs(previous)) * 100
  const prefix = delta > 0 ? '+' : ''
  return t('admin.operations.vsPreviousPeriod', { value: `${prefix}${delta.toFixed(1)}%` })
}

function comparisonTone(current: number | undefined, previous: number | undefined): 'positive' | 'negative' | 'neutral' {
  if (current === undefined || previous === undefined || current === previous) return 'neutral'
  return current > previous ? 'positive' : 'negative'
}

function formatMultiplier(value: number | undefined | null): string {
  return value ? `${Number(value).toFixed(2).replace(/\.?0+$/, '')}x` : '-'
}

function formatBudget(limit: number | undefined | null, remaining: number | undefined | null): string {
  return Number(limit || 0) > 0 ? `${formatUSD(remaining)} / ${formatUSD(limit)}` : t('admin.operations.unlimited')
}

function formatRewardPerCheckinUser(reward: number | undefined | null, checkins: number | undefined | null): string {
  const count = Number(checkins || 0)
  return count > 0 ? `${formatUSD(reward)} / ${formatCount(count)}` : `${formatUSD(reward)} / -`
}

function formatDateTime(value: string): string {
  if (!value) return '-'
  return new Date(value).toLocaleString()
}

function dateRangeQuery() {
  if (rangeDays.value === 0 && customStartDate.value && customEndDate.value) {
    return {
      start_date: customStartDate.value,
      end_date: customEndDate.value,
      timezone: reportingTimezone(),
    }
  }
  const end = new Date()
  const start = new Date()
  start.setDate(end.getDate() - rangeDays.value + 1)
  return {
    start_date: formatDateInput(start),
    end_date: formatDateInput(end),
    timezone: reportingTimezone(),
  }
}

function previousDateRangeQuery() {
  const current = dateRangeQuery()
  const currentStart = parseDateInput(current.start_date)
  const currentEnd = parseDateInput(current.end_date)
  const durationDays = Math.round((currentEnd.getTime() - currentStart.getTime()) / 86400000) + 1
  const previousEnd = new Date(currentStart)
  previousEnd.setDate(previousEnd.getDate() - 1)
  const previousStart = new Date(previousEnd)
  previousStart.setDate(previousStart.getDate() - durationDays + 1)
  return {
    start_date: formatDateInput(previousStart),
    end_date: formatDateInput(previousEnd),
    timezone: current.timezone,
  }
}

function reportingTimezone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
}

function parseDateInput(value: string): Date {
  const [year, month, day] = value.split('-').map(Number)
  return new Date(year, month - 1, day)
}

function dailyCheckinRecordQuery() {
  return {
    ...dateRangeQuery(),
    ...recordFilters,
  }
}

function exportFileDateRange(dataset: OperationsExportDataset) {
  const range = dateRangeQuery()
  if (dataset === 'daily_checkin_records') {
    return {
      start: recordFilters.date_from || range.start_date,
      end: recordFilters.date_to || range.end_date,
    }
  }
  return {
    start: range.start_date,
    end: range.end_date,
  }
}

function formatDateInput(date: Date): string {
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
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
  form.daily_checkin_budget_fallback_reward_usd = settings.daily_checkin_budget_fallback_reward_usd ?? 0.01
  form.daily_checkin_budget_fallback_message = settings.daily_checkin_budget_fallback_message || '今日签到预算已用完哦～奖励0.01'
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

function isCurrentRangeRequest(generation: number): boolean {
  return generation === rangeRequestGeneration
}

async function refreshAll() {
  const generation = ++rangeRequestGeneration
  loading.value = true
  try {
    await Promise.all([loadOverview(generation), loadActiveTab(true, generation)])
  } catch (error) {
    if (!isCurrentRangeRequest(generation)) return
    console.error('Failed to load operations center:', error)
    appStore.showError(t('admin.operations.loadFailed'))
  } finally {
    if (isCurrentRangeRequest(generation)) loading.value = false
  }
}

async function loadSettingsAndStats(generation = rangeRequestGeneration) {
  const [settings, nextStats] = await Promise.all([
    settingsAPI.getSettings(),
    operationsAPI.getDailyCheckinStats({ timezone: reportingTimezone() }),
  ])
  if (!isCurrentRangeRequest(generation)) return
  assignSettings(settings)
  stats.value = nextStats
  settingsLoaded.value = true
}

async function loadOverview(generation = rangeRequestGeneration) {
  const requestToken = ++overviewRequestToken
  loadingOverview.value = true
  overviewError.value = false
  try {
    const [current, previous] = await Promise.all([
      operationsAPI.getOperationsOverview(dateRangeQuery()),
      operationsAPI.getOperationsOverview(previousDateRangeQuery()),
    ])
    if (!isCurrentRangeRequest(generation)) return
    overview.value = current
    previousOverview.value = previous
  } catch (error) {
    if (!isCurrentRangeRequest(generation)) return
    overviewError.value = true
    throw error
  } finally {
    if (requestToken === overviewRequestToken) loadingOverview.value = false
  }
}

async function loadAnalytics(generation = rangeRequestGeneration) {
  const requestToken = ++analyticsRequestToken
  loadingAnalytics.value = true
  analyticsError.value = false
  try {
    const nextAnalytics = await operationsAPI.getDailyCheckinAnalytics(dateRangeQuery())
    if (!isCurrentRangeRequest(generation)) return
    checkinAnalytics.value = nextAnalytics
    analyticsLoaded.value = true
  } catch (error) {
    if (!isCurrentRangeRequest(generation)) return
    analyticsError.value = true
    throw error
  } finally {
    if (requestToken === analyticsRequestToken) loadingAnalytics.value = false
  }
}

async function loadBusinessInsights(generation = rangeRequestGeneration) {
  const requestToken = ++businessRequestToken
  loadingBusiness.value = true
  businessError.value = false
  try {
    const query = dateRangeQuery()
    const groupDetailsAvailable = calendarRangeDays(query.start_date, query.end_date) <= 31
    const [nextStats, modelResult, groupResult, rankingResult, paymentResult, analyticsResult, retentionResult] = await Promise.all([
      dashboardAPI.getStats(),
      dashboardAPI.getModelStats(query),
      groupDetailsAvailable ? dashboardAPI.getGroupStats(query) : Promise.resolve(null),
      dashboardAPI.getUserSpendingRanking({ ...query, limit: 10 }),
      adminPaymentAPI.getDashboard(query),
      operationsAPI.getDailyCheckinAnalytics(query),
      operationsAPI.getOperationsRetention(query),
    ])
    if (!isCurrentRangeRequest(generation)) return
    businessStats.value = nextStats
    businessModels.value = modelResult.models
    businessGroups.value = groupResult?.groups || []
    businessDataAvailable.value = modelResult.data_available !== false && rankingResult.data_available !== false
    businessAggregationComplete.value = modelResult.aggregation_complete !== false && rankingResult.aggregation_complete !== false
    businessGroupDetailsAvailable.value = groupDetailsAvailable
    businessRanking.value = rankingResult
    paymentStats.value = paymentResult.data
    businessRewardCost.value = analyticsResult.summary.reward_usd
    businessRetention.value = retentionResult
    checkinAnalytics.value = analyticsResult
    analyticsLoaded.value = true
    businessLoaded.value = true
  } catch (error) {
    if (!isCurrentRangeRequest(generation)) return
    businessError.value = true
    throw error
  } finally {
    if (requestToken === businessRequestToken) loadingBusiness.value = false
  }
}

function calendarRangeDays(startDate: string, endDate: string): number {
  const [startYear, startMonth, startDay] = startDate.split('-').map(Number)
  const [endYear, endMonth, endDay] = endDate.split('-').map(Number)
  const start = Date.UTC(startYear, startMonth - 1, startDay)
  const end = Date.UTC(endYear, endMonth - 1, endDay)
  return Math.floor((end - start) / 86400000) + 1
}

async function loadRecords(generation = rangeRequestGeneration) {
  const requestToken = ++recordsRequestToken
  loadingRecords.value = true
  recordsError.value = false
  try {
    const result = await operationsAPI.listDailyCheckinRecords({
      page: recordPage.value,
      page_size: recordPageSize,
      ...dailyCheckinRecordQuery(),
    })
    if (!isCurrentRangeRequest(generation)) return
    records.value = result.items
    totalRecords.value = result.total
    recordsLoaded.value = true
  } catch (error) {
    if (!isCurrentRangeRequest(generation)) return
    recordsError.value = true
    throw error
  } finally {
    if (requestToken === recordsRequestToken) loadingRecords.value = false
  }
}

async function setRange(days: number) {
  rangeDays.value = days
  customStartDate.value = ''
  customEndDate.value = ''
  recordPage.value = 1
  analyticsLoaded.value = false
  recordsLoaded.value = false
  businessLoaded.value = false
  await reloadForRangeChange()
}

async function applyCustomRange() {
  if (!customStartDate.value || !customEndDate.value || customStartDate.value > customEndDate.value) {
    appStore.showError(t('admin.operations.invalidDateRange'))
    return
  }
  rangeDays.value = 0
  recordPage.value = 1
  analyticsLoaded.value = false
  recordsLoaded.value = false
  businessLoaded.value = false
  await reloadForRangeChange()
}

async function reloadForRangeChange() {
  const generation = ++rangeRequestGeneration
  loading.value = true
  try {
    await Promise.all([loadOverview(generation), loadActiveTab(true, generation)])
  } catch (error) {
    if (!isCurrentRangeRequest(generation)) return
    console.error('Failed to reload operations range:', error)
    appStore.showError(t('admin.operations.loadFailed'))
  } finally {
    if (isCurrentRangeRequest(generation)) loading.value = false
  }
}

async function selectTab(tab: TabKey) {
  activeTab.value = tab
  const generation = rangeRequestGeneration
  try {
    await loadActiveTab(false, generation)
  } catch (error) {
    if (!isCurrentRangeRequest(generation)) return
    console.error(`Failed to load operations tab ${tab}:`, error)
    appStore.showError(t('admin.operations.loadFailed'))
  }
}

async function loadActiveTab(force: boolean, generation = rangeRequestGeneration) {
  switch (activeTab.value) {
    case 'business':
      if (force || !businessLoaded.value) await loadBusinessInsights(generation)
      break
    case 'checkin':
      await Promise.all([
        force || !settingsLoaded.value ? loadSettingsAndStats(generation) : Promise.resolve(),
        force || !analyticsLoaded.value ? loadAnalytics(generation) : Promise.resolve(),
      ])
      break
    case 'records':
      if (force || !recordsLoaded.value) await loadRecords(generation)
      break
    case 'rules':
      if (force || !settingsLoaded.value) await loadSettingsAndStats(generation)
      break
  }
}

async function applyRecordFilters() {
  recordPage.value = 1
  try {
    await loadRecords()
  } catch (error) {
    console.error('Failed to filter daily check-in records:', error)
    appStore.showError(t('admin.operations.recordsLoadFailed'))
  }
}

async function changeRecordPage(delta: number) {
  const previous = recordPage.value
  recordPage.value = Math.max(1, recordPage.value + delta)
  try {
    await loadRecords()
  } catch (error) {
    recordPage.value = previous
    console.error('Failed to paginate daily check-in records:', error)
    appStore.showError(t('admin.operations.recordsLoadFailed'))
  }
}

async function exportDataset(dataset: OperationsExportDataset) {
  exporting.value = true
  try {
    const blob = await operationsExportStepUp.run(() => operationsAPI.exportOperationsData({
      dataset,
      ...(dataset === 'daily_checkin_records' ? dailyCheckinRecordQuery() : dateRangeQuery()),
    }))
    const fileRange = exportFileDateRange(dataset)
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = `${dataset}_${fileRange.start}_to_${fileRange.end}.csv`
    document.body.appendChild(link)
    link.click()
    link.remove()
    URL.revokeObjectURL(url)
    appStore.showSuccess(t('admin.operations.exportSuccess'))
  } catch (error) {
    if (isStepUpCancelled(error)) {
      // 用户取消二次验证时保持静默。
    } else if (isStepUpBlocked(error)) {
      appStore.showError(
        stepUpBlockReason(error) === 'STEP_UP_ADMIN_API_KEY_FORBIDDEN'
          ? t('stepUp.adminApiKeyForbidden')
          : t('stepUp.notEnabled')
      )
    } else {
      console.error('Failed to export operations data:', error)
      appStore.showError(t('admin.operations.exportFailed'))
    }
  } finally {
    exporting.value = false
  }
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

function normalizedRules(): DailyCheckinSettingsUpdateRequest {
  const tiers = form.daily_checkin_reward_tiers.map((tier) => {
    const min = normalizeRewardAmount(tier.min_usd, 1)
    const max = Math.max(min, normalizeRewardAmount(tier.max_usd, min))
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
    daily_checkin_budget_fallback_reward_usd: normalizeRewardAmount(form.daily_checkin_budget_fallback_reward_usd, 0.01),
    daily_checkin_budget_fallback_message: form.daily_checkin_budget_fallback_message.trim() || '今日签到预算已用完哦～奖励0.01',
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

function normalizeRewardAmount(value: number, fallback: number): number {
  const normalized = Number(value)
  if (!Number.isFinite(normalized) || normalized < 0.01) {
    return fallback
  }
  return Math.round(Math.min(100, normalized) * 100) / 100
}

async function saveRules() {
  if (Math.abs(rewardProbabilityTotal.value - 100) >= 0.000001) {
    appStore.showError(`${t('admin.operations.probabilityTotal')}: ${rewardProbabilityTotal.value.toFixed(4)}%`)
    return
  }
  saving.value = true
  try {
    const updated = await operationsAPI.updateDailyCheckinSettings(normalizedRules())
    assignSettings(updated)
    await Promise.all([loadSettingsAndStats(), loadAnalytics()])
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
