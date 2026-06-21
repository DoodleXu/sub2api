<template>
  <AppLayout>
    <div class="space-y-6">
      <section class="rounded-lg border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-900">
        <div class="grid gap-3 md:grid-cols-6">
          <input v-model="filters.model" class="input" placeholder="模型" @keyup.enter="applyFilters" />
          <select v-model="filters.status" class="input" @change="applyFilters">
            <option value="">全部状态</option>
            <option value="completed">成功</option>
            <option value="failed">失败</option>
            <option value="pending">等待</option>
            <option value="running">归档中</option>
            <option value="skipped">跳过</option>
          </select>
          <input v-model="filters.user_id" class="input" placeholder="用户 ID" @keyup.enter="applyFilters" />
          <input v-model="filters.api_key_id" class="input" placeholder="API Key ID" @keyup.enter="applyFilters" />
          <button class="btn btn-primary" type="button" @click="applyFilters">筛选</button>
          <button class="btn btn-danger" type="button" :disabled="clearingArchives" @click="clearAllArchives">
            {{ clearingArchives ? '清空中...' : '清空所有归档' }}
          </button>
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
          <h2 class="text-base font-semibold">统计概览</h2>
          <div class="mt-3 grid grid-cols-2 gap-3 sm:grid-cols-4">
            <div class="rounded-md bg-gray-50 p-3 dark:bg-dark-800">
              <div class="text-xs text-gray-500">今日请求</div>
              <div class="mt-1 text-xl font-semibold">{{ today?.request_count || 0 }}</div>
            </div>
            <div class="rounded-md bg-gray-50 p-3 dark:bg-dark-800">
              <div class="text-xs text-gray-500">今日图片</div>
              <div class="mt-1 text-xl font-semibold">{{ today?.image_count || 0 }}</div>
            </div>
            <div class="rounded-md bg-gray-50 p-3 dark:bg-dark-800">
              <div class="text-xs text-gray-500">今日失败</div>
              <div class="mt-1 text-xl font-semibold">{{ today?.failed_count || 0 }}</div>
            </div>
            <div class="rounded-md bg-gray-50 p-3 dark:bg-dark-800">
              <div class="text-xs text-gray-500">总归档</div>
              <div class="mt-1 text-xl font-semibold">{{ formattedArchiveSize }}</div>
            </div>
          </div>
        </div>
      </section>

      <section>
        <div v-if="loading" class="py-12 text-center text-gray-500">加载中...</div>
        <div v-else-if="items.length === 0 || imageCards.length === 0" class="py-12 text-center text-gray-500">暂无生图资产</div>
        <div v-else class="image-masonry">
          <article
            v-for="item in visibleImageCards"
            :key="`${item.record.id}-${item.asset.id}`"
            class="mb-4 break-inside-avoid overflow-hidden rounded-lg border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-900"
          >
            <button class="block w-full" @click="handleAssetCardClick(item)">
              <div
                :ref="(element) => observeThumbnailElement(element, item.asset)"
                :data-testid="`image-thumbnail-${item.asset.id}`"
                class="flex aspect-square w-full items-center justify-center bg-gray-100 dark:bg-dark-800"
              >
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
        <div v-if="!loading && items.length > 0" class="mt-4 flex flex-col items-center justify-center gap-2 text-sm text-gray-500 dark:text-gray-400">
          <div>已加载 {{ items.length }} / {{ pagination.total }} 条请求，显示 {{ imageCards.length }} 张归档资产</div>
          <button
            v-if="hasMoreRecords"
            class="btn btn-secondary btn-sm"
            :disabled="loadingMore"
            @click="loadMoreRecords"
          >
            {{ loadingMore ? '加载中...' : '加载更多' }}
          </button>
        </div>
      </section>

      <div v-if="preview" class="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4" @click.self="closePreview">
        <div class="max-h-[92vh] max-w-5xl overflow-hidden rounded-lg bg-white shadow-xl dark:bg-dark-900">
          <img v-if="previewImageURL" :src="previewImageURL" class="max-h-[72vh] w-full object-contain" />
          <div v-else-if="previewImageFailed" class="flex h-72 w-[min(90vw,48rem)] items-center justify-center text-sm text-red-500">图片加载失败</div>
          <div v-else class="flex h-72 w-[min(90vw,48rem)] items-center justify-center text-sm text-gray-500">图片加载中...</div>
          <div class="flex items-center justify-between gap-3 p-4 text-sm">
            <div>
              <div class="font-medium">{{ preview.record.model }}</div>
              <div class="text-gray-500">{{ preview.record.prompt_excerpt }}</div>
            </div>
            <div class="flex gap-2">
              <button v-if="previewImageFailed" class="btn btn-secondary btn-sm" @click="retryPreviewLoad">重试</button>
              <button class="btn btn-secondary btn-sm" @click="openAsset(preview.asset)">打开</button>
              <button class="btn btn-primary btn-sm" @click="downloadAsset(preview.asset)">下载</button>
              <button class="btn btn-secondary btn-sm" @click="closePreview">关闭</button>
            </div>
          </div>
        </div>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, type ComponentPublicInstance } from 'vue'
