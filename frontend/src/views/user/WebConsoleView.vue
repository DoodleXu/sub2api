<template>
  <AppLayout>
    <div class="web-console mx-auto flex h-[calc(100vh-8rem)] max-w-7xl gap-4">
      <aside class="hidden w-72 shrink-0 overflow-hidden rounded-lg border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-900 lg:flex lg:flex-col">
        <div class="border-b border-gray-100 p-4 dark:border-dark-700">
          <button type="button" class="btn btn-primary w-full" @click="startSession(activeMode)">
            <Icon name="plus" size="sm" class="mr-2" />
            {{ activeMode === 'image' ? '创建新会话' : '新对话' }}
          </button>
        </div>
        <div class="flex-1 space-y-1 overflow-y-auto p-3">
          <button
            v-for="session in sessions"
            :key="session.id"
            type="button"
            class="w-full rounded-lg px-3 py-2 text-left transition-colors"
            :class="session.id === currentSessionId
              ? 'bg-primary-50 text-primary-700 dark:bg-primary-900/30 dark:text-primary-200'
              : 'text-gray-700 hover:bg-gray-50 dark:text-gray-300 dark:hover:bg-dark-800'"
            @click="selectSession(session.id)"
          >
            <div class="flex items-center justify-between gap-2">
              <span class="truncate text-sm font-medium">{{ session.title }}</span>
              <span class="rounded bg-gray-100 px-1.5 py-0.5 text-[10px] text-gray-500 dark:bg-dark-700 dark:text-gray-400">
                {{ session.mode === 'image' ? '生图' : '对话' }}
              </span>
            </div>
            <p class="mt-1 truncate text-xs text-gray-400">
              {{ formatSessionTime(session.updated_at) }}
            </p>
          </button>
        </div>
        <div class="border-t border-gray-100 p-[14px] dark:border-dark-700">
          <button type="button" class="btn btn-secondary h-[50px] w-full" @click="deleteCurrentSession" :disabled="!currentSession || deletingSession">
            <Icon name="trash" size="sm" class="mr-2" />
            删除当前会话
          </button>
        </div>
      </aside>

      <section class="flex min-w-0 flex-1 flex-col overflow-hidden rounded-lg border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-900">
        <div class="border-b border-gray-100 p-4 dark:border-dark-700">
          <div class="mb-3 flex items-center gap-2 lg:hidden">
            <Select
              v-model="currentSessionId"
              :options="sessionOptions"
              aria-label="切换会话"
              class="web-console-control min-w-0 flex-1"
            />
            <button
              type="button"
              class="btn btn-secondary shrink-0 px-3"
              title="新建会话"
              aria-label="新建会话"
              @click="startSession(activeMode)"
            >
              <Icon name="plus" size="sm" />
            </button>
            <button
              type="button"
              class="btn btn-secondary shrink-0 px-3"
              title="删除当前会话"
              aria-label="删除当前会话"
              :disabled="!currentSession || deletingSession"
              @click="deleteCurrentSession"
            >
              <Icon name="trash" size="sm" />
            </button>
          </div>
          <div class="flex flex-col gap-3 xl:flex-row xl:items-end">
            <div class="grid flex-1 grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
              <label class="block">
                <span class="input-label">API 端点</span>
                <Select
                  v-model="selectedEndpoint"
                  :options="endpointSelectOptions"
                  aria-label="API 端点"
                  class="web-console-control"
                />
              </label>
              <label class="block">
                <span class="input-label">API Key / 额度</span>
                <Select
                  v-model="selectedKeyId"
                  :options="apiKeySelectOptions"
                  aria-label="API Key / 额度"
                  class="web-console-control"
                />
                <p v-if="keyCompatibilityMessage" class="mt-1 text-xs text-amber-600 dark:text-amber-400">
                  {{ keyCompatibilityMessage }}
                </p>
              </label>
              <label class="block">
                <span class="input-label">模型</span>
                <Select
                  v-model="model"
                  :options="modelOptions"
                  aria-label="模型"
                  class="web-console-control"
                />
              </label>
            </div>
            <div class="flex rounded-lg border border-gray-200 p-1 dark:border-dark-700">
              <button
                type="button"
                class="rounded-md px-3 py-2 text-sm font-medium"
                :class="activeMode === 'chat' ? 'bg-primary-600 text-white' : 'text-gray-600 dark:text-gray-300'"
                @click="switchMode('chat')"
              >
                对话
              </button>
              <button
                type="button"
                class="rounded-md px-3 py-2 text-sm font-medium"
                :class="activeMode === 'image' ? 'bg-primary-600 text-white' : 'text-gray-600 dark:text-gray-300'"
                @click="switchMode('image')"
              >
                生图
              </button>
            </div>
          </div>
          <div v-if="selectedKey" class="mt-3 flex flex-wrap gap-2 text-xs text-gray-500 dark:text-gray-400">
            <span class="rounded bg-gray-100 px-2 py-1 dark:bg-dark-800">{{ isSubscriptionType(selectedKey.group?.subscription_type) ? '订阅额度优先' : '账户余额' }}</span>
            <span v-if="selectedKey.quota > 0" class="rounded bg-gray-100 px-2 py-1 dark:bg-dark-800">
              Key 额度 ${{ selectedKey.quota_used.toFixed(2) }} / ${{ selectedKey.quota.toFixed(2) }}
            </span>
            <span class="rounded bg-gray-100 px-2 py-1 dark:bg-dark-800">{{ selectedKey.group?.platform || '未分组' }}</span>
          </div>
          <div v-if="activeMode === 'image'" class="mt-3 space-y-3">
            <div class="flex flex-wrap items-center gap-3">
              <div class="flex rounded-lg border border-gray-200 p-1 dark:border-dark-700">
                <button
                  type="button"
                  class="rounded-md px-3 py-2 text-sm font-medium"
                  :class="imageTaskMode === 'generate' ? 'bg-primary-600 text-white' : 'text-gray-600 dark:text-gray-300'"
                  @click="setImageTaskMode('generate')"
                >
                  生成
                </button>
                <button
                  type="button"
                  class="rounded-md px-3 py-2 text-sm font-medium"
                  :class="imageTaskMode === 'edit' ? 'bg-primary-600 text-white' : 'text-gray-600 dark:text-gray-300'"
                  @click="setImageTaskMode('edit')"
                >
                  编辑
                </button>
              </div>
              <button type="button" class="btn btn-secondary h-[42px]" @click="referenceFileInput?.click()">
                <Icon name="upload" size="sm" class="mr-2" />
                添加参考图
              </button>
              <input ref="referenceFileInput" class="sr-only" type="file" accept="image/*" multiple @change="handleReferenceFilesChange" />
              <button type="button" class="btn btn-secondary h-[42px]" @click="maskFileInput?.click()">
                <Icon name="upload" size="sm" class="mr-2" />
                上传蒙版
              </button>
              <input ref="maskFileInput" class="sr-only" type="file" accept="image/png" @change="handleMaskFileChange" />
            </div>
            <div v-if="referenceImages.length" class="flex gap-2 overflow-x-auto rounded-lg border border-gray-200 bg-gray-50 p-2 dark:border-dark-700 dark:bg-dark-950">
              <figure
                v-for="(reference, referenceIndex) in referenceImages"
                :key="`${reference.name || 'reference'}-${referenceIndex}-${reference.data_url.slice(0, 48)}`"
                class="relative h-20 w-20 shrink-0 overflow-hidden rounded-lg border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-900"
                :title="reference.name || `参考图 ${referenceIndex + 1}`"
              >
                <img :src="reference.data_url" :alt="reference.name || `参考图 ${referenceIndex + 1}`" class="h-full w-full object-cover" />
                <button
                  type="button"
                  class="absolute right-1 top-1 rounded bg-black/60 p-1 text-white hover:bg-black/75"
                  title="移除参考图"
                  @click="removeReferenceImage(referenceIndex)"
                >
                  <Icon name="x" size="xs" />
                </button>
              </figure>
            </div>
            <div v-if="maskImage" class="flex items-center gap-3 rounded-lg border border-gray-200 bg-gray-50 p-2 dark:border-dark-700 dark:bg-dark-950">
              <figure
                class="relative h-20 w-20 shrink-0 overflow-hidden rounded-lg border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-900"
                :title="maskImage.name || '蒙版'"
              >
                <img :src="maskImage.data_url" :alt="maskImage.name || '蒙版'" class="h-full w-full object-cover" />
                <button
                  type="button"
                  class="absolute right-1 top-1 rounded bg-black/60 p-1 text-white hover:bg-black/75"
                  title="移除蒙版"
                  @click="removeMaskImage"
                >
                  <Icon name="x" size="xs" />
                </button>
              </figure>
              <div class="min-w-0 text-sm">
                <p class="font-medium text-gray-700 dark:text-gray-200">蒙版</p>
                <p class="truncate text-xs text-gray-500 dark:text-gray-400">{{ maskImage.name || '未命名图片' }}</p>
              </div>
            </div>
            <div class="grid grid-cols-2 gap-3 md:grid-cols-4 xl:grid-cols-7">
              <label class="block">
                <span class="input-label">尺寸</span>
                <Select v-model="imageSize" :options="imageSizeOptions" class="web-console-control" />
              </label>
              <label class="block">
                <span class="input-label">比例</span>
                <Select v-model="imageRatio" :options="imageRatioOptions" class="web-console-control" />
              </label>
              <label class="block">
                <span class="input-label">质量</span>
                <Select v-model="imageQuality" :options="imageQualityOptions" class="web-console-control" />
              </label>
              <label class="block">
                <span class="input-label">背景</span>
                <Select v-model="imageBackground" :options="imageBackgroundOptions" class="web-console-control" />
              </label>
              <label class="block">
                <span class="input-label">格式</span>
                <Select v-model="imageOutputFormat" :options="imageOutputFormatOptions" class="web-console-control" />
              </label>
              <label class="block">
                <span class="input-label">压缩</span>
                <input v-model.number="imageOutputCompression" class="input web-console-control" type="number" min="0" max="100" step="1" :disabled="imageOutputFormat === 'png'" @change="clampImageOutputCompression" />
              </label>
              <label class="block">
                <span class="input-label">张数</span>
                <input v-model.number="imageCount" class="input web-console-control" type="number" min="1" max="4" step="1" @change="clampImageCount" />
              </label>
            </div>
          </div>
        </div>

        <div ref="messagePanel" class="flex-1 space-y-4 overflow-y-auto bg-gray-50 p-4 dark:bg-dark-950">
          <div v-if="!currentSession || currentSession.messages.length === 0" class="flex h-full items-center justify-center text-center">
            <div>
              <Icon name="sparkles" size="xl" class="mx-auto text-primary-500" />
              <h2 class="mt-3 text-lg font-semibold text-gray-900 dark:text-white">{{ activeMode === 'image' ? '开始一次生图' : '开始一次对话' }}</h2>
              <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">选择端点和 API Key 后即可使用当前账户额度。</p>
            </div>
          </div>

          <div
            v-for="message in currentSession?.messages || []"
            :key="message.id"
            class="flex"
            :class="message.role === 'user' ? 'justify-end' : 'justify-start'"
          >
            <div
              class="max-w-[88%] rounded-lg px-4 py-3 shadow-sm"
              :class="[
                message.role === 'user'
                  ? 'bg-primary-600 text-white'
                  : 'border border-gray-200 bg-white text-gray-900 dark:border-dark-700 dark:bg-dark-900 dark:text-gray-100',
                message.images?.length ? 'w-fit' : '',
              ]"
            >
              <div
                v-if="isImageGenerationInProgress(message)"
                class="flex min-h-24 min-w-64 flex-col items-center justify-center gap-3 text-center"
                role="status"
                aria-live="polite"
              >
                <span class="relative flex h-10 w-10 items-center justify-center rounded-full bg-primary-50 text-primary-600 dark:bg-primary-900/30 dark:text-primary-300">
                  <span class="absolute h-full w-full animate-ping rounded-full bg-primary-300/40 dark:bg-primary-500/30"></span>
                  <Icon name="refresh" size="md" class="relative animate-spin" />
                </span>
                <span class="text-sm text-gray-600 dark:text-gray-300">{{ message.content || pendingImageContent() }}</span>
              </div>
              <p v-else-if="message.content" class="whitespace-pre-wrap text-sm leading-6">{{ message.content }}</p>
              <div
                v-if="message.images?.length"
                class="mt-3 grid grid-cols-1 gap-3"
                :class="message.images.length > 1 ? 'sm:grid-cols-2' : 'sm:grid-cols-1'"
              >
                <figure
                  v-for="(image, imageIndex) in message.images"
                  :key="`${message.id}-${imageIndex}-${image.url}`"
                  class="w-full max-w-[20rem] overflow-hidden rounded-lg border border-gray-200 bg-gray-50 dark:border-dark-700 dark:bg-dark-800"
                >
                  <a :href="image.url" target="_blank" rel="noopener noreferrer">
                    <img :src="image.url" :alt="image.alt || message.content" class="aspect-square w-full object-cover" />
                  </a>
                  <div class="flex items-center justify-between gap-2 border-t border-gray-200 px-2 py-2 dark:border-dark-700">
                    <span class="truncate text-xs text-gray-500 dark:text-gray-400">{{ imageLabel(message, imageIndex) }}</span>
                    <div class="flex shrink-0 items-center gap-1">
                      <button
                        type="button"
                        class="rounded-md p-1.5 text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-900 dark:text-gray-400 dark:hover:bg-dark-700 dark:hover:text-gray-100"
                        title="下载图片"
                        @click="downloadImage(image, message, imageIndex)"
                      >
                        <Icon name="download" size="sm" />
                      </button>
                      <button
                        type="button"
                        class="rounded-md p-1.5 text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-900 dark:text-gray-400 dark:hover:bg-dark-700 dark:hover:text-gray-100"
                        title="用作参考图"
                        @click="useImageAsReference(image, message, imageIndex)"
                      >
                        <Icon name="upload" size="sm" />
                      </button>
                      <a
                        :href="image.url"
                        target="_blank"
                        rel="noopener noreferrer"
                        class="rounded-md p-1.5 text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-900 dark:text-gray-400 dark:hover:bg-dark-700 dark:hover:text-gray-100"
                        title="打开图片"
                      >
                        <Icon name="externalLink" size="sm" />
                      </a>
                    </div>
                  </div>
                </figure>
              </div>
              <div v-if="message.role === 'assistant' && message.imageRequest && !isImageGenerationInProgress(message)" class="mt-3 flex justify-end">
                <button
                  type="button"
                  class="inline-flex items-center rounded-md border border-gray-200 px-2.5 py-1.5 text-xs font-medium text-gray-600 transition-colors hover:bg-gray-50 disabled:cursor-not-allowed disabled:opacity-60 dark:border-dark-700 dark:text-gray-300 dark:hover:bg-dark-800"
                  :disabled="submitting || !selectedKey"
                  @click="regenerateImage(message)"
                >
                  <Icon name="refresh" size="sm" class="mr-1.5" :class="submitting ? 'animate-spin' : ''" />
                  重新生成
                </button>
              </div>
            </div>
          </div>
        </div>

        <form class="border-t border-gray-100 p-[14px] dark:border-dark-700" @submit.prevent="submit">
          <div v-if="errorMessage" class="mb-3 rounded-lg border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300">
            {{ errorMessage }}
          </div>
          <div class="flex items-center gap-3">
            <textarea
              v-model="prompt"
              class="input h-[50px] min-h-[50px] flex-1 resize-none overflow-y-auto"
              :placeholder="activeMode === 'image' ? (imageTaskMode === 'edit' ? '描述你想怎样编辑参考图...' : '描述你想生成的图片...') : '输入消息...'"
              @keydown.enter.exact.prevent="submit"
              @paste="handlePromptPaste"
            />
            <button type="submit" class="btn btn-primary h-[50px] self-center" :disabled="submitting || !canSubmit">
              <Icon :name="submitting ? 'refresh' : 'arrowRight'" size="md" :class="submitting ? 'mr-2 animate-spin' : 'mr-2'" />
              {{ submitting ? '处理中' : activeMode === 'image' ? (imageTaskMode === 'edit' ? '编辑' : '生成') : '发送' }}
            </button>
          </div>
        </form>
      </section>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import Select, { type SelectOption } from '@/components/common/Select.vue'
