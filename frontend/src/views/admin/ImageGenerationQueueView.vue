<template>
  <AppLayout>
    <div class="space-y-5">
      <section class="flex flex-wrap items-end justify-between gap-4 border-b border-gray-200 pb-5 dark:border-dark-700">
        <div class="min-w-0">
          <p class="text-xs font-medium uppercase tracking-[0.08em] text-gray-500 dark:text-gray-400">{{ t('admin.imageGenerations.queueEyebrow') }}</p>
          <h1 class="mt-1 text-xl font-semibold text-gray-900 dark:text-white">{{ t('admin.imageGenerations.queueTitle') }}</h1>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.imageGenerations.queueDescription') }}</p>
        </div>
        <div class="flex flex-wrap items-center gap-3">
          <label class="inline-flex cursor-pointer items-center gap-2 text-sm text-gray-600 dark:text-gray-300">
            <input v-model="autoRefresh" type="checkbox" class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500 dark:border-dark-600 dark:bg-dark-800" />
            {{ t('admin.imageGenerations.autoRefresh') }}
          </label>
          <button type="button" class="btn btn-secondary btn-sm" :disabled="loading" :title="t('common.refresh')" @click="reload">
            <Icon name="refresh" size="sm" class="mr-2" />
            {{ t('common.refresh') }}
          </button>
        </div>
      </section>

      <section class="grid gap-3 border-y border-gray-200 py-4 dark:border-dark-700 sm:grid-cols-3">
        <div class="border-l-2 border-amber-500 pl-3">
          <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.imageGenerations.processing') }}</div>
          <div class="mt-1 text-xl font-semibold text-gray-900 dark:text-white">{{ stats.processing }}</div>
        </div>
        <div class="border-l-2 border-emerald-500 pl-3">
          <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.imageGenerations.completed') }}</div>
          <div class="mt-1 text-xl font-semibold text-gray-900 dark:text-white">{{ stats.completed }}</div>
        </div>
        <div class="border-l-2 border-red-500 pl-3">
          <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.imageGenerations.failed') }}</div>
          <div class="mt-1 text-xl font-semibold text-gray-900 dark:text-white">{{ stats.failed }}</div>
        </div>
      </section>

      <section class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-200 pb-4 dark:border-dark-700">
        <div class="flex flex-wrap items-center gap-2">
          <label for="image-task-status" class="text-sm text-gray-600 dark:text-gray-300">{{ t('admin.imageGenerations.statusFilter') }}</label>
          <select id="image-task-status" v-model="status" class="input min-w-[150px]" :disabled="loading">
            <option value="all">{{ t('admin.imageGenerations.allStatuses') }}</option>
            <option value="processing">{{ t('admin.imageGenerations.processing') }}</option>
            <option value="completed">{{ t('admin.imageGenerations.completed') }}</option>
            <option value="failed">{{ t('admin.imageGenerations.failed') }}</option>
          </select>
        </div>
        <div class="text-xs text-gray-500 dark:text-gray-400">
          <span v-if="autoRefresh">{{ t('admin.imageGenerations.refreshingEveryTwoSeconds') }}</span>
          <span v-if="lastUpdated">{{ t('admin.imageGenerations.lastUpdated', { time: formatTime(lastUpdated) }) }}</span>
        </div>
      </section>

      <div v-if="errorMessage" class="rounded-md border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-900/60 dark:bg-red-950/30 dark:text-red-300">
        {{ errorMessage }}
      </div>

      <div class="overflow-x-auto border-y border-gray-200 dark:border-dark-700">
        <table class="min-w-full divide-y divide-gray-200 text-left text-sm dark:divide-dark-700">
          <thead class="bg-gray-50 text-xs uppercase tracking-wide text-gray-500 dark:bg-dark-900/70 dark:text-gray-400">
            <tr>
              <th class="px-3 py-3 font-medium">{{ t('admin.imageGenerations.task') }}</th>
              <th class="px-3 py-3 font-medium">{{ t('admin.imageGenerations.model') }}</th>
              <th class="px-3 py-3 font-medium">{{ t('admin.imageGenerations.owner') }}</th>
              <th class="px-3 py-3 font-medium">{{ t('admin.imageGenerations.submittedAt') }}</th>
              <th class="px-3 py-3 font-medium">{{ t('admin.imageGenerations.duration') }}</th>
              <th class="px-3 py-3 font-medium">{{ t('admin.imageGenerations.status') }}</th>
              <th class="px-3 py-3 font-medium">{{ t('admin.imageGenerations.results') }}</th>
              <th class="px-3 py-3 font-medium">{{ t('admin.imageGenerations.stopReason') }}</th>
            </tr>
          </thead>
          <tbody v-if="loading" class="divide-y divide-gray-100 dark:divide-dark-800">
            <tr v-for="index in 6" :key="index" class="animate-pulse">
              <td v-for="column in 8" :key="column" class="px-3 py-4"><div class="h-4 rounded bg-gray-100 dark:bg-dark-800" /></td>
            </tr>
          </tbody>
          <tbody v-else-if="items.length" class="divide-y divide-gray-100 dark:divide-dark-800">
            <tr
              v-for="task in items"
              :key="task.id"
              tabindex="0"
              class="cursor-pointer transition-colors hover:bg-gray-50 focus:bg-gray-50 focus:outline-none dark:hover:bg-dark-900/70 dark:focus:bg-dark-900/70"
              @click="selectedTask = task"
              @keydown.enter="selectedTask = task"
            >
              <td class="whitespace-nowrap px-3 py-3">
                <div class="font-mono text-xs text-gray-800 dark:text-gray-100">{{ shortID(task.task_id) }}</div>
                <div class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ task.platform || '-' }} · {{ operationLabel(task.operation) }}</div>
              </td>
              <td class="max-w-[180px] truncate px-3 py-3 text-gray-700 dark:text-gray-200" :title="task.model || '-'">{{ task.model || '-' }}</td>
              <td class="whitespace-nowrap px-3 py-3 text-xs text-gray-600 dark:text-gray-300">U{{ task.user_id }} · K{{ task.api_key_id }}</td>
              <td class="whitespace-nowrap px-3 py-3 text-xs text-gray-600 dark:text-gray-300">{{ formatTime(task.created_at) }}</td>
              <td class="whitespace-nowrap px-3 py-3 text-xs text-gray-600 dark:text-gray-300">{{ formatDuration(task.duration_ms) }}</td>
              <td class="whitespace-nowrap px-3 py-3"><span class="inline-flex items-center gap-1.5" :class="statusTone(task.status)"><span class="h-1.5 w-1.5 rounded-full bg-current" />{{ statusLabel(task.status) }}</span></td>
              <td class="whitespace-nowrap px-3 py-3 text-xs text-gray-600 dark:text-gray-300">{{ resultCount(task) }}</td>
              <td class="max-w-[260px] truncate px-3 py-3 text-xs text-gray-600 dark:text-gray-300" :title="task.stop_reason || ''">{{ task.stop_reason || '-' }}</td>
            </tr>
          </tbody>
          <tbody v-else>
            <tr><td colspan="8" class="px-3 py-16 text-center text-sm text-gray-500 dark:text-gray-400">{{ t('admin.imageGenerations.noTasks') }}</td></tr>
          </tbody>
        </table>
      </div>

      <div class="flex items-center justify-between border-t border-gray-200 pt-4 dark:border-dark-700">
        <button type="button" class="btn btn-secondary btn-sm" :disabled="cursorHistory.length === 0 || loading" @click="previousPage">{{ t('pagination.previous') }}</button>
        <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.imageGenerations.page', { page: cursorHistory.length + 1 }) }}</span>
        <button type="button" class="btn btn-secondary btn-sm" :disabled="!hasMore || loading" @click="nextPage">{{ t('pagination.next') }}</button>
      </div>
    </div>

    <div v-if="selectedTask" class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4" @click.self="selectedTask = null">
      <section class="max-h-[88vh] w-full max-w-2xl overflow-y-auto bg-white dark:bg-dark-900">
        <header class="flex items-center justify-between gap-4 border-b border-gray-200 px-5 py-4 dark:border-dark-700">
          <div class="min-w-0">
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.imageGenerations.taskDetails') }}</p>
            <h2 class="mt-1 truncate font-mono text-sm text-gray-900 dark:text-white">{{ selectedTask.task_id }}</h2>
          </div>
          <button type="button" class="icon-btn" :title="t('common.close')" @click="selectedTask = null"><Icon name="x" /></button>
        </header>
        <div class="grid gap-x-6 gap-y-4 px-5 py-5 sm:grid-cols-2">
          <div><dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.imageGenerations.status') }}</dt><dd class="mt-1 text-sm" :class="statusTone(selectedTask.status)">{{ statusLabel(selectedTask.status) }}</dd></div>
          <div><dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.imageGenerations.model') }}</dt><dd class="mt-1 text-sm text-gray-900 dark:text-white">{{ selectedTask.model || '-' }}</dd></div>
          <div><dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.imageGenerations.operation') }}</dt><dd class="mt-1 text-sm text-gray-900 dark:text-white">{{ operationLabel(selectedTask.operation) }}</dd></div>
          <div><dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.imageGenerations.duration') }}</dt><dd class="mt-1 text-sm text-gray-900 dark:text-white">{{ formatDuration(selectedTask.duration_ms) }}</dd></div>
          <div class="sm:col-span-2"><dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.imageGenerations.stopReason') }}</dt><dd class="mt-1 whitespace-pre-wrap text-sm text-gray-900 dark:text-white">{{ selectedTask.stop_reason || '-' }}</dd></div>
          <div v-if="selectedTask.result_urls?.length" class="sm:col-span-2">
            <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.imageGenerations.results') }}</dt>
            <dd class="mt-2 space-y-2">
              <a v-for="(url, index) in selectedTask.result_urls" :key="url" :href="url" target="_blank" rel="noopener noreferrer" class="block truncate text-sm text-primary-600 hover:underline dark:text-primary-400">{{ t('admin.imageGenerations.resultNumber', { number: index + 1 }) }}</a>
            </dd>
          </div>
        </div>
      </section>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import imageGenerationsAPI, { type AsyncImageTaskAdmin, type AsyncImageTaskStatus, type AsyncImageTaskStats } from '@/api/admin/imageGenerations'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'