import AppLayout from '@/components/layout/AppLayout.vue'
import Toggle from '@/components/common/Toggle.vue'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import imageGenerationsAPI, {
  type ImageArchiveStorageConfig,
  type ImageGenerationAsset,
  type ImageGenerationDailyStat,
  type ImageGenerationListItem,
  type ImageGenerationRecord,
  type ImageGenerationStorageStats,
} from '@/api/admin/imageGenerations'

const appStore = useAppStore()
const loading = ref(false)
const loadingMore = ref(false)
const clearingArchives = ref(false)
const items = ref<ImageGenerationListItem[]>([])
const stats = ref<ImageGenerationDailyStat[]>([])
const storageStats = ref<ImageGenerationStorageStats>({ total_bytes: 0 })
const preview = ref<{ record: ImageGenerationRecord; asset: ImageGenerationAsset } | null>(null)
const previewImageURL = ref('')
const previewImageFailed = ref(false)
const thumbnailObjectURLs = ref<Record<number, string>>({})
const failedThumbnailLoads = ref<Record<number, boolean>>({})
const renderedImageCardLimit = ref(24)
const thumbnailRequests = new Map<number, Promise<void>>()
const thumbnailAbortControllers = new Map<number, AbortController>()
const pendingThumbnailJobs = new Map<number, ThumbnailLoadJob>()
const thumbnailElementsByAssetID = new Map<number, Element>()
const thumbnailAssetsByElement = new WeakMap<Element, ImageGenerationAsset>()
let thumbnailObserver: IntersectionObserver | null = null
let activeThumbnailLoads = 0
let previewLoadToken = 0
let previewAbortController: AbortController | null = null
let thumbnailLoadGeneration = 0
let renderBatchTimer: number | null = null
const filters = reactive({ model: '', status: '', user_id: '', api_key_id: '' })
const pagination = reactive({ page: 1, page_size: 60, total: 0, pages: 0 })
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
const hasMoreRecords = computed(() => pagination.page < pagination.pages)
const ADMIN_IMAGE_CACHE_NAME = 'sub2api-admin-image-assets-v1'
const MAX_CONCURRENT_THUMBNAIL_LOADS = 4
const INITIAL_RENDERED_IMAGE_CARD_LIMIT = 24
const IMAGE_CARD_RENDER_BATCH_SIZE = 24
const IMAGE_CARD_RENDER_BATCH_DELAY_MS = 16
const visibleImageCards = computed(() => imageCards.value.slice(0, renderedImageCardLimit.value))

interface ThumbnailLoadJob {
  asset: ImageGenerationAsset
  controller: AbortController
  generation: number
  resolve: () => void
}

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
  return thumbnailObjectURLs.value[asset.id] || ''
}

function assetImageFailed(asset: ImageGenerationAsset): boolean {
  return Boolean(failedThumbnailLoads.value[asset.id])
}

function clearRenderBatchTimer(): void {
  if (renderBatchTimer === null) return
  window.clearTimeout(renderBatchTimer)
  renderBatchTimer = null
}