import { keysAPI, webConsoleImageTasksAPI } from '@/api'
import { useAppStore } from '@/stores/app'
import { isSubscriptionType } from '@/utils/subscriptionType'
import type { ApiKey } from '@/types'
import {
  createWebConsoleMessageId,
  createWebConsoleSession,
  loadWebConsoleSessions,
  saveWebConsoleSessions,
  titleFromPrompt,
} from '@/features/web-console/storage'
import {
  isWebConsoleOpenAICompatibleEndpoint,
  sendWebConsoleChat,
  webConsoleErrorMessage,
} from '@/features/web-console/openaiClient'
import type {
  WebConsoleImage,
  WebConsoleImageOptions,
  WebConsoleImageReference,
  WebConsoleImageRequest,
  WebConsoleImageTaskMode,
  WebConsoleMessage,
  WebConsoleMode,
  WebConsoleSession,
} from '@/features/web-console/types'

interface EndpointOption {
  name: string
  endpoint: string
  description?: string
}

const appStore = useAppStore()
const sessions = ref<WebConsoleSession[]>([])
const currentSessionId = ref('')
const apiKeys = ref<ApiKey[]>([])
const selectedKeyId = ref(0)
const selectedEndpoint = ref('')
const chatModel = ref('gpt-5.5')
const imageModelValue = ref('gpt-5.5')
const activeMode = ref<WebConsoleMode>('chat')
const imageTaskMode = ref<WebConsoleImageTaskMode>('generate')
const imageSize = ref('')
const imageRatio = ref('')
const imageQuality = ref('')
const imageBackground = ref('')
const imageOutputFormat = ref('png')
const imageOutputCompression = ref(80)
const imageCount = ref(1)
const referenceImages = ref<WebConsoleImageReference[]>([])
const maskImage = ref<WebConsoleImageReference | null>(null)
const prompt = ref('')
const submitting = ref(false)
const deletingSession = ref(false)
const errorMessage = ref('')
const messagePanel = ref<HTMLElement | null>(null)
const referenceFileInput = ref<HTMLInputElement | null>(null)
const maskFileInput = ref<HTMLInputElement | null>(null)
const deletingSessionIds = new Set<string>()
const pendingImageEditPayloads = new Map<string, {
  referenceImages: WebConsoleImageReference[]
  maskImage: WebConsoleImageReference | null
}>()
const IMAGE_REFERENCE_MAX_BYTES = 8 * 1024 * 1024
const IMAGE_REFERENCE_TOTAL_MAX_BYTES = 32 * 1024 * 1024

