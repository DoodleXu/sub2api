<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import {
  nativeAvailable,
  nativeCall,
  type ImageCapabilities,
  type ImageEditUpload,
  type ImageHistoryItem,
  type ImageTaskSummary,
  type ImageTaskView,
  type LocalImageAssetSummary,
} from '../native'

const prompt = ref('')
const model = ref('')
const mode = ref<'generate' | 'edit'>('generate')
const size = ref('')
const quality = ref('')
const count = ref(1)
const outputFormat = ref('')
const background = ref('')
const task = ref<ImageTaskView | null>(null)
const capabilities = ref<ImageCapabilities | null>(null)
const running = ref(false)
const message = ref('')
const downloading = ref<string | null>(null)
const downloaded = ref<Record<string, string>>({})
const referenceImages = ref<ImageEditUpload[]>([])
const maskImage = ref<ImageEditUpload | null>(null)
const referenceInput = ref<HTMLInputElement | null>(null)
const maskInput = ref<HTMLInputElement | null>(null)
const recentTasks = ref<ImageTaskSummary[]>([])
const localLibrary = ref<LocalImageAssetSummary[]>([])
const serverHistory = ref<ImageHistoryItem[]>([])
const historyLoading = ref(false)
const historyMessage = ref('')
const deletingAsset = ref<string | null>(null)
const deletingHistory = ref<string | null>(null)
const downloadingHistory = ref<string | null>(null)
let pollTimer: number | null = null

const fallbackUploadPartBytes = 20 * 1024 * 1024
const fallbackUploadTotalBytes = 80 * 1024 * 1024

const modelOptions = computed(() => {
  const operation = mode.value === 'edit' ? 'edits/async' : 'generations/async'
  const models = capabilities.value?.models.filter((item) => item.enabled && item.operations.includes(operation)) || []
  if (models.length > 0) return models
  return []
})
// Capability discovery is authoritative.  Until it succeeds, fail closed so
// an offline or partially deployed gateway cannot make the UI submit a request
// whose limits and storage guarantees are unknown.
const asyncAvailable = computed(() => capabilities.value?.async?.enabled === true)
const countOptions = computed(() => {
  const reported = capabilities.value?.limits?.max_images
  const maximum = reported !== undefined && Number.isFinite(reported) && reported > 0
    ? Math.min(4, Math.trunc(reported))
    : 4
  return Array.from({ length: maximum }, (_, index) => index + 1)
})

async function loadCapabilities() {
  if (!nativeAvailable()) return
  try {
    const next = await nativeCall((app) => app.GetImageCapabilities())
    capabilities.value = next
    if (next.async && !next.async.enabled) {
      message.value = next.async.reason === 'object_storage_unavailable'
        ? '服务端暂未启用异步图片存储，当前无法提交新的创作任务。'
        : '服务端异步图片任务当前不可用。'
    }
    if (next.defaults.model && next.models.some((item) => item.id === next.defaults.model && item.enabled)) model.value = next.defaults.model
    if (next.defaults.size) size.value = next.defaults.size
    if (next.defaults.quality) quality.value = next.defaults.quality
    if (next.defaults.output_format) outputFormat.value = next.defaults.output_format
    if (next.defaults.background) background.value = next.defaults.background
    if (next.defaults.n > 0) count.value = Math.min(next.defaults.n, countOptions.value.length)
  } catch (capabilityError) {
    capabilities.value = null
    message.value = capabilityError instanceof Error
      ? `无法读取图片能力：${capabilityError.message}`
      : '无法读取图片能力，暂不能提交创作任务。'
  }
}

function isTerminalStatus(status: string) {
  return ['completed', 'failed', 'cancelled', 'canceled', 'expired', 'succeeded', 'success'].includes(String(status || '').toLowerCase())
}

