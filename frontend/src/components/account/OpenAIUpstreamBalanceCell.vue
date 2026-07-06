<template>
  <div v-if="visible" class="space-y-1 text-xs">
    <div v-if="isInfiniteBalanceAccount" class="text-sm font-semibold text-gray-800 dark:text-gray-100">
      ♾️
    </div>
    <template v-else>
      <div class="font-semibold" :class="amountClass">{{ balanceLabel }}</div>
      <div class="flex flex-wrap items-center gap-1 text-[11px] text-gray-400">
        <span v-if="providerLabel">{{ providerLabel }}</span>
        <button type="button" class="text-blue-600 hover:underline disabled:opacity-50 dark:text-blue-400" :disabled="loading" @click="refresh">
          {{ loading ? t('common.loading') : t('admin.accounts.upstreamBalance.refresh') }}
        </button>
      </div>
      <div v-if="groupLabel" class="inline-flex max-w-[160px] rounded border border-blue-200 bg-blue-50 px-2 py-1 text-[11px] font-semibold text-blue-800 dark:border-blue-500/30 dark:bg-blue-500/10 dark:text-blue-200" :title="groupLabel">
        <span class="truncate">{{ groupLabel }}</span>
      </div>
      <div v-if="rateLabel" class="text-[11px] text-gray-500 dark:text-gray-400">{{ rateLabel }}</div>
      <div v-if="showErrorHint" class="text-[11px] text-red-500">{{ t('admin.accounts.upstreamBalance.errorHint') }}</div>
      <div v-else-if="updatedAtLabel" class="text-[11px] text-gray-400">{{ updatedAtLabel }}</div>
    </template>
  </div>
  <span v-else class="text-sm text-gray-400 dark:text-dark-500">-</span>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { accountsAPI } from '@/api/admin/accounts'
import type { Account } from '@/types'
import { formatDateTime } from '@/utils/format'

const props = defineProps<{ account: Account }>()
const emit = defineEmits<{ refreshed: [account: Account] }>()
const { t } = useI18n()
const loading = ref(false)
const localAccount = ref<Account | null>(null)
const localError = ref('')
const INFINITE_UPSTREAM_BALANCE_ACCOUNT_ID = 401
const INFINITE_UPSTREAM_BALANCE_ACCOUNT_NAME = 'AI Nexus'

const account = computed(() => localAccount.value ?? props.account)
const extra = computed(() => account.value.extra ?? {})
const visible = computed(() => props.account.platform === 'openai' && props.account.type === 'apikey')
const isInfiniteBalanceAccount = computed(() =>
  account.value.id === INFINITE_UPSTREAM_BALANCE_ACCOUNT_ID &&
  account.value.name === INFINITE_UPSTREAM_BALANCE_ACCOUNT_NAME
)
const status = computed(() => String(extra.value.upstream_balance_status ?? '').toLowerCase())
const providerLabel = computed(() => String(extra.value.upstream_balance_provider ?? '').trim())
const groupLabel = computed(() => String(extra.value.upstream_group ?? '').trim())

const balanceLabel = computed(() => {
  const value = extra.value.upstream_balance_remaining
  const unit = String(extra.value.upstream_balance_unit ?? 'USD')
  if (typeof value === 'number' && Number.isFinite(value)) {
    if (unit.toUpperCase() === 'USD') return `$${new Intl.NumberFormat(undefined, { maximumFractionDigits: 4 }).format(value)}`
    return `${new Intl.NumberFormat(undefined, { maximumFractionDigits: 4 }).format(value)} ${unit || 'quota'}`
  }
  if (status.value === 'error') return t('admin.accounts.upstreamBalance.failed')
  return t('admin.accounts.upstreamBalance.unknown')
})

const rateLabel = computed(() => {
  const real = extra.value.upstream_effective_rate_multiplier
  const base = extra.value.upstream_group_rate_multiplier
  if (typeof real === 'number' && Number.isFinite(real)) return t('admin.accounts.upstreamBalance.realRate', { rate: real.toFixed(2) })
  if (typeof base === 'number' && Number.isFinite(base)) return t('admin.accounts.upstreamBalance.baseRate', { rate: base.toFixed(2) })
  return t('admin.accounts.upstreamBalance.accountRateFallback', { rate: formatRate(account.value.rate_multiplier) })
})

const updatedAtLabel = computed(() => {
  const value = extra.value.upstream_balance_updated_at
  if (typeof value !== 'string' || !value) return ''
  return t('admin.accounts.upstreamBalance.updatedAt', { time: formatDateTime(value) })
})

const amountClass = computed(() => status.value === 'error' ? 'text-red-500' : 'text-gray-800 dark:text-gray-100')
const showErrorHint = computed(() => status.value === 'error' || Boolean(localError.value || extra.value.upstream_balance_error))
const formatRate = (value?: number | null) => {
  const rate = typeof value === 'number' && Number.isFinite(value) ? value : 1
  return rate.toFixed(2)
}

async function refresh() {
  if (loading.value) return
  loading.value = true
  localError.value = ''
  try {
    const updated = await accountsAPI.refreshUpstreamBalance(props.account.id)
    localAccount.value = updated
    emit('refreshed', updated)
  } catch (error: any) {
    localError.value = error?.message || error?.response?.data?.message || t('common.error')
  } finally {
    loading.value = false
  }
}

watch(() => props.account, () => {
  localAccount.value = null
  localError.value = ''
})
</script>