const publicSettings = computed(() => appStore.cachedPublicSettings)
const endpointOptions = computed<EndpointOption[]>(() => {
  const settings = publicSettings.value
  const items: EndpointOption[] = []
  const add = (name: string, endpoint: string, description?: string) => {
    const value = endpoint.trim()
    if (!value || !isAbsoluteHttpEndpoint(value) || !isWebConsoleOpenAICompatibleEndpoint(value) || items.some((item) => item.endpoint === value)) return
    items.push({ name, endpoint: value, description })
  }
  add('主端点', settings?.api_base_url || '')
  for (const endpoint of settings?.custom_endpoints || []) {
    add(endpoint.name || endpoint.endpoint, endpoint.endpoint, endpoint.description)
  }
  add('默认端点', settings?.web_console_default_endpoint || '')
  return items
})

function isAbsoluteHttpEndpoint(endpoint: string): boolean {
  try {
    const parsed = new URL(endpoint)
    return parsed.protocol === 'http:' || parsed.protocol === 'https:'
  } catch {
    return false
  }
}

function compatiblePlatformsForEndpoint(endpoint: string): string[] {
  return isWebConsoleOpenAICompatibleEndpoint(endpoint) ? ['openai'] : []
}

function platformLabel(platform: string): string {
  const labels: Record<string, string> = {
    openai: 'OpenAI',
    anthropic: 'Anthropic',
    gemini: 'Gemini',
    antigravity: 'Antigravity',
  }
  return labels[platform] || platform
}