function formatBytes(value: number) {
  if (!Number.isFinite(value) || value < 0) return '—'
  if (value < 1024) return `${value} B`
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KiB`
  return `${(value / (1024 * 1024)).toFixed(1)} MiB`
}

function formatHistoryTime(value: number) {
  if (!Number.isFinite(value) || value <= 0) return '时间未知'
  return new Date(value * 1000).toLocaleString()
}

function safeTaskName(value: string) {
  const cleaned = value.replace(/[^a-zA-Z0-9_-]/g, '_').slice(0, 40)
  return cleaned || 'task'
}

async function loadHistoryWorkspace() {
  if (!nativeAvailable() || historyLoading.value) return
  historyLoading.value = true
  historyMessage.value = ''
  const [libraryResult, tasksResult, historyResult] = await Promise.allSettled([
    nativeCall((app) => app.ImageLibrary()),
    nativeCall((app) => app.ListImageTasks()),
    nativeCall((app) => app.ListImageHistory({ limit: 20 })),
  ])
  if (libraryResult.status === 'fulfilled') localLibrary.value = libraryResult.value
  if (tasksResult.status === 'fulfilled') recentTasks.value = tasksResult.value
  if (historyResult.status === 'fulfilled') serverHistory.value = historyResult.value.items
  const failures = [libraryResult, tasksResult, historyResult]
    .filter((result): result is PromiseRejectedResult => result.status === 'rejected')
    .map((result) => result.reason instanceof Error ? result.reason.message : '读取失败')
  if (failures.length) historyMessage.value = failures.join(' · ')
  historyLoading.value = false
}

async function deleteLocalAsset(asset: LocalImageAssetSummary) {
  if (!nativeAvailable() || deletingAsset.value) return
  if (!window.confirm(`删除本地图片“${asset.name || asset.id}”？此操作不可撤销。`)) return
  deletingAsset.value = asset.id
  try {
    await nativeCall((app) => app.DeleteImage(asset.id))
    localLibrary.value = localLibrary.value.filter((item) => item.id !== asset.id)
  } catch (error) {
    historyMessage.value = error instanceof Error ? error.message : '删除本地图片失败'
  } finally {
    deletingAsset.value = null
  }
}

async function downloadHistoryResults(item: ImageHistoryItem) {
  const taskID = item.task_id || item.id
  if (!nativeAvailable() || !taskID || !item.assets_available || item.assets_expired || downloadingHistory.value) return
  const key = `${taskID}:history`
  downloadingHistory.value = key
  try {
    const requested = Math.max(1, item.result_count || item.image_count || 1)
    const count = Math.min(requested, 16)
    const saved: LocalImageAssetSummary[] = []
    for (let index = 0; index < count; index += 1) {
      // Resolve a short-lived, server-authorized asset URL immediately before
      // handing it to Go. The URL is never rendered or persisted in the UI.
      const asset = await nativeCall((app) => app.GetImageHistoryAsset(taskID, index))
      if (!asset.url) continue
      const local = await nativeCall((app) => app.DownloadImage(asset.url, `history-${safeTaskName(taskID)}-${index + 1}.${outputExtension()}`))
      saved.push(local)
    }
    if (saved.length) {
      localLibrary.value = [...saved, ...localLibrary.value.filter((item) => !saved.some((entry) => entry.id === item.id))]
      historyMessage.value = `已将 ${saved.length} 个结果保存到本地图库。`
    } else {
      historyMessage.value = '服务端没有可下载的资产。'
    }
  } catch (error) {
    historyMessage.value = error instanceof Error ? error.message : '下载服务端结果失败'
  } finally {
    downloadingHistory.value = null
  }
}

async function deleteServerHistory(item: ImageHistoryItem) {
  const taskID = item.task_id || item.id
  if (!nativeAvailable() || !taskID || deletingHistory.value || !isTerminalStatus(item.status)) return
  if (!window.confirm(`删除服务端任务“${taskID}”及其结果？此操作不可撤销。`)) return
  deletingHistory.value = taskID
  try {
    await nativeCall((app) => app.DeleteImageHistory(taskID))
    serverHistory.value = serverHistory.value.filter((entry) => (entry.task_id || entry.id) !== taskID)
    historyMessage.value = '服务端历史已删除。'
  } catch (error) {
    historyMessage.value = error instanceof Error ? error.message : '删除服务端历史失败'
  } finally {
    deletingHistory.value = null
  }
}

function stopPolling() {
  if (pollTimer !== null) {
    window.clearTimeout(pollTimer)
    pollTimer = null
  }
}

function schedulePoll(taskID: string) {
  stopPolling()
  const delay = Math.max(2, capabilities.value?.defaults.poll_after_seconds || 3) * 1000
  pollTimer = window.setTimeout(() => { void poll(taskID) }, delay)
}

function setMode(next: 'generate' | 'edit') {
  mode.value = next
  if (next === 'generate') {
    referenceImages.value = []
    maskImage.value = null
  }
}

function fileToDataURL(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(typeof reader.result === 'string' ? reader.result : '')
    reader.onerror = () => reject(reader.error || new Error('读取图片失败'))
    reader.readAsDataURL(file)
  })
}

function uploadPartLimit() {
  const reported = capabilities.value?.limits?.max_upload_part_bytes
  return reported && Number.isFinite(reported) && reported > 0 ? reported : fallbackUploadPartBytes
}

function uploadTotalLimit() {
  const reported = capabilities.value?.limits?.max_upload_total_bytes
  return reported && Number.isFinite(reported) && reported > 0 ? reported : fallbackUploadTotalBytes
}

function dataURLBytes(value: ImageEditUpload | null) {
  if (!value) return 0
  if (typeof value.bytes === 'number' && Number.isFinite(value.bytes) && value.bytes >= 0) return value.bytes
  if (!value.data_url) return 0
  const comma = value.data_url.indexOf(',')
  if (comma < 0) return 0
  const encoded = value.data_url.slice(comma + 1).trim().replace(/=+$/, '')
  return Math.floor(encoded.length * 3 / 4)
}

function currentUploadBytes() {
  return referenceImages.value.reduce((total, image) => total + dataURLBytes(image), 0) + dataURLBytes(maskImage.value)
}

async function addReferenceFiles(event: Event) {
  const input = event.target as HTMLInputElement
  const files = Array.from(input.files || []).filter((file) => ['image/png', 'image/jpeg', 'image/webp', 'image/gif'].includes(file.type))
  input.value = ''
  const maximum = capabilities.value?.limits?.max_reference_images || 8
  let totalBytes = currentUploadBytes()
  try {
    for (const file of files.slice(0, Math.max(0, maximum - referenceImages.value.length))) {
      if (file.size > uploadPartLimit()) {
        message.value = `参考图超过 ${Math.round(uploadPartLimit() / 1024 / 1024)} MiB 限制。`
        continue
      }
      if (totalBytes + file.size > uploadTotalLimit()) {
        message.value = `参考图总大小超过 ${Math.round(uploadTotalLimit() / 1024 / 1024)} MiB 限制。`
        break
      }
      referenceImages.value.push({ name: file.name, content_type: file.type, data_url: await fileToDataURL(file) })
      totalBytes += file.size
    }
  } catch (error) {
    message.value = error instanceof Error ? error.message : '读取参考图失败'
  }
}

async function pickReferenceImages() {
  if (!nativeAvailable()) {
    referenceInput.value?.click()
    return
  }
  try {
    const handles = await nativeCall((app) => app.PickImageFiles(true))
    const maximum = capabilities.value?.limits?.max_reference_images || 8
    let totalBytes = currentUploadBytes()
    const remaining = Math.max(0, maximum - referenceImages.value.length)
    for (const handle of handles.slice(0, remaining)) {
      if (handle.bytes > uploadPartLimit()) {
        message.value = `参考图超过 ${Math.round(uploadPartLimit() / 1024 / 1024)} MiB 限制。`
        continue
      }
      if (totalBytes + handle.bytes > uploadTotalLimit()) {
        message.value = `参考图总大小超过 ${Math.round(uploadTotalLimit() / 1024 / 1024)} MiB 限制。`
        break
      }
      referenceImages.value.push({ name: handle.name, content_type: handle.content_type, file_handle: handle.id, bytes: handle.bytes })
      totalBytes += handle.bytes
    }
  } catch (error) {
    message.value = error instanceof Error ? error.message : '打开原生文件选择器失败'
  }
}

async function addMaskFile(event: Event) {
  const input = event.target as HTMLInputElement
  const file = Array.from(input.files || []).find((item) => item.type === 'image/png' || item.type === 'image/jpeg' || item.type === 'image/webp')
  input.value = ''
  if (!file) return
  if (file.size > uploadPartLimit()) {
    message.value = `蒙版超过 ${Math.round(uploadPartLimit() / 1024 / 1024)} MiB 限制。`
    return
  }
  const totalWithoutMask = referenceImages.value.reduce((total, image) => total + dataURLBytes(image), 0)
  if (totalWithoutMask + file.size > uploadTotalLimit()) {
    message.value = `图片总大小超过 ${Math.round(uploadTotalLimit() / 1024 / 1024)} MiB 限制。`
    return
  }
  try {
    maskImage.value = { name: file.name, content_type: file.type, data_url: await fileToDataURL(file) }
  } catch (error) {
    message.value = error instanceof Error ? error.message : '读取蒙版失败'
  }
}

async function pickMaskImage() {
  if (!nativeAvailable()) {
    maskInput.value?.click()
    return
  }
  try {
    const handles = await nativeCall((app) => app.PickImageFiles(false))
    const handle = handles[0]
    if (!handle) return
    if (handle.bytes > uploadPartLimit()) {
      message.value = `蒙版超过 ${Math.round(uploadPartLimit() / 1024 / 1024)} MiB 限制。`
      return
    }
    const totalWithoutMask = referenceImages.value.reduce((total, image) => total + dataURLBytes(image), 0)
    if (totalWithoutMask + handle.bytes > uploadTotalLimit()) {
      message.value = `图片总大小超过 ${Math.round(uploadTotalLimit() / 1024 / 1024)} MiB 限制。`
      return
    }
    maskImage.value = { name: handle.name, content_type: handle.content_type, file_handle: handle.id, bytes: handle.bytes }
  } catch (error) {
    message.value = error instanceof Error ? error.message : '打开原生文件选择器失败'
  }
}

async function poll(taskID: string) {
  try {
    const next = await nativeCall((app) => app.GetImageTask(taskID))
    task.value = next
    const status = next.status.toLowerCase()
    if (['processing', 'pending', 'queued', 'in_progress'].includes(status)) {
      running.value = true
      schedulePoll(taskID)
      return
    }
    stopPolling()
    if (status === 'completed' || status === 'succeeded' || status === 'success') {
      running.value = true
      const failed = await downloadCompletedAssets(next)
      running.value = false
      message.value = !next.assets?.length
        ? '生成完成，但服务端没有返回可下载结果。'
        : failed
          ? '生成完成，部分结果未能自动保存，请使用下方按钮重试。'
          : '生成完成，结果已自动保存到本地图库。'
      void loadHistoryWorkspace()
    } else {
      running.value = false
      message.value = next.error?.message || '生成任务失败'
    }
  } catch (error) {
    running.value = false
    message.value = error instanceof Error ? error.message : '查询任务失败'
  }
}

async function restoreUnfinishedTask() {
  if (!nativeAvailable()) return
  try {
    const tasks = await nativeCall((app) => app.ListImageTasks())
    const pending = tasks.find((item) => typeof item.task_id === 'string' && item.task_id.trim() !== '' && ['processing', 'pending', 'queued', 'in_progress'].includes(String(item.status || '').toLowerCase()))
    if (!pending) return
    task.value = { id: pending.task_id, task_id: pending.task_id, status: pending.status }
    running.value = true
    message.value = '已恢复未完成任务，正在查询结果…'
    schedulePoll(pending.task_id)
  } catch {
    // A missing local checkpoint should not prevent a new generation.
  }
}

async function downloadCompletedAssets(completed: ImageTaskView) {
  if (!nativeAvailable() || !completed.assets?.length) return false
  let failed = false
  const extension = outputExtension()
  for (const [index, asset] of completed.assets.entries()) {
    const key = `${completed.task_id}:${index}:${asset.url}`
    if (downloaded.value[key]) continue
    try {
      const saved = await nativeCall((app) => app.DownloadImage(asset.url, `generated-${index + 1}.${extension}`))
      downloaded.value[key] = saved.name
    } catch (error) {
      failed = true
      message.value = error instanceof Error ? `生成完成，但自动保存失败：${error.message}` : '生成完成，但自动保存失败。'
    }
  }
  if (!failed && completed.task_id && completed.assets.length > 0) {
    try {
      await nativeCall((app) => app.MarkImageTaskAssetsDownloaded(completed.task_id))
    } catch {
      // The files are already safe locally; leaving the marker unset makes the
      // startup recovery pass retry the acknowledgement without losing them.
    }
  }
  return failed
}

function outputExtension() {
  const candidate = outputFormat.value.trim().toLowerCase()
  return ['png', 'jpeg', 'jpg', 'webp'].includes(candidate) ? candidate : 'png'
}

async function generate() {
  stopPolling()
  message.value = ''
  task.value = null
  if (!asyncAvailable.value) {
    message.value = capabilities.value?.async?.reason === 'object_storage_unavailable'
      ? '服务端暂未启用异步图片存储，请稍后再试。'
      : '服务端异步图片任务当前不可用，请稍后再试。'
    return
  }
  if (!prompt.value.trim()) {
    message.value = '请先写下想生成的画面。'
    return
  }
  if (mode.value === 'edit' && referenceImages.value.length === 0) {
    message.value = '编辑模式至少需要一张参考图。'
    return
  }
  if (!nativeAvailable()) {
    message.value = '请在桌面应用中运行生图操作。'
    return
  }
  running.value = true
  try {
    const created = mode.value === 'edit'
      ? await nativeCall((app) => app.EditImage({ model: model.value || undefined, prompt: prompt.value, n: count.value, size: size.value || undefined, quality: quality.value || undefined, background: background.value || undefined, output_format: outputFormat.value || undefined, images: referenceImages.value, mask: maskImage.value || undefined }))
      : await nativeCall((app) => app.GenerateImage({ model: model.value || undefined, prompt: prompt.value, n: count.value, size: size.value || undefined, quality: quality.value || undefined, background: background.value || undefined, output_format: outputFormat.value || undefined }))
    task.value = created
    const status = created.status.toLowerCase()
    message.value = ['processing', 'pending', 'queued', 'in_progress'].includes(status) ? '任务已提交，正在等待结果…' : '任务已返回。'
    if (['processing', 'pending', 'queued', 'in_progress'].includes(status)) schedulePoll(created.task_id)
    else if (['completed', 'succeeded', 'success'].includes(status)) {
      const failed = await downloadCompletedAssets(created)
      running.value = false
      if (created.assets?.length) message.value = failed ? '任务完成，部分结果未能自动保存。' : '任务完成，结果已自动保存到本地图库。'
      void loadHistoryWorkspace()
    } else {
      running.value = false
    }
    if (['processing', 'pending', 'queued', 'in_progress'].includes(status)) void loadHistoryWorkspace()
  } catch (error) {
    running.value = false
    message.value = error instanceof Error ? error.message : '提交生图任务失败'
  }
}

function assetKey(index: number, url: string) {
  return `${task.value?.task_id || 'image'}:${index}:${url}`
}

async function downloadAsset(url: string, index: number) {
  if (!nativeAvailable() || !task.value || downloading.value) return
  const key = assetKey(index, url)
  if (downloaded.value[key]) return
  downloading.value = key
  try {
    const asset = await nativeCall((app) => app.DownloadImage(url, `generated-${index + 1}.${outputExtension()}`))
    downloaded.value[key] = asset.name
    if (task.value.assets?.length && task.value.task_id && task.value.assets.every((entry, entryIndex) => downloaded.value[assetKey(entryIndex, entry.url)])) {
      await nativeCall((app) => app.MarkImageTaskAssetsDownloaded(task.value!.task_id))
    }
    message.value = `已保存到本地图库：${asset.name}`
  } catch (error) {
    message.value = error instanceof Error ? error.message : '下载图片失败'
  } finally {
    downloading.value = null
  }
}

onMounted(async () => {
  await loadCapabilities()
  await Promise.all([restoreUnfinishedTask(), loadHistoryWorkspace()])
})
onBeforeUnmount(stopPolling)
</script>

<template>
  <div>
  <div class="grid grid-cols-[1.05fr_.95fr] gap-12">
    <div>
      <p class="max-w-lg text-sm leading-6 text-muted">直接使用站点的异步 Images API。任务 ID 会在收到 202 后立即落盘，窗口重开仍可用原 key 继续轮询。</p>
      <div class="mt-8 space-y-5">
        <div class="flex items-center gap-2"><button class="action-secondary" :class="mode === 'generate' ? 'border-teal-300/50 text-teal-200' : ''" type="button" @click="setMode('generate')">生成</button><button class="action-secondary" :class="mode === 'edit' ? 'border-teal-300/50 text-teal-200' : ''" type="button" @click="setMode('edit')">编辑</button></div>
        <div v-if="mode === 'edit'" class="space-y-3 border-l border-teal-300/30 pl-4"><div class="flex flex-wrap items-center gap-3"><button class="action-secondary" type="button" @click="pickReferenceImages">添加参考图</button><input ref="referenceInput" class="hidden" type="file" accept="image/png,image/jpeg,image/webp,image/gif" multiple @change="addReferenceFiles" /><button class="action-secondary" type="button" @click="pickMaskImage">上传蒙版</button><input ref="maskInput" class="hidden" type="file" accept="image/png,image/jpeg,image/webp" @change="addMaskFile" /></div><div v-if="referenceImages.length" class="flex flex-wrap gap-2"><div v-for="(image, index) in referenceImages" :key="`${image.name}-${index}`" class="flex items-center gap-2 rounded-lg border border-white/10 px-2 py-1 text-xs text-muted"><span class="max-w-[150px] truncate">{{ image.name || `参考图 ${index + 1}` }}</span><button class="text-muted hover:text-white" type="button" @click="referenceImages.splice(index, 1)">移除</button></div></div><div v-if="maskImage" class="flex items-center gap-2 text-xs text-muted"><span class="max-w-[180px] truncate">蒙版：{{ maskImage.name }}</span><button class="text-muted hover:text-white" type="button" @click="maskImage = null">移除</button></div></div>
        <label class="block text-sm text-ink">画面描述<textarea v-model="prompt" class="field min-h-[180px] resize-y" placeholder="一座被晨雾包围的海边灯塔，电影感光线…" /></label>
        <div class="grid grid-cols-2 gap-4">
          <label class="block text-sm text-ink">模型<select v-if="modelOptions.length" v-model="model" class="field"><option v-for="item in modelOptions" :key="item.id" :value="item.id">{{ item.id }}</option></select><input v-else v-model="model" class="field" type="text" placeholder="由服务端能力默认值决定" /></label>
          <label class="block text-sm text-ink">尺寸<input v-model="size" class="field" type="text" :placeholder="capabilities?.defaults.size || '由服务端默认'" /></label>
          <label class="block text-sm text-ink">质量<input v-model="quality" class="field" type="text" :placeholder="capabilities?.defaults.quality || '由服务端默认'" /></label>
          <label class="block text-sm text-ink">张数<select v-model.number="count" class="field"><option v-for="item in countOptions" :key="item" :value="item">{{ item }}</option></select></label>
          <label class="block text-sm text-ink">输出格式<input v-model="outputFormat" class="field" type="text" :placeholder="capabilities?.defaults.output_format || '由服务端默认'" /></label>
          <label class="block text-sm text-ink">背景<input v-model="background" class="field" type="text" :placeholder="capabilities?.defaults.background || '由服务端默认'" /></label>
        </div>
        <button class="action-primary" :disabled="running || !asyncAvailable" @click="generate">{{ running ? '任务进行中…' : (asyncAvailable ? '开始生成' : '异步任务不可用') }}</button>
      </div>
      <p v-if="message" class="mt-5 text-sm" :class="task?.status === 'failed' ? 'text-rose-200' : 'text-teal-300'">{{ message }}</p>
    </div>

    <div class="min-h-[420px] border-l border-white/[.08] pl-10">
      <p class="text-xs uppercase tracking-[.18em] text-muted">任务结果</p>
      <div v-if="!task" class="mt-6 grid min-h-[350px] place-items-center rounded-md border border-dashed border-white/10 bg-black/10 p-8 text-center text-sm leading-6 text-muted">提交后会在这里显示任务状态。<br />完成的图片可通过 Go bridge 下载到本地图库。</div>
      <div v-else class="mt-6 space-y-5">
        <div class="flex items-center justify-between border-b border-white/[.08] pb-4"><span class="font-mono text-xs text-muted">{{ task.task_id }}</span><span class="rounded-full bg-teal-300/10 px-2.5 py-1 text-xs text-teal-300">{{ task.status }}</span></div>
        <div v-if="task.assets?.length" class="grid grid-cols-2 gap-3">
          <div v-for="(asset, index) in task.assets" :key="asset.url" class="group overflow-hidden rounded-md border border-white/10 bg-black/20">
            <img :src="asset.url" alt="生成结果" class="aspect-square w-full object-cover transition duration-500 group-hover:scale-[1.03]" />
            <div class="flex items-center justify-end px-3 py-2"><button class="shrink-0 text-xs text-muted hover:text-white disabled:opacity-50" :disabled="!!downloading || !!downloaded[assetKey(index, asset.url)]" type="button" @click="downloadAsset(asset.url, index)">{{ downloaded[assetKey(index, asset.url)] ? '已保存' : (downloading === assetKey(index, asset.url) ? '下载中…' : '下载到本地图库') }}</button></div>
          </div>
        </div>
        <p v-else class="text-sm leading-6 text-muted">{{ task.status === 'processing' ? '服务端正在执行，客户端每 3 秒检查一次。' : '暂无可展示的结果。' }}</p>
    </div>
  </div>
  </div>

  <div class="mt-12 grid gap-10 border-t border-white/[.08] pt-8 xl:grid-cols-[.9fr_1.1fr]">
    <section>
      <div class="flex items-end justify-between gap-3">
        <div>
          <p class="text-xs uppercase tracking-[.18em] text-muted">最近任务</p>
          <p class="mt-2 text-xs text-muted">本机 checkpoint 仅保存任务摘要，密钥引用不会进入页面。</p>
        </div>
        <button class="text-xs text-teal-300 hover:text-teal-200 disabled:opacity-50" :disabled="historyLoading || !nativeAvailable()" type="button" @click="loadHistoryWorkspace">{{ historyLoading ? '读取中…' : '刷新' }}</button>
      </div>
      <div v-if="recentTasks.length" class="mt-5 space-y-3">
        <div v-for="recent in recentTasks.slice(0, 8)" :key="recent.id || recent.task_id" class="border-b border-white/[.07] pb-3">
          <div class="flex items-center justify-between gap-3"><span class="font-mono text-[11px] text-muted">{{ recent.task_id }}</span><span class="text-xs text-teal-300">{{ recent.status }}</span></div>
          <p v-if="recent.prompt" class="mt-1 truncate text-xs text-white">{{ recent.prompt }}</p>
          <p v-if="recent.model" class="mt-1 text-[11px] text-muted">{{ recent.model }}<span v-if="recent.updated_at"> · {{ recent.updated_at }}</span></p>
        </div>
      </div>
      <p v-else class="mt-5 text-xs text-muted">暂无本地任务记录。</p>
    </section>

    <section class="border-l border-white/[.08] pl-8">
      <div class="flex items-end justify-between gap-3">
        <div>
          <p class="text-xs uppercase tracking-[.18em] text-muted">服务端历史</p>
          <p class="mt-2 text-xs text-muted">下载前由服务端重新签发短期资产 URL，页面不展示该 URL。</p>
        </div>
        <span class="text-xs text-muted">{{ serverHistory.length }} 条</span>
      </div>
      <div v-if="serverHistory.length" class="mt-5 space-y-3">
        <div v-for="history in serverHistory.slice(0, 12)" :key="history.id || history.task_id" class="border-b border-white/[.07] pb-3">
          <div class="flex items-center justify-between gap-3"><span class="font-mono text-[11px] text-muted">{{ history.task_id || history.id }}</span><span class="text-xs" :class="isTerminalStatus(history.status) ? 'text-teal-300' : 'text-amber-200'">{{ history.status }}</span></div>
          <p class="mt-1 text-xs text-muted">{{ history.model || history.operation || 'Images' }} · {{ history.result_count || history.image_count || 0 }} 个结果 · {{ formatHistoryTime(history.created_at) }}</p>
          <div class="mt-2 flex flex-wrap gap-3">
            <button class="text-xs text-teal-300 hover:text-teal-200 disabled:cursor-not-allowed disabled:opacity-40" :disabled="!history.assets_available || !!history.assets_expired || !!downloadingHistory" type="button" @click="downloadHistoryResults(history)">{{ history.assets_expired ? '资产已过期' : (downloadingHistory === `${history.task_id || history.id}:history` ? '下载中…' : (history.assets_available ? '下载结果到图库' : '暂无可下载资产')) }}</button>
            <button class="text-xs text-rose-200 hover:text-rose-100 disabled:cursor-not-allowed disabled:opacity-40" :disabled="!isTerminalStatus(history.status) || !!deletingHistory" type="button" @click="deleteServerHistory(history)">{{ deletingHistory === (history.task_id || history.id) ? '删除中…' : (isTerminalStatus(history.status) ? '删除历史' : '执行中不可删') }}</button>
          </div>
        </div>
      </div>
      <p v-else class="mt-5 text-xs text-muted">暂无服务端历史，或当前设备授权未包含账户历史权限。</p>
    </section>
  </div>

  <section class="mt-12 border-t border-white/[.08] pt-8">
    <div class="flex items-end justify-between gap-3">
      <div>
        <p class="text-xs uppercase tracking-[.18em] text-muted">本地图库</p>
        <p class="mt-2 text-xs text-muted">只显示安全元数据；文件路径由 Go 层保留，不会跨 Wails 边界。</p>
      </div>
      <span class="text-xs text-muted">{{ localLibrary.length }} 张</span>
    </div>
    <div v-if="localLibrary.length" class="mt-5 grid gap-x-6 gap-y-3 sm:grid-cols-2 lg:grid-cols-3">
      <div v-for="asset in localLibrary.slice(0, 18)" :key="asset.id" class="flex min-w-0 items-start justify-between gap-3 border-b border-white/[.07] pb-3">
        <div class="min-w-0"><p class="truncate text-xs text-white">{{ asset.name || '未命名图片' }}</p><p class="mt-1 text-[11px] text-muted">{{ asset.mime_type }} · {{ formatBytes(asset.bytes) }}</p><p v-if="asset.created_at" class="mt-1 text-[11px] text-muted">{{ asset.created_at }}</p></div>
        <button class="shrink-0 text-[11px] text-rose-200 hover:text-rose-100 disabled:opacity-40" :disabled="deletingAsset === asset.id || !!deletingAsset" type="button" @click="deleteLocalAsset(asset)">{{ deletingAsset === asset.id ? '删除中…' : '删除' }}</button>
      </div>
    </div>
    <p v-else class="mt-5 text-xs text-muted">本地图库为空。生成完成后结果会自动保存。</p>
  </section>

  <p v-if="historyMessage" class="mt-6 border-l-2 border-amber-300/60 bg-amber-300/[.05] px-4 py-3 text-xs leading-5 text-amber-100">{{ historyMessage }}</p>
</div>
</template>