function scheduleImageCardRenderBatch(): void {
  clearRenderBatchTimer()
  if (renderedImageCardLimit.value >= imageCards.value.length) return
  renderBatchTimer = window.setTimeout(() => {
    renderBatchTimer = null
    renderedImageCardLimit.value = Math.min(
      imageCards.value.length,
      renderedImageCardLimit.value + IMAGE_CARD_RENDER_BATCH_SIZE
    )
    scheduleImageCardRenderBatch()
  }, IMAGE_CARD_RENDER_BATCH_DELAY_MS)
}

function resetImageCardRenderLimit(): void {
  clearRenderBatchTimer()
  renderedImageCardLimit.value = INITIAL_RENDERED_IMAGE_CARD_LIMIT
  scheduleImageCardRenderBatch()
}

function versionedAssetURL(baseURL: string, asset: ImageGenerationAsset): string {
  if (!baseURL) return ''
  const version = asset.sha256 || `${asset.bytes}-${asset.created_at}`
  const separator = baseURL.includes('?') ? '&' : '?'
  return `${baseURL}${separator}v=${encodeURIComponent(version)}`
}

function adminAssetURL(asset: ImageGenerationAsset): string {
  return versionedAssetURL(asset.admin_url, asset)
}

function thumbnailAssetURL(asset: ImageGenerationAsset): string {
  return versionedAssetURL(asset.thumbnail_admin_url || asset.admin_url, asset)
}

function authHeaders(): Record<string, string> {
  const token = localStorage.getItem('auth_token')
  return token ? { Authorization: `Bearer ${token}` } : {}
}

function isAbortError(error: unknown): boolean {
  return error instanceof DOMException && error.name === 'AbortError'
}

function throwIfAborted(signal?: AbortSignal): void {
  if (!signal?.aborted) return
  throw new DOMException('The operation was aborted.', 'AbortError')
}

async function openAdminImageCache(): Promise<Cache | null> {
  if (typeof window === 'undefined' || !('caches' in window)) return null
  try {
    return await window.caches.open(ADMIN_IMAGE_CACHE_NAME)
  } catch {
    return null
  }
}

async function fetchAdminAssetBlob(asset: ImageGenerationAsset, options: { signal?: AbortSignal; url?: string } = {}): Promise<Blob> {
  const url = options.url || adminAssetURL(asset)
  if (!url) throw new Error('admin image asset URL is not available')
  throwIfAborted(options.signal)

  const cache = await openAdminImageCache()
  throwIfAborted(options.signal)

  const cached = await cache?.match(url)
  throwIfAborted(options.signal)
  if (cached?.ok) {
    return cached.blob()
  }

  const response = await fetch(url, { credentials: 'include', headers: authHeaders(), signal: options.signal })
  if (!response.ok) throw new Error(`failed to load image asset ${asset.id}`)
  if (cache) {
    await cache.put(url, response.clone()).catch(() => undefined)
  }
  throwIfAborted(options.signal)
  return response.blob()
}

async function cacheThumbnailAsset(asset: ImageGenerationAsset): Promise<void> {
  if (thumbnailObjectURLs.value[asset.id] || thumbnailRequests.has(asset.id) || pendingThumbnailJobs.has(asset.id)) return
  const controller = new AbortController()
  const generation = thumbnailLoadGeneration
  const request = new Promise<void>((resolve) => {
    pendingThumbnailJobs.set(asset.id, { asset, controller, generation, resolve })
    thumbnailAbortControllers.set(asset.id, controller)
    drainThumbnailQueue()
  })
  thumbnailRequests.set(asset.id, request)
  return request
}

function drainThumbnailQueue(): void {
  while (activeThumbnailLoads < MAX_CONCURRENT_THUMBNAIL_LOADS && pendingThumbnailJobs.size > 0) {
    const [assetID, job] = pendingThumbnailJobs.entries().next().value as [number, ThumbnailLoadJob]
    pendingThumbnailJobs.delete(assetID)
    activeThumbnailLoads += 1
    void runThumbnailJob(job).finally(() => {
      activeThumbnailLoads = Math.max(0, activeThumbnailLoads - 1)
      thumbnailRequests.delete(assetID)
      thumbnailAbortControllers.delete(assetID)
      job.resolve()
      drainThumbnailQueue()
    })
  }
}

