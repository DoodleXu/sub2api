<template>
  <AppLayout>
    <TablePageLayout class="image-queue-layout">
      <template #filters>
        <section class="mb-4 px-1 lg:hidden">
          <h1 class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('admin.imageGenerations.queueTitle') }}</h1>
          <p class="mt-1 text-sm leading-5 text-gray-500 dark:text-gray-400">{{ t('admin.imageGenerations.queueDescription') }}</p>
        </section>

        <div class="space-y-4">
          <div
            v-if="errorMessage"
            role="alert"
            class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-900/60 dark:bg-red-950/30 dark:text-red-300"
          >
            {{ errorMessage }}
          </div>

          <section class="card overflow-hidden">
            <div class="grid divide-y divide-gray-100 dark:divide-dark-700 sm:grid-cols-3 sm:divide-x sm:divide-y-0">
              <div class="flex min-w-0 items-center gap-3 px-4 py-4 sm:px-5">
                <div class="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-amber-50 text-amber-600 dark:bg-amber-950/40 dark:text-amber-400">
                  <Icon name="clock" size="md" />
                </div>
                <div class="min-w-0">
                  <p class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('admin.imageGenerations.processing') }}</p>
                  <p class="mt-0.5 text-2xl font-semibold text-gray-900 dark:text-white">{{ stats.processing }}</p>
                </div>
              </div>
              <div class="flex min-w-0 items-center gap-3 px-4 py-4 sm:px-5">
                <div class="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-emerald-50 text-emerald-600 dark:bg-emerald-950/40 dark:text-emerald-400">
                  <Icon name="checkCircle" size="md" />
                </div>
                <div class="min-w-0">
                  <p class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('admin.imageGenerations.completed') }}</p>
                  <p class="mt-0.5 text-2xl font-semibold text-gray-900 dark:text-white">{{ stats.completed }}</p>
                </div>
              </div>
              <div class="flex min-w-0 items-center gap-3 px-4 py-4 sm:px-5">
                <div class="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-red-50 text-red-600 dark:bg-red-950/40 dark:text-red-400">
                  <Icon name="xCircle" size="md" />
                </div>
                <div class="min-w-0">
                  <p class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('admin.imageGenerations.failed') }}</p>
                  <p class="mt-0.5 text-2xl font-semibold text-gray-900 dark:text-white">{{ stats.failed }}</p>
                </div>
              </div>
            </div>

            <div class="flex flex-col gap-4 border-t border-gray-100 bg-white px-4 py-4 dark:border-dark-700 dark:bg-dark-800 sm:px-5 lg:flex-row lg:items-end lg:justify-between">
              <div class="w-full sm:w-44">
                <label for="image-task-status" class="input-label">{{ t('admin.imageGenerations.statusFilter') }}</label>
                <Select
                  id="image-task-status"
                  :model-value="status"
                  :options="statusOptions"
                  :disabled="initialLoading"
                  @update:model-value="setStatusFilter"
                />
              </div>

              <div class="grid w-full gap-3 sm:grid-cols-2 lg:w-auto">
                <div class="w-full sm:w-44">
                  <label for="image-task-start-date" class="input-label">{{ t('admin.imageGenerations.startDate') }}</label>
                  <input id="image-task-start-date" v-model="startDate" type="date" class="input w-full" @change="applyDateFilter" />
                </div>
                <div class="w-full sm:w-44">
                  <label for="image-task-end-date" class="input-label">{{ t('admin.imageGenerations.endDate') }}</label>
                  <input id="image-task-end-date" v-model="endDate" type="date" class="input w-full" @change="applyDateFilter" />
                </div>
              </div>

              <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between lg:justify-end">
                <div class="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-gray-500 dark:text-gray-400">
                  <span v-if="autoRefresh" class="inline-flex items-center gap-1.5">
                    <span class="h-1.5 w-1.5 rounded-full bg-emerald-500" />
                    {{ t('admin.imageGenerations.refreshingEveryTwoSeconds') }}
                  </span>
                  <span v-if="lastUpdated">{{ t('admin.imageGenerations.lastUpdated', { time: formatTime(lastUpdated) }) }}</span>
                </div>
                <div class="flex items-center justify-between gap-3 sm:justify-end">
                  <div class="flex items-center gap-2">
                    <span id="image-task-auto-refresh" class="text-sm text-gray-600 dark:text-gray-300">{{ t('admin.imageGenerations.autoRefresh') }}</span>
                    <Toggle v-model="autoRefresh" aria-labelledby="image-task-auto-refresh" />
                  </div>
                  <button
                    type="button"
                    class="btn btn-secondary btn-sm"
                    :disabled="initialLoading || manualRefreshing"
                    :title="t('common.refresh')"
                    @click="reload"
                  >
                    <Icon name="refresh" size="sm" class="mr-2" :class="{ 'animate-spin': manualRefreshing }" />
                    {{ t('common.refresh') }}
                  </button>
                </div>
              </div>
            </div>
          </section>
        </div>
      </template>

      <template #table>
        <DataTable
          :columns="columns"
          :data="items"
          :loading="initialLoading"
          row-key="id"
          :clickable-rows="true"
          :estimate-row-height="72"
          @row-click="selectTask"
        >
          <template #cell-task_id="{ row }">
            <button type="button" class="group block min-w-0 text-left" @click.stop="selectTask(row)">
              <span class="block whitespace-nowrap font-mono text-xs font-medium text-gray-900 group-hover:text-primary-600 dark:text-gray-100 dark:group-hover:text-primary-400">
                {{ shortID(row.task_id) }}
              </span>
              <span class="mt-1 block whitespace-nowrap text-xs text-gray-500 dark:text-gray-400">
                {{ row.platform || '-' }} · {{ operationLabel(row.operation) }}
              </span>
            </button>
          </template>

          <template #cell-model="{ row }">
            <span class="block max-w-[220px] truncate text-gray-700 dark:text-gray-200" :title="row.model || '-'">
              {{ row.model || '-' }}
            </span>
          </template>

          <template #cell-owner="{ row }">
            <span class="whitespace-nowrap font-mono text-xs text-gray-600 dark:text-gray-300">
              U{{ row.user_id }} · K{{ row.api_key_id }}
            </span>
          </template>

          <template #cell-created_at="{ value }">
            <span class="whitespace-nowrap text-xs text-gray-600 dark:text-gray-300">{{ formatTime(value) }}</span>
          </template>

          <template #cell-status="{ row }">
            <div class="space-y-1.5">
              <StatusBadge :status="statusBadgeStatus(row.status)" :label="statusLabel(row.status)" />
              <p class="whitespace-nowrap text-[11px] text-gray-500 dark:text-gray-400">
                {{ t('admin.imageGenerations.duration') }} {{ formatDuration(row.duration_ms) }}
                · {{ t('admin.imageGenerations.results') }} {{ resultCount(row) }}
              </p>
            </div>
          </template>

          <template #cell-stop_reason="{ row }">
            <p class="max-w-[420px] whitespace-normal break-words text-xs leading-5 text-gray-600 dark:text-gray-300">
              {{ row.stop_reason || '-' }}
            </p>
          </template>

          <template #empty>
            <div class="flex flex-col items-center py-8">
              <Icon name="inbox" size="xl" class="mb-3 h-10 w-10 text-gray-300 dark:text-dark-600" />
              <p class="text-sm font-medium text-gray-500 dark:text-gray-400">{{ t('admin.imageGenerations.noTasks') }}</p>
            </div>
          </template>
        </DataTable>
      </template>

      <template #pagination>
        <div class="card flex items-center justify-between bg-white px-4 py-3 dark:bg-dark-800 sm:px-5">
          <button
            type="button"
            data-test="previous-page"
            class="btn btn-secondary btn-sm"
            :disabled="cursorHistory.length === 0 || initialLoading || manualRefreshing"
            @click="previousPage"
          >
            <Icon name="chevronLeft" size="sm" class="mr-1" />
            {{ t('pagination.previous') }}
          </button>
          <span class="text-xs font-medium text-gray-500 dark:text-gray-400">
            {{ t('admin.imageGenerations.page', { page: cursorHistory.length + 1 }) }}
          </span>
          <button
            type="button"
            data-test="next-page"
            class="btn btn-secondary btn-sm"
            :disabled="!hasMore || initialLoading || manualRefreshing"
            @click="nextPage"
          >
            {{ t('pagination.next') }}
            <Icon name="chevronRight" size="sm" class="ml-1" />
          </button>
        </div>
      </template>
    </TablePageLayout>

    <BaseDialog
      :show="selectedTask !== null"
      :title="t('admin.imageGenerations.taskDetails')"
      width="wide"
      :close-on-click-outside="true"
      @close="selectedTask = null"
    >
      <div v-if="selectedTask" class="space-y-5">
        <div class="rounded-lg border border-gray-200 bg-gray-50 px-4 py-3 dark:border-dark-700 dark:bg-dark-800">
          <p class="break-all font-mono text-sm font-medium text-gray-900 dark:text-white">{{ selectedTask.task_id }}</p>
          <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
            {{ selectedTask.platform || '-' }} · {{ operationLabel(selectedTask.operation) }}
          </p>
        </div>

        <dl class="grid gap-x-6 gap-y-5 sm:grid-cols-2">
          <div>
            <dt class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('admin.imageGenerations.status') }}</dt>
            <dd class="mt-1.5"><StatusBadge :status="statusBadgeStatus(selectedTask.status)" :label="statusLabel(selectedTask.status)" /></dd>
          </div>
          <div>
            <dt class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('admin.imageGenerations.model') }}</dt>
            <dd class="mt-1.5 break-words text-sm text-gray-900 dark:text-white">{{ selectedTask.model || '-' }}</dd>
          </div>
          <div>
            <dt class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('admin.imageGenerations.owner') }}</dt>
            <dd class="mt-1.5 font-mono text-sm text-gray-900 dark:text-white">U{{ selectedTask.user_id }} · K{{ selectedTask.api_key_id }}</dd>
          </div>
          <div>
            <dt class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('admin.imageGenerations.submittedAt') }}</dt>
            <dd class="mt-1.5 text-sm text-gray-900 dark:text-white">{{ formatTime(selectedTask.created_at) }}</dd>
          </div>
          <div>
            <dt class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('admin.imageGenerations.operation') }}</dt>
            <dd class="mt-1.5 text-sm text-gray-900 dark:text-white">{{ operationLabel(selectedTask.operation) }}</dd>
          </div>
          <div>
            <dt class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('admin.imageGenerations.duration') }}</dt>
            <dd class="mt-1.5 text-sm text-gray-900 dark:text-white">{{ formatDuration(selectedTask.duration_ms) }}</dd>
          </div>
          <div class="sm:col-span-2">
            <dt class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('admin.imageGenerations.stopReason') }}</dt>
            <dd class="mt-1.5 whitespace-pre-wrap break-words rounded-lg bg-gray-50 px-3 py-2.5 text-sm leading-6 text-gray-900 dark:bg-dark-800 dark:text-white">
              {{ selectedTask.stop_reason || '-' }}
            </dd>
          </div>
          <div v-if="selectedTask.result_urls?.length" class="sm:col-span-2">
            <dt class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('admin.imageGenerations.results') }}</dt>
            <dd class="mt-2 grid gap-2 sm:grid-cols-2">
              <a
                v-for="(url, index) in selectedTask.result_urls"
                :key="url"
                :href="url"
                target="_blank"
                rel="noopener noreferrer"
                class="flex min-w-0 items-center gap-2 rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm font-medium text-primary-600 transition-colors hover:border-primary-300 hover:bg-primary-50 dark:border-dark-700 dark:bg-dark-800 dark:text-primary-400 dark:hover:border-primary-700 dark:hover:bg-primary-950/20"
              >
                <Icon name="externalLink" size="sm" class="shrink-0" />
                <span class="truncate">{{ t('admin.imageGenerations.resultNumber', { number: index + 1 }) }}</span>
              </a>
            </dd>
          </div>
        </dl>
      </div>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import imageGenerationsAPI, { type AsyncImageTaskAdmin, type AsyncImageTaskStatus, type AsyncImageTaskStats } from '@/api/admin/imageGenerations'
