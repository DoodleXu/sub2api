<template>
  <button
    type="button"
    class="inline-flex min-h-9 items-center gap-1.5 rounded-lg px-2.5 py-1.5 text-sm font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-60"
    :class="buttonClass"
    :disabled="loading || submitting || status?.checked_in"
    :title="buttonTitle"
    @click="handleCheckin"
  >
    <Icon
      :name="status?.checked_in ? 'checkCircle' : 'calendarCheck'"
      size="sm"
      :class="submitting ? 'animate-pulse' : ''"
    />
    <span class="hidden sm:inline">
      {{ status?.checked_in ? t('common.checkedIn') : t('common.checkin') }}
    </span>
  </button>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { userAPI, type DailyCheckinStatus } from '@/api/user'
import { useAppStore, useAuthStore } from '@/stores'
import { extractApiErrorCode, extractApiErrorMessage, extractApiErrorMetadata } from '@/utils/apiError'

const { t } = useI18n()
const appStore = useAppStore()
const authStore = useAuthStore()

const loading = ref(false)
const submitting = ref(false)
const status = ref<DailyCheckinStatus | null>(null)

const buttonClass = computed(() => {
  if (status.value?.checked_in) {
    return 'bg-green-50 text-green-700 hover:bg-green-100 dark:bg-green-900/20 dark:text-green-300 dark:hover:bg-green-900/30'
  }
  if (status.value?.eligible) {
    return 'bg-amber-50 text-amber-700 hover:bg-amber-100 dark:bg-amber-900/20 dark:text-amber-300 dark:hover:bg-amber-900/30'
  }
  return 'text-gray-600 hover:bg-gray-100 hover:text-gray-900 dark:text-dark-400 dark:hover:bg-dark-800 dark:hover:text-white'
})

const buttonTitle = computed(() => {
  if (status.value?.checked_in) {
    return t('profile.checkin.alreadyCheckedIn')
  }
  if (status.value?.eligible) {
    return t('profile.checkin.readyTitle', {
      min: status.value.reward_min_usd,
      max: status.value.reward_max_usd,
    })
  }
  const current = status.value?.today_usage_usd ?? 0
  const required = status.value?.required_usage_usd ?? 1
  return t('profile.checkin.needUsageTitle', {
    current: formatUSD(current),
    required: formatUSD(required),
  })
})

function formatUSD(value: number): string {
  return `$${Number(value || 0).toFixed(2)}`
}

async function loadStatus() {
  if (!authStore.user) return
  loading.value = true
  try {
    status.value = await userAPI.getDailyCheckinStatus()
  } catch (error) {
    console.error('Failed to load daily check-in status:', error)
  } finally {
    loading.value = false
  }
}

function checkinErrorMessage(error: unknown): string {
  const code = extractApiErrorCode(error)
  if (code === 'DAILY_CHECKIN_ALREADY_CHECKED_IN') {
    return t('profile.checkin.alreadyCheckedIn')
  }
  if (code === 'DAILY_CHECKIN_USAGE_NOT_ENOUGH') {
    const metadata = extractApiErrorMetadata(error)
    return t('profile.checkin.usageNotEnough', {
      current: formatUSD(Number(metadata?.today_usage_usd ?? status.value?.today_usage_usd ?? 0)),
      required: formatUSD(Number(metadata?.required_usage_usd ?? status.value?.required_usage_usd ?? 1)),
    })
  }
  return extractApiErrorMessage(error, t('profile.checkin.failed'))
}

async function handleCheckin() {
  if (submitting.value || status.value?.checked_in) return
  submitting.value = true
  try {
    const result = await userAPI.dailyCheckin()
    status.value = result
    appStore.showSuccess(t('profile.checkin.success', { amount: formatUSD(result.reward_amount) }))
    window.dispatchEvent(new CustomEvent('daily-checkin-updated'))
    authStore.refreshUser().catch((error) => {
      console.error('Failed to refresh user after daily check-in:', error)
    })
  } catch (error) {
    appStore.showError(checkinErrorMessage(error))
    await loadStatus()
  } finally {
    submitting.value = false
  }
}

onMounted(() => {
  loadStatus()
})
</script>
