<template>
  <AppLayout>
    <div class="space-y-6">
      <section class="rounded-lg border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-900">
        <div class="grid gap-3 md:grid-cols-5">
          <input v-model="filters.model" class="input" placeholder="模型" @keyup.enter="loadRecords" />
          <select v-model="filters.status" class="input">
            <option value="">全部状态</option>
            <option value="completed">成功</option>
            <option value="failed">失败</option>
            <option value="pending">等待</option>
            <option value="running">归档中</option>
            <option value="skipped">跳过</option>
          </select>
          <input v-model="filters.user_id" class="input" placeholder="用户 ID" @keyup.enter="loadRecords" />
          <input v-model="filters.api_key_id" class="input" placeholder="API Key ID" @keyup.enter="loadRecords" />
          <button class="btn btn-primary" @click="loadRecords">筛选</button>
        </div>
      </section>

      <section class="grid gap-4 lg:grid-cols-[minmax(0,1fr)_360px]">
        <div class="rounded-lg border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-900">
          <div class="mb-3 flex items-center justify-between">
            <h2 class="text-base font-semibold">存储设置</h2>
            <button class="btn btn-secondary btn-sm" @click="saveStorage">保存</button>
          </div>
          <div class="mb-3 flex items-center justify-between rounded-md bg-gray-50 px-3 py-2 dark:bg-dark-800">
            <div>
              <div class="text-sm font-medium text-gray-800 dark:text-gray-100">启用归档</div>
              <div class="text-xs text-gray-500 dark:text-gray-400">关闭后不再保存新的生图资产</div>
            </div>
            <Toggle v-model="storage.enabled" data-testid="image-archive-enabled" />
          </div>
          <div class="grid gap-3 md:grid-cols-4">
            <select v-model="storage.type" class="input">
              <option value="local">本地存储</option>
              <option value="s3">对象存储</option>
            </select>
            <input v-model="storage.local_dir" class="input md:col-span-3" placeholder="本地目录" />
            <template v-if="storage.type === 's3'">
              <input v-model="storage.s3_endpoint" class="input" placeholder="Endpoint" />
              <input v-model="storage.s3_region" class="input" placeholder="Region" />
              <input v-model="storage.s3_bucket" class="input" placeholder="Bucket" />
              <input v-model="storage.s3_prefix" class="input" placeholder="Path prefix" />
              <input v-model="storage.s3_access_key" class="input" placeholder="Access key" />
              <input v-model="storage.s3_secret_key" class="input" placeholder="Secret key" type="password" />
              <input v-model="storage.public_base_url" class="input md:col-span-2" placeholder="Public base URL" />
            </template>
          </div>
        </div>
        <div class="rounded-lg border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-900">
          <h2 class="text-base font-semibold">今日数据</h2>
          <div class="mt-3 grid grid-cols-2 gap-3 sm:grid-cols-4">
            <div class="rounded-md bg-gray-50 p-3 dark:bg-dark-800">
              <div class="text-xs text-gray-500">请求</div>
              <div class="mt-1 text-xl font-semibold">{{ today?.request_count || 0 }}</div>
            </div>
            <div class="rounded-md bg-gray-50 p-3 dark:bg-dark-800">
              <div class="text-xs text-gray-500">图片</div>
              <div class="mt-1 text-xl font-semibold">{{ today?.image_count || 0 }}</div>
            </div>
            <div class="rounded-md bg-gray-50 p-3 dark:bg-dark-800">
              <div class="text-xs text-gray-500">失败</div>
              <div class="mt-1 text-xl font-semibold">{{ today?.failed_count || 0 }}</div>
            </div>
            <div class="rounded-md bg-gray-50 p-3 dark:bg-dark-800">
              <div class="text-xs text-gray-500">归档</div>
              <div class="mt-1 text-xl font-semibold">{{ formattedArchiveSize }}</div>
            </div>
          </div>
        </div>
      </section>

      <section>
        <div v-if="loading" class="py-12 text-center text-gray-500">加载中...</div>
        <div v-else-if="items.length === 0" class="py-12 text-center text-gray-500">暂无生图归档</div>
        <div v-else class="image-masonry">
          <article
            v-for="item in imageCards"
            :key="`${item.record.id}-${item.asset.id}`"
            class="mb-4 break-inside-avoid overflow-hidden rounded-lg border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-900"
          >
            <button class="block w-full" @click="handleAssetCardClick(item)">
              <div class="flex aspect-square w-full items-center justify-center bg-gray-100 dark:bg-dark-800">
                <img
                  v-if="assetImageSrc(item.asset)"
                  :src="assetImageSrc(item.asset)"
                  class="h-full w-full object-cover"
                  loading="lazy"
                />
                <span v-else-if="assetImageFailed(item.asset)" class="px-3 text-xs text-red-500">加载失败，点击重试</span>
                <span v-else class="text-xs text-gray-400">加载中...</span>
              </div>
            </button>
            <div class="space-y-1 p-3 text-xs text-gray-500 dark:text-gray-400">
              <div class="flex items-center justify-between gap-2">
                <span class="truncate font-medium text-gray-800 dark:text-gray-100">{{ item.record.model || 'unknown' }}</span>
                <span>{{ item.record.status }}</span>
              </div>
              <div>用户 {{ item.record.user_id || '-' }} · Key {{ item.record.api_key_id || '-' }}</div>
              <div>{{ formatTime(item.record.created_at) }}</div>
              <div class="line-clamp-2">{{ item.record.prompt_excerpt }}</div>
            </div>
          </article>
        </div>
      </section>

      <div v-if="preview" class="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4" @click.self="preview = null">
        <div class="max-h-[92vh] max-w-5xl overflow-hidden rounded-lg bg-white shadow-xl dark:bg-dark-900">
          <img v-if="assetImageSrc(preview.asset)" :src="assetImageSrc(preview.asset)" class="max-h-[72vh] w-full object-contain" />
          <div v-else-if="assetImageFailed(preview.asset)" class="flex h-72 w-[min(90vw,48rem)] items-center justify-center text-sm text-red-500">图片加载失败</div>
          <div v-else class="flex h-72 w-[min(90vw,48rem)] items-center justify-center text-sm text-gray-500">图片加载中...</div>
          <div class="flex items-center justify-between gap-3 p-4 text-sm">
            <div>
              <div class="font-medium">{{ preview.record.model }}</div>
              <div class="text-gray-500">{{ preview.record.prompt_excerpt }}</div>
            </div>
            <div class="flex gap-2">
              <button v-if="assetImageFailed(preview.asset)" class="btn btn-secondary btn-sm" @click="retryAssetLoad(preview.asset)">重试</button>
              <button class="btn btn-secondary btn-sm" @click="openAsset(preview.asset)">打开</button>
              <button class="btn btn-primary btn-sm" @click="downloadAsset(preview.asset)">下载</button>
              <button class="btn btn-secondary btn-sm" @click="preview = null">关闭</button>
            </div>
          </div>
        </div>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import AppLayout from '@/components/layout/AppLayout.vue'