import BaseDialog from '@/components/common/BaseDialog.vue'
import DataTable from '@/components/common/DataTable.vue'
import Select, { type SelectOption } from '@/components/common/Select.vue'
import StatusBadge from '@/components/common/StatusBadge.vue'
import Toggle from '@/components/common/Toggle.vue'
import type { Column } from '@/components/common/types'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import Icon from '@/components/icons/Icon.vue'

const { t } = useI18n()
const items = ref<AsyncImageTaskAdmin[]>([])
const selectedTask = ref<AsyncImageTaskAdmin | null>(null)
const status = ref('all')
const startDate = ref('')
const endDate = ref('')
const loading = ref(false)
const initialLoading = ref(true)
const manualRefreshing = ref(false)
const hasLoaded = ref(false)
const errorMessage = ref('')
const autoRefresh = ref(true)
const lastUpdated = ref(0)
const cursor = ref('')
const nextCursor = ref('')
const hasMore = ref(false)
const cursorHistory = ref<string[]>([])
const stats = reactive<AsyncImageTaskStats>({ processing: 0, completed: 0, failed: 0 })
let refreshTimer: ReturnType<typeof setInterval> | null = null

const columns = computed<Column[]>(() => [
  { key: 'task_id', label: t('admin.imageGenerations.task'), class: 'min-w-[180px]' },
  { key: 'model', label: t('admin.imageGenerations.model'), class: 'min-w-[120px] max-w-[200px]' },
  { key: 'owner', label: t('admin.imageGenerations.owner'), class: 'min-w-[100px]' },
  { key: 'created_at', label: t('admin.imageGenerations.submittedAt'), class: 'min-w-[130px]' },
  { key: 'status', label: t('admin.imageGenerations.status'), class: 'min-w-[140px]' },
  { key: 'stop_reason', label: t('admin.imageGenerations.stopReason'), class: 'min-w-[240px] max-w-[420px] !whitespace-normal' },
])