async function runThumbnailJob(job: ThumbnailLoadJob): Promise<void> {
  try {
    failedThumbnailLoads.value = {
      ...failedThumbnailLoads.value,
      [job.asset.id]: false,
    }
    const blob = await fetchAdminAssetBlob(job.asset, {
      signal: job.controller.signal,
      url: thumbnailAssetURL(job.asset),
    })
    if (job.controller.signal.aborted || job.generation !== thumbnailLoadGeneration) return
    thumbnailObjectURLs.value = {
      ...thumbnailObjectURLs.value,
      [job.asset.id]: URL.createObjectURL(blob),
    }
  } catch (error) {
    if (isAbortError(error) || job.controller.signal.aborted || job.generation !== thumbnailLoadGeneration) return
    failedThumbnailLoads.value = {
      ...failedThumbnailLoads.value,
      [job.asset.id]: true,
    }
  }
}

function ensureThumbnailObserver(): IntersectionObserver | null {
  if (typeof IntersectionObserver === 'undefined') return null
  if (!thumbnailObserver) {
    thumbnailObserver = new IntersectionObserver((entries, observer) => {
      for (const entry of entries) {
        if (!entry.isIntersecting) continue
        observer.unobserve(entry.target)
        const asset = thumbnailAssetsByElement.get(entry.target)
        if (asset) void cacheThumbnailAsset(asset)
      }
    }, { rootMargin: '240px 0px' })
  }
  return thumbnailObserver
}

function observeThumbnailElement(element: Element | ComponentPublicInstance | null, asset: ImageGenerationAsset): void {
  const existing = thumbnailElementsByAssetID.get(asset.id)
  if (existing && existing !== element) {
    thumbnailObserver?.unobserve(existing)
    thumbnailAssetsByElement.delete(existing)
    thumbnailElementsByAssetID.delete(asset.id)
  }

  if (!(element instanceof Element)) return
  if (thumbnailObjectURLs.value[asset.id] || failedThumbnailLoads.value[asset.id] || thumbnailRequests.has(asset.id)) return

  const observer = ensureThumbnailObserver()
  if (!observer) {
    void cacheThumbnailAsset(asset)
    return
  }

  thumbnailAssetsByElement.set(element, asset)
  thumbnailElementsByAssetID.set(asset.id, element)
  observer.observe(element)
}

function retryAssetLoad(asset: ImageGenerationAsset): void {
  failedThumbnailLoads.value = {
    ...failedThumbnailLoads.value,
    [asset.id]: false,
  }
  void cacheThumbnailAsset(asset)
}

function handleAssetCardClick(item: { record: ImageGenerationRecord; asset: ImageGenerationAsset }): void {
  if (assetImageFailed(item.asset) && !assetImageSrc(item.asset)) {
    retryAssetLoad(item.asset)
    return
  }
  preview.value = item
  void loadPreviewAsset(item.asset)
}

function revokePreviewObjectURL(): void {
  if (!previewImageURL.value) return
  URL.revokeObjectURL(previewImageURL.value)
  previewImageURL.value = ''
}

async function loadPreviewAsset(asset: ImageGenerationAsset): Promise<void> {
  const token = ++previewLoadToken
  previewAbortController?.abort()
  const controller = new AbortController()
  previewAbortController = controller
  revokePreviewObjectURL()
  previewImageFailed.value = false
  try {
    const blob = await fetchAdminAssetBlob(asset, { signal: controller.signal })
    const objectURL = URL.createObjectURL(blob)
    if (token !== previewLoadToken) {
      URL.revokeObjectURL(objectURL)
      return
    }
    previewImageURL.value = objectURL
  } catch (error) {
    if (token === previewLoadToken && !isAbortError(error)) {
      previewImageFailed.value = true
    }
  } finally {
    if (previewAbortController === controller) {
      previewAbortController = null
    }
  }
}

function retryPreviewLoad(): void {
  if (preview.value) void loadPreviewAsset(preview.value.asset)
}