import Toggle from '@/components/common/Toggle.vue'
import imageGenerationsAPI, {
  type ImageArchiveStorageConfig,
  type ImageGenerationAsset,
  type ImageGenerationDailyStat,
  type ImageGenerationListItem,
  type ImageGenerationRecord,
  type ImageGenerationStorageStats,
} from '@/api/admin/imageGenerations'

const loading = ref(false)
const items = ref<ImageGenerationListItem[]>([])
const stats = ref<ImageGenerationDailyStat[]>([])
const storageStats = ref<ImageGenerationStorageStats>({ total_bytes: 0 })
const preview = ref<{ record: ImageGenerationRecord; asset: ImageGenerationAsset } | null>(null)
const cachedAssetURLs = ref<Record<number, string>>({})
const failedAssetLoads = ref<Record<number, boolean>>({})
const cacheRequests = new Map<number, Promise<void>>()
const filters = reactive({ model: '', status: '', user_id: '', api_key_id: '' })
const storage = reactive<ImageArchiveStorageConfig>({
  enabled: true,
  type: 'local',
  local_dir: '',
})

const imageCards = computed(() =>
  items.value.flatMap((item) => item.assets.map((asset) => ({ record: item.record, asset })))
)
const todayDateKey = computed(() => formatLocalDateKey(new Date()))
const today = computed(() => stats.value.find((item) => item.date === todayDateKey.value))
const formattedArchiveSize = computed(() => formatArchiveSize(storageStats.value.total_bytes))

function formatTime(value: string): string {
  return new Date(value).toLocaleString()
}

function assetFilename(asset: ImageGenerationAsset): string {
  const ext = (asset.extension || asset.mime_type.split('/')[1] || 'png').replace(/^\./, '')
  return `image-generation-${asset.id}.${ext}`
}