const statusOptions = computed<SelectOption[]>(() => [
  { value: 'all', label: t('admin.imageGenerations.allStatuses') },
  { value: 'processing', label: t('admin.imageGenerations.processing') },
  { value: 'completed', label: t('admin.imageGenerations.completed') },
  { value: 'failed', label: t('admin.imageGenerations.failed') },
])

async function load(options: { background?: boolean } = {}): Promise<void> {
  if (loading.value) return
  loading.value = true
  initialLoading.value = !hasLoaded.value
  manualRefreshing.value = hasLoaded.value && !options.background
  errorMessage.value = ''
  try {
    const params: Parameters<typeof imageGenerationsAPI.listTasks>[0] = {
      status: status.value,
      cursor: cursor.value || undefined,
      limit: 50,
    }
    if (startDate.value) params.start_date = startDate.value
    if (endDate.value) params.end_date = endDate.value
    if (startDate.value || endDate.value) params.timezone = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
    const page = await imageGenerationsAPI.listTasks(params)
    items.value = page.items || []
    nextCursor.value = page.next_cursor || ''
    hasMore.value = Boolean(page.has_more && page.next_cursor)
    stats.processing = page.stats?.processing || 0
    stats.completed = page.stats?.completed || 0
    stats.failed = page.stats?.failed || 0
    lastUpdated.value = Date.now()
    hasLoaded.value = true
  } catch (error: any) {
    errorMessage.value = error?.message || t('admin.imageGenerations.loadFailed')
  } finally {
    loading.value = false
    initialLoading.value = false
    manualRefreshing.value = false
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
    refreshTimer = setInterval(() => { void load({ background: true }) }, 2000)
  }
}

