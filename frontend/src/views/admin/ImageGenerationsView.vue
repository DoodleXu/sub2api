<template>
  <AppLayout>
    <div class="space-y-5">
      <section class="border-b border-gray-200 pb-5 dark:border-dark-700">
        <div class="flex flex-wrap items-end justify-between gap-4">
          <div class="min-w-0">
            <p class="text-xs font-medium text-gray-500 dark:text-gray-400">异步生图对象存储</p>
            <div class="mt-1 flex flex-wrap items-center gap-x-4 gap-y-1">
              <span class="font-mono text-sm text-gray-800 dark:text-gray-100">{{ bucket || '未配置' }}</span>
              <span class="font-mono text-xs text-gray-500 dark:text-gray-400">{{ configuredPrefix || 'images/' }}</span>
            </div>
          </div>
          <button type="button" class="btn btn-secondary btn-sm" :disabled="loading" title="刷新对象列表" @click="reload">
            <Icon name="refresh" size="sm" class="mr-2" />
            刷新
          </button>
        </div>
      </section>

      <section class="grid gap-3 sm:grid-cols-3">
        <div class="border-l-2 border-primary-500 pl-3">
          <div class="text-xs text-gray-500 dark:text-gray-400">当前页对象</div>
          <div class="mt-1 text-xl font-semibold text-gray-900 dark:text-white">{{ items.length }}</div>
        </div>
        <div class="border-l-2 border-emerald-500 pl-3">
          <div class="text-xs text-gray-500 dark:text-gray-400">当前页容量</div>
          <div class="mt-1 text-xl font-semibold text-gray-900 dark:text-white">{{ formatBytes(pageBytes) }}</div>
        </div>
        <div class="border-l-2 border-gray-300 pl-3 dark:border-dark-600">
          <div class="text-xs text-gray-500 dark:text-gray-400">数据来源</div>
          <div class="mt-1 text-sm font-medium text-gray-900 dark:text-white">上游异步生图桶</div>
        </div>
      </section>

      <section class="flex flex-wrap items-center gap-2 border-y border-gray-200 py-3 dark:border-dark-700">
        <input
          v-model="prefixInput"
          class="input min-w-[240px] flex-1"
          :placeholder="configuredPrefix || 'images/'"
          @keyup.enter="applyPrefix"
        />
        <button type="button" class="btn btn-primary btn-sm" @click="applyPrefix">查看前缀</button>
        <button type="button" class="btn btn-secondary btn-sm" @click="resetPrefix">重置</button>
      </section>

      <div v-if="errorMessage" class="rounded-md border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-900/60 dark:bg-red-950/30 dark:text-red-300">
        {{ errorMessage }}
      </div>

      <section v-if="loading" class="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-5">
        <div v-for="index in 10" :key="index" class="aspect-square animate-pulse bg-gray-100 dark:bg-dark-800" />
      </section>

      <section v-else-if="items.length" class="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-5">
        <article v-for="item in items" :key="item.key" class="group min-w-0 border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-900">
          <button type="button" class="block aspect-square w-full overflow-hidden bg-gray-100 dark:bg-dark-800" @click="preview = item">
            <img :src="item.url" :alt="item.key" class="h-full w-full object-cover transition-transform duration-200 group-hover:scale-[1.02]" loading="lazy" />
          </button>
          <div class="p-3">
            <p class="truncate font-mono text-xs text-gray-700 dark:text-gray-200" :title="item.key">{{ objectName(item.key) }}</p>
            <div class="mt-2 flex items-center justify-between gap-2 text-[11px] text-gray-500 dark:text-gray-400">
              <span>{{ formatBytes(item.size) }}</span>
              <span>{{ formatTime(item.last_modified) }}</span>
            </div>
            <div class="mt-3 flex gap-2">
              <button type="button" class="btn btn-secondary btn-sm flex-1" @click="preview = item">预览</button>
              <a class="btn btn-secondary btn-sm flex-1" :href="item.url" :download="objectName(item.key)">下载</a>
            </div>
          </div>
        </article>
      </section>

      <section v-else class="py-20 text-center text-sm text-gray-500 dark:text-gray-400">
        当前前缀下没有异步生图结果
      </section>

      <div class="flex items-center justify-between border-t border-gray-200 pt-4 dark:border-dark-700">
        <button type="button" class="btn btn-secondary btn-sm" :disabled="cursorHistory.length === 0 || loading" @click="previousPage">上一页</button>
        <span class="text-xs text-gray-500 dark:text-gray-400">第 {{ cursorHistory.length + 1 }} 页</span>
        <button type="button" class="btn btn-secondary btn-sm" :disabled="!hasMore || loading" @click="nextPage">下一页</button>
      </div>
    </div>

    <div v-if="preview" class="fixed inset-0 z-50 flex items-center justify-center bg-black/80 p-4" @click.self="preview = null">
      <div class="flex max-h-[92vh] max-w-6xl flex-col bg-white dark:bg-dark-900">
        <div class="flex items-center justify-between gap-4 border-b border-gray-200 px-4 py-3 dark:border-dark-700">
          <p class="min-w-0 truncate font-mono text-xs text-gray-700 dark:text-gray-200">{{ preview.key }}</p>
          <button type="button" class="icon-btn" title="关闭" @click="preview = null"><Icon name="x" /></button>
        </div>
        <img :src="preview.url" :alt="preview.key" class="min-h-0 max-h-[80vh] w-auto max-w-full object-contain" />
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'

import imageGenerationsAPI, { type AsyncImageObject } from '@/api/admin/imageGenerations'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'

const items = ref<AsyncImageObject[]>([])
const loading = ref(false)
const errorMessage = ref('')
const bucket = ref('')
const configuredPrefix = ref('')
const prefixInput = ref('')
const activePrefix = ref('')
const cursor = ref('')
const nextCursor = ref('')
const hasMore = ref(false)
const cursorHistory = ref<string[]>([])
const preview = ref<AsyncImageObject | null>(null)
const pageBytes = computed(() => items.value.reduce((sum, item) => sum + item.size, 0))

async function load(): Promise<void> {
  loading.value = true
  errorMessage.value = ''
  try {
    const page = await imageGenerationsAPI.list({ prefix: activePrefix.value || undefined, cursor: cursor.value || undefined, limit: 60 })
    items.value = page.items || []
    bucket.value = page.bucket || ''
    configuredPrefix.value = page.prefix || 'images/'
    if (!activePrefix.value) prefixInput.value = configuredPrefix.value
    nextCursor.value = page.next_cursor || ''
    hasMore.value = Boolean(page.has_more && page.next_cursor)
  } catch (error: any) {
    items.value = []
    errorMessage.value = error?.message || '读取异步生图对象失败，请检查 ListBucket 权限。'
  } finally {
    loading.value = false
  }
}

function reload(): void { void load() }
function applyPrefix(): void {
  activePrefix.value = prefixInput.value.trim()
  cursor.value = ''
  cursorHistory.value = []
  void load()
}
function resetPrefix(): void {
  activePrefix.value = ''
  prefixInput.value = configuredPrefix.value
  cursor.value = ''
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
  const previous = cursorHistory.value.pop()
  if (previous === undefined) return
  cursor.value = previous
  void load()
}
function objectName(key: string): string { return key.split('/').filter(Boolean).pop() || key }
function formatBytes(value: number): string {
  if (!value) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const index = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1)
  return `${(value / 1024 ** index).toFixed(index === 0 ? 0 : 1)} ${units[index]}`
}
function formatTime(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '-' : date.toLocaleString(undefined, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
}

onMounted(load)
</script>