const { t } = useI18n()
const items = ref<AsyncImageTaskAdmin[]>([])
const selectedTask = ref<AsyncImageTaskAdmin | null>(null)
const status = ref('all')
const loading = ref(false)
const errorMessage = ref('')
const autoRefresh = ref(true)
const lastUpdated = ref(0)
const cursor = ref('')
const nextCursor = ref('')
const hasMore = ref(false)
const cursorHistory = ref<string[]>([])
const stats = reactive<AsyncImageTaskStats>({ processing: 0, completed: 0, failed: 0 })
let refreshTimer: ReturnType<typeof setInterval> | null = null

async function load(): Promise<void> {
  if (loading.value) return
  loading.value = true
  errorMessage.value = ''
  try {
    const page = await imageGenerationsAPI.listTasks({ status: status.value, cursor: cursor.value || undefined, limit: 50 })
    items.value = page.items || []
    nextCursor.value = page.next_cursor || ''
    hasMore.value = Boolean(page.has_more && page.next_cursor)
    stats.processing = page.stats?.processing || 0
    stats.completed = page.stats?.completed || 0
    stats.failed = page.stats?.failed || 0
    lastUpdated.value = Date.now()
  } catch (error: any) {
    errorMessage.value = error?.message || t('admin.imageGenerations.loadFailed')
  } finally {
    loading.value = false
  }
}