const activeApiKeys = computed(() => apiKeys.value.filter((key) => key.status === 'active'))
const compatibleEndpointPlatforms = computed(() => compatiblePlatformsForEndpoint(selectedEndpoint.value))
const compatibleApiKeys = computed(() => {
  const platforms = new Set(compatibleEndpointPlatforms.value)
  return activeApiKeys.value.filter((key) => {
    const platform = key.group?.platform?.trim().toLowerCase()
    return Boolean(platform && platforms.has(platform))
  })
})
const sessionOptions = computed<SelectOption[]>(() => sessions.value.map((session) => ({
  value: session.id,
  label: `${session.title} · ${session.mode === 'image' ? '生图' : '对话'}`,
})))
const endpointSelectOptions = computed<SelectOption[]>(() => endpointOptions.value.map((endpoint) => ({
  value: endpoint.endpoint,
  label: endpoint.name,
})))
const apiKeySelectOptions = computed<SelectOption[]>(() => [
  {
    value: 0,
    label: compatibleApiKeys.value.length > 0 ? '请选择 API Key' : '当前端点无可用 API Key',
  },
  ...compatibleApiKeys.value.map((key) => ({
    value: key.id,
    label: `${key.name} - ${key.group?.name || '未分组'}`,
  })),
])
const selectedKey = computed(() => compatibleApiKeys.value.find((key) => key.id === selectedKeyId.value) || null)
const keyCompatibilityMessage = computed(() => {
  if (!selectedEndpoint.value || compatibleApiKeys.value.length > 0) return ''
  const platforms = (compatibleEndpointPlatforms.value.length > 0 ? compatibleEndpointPlatforms.value : ['openai']).map(platformLabel).join(' / ')
  return `当前端点仅支持 ${platforms} 分组的 API Key，请切换端点或选择对应平台额度。`
})
const currentSession = computed(() => sessions.value.find((session) => session.id === currentSessionId.value) || null)
const model = computed({
  get: () => activeMode.value === 'image' ? imageModelValue.value : chatModel.value,
  set: (value: string | number | boolean | null) => {
    const nextValue = String(value ?? '')
    if (activeMode.value === 'image') {
      imageModelValue.value = nextValue
    } else {
      chatModel.value = nextValue
    }
  },
})
const canSubmit = computed(() => {
  if (!prompt.value.trim() || !selectedEndpoint.value || !selectedKey.value || !model.value.trim()) return false
  if (activeMode.value === 'image' && imageTaskMode.value === 'edit' && referenceImages.value.length === 0) return false
  return true
})
const modelOptions: SelectOption[] = [
  { value: 'gpt-5.5', label: 'gpt-5.5' },
  { value: 'gpt-5.4', label: 'gpt-5.4' },
]
const imageSizeOptions: SelectOption[] = [
  { value: '', label: '默认' },
  { value: '1024x1024', label: '1024 x 1024' },
  { value: '1536x1024', label: '1536 x 1024' },
  { value: '1024x1536', label: '1024 x 1536' },
  { value: '2048x2048', label: '2048 x 2048' },
  { value: '2048x1152', label: '2048 x 1152' },
  { value: '3840x2160', label: '3840 x 2160' },
  { value: '2160x3840', label: '2160 x 3840' },
]
const imageRatioOptions: SelectOption[] = [
  { value: '', label: '默认' },
  { value: '1:1', label: '1:1' },
  { value: '4:5', label: '4:5' },
  { value: '5:4', label: '5:4' },
  { value: '3:4', label: '3:4' },
  { value: '4:3', label: '4:3' },
  { value: '2:3', label: '2:3' },
  { value: '3:2', label: '3:2' },
  { value: '9:16', label: '9:16' },
  { value: '16:9', label: '16:9' },
  { value: '9:21', label: '9:21' },
  { value: '21:9', label: '21:9' },
]
const imageQualityOptions: SelectOption[] = [
  { value: '', label: '默认' },
  { value: 'low', label: '低' },
  { value: 'medium', label: '中' },
  { value: 'high', label: '高' },
]
const imageBackgroundOptions: SelectOption[] = [
  { value: '', label: '默认' },
  { value: 'auto', label: '自动' },
  { value: 'opaque', label: '不透明' },
]
const imageOutputFormatOptions: SelectOption[] = [
  { value: 'png', label: 'PNG' },
  { value: 'jpeg', label: 'JPEG' },
  { value: 'webp', label: 'WebP' },
]
function persistSessions(): void {
  saveWebConsoleSessions(sessions.value)
}

