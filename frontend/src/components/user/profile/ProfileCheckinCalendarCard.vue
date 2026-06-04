<template>
  <div v-if="status !== null && status.enabled !== false" class="card p-4">
    <div class="mb-3 flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
      <div>
        <div class="flex items-center gap-2">
          <div class="rounded-lg bg-green-50 p-1.5 text-green-600 dark:bg-green-900/20 dark:text-green-300">
            <Icon name="calendarCheck" size="sm" />
          </div>
          <div>
            <h3 class="font-semibold text-gray-900 dark:text-white">
              {{ t('profile.checkin.calendarTitle') }}
            </h3>
            <p class="text-xs text-gray-500 dark:text-gray-400">
              {{ t('profile.checkin.calendarDescription') }}
            </p>
          </div>
        </div>
      </div>
      <button
        type="button"
        class="inline-flex h-8 w-8 items-center justify-center rounded-lg text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-800 dark:text-gray-400 dark:hover:bg-dark-800 dark:hover:text-white"
        :title="t('common.refresh')"
        :disabled="loading"
        @click="loadStatus"
      >
        <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
      </button>
    </div>

    <div class="mb-3 grid grid-cols-3 gap-2 text-center">
      <div class="min-w-0">
        <p class="text-[11px] text-gray-500 dark:text-gray-400">
          {{ t('profile.checkin.checkedDays') }}
        </p>
        <p class="mt-0.5 text-base font-semibold text-gray-900 dark:text-white">
          {{ checkedCount }}
        </p>
      </div>
      <div class="min-w-0">
        <p class="text-[11px] text-gray-500 dark:text-gray-400">
          {{ t('profile.checkin.todayUsage') }}
        </p>
        <p class="mt-0.5 text-base font-semibold text-gray-900 dark:text-white">
          {{ formatUSD(status?.today_usage_usd ?? 0) }}
        </p>
      </div>
      <div class="min-w-0">
        <p class="text-[11px] text-gray-500 dark:text-gray-400">
          {{ t('profile.checkin.rewardRange') }}
        </p>
        <p class="mt-0.5 text-base font-semibold text-gray-900 dark:text-white">
          {{ rewardRangeText }}
        </p>
      </div>
    </div>

    <div class="grid grid-cols-7 gap-y-1 text-center">
      <div
        v-for="weekday in weekdays"
        :key="weekday"
        class="pb-1 text-[11px] font-medium text-gray-400 dark:text-gray-500"
      >
        {{ weekday }}
      </div>
      <div
        v-for="blank in leadingBlankDays"
        :key="`blank-${blank}`"
        class="h-8"
      />
      <div
        v-for="day in monthDays"
        :key="day.date"
        class="flex h-8 items-center justify-center"
      >
        <div
          class="flex h-7 w-7 items-center justify-center rounded-full text-xs font-medium transition-colors"
          :class="dayCellClass(day)"
          :aria-label="day.ariaLabel"
        >
          <Icon v-if="day.checked" name="check" size="sm" :stroke-width="2.2" />
          <span v-else>{{ day.day }}</span>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { userAPI, type DailyCheckinStatus } from '@/api/user'
import { useAppStore } from '@/stores'
import { extractApiErrorMessage } from '@/utils/apiError'

interface MonthDay {
  day: number
  date: string
  checked: boolean
  today: boolean
  ariaLabel: string
}

const { t } = useI18n()
const appStore = useAppStore()
const loading = ref(false)
const status = ref<DailyCheckinStatus | null>(null)

const weekdays = computed(() => [
  t('profile.checkin.weekdays.mon'),
  t('profile.checkin.weekdays.tue'),
  t('profile.checkin.weekdays.wed'),
  t('profile.checkin.weekdays.thu'),
  t('profile.checkin.weekdays.fri'),
  t('profile.checkin.weekdays.sat'),
  t('profile.checkin.weekdays.sun'),
])

const monthParts = computed(() => {
  const raw = status.value?.month || new Date().toISOString().slice(0, 7)
  const [yearRaw, monthRaw] = raw.split('-')
  const year = Number(yearRaw) || new Date().getFullYear()
  const monthIndex = Math.max(0, Math.min(11, (Number(monthRaw) || 1) - 1))
  return { year, monthIndex }
})

const checkedDates = computed(() => new Set((status.value?.month_checkins ?? []).map((record) => record.date)))
const checkedCount = computed(() => checkedDates.value.size)

const leadingBlankDays = computed(() => {
  const firstDay = new Date(monthParts.value.year, monthParts.value.monthIndex, 1).getDay()
  return (firstDay + 6) % 7
})

const monthDays = computed<MonthDay[]>(() => {
  const { year, monthIndex } = monthParts.value
  const count = new Date(year, monthIndex + 1, 0).getDate()
  const today = status.value?.today || ''
  return Array.from({ length: count }, (_, index) => {
    const day = index + 1
    const date = `${year}-${String(monthIndex + 1).padStart(2, '0')}-${String(day).padStart(2, '0')}`
    const checked = checkedDates.value.has(date)
    return {
      day,
      date,
      checked,
      today: today === date,
      ariaLabel: checked
        ? t('profile.checkin.checkedDayLabel', { date })
        : t('profile.checkin.uncheckedDayLabel', { date }),
    }
  })
})

const rewardRangeText = computed(() => {
  const min = status.value?.reward_min_usd ?? 1
  const max = status.value?.reward_max_usd ?? 3
  return min === max ? formatUSD(min) : `${formatUSD(min)} - ${formatUSD(max)}`
})

function formatUSD(value: number): string {
  return `$${Number(value || 0).toFixed(2)}`
}

function dayCellClass(day: MonthDay): string {
  if (day.checked) {
    return 'bg-green-500 text-white shadow-sm dark:bg-green-500'
  }
  if (day.today) {
    return 'border border-primary-300 bg-primary-50 text-primary-700 dark:border-primary-700 dark:bg-primary-900/20 dark:text-primary-300'
  }
  return 'text-gray-600 hover:bg-gray-50 dark:text-gray-400 dark:hover:bg-dark-800'
}

async function loadStatus() {
  loading.value = true
  try {
    status.value = await userAPI.getDailyCheckinStatus()
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('profile.checkin.loadFailed')))
  } finally {
    loading.value = false
  }
}

function handleCheckinUpdated() {
  loadStatus()
}

onMounted(() => {
  loadStatus()
  window.addEventListener('daily-checkin-updated', handleCheckinUpdated)
})

onBeforeUnmount(() => {
  window.removeEventListener('daily-checkin-updated', handleCheckinUpdated)
})
</script>