function reload(): void {
  cursor.value = ''
  nextCursor.value = ''
  cursorHistory.value = []
  void load()
}

function nextPage(): void {
  if (!nextCursor.value) return
  cursorHistory.value.push(cursor.value)
  cursor.value = nextCursor.value
  void load()
}

function previousPage(): void {
  const previous = cursorHistory.value.pop() || ''
  cursor.value = previous
  void load()
}

function setRefreshTimer(): void {
  if (refreshTimer) clearInterval(refreshTimer)
  refreshTimer = null
  if (autoRefresh.value && document.visibilityState === 'visible') {
    refreshTimer = setInterval(() => { void load() }, 2000)
  }
}

function handleVisibilityChange(): void {
  setRefreshTimer()
  if (document.visibilityState === 'visible') void load()
}

function statusLabel(value: AsyncImageTaskStatus): string {
  return t(`admin.imageGenerations.${value}`)
}

function statusTone(value: AsyncImageTaskStatus): string {
  if (value === 'completed') return 'text-emerald-600 dark:text-emerald-400'
  if (value === 'failed') return 'text-red-600 dark:text-red-400'
  return 'text-amber-600 dark:text-amber-400'
}

function operationLabel(value?: string): string {
  return value === 'edit' ? t('admin.imageGenerations.edit') : t('admin.imageGenerations.generation')
}

function resultCount(task: AsyncImageTaskAdmin): number {
  return task.result_count ?? 0
}

function shortID(value: string): string {
  return value.length > 24 ? `${value.slice(0, 12)}...${value.slice(-8)}` : value
}

function formatTime(value: number): string {
  const date = new Date(value > 10_000_000_000 ? value : value * 1000)
  return Number.isNaN(date.getTime()) ? '-' : date.toLocaleString(undefined, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

function formatDuration(value: number): string {
  if (value < 1000) return `${value} ms`
  return `${(value / 1000).toFixed(value >= 10_000 ? 0 : 1)} s`
}

watch(status, reload)
watch(autoRefresh, setRefreshTimer)

onMounted(() => {
  document.addEventListener('visibilitychange', handleVisibilityChange)
  void load()
  setRefreshTimer()
})

onBeforeUnmount(() => {
  document.removeEventListener('visibilitychange', handleVisibilityChange)
  if (refreshTimer) clearInterval(refreshTimer)
})
</script>