function ensureSession(mode: WebConsoleMode): WebConsoleSession {
  if (currentSession.value) return currentSession.value
  const session = createWebConsoleSession(mode)
  sessions.value.unshift(session)
  currentSessionId.value = session.id
  persistSessions()
  return session
}

function startSession(mode: WebConsoleMode): void {
  activeMode.value = mode
  const session = createWebConsoleSession(mode)
  sessions.value.unshift(session)
  currentSessionId.value = session.id
  persistSessions()
}

function selectSession(sessionId: string): void {
  currentSessionId.value = sessionId
  const session = sessions.value.find((item) => item.id === sessionId)
  if (session) {
    activeMode.value = session.mode
  }
}

function switchMode(mode: WebConsoleMode): void {
  activeMode.value = mode
  const existing = sessions.value.find((session) => session.mode === mode)
  if (existing) {
    currentSessionId.value = existing.id
  } else {
    startSession(mode)
  }
}

function touchSession(session: WebConsoleSession, titlePrompt?: string): void {
  if (deletingSessionIds.has(session.id)) return
  session.updated_at = new Date().toISOString()
  if (titlePrompt && session.messages.length <= 1) {
    session.title = titleFromPrompt(titlePrompt, session.mode === 'image' ? '创建新会话' : '新对话')
  }
  sessions.value = [
    session,
    ...sessions.value.filter((item) => item.id !== session.id),
  ]
  currentSessionId.value = session.id
  persistSessions()
}

async function deleteCurrentSession(): Promise<void> {
  const session = currentSession.value
  if (!session || deletingSession.value) return
  deletingSession.value = true
  errorMessage.value = ''
  deletingSessionIds.add(session.id)
  try {
    if (session.mode === 'image') {
      await webConsoleImageTasksAPI.deleteSession(session.id)
    }
  } catch (error) {
    deletingSessionIds.delete(session.id)
    errorMessage.value = webConsoleErrorMessage(error)
    return
  } finally {
    deletingSession.value = false
  }
  const deletedMode = session.mode
  for (const message of session.messages) {
    pendingImageEditPayloads.delete(message.id)
  }
  sessions.value = sessions.value.filter((item) => item.id !== session.id)
  const nextSession = sessions.value.find((item) => item.mode === deletedMode) || sessions.value[0]
  if (nextSession) {
    currentSessionId.value = nextSession.id
    activeMode.value = nextSession.mode
  } else {
    const replacement = createWebConsoleSession(deletedMode)
    sessions.value.unshift(replacement)
    currentSessionId.value = replacement.id
    activeMode.value = replacement.mode
  }
  persistSessions()
  deletingSessionIds.delete(session.id)
}

function clampImageCount(): void {
  imageCount.value = Math.min(Math.max(Math.trunc(Number(imageCount.value) || 1), 1), 4)
}