function closePreview(): void {
  previewLoadToken += 1
  previewAbortController?.abort()
  previewAbortController = null
  revokePreviewObjectURL()
  previewImageFailed.value = false
  preview.value = null
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
    page: pagination.page,
    page_size: pagination.page_size,
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
    syncPagination(data)
    pruneThumbnailAssets(imageCards.value.map((item) => item.asset))
    resetImageCardRenderLimit()
  } finally {
    loading.value = false
  }
}

function applyFilters(): void {
  pagination.page = 1
  clearLoadedAssetState()
  void loadRecords()
}

async function loadMoreRecords() {
  if (!hasMoreRecords.value || loadingMore.value) return
  loadingMore.value = true
  pagination.page += 1
  try {
    const data = await imageGenerationsAPI.list(params())
    items.value = [...items.value, ...(data.items || [])]
    syncPagination(data)
    pruneThumbnailAssets(imageCards.value.map((item) => item.asset))
    scheduleImageCardRenderBatch()
  } catch (error) {
    pagination.page = Math.max(1, pagination.page - 1)
    throw error
  } finally {
    loadingMore.value = false
  }
}

function syncPagination(data: { total: number; page: number; page_size: number; pages: number }): void {
  pagination.total = data.total || 0
  pagination.page = data.page || pagination.page
  pagination.page_size = data.page_size || pagination.page_size
  pagination.pages = data.pages || 0
}

function pruneThumbnailAssets(assets: ImageGenerationAsset[]): void {
  const visibleAssetIDs = new Set<number>()
  for (const asset of assets) {
    visibleAssetIDs.add(asset.id)
  }

  const nextObjectURLs = { ...thumbnailObjectURLs.value }
  for (const [assetID, objectURL] of Object.entries(thumbnailObjectURLs.value)) {
    if (visibleAssetIDs.has(Number(assetID))) continue
    URL.revokeObjectURL(objectURL)
    delete nextObjectURLs[Number(assetID)]
  }
  thumbnailObjectURLs.value = nextObjectURLs

  failedThumbnailLoads.value = Object.fromEntries(
    Object.entries(failedThumbnailLoads.value).filter(([assetID]) => visibleAssetIDs.has(Number(assetID)))
  )
}

function clearLoadedAssetState(): void {
  closePreview()
  abortThumbnailRequests()
  for (const element of thumbnailElementsByAssetID.values()) {
    thumbnailObserver?.unobserve(element)
    thumbnailAssetsByElement.delete(element)
  }
  thumbnailElementsByAssetID.clear()
  for (const url of Object.values(thumbnailObjectURLs.value)) {
    URL.revokeObjectURL(url)
  }
  thumbnailObjectURLs.value = {}
  failedThumbnailLoads.value = {}
  resetImageCardRenderLimit()
}

function abortThumbnailRequests(): void {
  thumbnailLoadGeneration += 1
  for (const controller of thumbnailAbortControllers.values()) {
    controller.abort()
  }
  for (const job of pendingThumbnailJobs.values()) {
    job.resolve()
  }
  pendingThumbnailJobs.clear()
  thumbnailAbortControllers.clear()
  thumbnailRequests.clear()
}

async function clearAllArchives() {
  if (clearingArchives.value) return
  if (!window.confirm('确定要清空所有生图归档吗？该操作不可撤销。')) return
  clearingArchives.value = true
  try {
    const result = await imageGenerationsAPI.clearAllArchives()
    clearLoadedAssetState()
    items.value = []
    pagination.page = 1
    await Promise.all([loadRecords(), loadStats(), loadStorageStats()])
    if (result.storage_delete_failures > 0) {
      appStore.showWarning(`有 ${result.storage_delete_failures} 个存储对象清理失败，归档记录已保留，可稍后重试；本次已清理 ${result.assets_deleted} 个存储对象`)
    } else {
      appStore.showSuccess(`已清空 ${result.records_deleted} 条归档记录、${result.assets_deleted} 个资产`)
    }
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, '清空归档失败'))
  } finally {
    clearingArchives.value = false
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

onBeforeUnmount(() => {
  thumbnailObserver?.disconnect()
  thumbnailObserver = null
  clearRenderBatchTimer()
  closePreview()
  abortThumbnailRequests()
  for (const url of Object.values(thumbnailObjectURLs.value)) {
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
