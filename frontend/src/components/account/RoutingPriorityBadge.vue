<template>
  <button
    v-if="summary"
    type="button"
    class="inline-flex min-w-[132px] items-center gap-1.5 rounded-md border px-2 py-1 text-left text-xs transition-colors"
    :class="badgeClass"
    @click="$emit('open')"
  >
    <span class="font-semibold">{{ summary.is_schedulable_now && summary.rank ? `#${summary.rank}` : statusText }}</span>
    <span class="rounded bg-white/70 px-1 py-0.5 text-[10px] font-medium dark:bg-dark-900/40">{{ summary.quality_score }}</span>
    <span class="truncate">{{ reasonText }}</span>
  </button>
  <span v-else class="text-sm text-gray-400 dark:text-dark-500">-</span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { OpenAIRoutingSummary } from '@/types'

const props = defineProps<{ summary?: OpenAIRoutingSummary | null }>()
defineEmits<{ open: [] }>()
const { t, te } = useI18n()

const translate = (prefix: string, value: string) => {
  const key = `${prefix}.${value}`
  return te(key) ? t(key) : value
}

const badgeClass = computed(() => {
  const s = props.summary
  if (!s?.is_schedulable_now) return 'border-amber-200 bg-amber-50 text-amber-700 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-300'
  if (s.tier === 'primary') return 'border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-500/30 dark:bg-emerald-500/10 dark:text-emerald-300'
  if (s.tier === 'backup') return 'border-sky-200 bg-sky-50 text-sky-700 dark:border-sky-500/30 dark:bg-sky-500/10 dark:text-sky-300'
  return 'border-gray-200 bg-gray-50 text-gray-700 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-200'
})

const statusText = computed(() => props.summary ? translate('admin.accounts.routingPriority.status', props.summary.status_label) : '')
const reasonText = computed(() => props.summary ? translate('admin.accounts.routingPriority.summary', props.summary.summary_reason) : '')
</script>