function formatLocalDateKey(value: Date): string {
  const year = value.getFullYear()
  const month = String(value.getMonth() + 1).padStart(2, '0')
  const day = String(value.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}

function formatArchiveSize(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 MB'
  const gb = bytes / (1024 ** 3)
  if (gb >= 1) return `${formatSizeNumber(gb)} GB`
  const mb = bytes / (1024 ** 2)
  return `${formatSizeNumber(mb)} MB`
}

function formatSizeNumber(value: number): string {
  if (value >= 100) return value.toFixed(0)
  if (value >= 10) return value.toFixed(1)
  return value.toFixed(2)
}

function assetImageSrc(asset: ImageGenerationAsset): string {
  return cachedAssetURLs.value[asset.id] || ''
}

function assetImageFailed(asset: ImageGenerationAsset): boolean {
  return Boolean(failedAssetLoads.value[asset.id])
}

function adminAssetURL(asset: ImageGenerationAsset): string {
  const baseURL = asset.admin_url
  if (!baseURL) return ''
  const version = asset.sha256 || `${asset.bytes}-${asset.created_at}`
  const separator = baseURL.includes('?') ? '&' : '?'
  return `${baseURL}${separator}v=${encodeURIComponent(version)}`
}

function authHeaders(): Record<string, string> {
  const token = localStorage.getItem('auth_token')
  return token ? { Authorization: `Bearer ${token}` } : {}
}

async function fetchAdminAssetBlob(asset: ImageGenerationAsset): Promise<Blob> {
  const url = adminAssetURL(asset)
  if (!url) throw new Error('admin image asset URL is not available')
  const response = await fetch(url, { credentials: 'include', headers: authHeaders() })
  if (!response.ok) throw new Error(`failed to load image asset ${asset.id}`)
  return response.blob()
}

async function cacheImageAsset(asset: ImageGenerationAsset): Promise<void> {
  if (cachedAssetURLs.value[asset.id] || cacheRequests.has(asset.id)) return
  const request = (async () => {
    try {
      failedAssetLoads.value = {
        ...failedAssetLoads.value,
        [asset.id]: false,
      }
      const blob = await fetchAdminAssetBlob(asset)
      cachedAssetURLs.value = {
        ...cachedAssetURLs.value,
        [asset.id]: URL.createObjectURL(blob),
      }
    } catch {
      failedAssetLoads.value = {
        ...failedAssetLoads.value,
        [asset.id]: true,
      }
    } finally {
      cacheRequests.delete(asset.id)
    }
  })()
  cacheRequests.set(asset.id, request)
}

function warmImageCache(cards: Array<{ asset: ImageGenerationAsset }>): void {
  for (const item of cards) {
    void cacheImageAsset(item.asset)
  }
}

function retryAssetLoad(asset: ImageGenerationAsset): void {
  failedAssetLoads.value = {
    ...failedAssetLoads.value,
    [asset.id]: false,
  }
  void cacheImageAsset(asset)
}

function handleAssetCardClick(item: { record: ImageGenerationRecord; asset: ImageGenerationAsset }): void {
  if (assetImageFailed(item.asset) && !assetImageSrc(item.asset)) {
    retryAssetLoad(item.asset)
    return
  }
  preview.value = item
}

function triggerDownload(url: string, filename: string): void {
  const link = document.createElement('a')
  link.href = url
  link.download = filename
  link.rel = 'noopener noreferrer'
  document.body.appendChild(link)
  link.click()
  link.remove()
}

async function openAsset(asset: ImageGenerationAsset): Promise<void> {
  const blob = await fetchAdminAssetBlob(asset)
  const objectURL = URL.createObjectURL(blob)
  window.open(objectURL, '_blank', 'noopener')
  window.setTimeout(() => URL.revokeObjectURL(objectURL), 60_000)
}

async function downloadAsset(asset: ImageGenerationAsset): Promise<void> {
  const blob = await fetchAdminAssetBlob(asset)
  const objectURL = URL.createObjectURL(blob)
  triggerDownload(objectURL, assetFilename(asset))
  window.setTimeout(() => URL.revokeObjectURL(objectURL), 60_000)
}

function params() {
  return {
    page: 1,
    page_size: 60,
    model: filters.model || undefined,
    status: filters.status || undefined,
    user_id: filters.user_id || undefined,
    api_key_id: filters.api_key_id || undefined,
  }
}

async function loadRecords() {
  loading.value = true
  try {
    const data = await imageGenerationsAPI.list(params())
    items.value = data.items || []
  } finally {
    loading.value = false
  }
}

async function loadStats() {
  const end = new Date()
  const start = new Date()
  start.setDate(end.getDate() - 29)
  stats.value = await imageGenerationsAPI.dailyStats({
    start_date: start.toISOString().slice(0, 10),
    end_date: end.toISOString().slice(0, 10),
  })
}

async function loadStorageStats() {
  storageStats.value = await imageGenerationsAPI.storageStats()
}

async function loadStorage() {
  Object.assign(storage, await imageGenerationsAPI.getStorageConfig())
}

async function saveStorage() {
  Object.assign(storage, await imageGenerationsAPI.updateStorageConfig({ ...storage }))
}

onMounted(async () => {
  await Promise.all([loadRecords(), loadStats(), loadStorageStats(), loadStorage()])
})

watch(imageCards, (cards) => warmImageCache(cards), { immediate: true })

onBeforeUnmount(() => {
  for (const url of Object.values(cachedAssetURLs.value)) {
    URL.revokeObjectURL(url)
  }
})

</script>

<style scoped>
.image-masonry {
  column-count: 1;
  column-gap: 1rem;
}
@media (min-width: 768px) {
  .image-masonry {
    column-count: 3;
  }
}
@media (min-width: 1280px) {
  .image-masonry {
    column-count: 5;
  }
}
</style>