function clampImageOutputCompression(): void {
  imageOutputCompression.value = Math.min(Math.max(Math.trunc(Number(imageOutputCompression.value) || 80), 0), 100)
}

function setImageTaskMode(mode: WebConsoleImageTaskMode): void {
  imageTaskMode.value = mode
  if (mode === 'generate') {
    referenceImages.value = []
    maskImage.value = null
  }
}

function currentImageOptions(): WebConsoleImageOptions {
  clampImageCount()
  clampImageOutputCompression()
  return {
    size: imageSize.value,
    quality: imageQuality.value,
    background: imageBackground.value,
    outputFormat: imageOutputFormat.value,
    count: imageCount.value,
    ratio: imageRatio.value,
    outputCompression: imageOutputFormat.value === 'png' ? null : imageOutputCompression.value,
  }
}

function defaultChatTools(): unknown[] {
  return [
    { type: 'web_search' },
    { type: 'image_generation' },
  ]
}

function createImageRequest(input: string): WebConsoleImageRequest {
  return {
    prompt: input,
    mode: imageTaskMode.value,
    model: imageModelValue.value.trim(),
    options: currentImageOptions(),
    referenceImages: [],
    maskImage: null,
  }
}

function currentImageEditPayload(): { referenceImages: WebConsoleImageReference[]; maskImage: WebConsoleImageReference | null } {
  if (imageTaskMode.value !== 'edit') {
    return { referenceImages: [], maskImage: null }
  }
  return {
    referenceImages: referenceImages.value.map((item) => ({ ...item })),
    maskImage: maskImage.value ? { ...maskImage.value } : null,
  }
}

function fileToDataURL(file: File | Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result || ''))
    reader.onerror = () => reject(reader.error || new Error('读取图片失败'))
    reader.readAsDataURL(file)
  })
}

function isImageFile(file: File): boolean {
  if (file.type.startsWith('image/')) return true
  return /\.(avif|bmp|gif|heic|heif|jpe?g|png|tiff?|webp)$/i.test(file.name)
}

function imageDataURLByteSize(dataURL: string): number {
  const raw = dataURL.includes(',') ? dataURL.slice(dataURL.indexOf(',') + 1) : dataURL
  const compact = raw.trim()
  if (!compact) return 0
  const padding = compact.endsWith('==') ? 2 : compact.endsWith('=') ? 1 : 0
  return Math.max(0, Math.floor((compact.length * 3) / 4) - padding)
}

function imageReferenceTotalBytes(nextReferences = referenceImages.value, nextMask = maskImage.value): number {
  return [
    ...nextReferences.map((item) => item.data_url),
    ...(nextMask ? [nextMask.data_url] : []),
  ].reduce((sum, dataURL) => sum + imageDataURLByteSize(dataURL), 0)
}

function validateImageReferenceSize(byteSize: number): boolean {
  if (byteSize > IMAGE_REFERENCE_MAX_BYTES) {
    errorMessage.value = '单张参考图最大 8MiB。'
    return false
  }
  if (imageReferenceTotalBytes() + byteSize > IMAGE_REFERENCE_TOTAL_MAX_BYTES) {
    errorMessage.value = '参考图总大小最大 32MiB。'
    return false
  }
  return true
}

function addReferenceImage(reference: WebConsoleImageReference, byteSize = imageDataURLByteSize(reference.data_url)): boolean {
  if (!reference.data_url) return false
  const exists = referenceImages.value.some((item) => item.data_url === reference.data_url)
  if (exists) return true
  if (!validateImageReferenceSize(byteSize)) return false
  activeMode.value = 'image'
  imageTaskMode.value = 'edit'
  referenceImages.value.push(reference)
  return true
}

async function addReferenceFiles(files: File[]): Promise<void> {
  const imageFiles = files.filter(isImageFile)
  if (imageFiles.length === 0) return
  errorMessage.value = ''
  for (const file of imageFiles.slice(0, 8 - referenceImages.value.length)) {
    if (!validateImageReferenceSize(file.size)) return
    addReferenceImage({
      data_url: await fileToDataURL(file),
      name: file.name,
    }, file.size)
  }
}

async function handleReferenceFilesChange(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement
  await addReferenceFiles(Array.from(input.files || []))
  input.value = ''
}

async function handleMaskFileChange(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement
  const file = Array.from(input.files || []).find(isImageFile)
  input.value = ''
  if (!file) return
  if (file.type !== 'image/png' && !/\.png$/i.test(file.name)) {
    errorMessage.value = '蒙版必须使用 PNG 图片。'
    return
  }
  if (file.size > IMAGE_REFERENCE_MAX_BYTES) {
    errorMessage.value = '单张参考图最大 8MiB。'
    return
  }
  if (imageReferenceTotalBytes(referenceImages.value, null) + file.size > IMAGE_REFERENCE_TOTAL_MAX_BYTES) {
    errorMessage.value = '参考图总大小最大 32MiB。'
    return
  }
  errorMessage.value = ''
  activeMode.value = 'image'
  imageTaskMode.value = 'edit'
  maskImage.value = {
    data_url: await fileToDataURL(file),
    name: file.name,
  }
}

function removeReferenceImage(index: number): void {
  referenceImages.value.splice(index, 1)
  if (referenceImages.value.length === 0 && !maskImage.value && imageTaskMode.value === 'edit') {
    imageTaskMode.value = 'generate'
  }
}

function removeMaskImage(): void {
  maskImage.value = null
  if (referenceImages.value.length === 0 && imageTaskMode.value === 'edit') {
    imageTaskMode.value = 'generate'
  }
}