function handleVisibilityChange(): void {
  setRefreshTimer()
  if (document.visibilityState === 'visible') void load({ background: hasLoaded.value })
}

function statusLabel(value: AsyncImageTaskStatus): string {
  return t(`admin.imageGenerations.${value}`)
}

function statusBadgeStatus(value: AsyncImageTaskStatus): string {
  if (value === 'completed') return 'success'
  if (value === 'failed') return 'danger'
  return 'warning'
}

function operationLabel(value?: string): string {
  return value === 'edit' ? t('admin.imageGenerations.edit') : t('admin.imageGenerations.generation')
}

function resultCount(task: AsyncImageTaskAdmin): number {
  return task.result_count ?? 0
}

function shortID(value: string): string {
  return value.length > 20 ? `${value.slice(0, 10)}...${value.slice(-7)}` : value
}

function formatTime(value: number): string {
  const date = new Date(value > 10_000_000_000 ? value : value * 1000)
  return Number.isNaN(date.getTime()) ? '-' : date.toLocaleString(undefined, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

function formatDuration(value: number): string {
  if (value < 1000) return `${value} ms`
  return `${(value / 1000).toFixed(value >= 10_000 ? 0 : 1)} s`
}

function selectTask(task: AsyncImageTaskAdmin): void {
  selectedTask.value = task
}

function setStatusFilter(value: string | number | boolean | null): void {
  if (typeof value !== 'string' || !['all', 'processing', 'completed', 'failed'].includes(value) || value === status.value) return
  status.value = value
  items.value = []
  hasLoaded.value = false
  reload()
}

function applyDateFilter(): void {
  cursor.value = ''
  nextCursor.value = ''
  cursorHistory.value = []
  hasLoaded.value = false
  void load()
}

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

<style scoped>
.image-queue-layout :deep(.table-scroll-container th),
.image-queue-layout :deep(.table-scroll-container td) {
  padding-left: 0.75rem;
  padding-right: 0.75rem;
}

.image-queue-layout :deep([data-field] > span:first-child) {
  flex-shrink: 0;
  white-space: nowrap;
}
</style>