async function handlePromptPaste(event: ClipboardEvent): Promise<void> {
  if (activeMode.value !== 'image') return
  const files = Array.from(event.clipboardData?.files || []).filter(isImageFile)
  if (files.length === 0) return
  event.preventDefault()
  await addReferenceFiles(files)
}

function formatSessionTime(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return ''
  return date.toLocaleString()
}

async function scrollToBottom(): Promise<void> {
  await nextTick()
  if (messagePanel.value) {
    messagePanel.value.scrollTop = messagePanel.value.scrollHeight
  }
}

function assistantImageContent(result: { images: WebConsoleImage[]; text?: string }): string {
  return result.text || `已生成 ${result.images.length} 张图片。`
}

function pendingImageContent(): string {
  return '生图任务已提交，正在生成图片。'
}

function failedImageContent(message?: string): string {
  return message ? `生图失败：${message}` : '生图失败，请稍后重试。'
}

function isImageGenerationInProgress(message: WebConsoleMessage): boolean {
  return Boolean(message.imageRequest && (message.status === 'pending' || message.status === 'running'))
}

async function createImageTaskForMessage(session: WebConsoleSession, message: WebConsoleMessage): Promise<void> {
  if (!selectedKey.value || !message.imageRequest) return
  const editPayload = message.imageRequest.mode === 'edit' ? pendingImageEditPayloads.get(message.id) : null
  const referenceImagePayload = editPayload?.referenceImages || message.imageRequest.referenceImages || []
  const maskImagePayload = editPayload ? editPayload.maskImage : (message.imageRequest.maskImage || null)
  if (message.imageRequest.mode === 'edit' && referenceImagePayload.length === 0) {
    throw new Error('该编辑请求的参考图只保存在本页内，刷新后无法重新生成，请重新添加参考图后再编辑。')
  }
  const { task } = await webConsoleImageTasksAPI.create({
    api_key_id: selectedKey.value.id,
    endpoint: selectedEndpoint.value,
    mode: message.imageRequest.mode || 'generate',
    model: message.imageRequest.model,
    prompt: message.imageRequest.prompt,
    options: message.imageRequest.options,
    reference_images: referenceImagePayload,
    mask_image: maskImagePayload,
    session_id: session.id,
    message_id: message.id,
  })
  message.imageTaskId = task.id
  message.status = task.status
  touchSession(session)
  void pollImageTask(session, message)
}

async function pollImageTask(session: WebConsoleSession, message: WebConsoleMessage): Promise<void> {
  if (!message.imageTaskId) return
  for (let attempt = 0; attempt < 900; attempt++) {
    if (deletingSessionIds.has(session.id)) return
    const task = await webConsoleImageTasksAPI.get(message.imageTaskId)
    if (deletingSessionIds.has(session.id)) return
    if (!task) return
    message.status = task.status
    if (task.status === 'completed') {
      message.images = task.assets.map((asset) => ({ url: asset.url, alt: message.imageRequest?.prompt }))
      message.content = assistantImageContent({ images: message.images })
      touchSession(session)
      await scrollToBottom()
      return
    }
    if (task.status === 'failed') {
      message.content = failedImageContent(task.error_message)
      touchSession(session)
      await scrollToBottom()
      return
    }
    touchSession(session)
    await new Promise((resolve) => window.setTimeout(resolve, 2000))
  }
}

function resumePendingImageTasks(): void {
  for (const session of sessions.value) {
    for (const message of session.messages) {
      if (message.imageTaskId && (message.status === 'pending' || message.status === 'running')) {
        void pollImageTask(session, message)
      }
    }
  }
}

function clearSubmitState(): void {
  prompt.value = ''
  errorMessage.value = ''
}

function imageLabel(message: WebConsoleMessage, index: number): string {
  const request = message.imageRequest
  if (!request) return `图片 ${index + 1}`
  const size = request.options.size || '默认尺寸'
  const quality = request.options.quality || '默认质量'
  return `${size} / ${quality}`
}

function imageFileExtension(image: WebConsoleImage, request?: WebConsoleImageRequest): string {
  const dataMatch = image.url.match(/^data:image\/([^;,]+)/)
  if (dataMatch?.[1]) return dataMatch[1] === 'jpeg' ? 'jpg' : dataMatch[1]
  const urlMatch = image.url.match(/\.([a-z0-9]+)(?:[?#]|$)/i)
  if (urlMatch?.[1]) return urlMatch[1].toLowerCase()
  const format = request?.options.outputFormat || 'png'
  return format === 'jpeg' ? 'jpg' : format
}

function downloadFile(url: string, filename: string): void {
  const link = document.createElement('a')
  link.href = url
  link.download = filename
  link.rel = 'noopener noreferrer'
  document.body.appendChild(link)
  link.click()
  link.remove()
}

async function downloadImage(image: WebConsoleImage, message: WebConsoleMessage, index: number): Promise<void> {
  const extension = imageFileExtension(image, message.imageRequest)
  const filename = `sub2api-image-${new Date().toISOString().replace(/[:.]/g, '-')}-${index + 1}.${extension}`
  if (image.url.startsWith('data:')) {
    downloadFile(image.url, filename)
    return
  }

  try {
    const response = await fetch(image.url)
    if (!response.ok) throw new Error(`HTTP ${response.status}`)
    const blob = await response.blob()
    const objectUrl = URL.createObjectURL(blob)
    downloadFile(objectUrl, filename)
    URL.revokeObjectURL(objectUrl)
  } catch {
    downloadFile(image.url, filename)
  }
}

async function useImageAsReference(image: WebConsoleImage, message: WebConsoleMessage, index: number): Promise<void> {
  try {
    let dataURL = image.url
    if (!dataURL.startsWith('data:image/')) {
      const response = await fetch(image.url)
      if (!response.ok) throw new Error(`HTTP ${response.status}`)
      dataURL = await fileToDataURL(await response.blob())
    }
    addReferenceImage({
      data_url: dataURL,
      name: `${imageLabel(message, index)}.${imageFileExtension(image, message.imageRequest)}`,
    })
  } catch (error) {
    errorMessage.value = webConsoleErrorMessage(error)
  }
}

async function regenerateImage(message: WebConsoleMessage): Promise<void> {
  if (submitting.value || !selectedKey.value || !message.imageRequest || isImageGenerationInProgress(message)) return
  errorMessage.value = ''
  submitting.value = true

  try {
    message.content = pendingImageContent()
    message.images = []
    message.status = 'pending'
    message.imageTaskId = undefined
    message.created_at = new Date().toISOString()
    const session = currentSession.value
    if (session) {
      await createImageTaskForMessage(session, message)
      touchSession(session)
    }
    await scrollToBottom()
  } catch (error) {
    errorMessage.value = webConsoleErrorMessage(error)
  } finally {
    submitting.value = false
  }
}

async function submit(): Promise<void> {
  if (!compatibleApiKeys.value.length) {
    errorMessage.value = keyCompatibilityMessage.value || '当前端点没有可用 API Key。'
    return
  }
  if (activeMode.value === 'image' && imageTaskMode.value === 'edit' && referenceImages.value.length === 0) {
    errorMessage.value = '编辑模式需要至少添加一张参考图。'
    return
  }
  if (!canSubmit.value || submitting.value || !selectedKey.value) return
  const chatTools = activeMode.value === 'chat' ? defaultChatTools() : []
  const toolChoice = chatTools.length > 0 ? 'auto' : undefined
  const session = ensureSession(activeMode.value)
  const input = prompt.value.trim()
  prompt.value = ''
  errorMessage.value = ''

  session.mode = activeMode.value
  session.messages.push({
    id: createWebConsoleMessageId(),
    role: 'user',
    content: input,
    created_at: new Date().toISOString(),
  })
  touchSession(session, input)
  await scrollToBottom()

  submitting.value = true
  try {
    if (activeMode.value === 'image') {
      const imageRequest = createImageRequest(input)
      const editPayload = currentImageEditPayload()
      const assistantMessage: WebConsoleMessage = {
        id: createWebConsoleMessageId(),
        role: 'assistant',
        content: pendingImageContent(),
        images: [],
        imageRequest,
        status: 'pending',
        created_at: new Date().toISOString(),
      }
      if (imageRequest.mode === 'edit') {
        pendingImageEditPayloads.set(assistantMessage.id, editPayload)
      }
      session.messages.push(assistantMessage)
      touchSession(session)
      await scrollToBottom()
      await createImageTaskForMessage(session, assistantMessage)
    } else {
      const result = await sendWebConsoleChat(
        {
          endpoint: selectedEndpoint.value,
          apiKey: selectedKey.value.key,
          model: model.value.trim(),
          prompt: input,
          history: session.messages.slice(0, -1),
          tools: chatTools,
          toolChoice,
        }
      )
      session.messages.push({
        id: createWebConsoleMessageId(),
        role: 'assistant',
        content: result.text,
        images: result.images,
        created_at: new Date().toISOString(),
      })
    }
    clearSubmitState()
    touchSession(session)
    await scrollToBottom()
  } catch (error) {
    errorMessage.value = webConsoleErrorMessage(error)
    prompt.value = input
  } finally {
    submitting.value = false
  }
}

async function loadApiKeys(): Promise<void> {
  const response = await keysAPI.list(1, 100, { status: 'active' })
  apiKeys.value = response.items || []
  syncSelectedKeyWithEndpoint()
}

function applyDefaultEndpoint(): void {
  const preferred = publicSettings.value?.web_console_default_endpoint?.trim()
  const options = endpointOptions.value
  selectedEndpoint.value =
    (preferred && options.some((item) => item.endpoint === preferred) ? preferred : '') ||
    options[0]?.endpoint ||
    ''
}

function syncSelectedKeyWithEndpoint(): void {
  if (compatibleApiKeys.value.some((key) => key.id === selectedKeyId.value)) return
  selectedKeyId.value = compatibleApiKeys.value[0]?.id || 0
}

watch(endpointOptions, () => {
  if (!endpointOptions.value.some((item) => item.endpoint === selectedEndpoint.value)) {
    applyDefaultEndpoint()
  }
})

watch([selectedEndpoint, apiKeys], () => {
  syncSelectedKeyWithEndpoint()
})

watch(currentSessionId, (sessionId) => {
  const session = sessions.value.find((item) => item.id === sessionId)
  if (session) {
    activeMode.value = session.mode
  }
})

onMounted(async () => {
  sessions.value = loadWebConsoleSessions()
  if (sessions.value.length > 0) {
    currentSessionId.value = sessions.value[0].id
    activeMode.value = sessions.value[0].mode
  } else {
    startSession('chat')
  }
  if (!publicSettings.value) {
    await appStore.fetchPublicSettings()
  }
  applyDefaultEndpoint()
  await loadApiKeys()
  resumePendingImageTasks()
})
</script>

<style scoped>
.web-console-control {
  @apply h-[42px];
}

.web-console-control :deep(.select-trigger) {
  @apply h-[42px];
}
</style>
