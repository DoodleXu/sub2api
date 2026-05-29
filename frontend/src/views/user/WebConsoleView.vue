<template>
  <AppLayout>
    <div class="mx-auto flex h-[calc(100vh-8rem)] max-w-7xl gap-4">
      <aside class="hidden w-72 shrink-0 overflow-hidden rounded-lg border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-900 lg:flex lg:flex-col">
        <div class="border-b border-gray-100 p-4 dark:border-dark-700">
          <button type="button" class="btn btn-primary w-full" @click="startSession(activeMode)">
            <Icon name="plus" size="sm" class="mr-2" />
            {{ activeMode === 'image' ? '新生图会话' : '新对话' }}
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
        <div class="border-t border-gray-100 p-3 dark:border-dark-700">
          <button type="button" class="btn btn-secondary w-full" @click="clearCurrentMessages" :disabled="!currentSession">
            <Icon name="trash" size="sm" class="mr-2" />
            清空当前会话
          </button>
        </div>
      </aside>

      <section class="flex min-w-0 flex-1 flex-col overflow-hidden rounded-lg border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-900">
        <div class="border-b border-gray-100 p-4 dark:border-dark-700">
          <div class="mb-3 flex items-center gap-2 lg:hidden">
            <select v-model="currentSessionId" class="input min-w-0 flex-1" aria-label="切换会话">
              <option v-for="session in sessions" :key="session.id" :value="session.id">
                {{ session.title }} · {{ session.mode === 'image' ? '生图' : '对话' }}
              </option>
            </select>
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
              title="清空当前会话"
              aria-label="清空当前会话"
              :disabled="!currentSession"
              @click="clearCurrentMessages"
            >
              <Icon name="trash" size="sm" />
            </button>
          </div>
          <div class="flex flex-col gap-3 xl:flex-row xl:items-end">
            <div class="grid flex-1 grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-4">
              <label class="block">
                <span class="input-label">API 端点</span>
                <select v-model="selectedEndpoint" class="input" aria-label="API 端点">
                  <option v-for="endpoint in endpointOptions" :key="endpoint.endpoint" :value="endpoint.endpoint">
                    {{ endpoint.name }}
                  </option>
                </select>
              </label>
              <label class="block">
                <span class="input-label">API Key / 额度</span>
                <select v-model.number="selectedKeyId" class="input" aria-label="API Key / 额度">
                  <option :value="0">{{ compatibleApiKeys.length > 0 ? '请选择 API Key' : '当前端点无可用 API Key' }}</option>
                  <option v-for="key in compatibleApiKeys" :key="key.id" :value="key.id">
                    {{ key.name }} - {{ key.group?.name || '未分组' }}
                  </option>
                </select>
                <p v-if="keyCompatibilityMessage" class="mt-1 text-xs text-amber-600 dark:text-amber-400">
                  {{ keyCompatibilityMessage }}
                </p>
              </label>
              <label class="block">
                <span class="input-label">模型</span>
                <input v-model.trim="model" class="input" placeholder="gpt-5.4" />
              </label>
              <label class="block">
                <span class="input-label">响应模式</span>
                <select v-if="activeMode === 'chat'" v-model="chatMode" class="input">
                  <option value="auto">自动 fallback</option>
                  <option value="responses">Responses</option>
                  <option value="chat">Chat Completions</option>
                </select>
                <select v-else v-model="imageMode" class="input">
                  <option value="auto">自动 fallback</option>
                  <option value="images">Images 原生接口</option>
                  <option value="responses">Responses 生图</option>
                </select>
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
          <div v-if="activeMode === 'image'" class="mt-3 grid grid-cols-2 gap-3 md:grid-cols-5">
            <label class="block">
              <span class="input-label">尺寸</span>
              <select v-model="imageSize" class="input">
                <option value="">默认</option>
                <option value="1024x1024">1024 x 1024</option>
                <option value="1536x1024">1536 x 1024</option>
                <option value="1024x1536">1024 x 1536</option>
                <option value="2048x2048">2048 x 2048</option>
                <option value="2048x1152">2048 x 1152</option>
                <option value="3840x2160">3840 x 2160</option>
                <option value="2160x3840">2160 x 3840</option>
              </select>
            </label>
            <label class="block">
              <span class="input-label">质量</span>
              <select v-model="imageQuality" class="input">
                <option value="">默认</option>
                <option value="low">低</option>
                <option value="medium">中</option>
                <option value="high">高</option>
              </select>
            </label>
            <label class="block">
              <span class="input-label">背景</span>
              <select v-model="imageBackground" class="input">
                <option value="">默认</option>
                <option value="auto">自动</option>
                <option value="transparent">透明</option>
                <option value="opaque">不透明</option>
              </select>
            </label>
            <label class="block">
              <span class="input-label">格式</span>
              <select v-model="imageOutputFormat" class="input">
                <option value="png">PNG</option>
                <option value="jpeg">JPEG</option>
                <option value="webp">WebP</option>
              </select>
            </label>
            <label class="block">
              <span class="input-label">张数</span>
              <input v-model.number="imageCount" class="input" type="number" min="1" max="4" step="1" @change="clampImageCount" />
            </label>
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
              :class="message.role === 'user'
                ? 'bg-primary-600 text-white'
                : 'border border-gray-200 bg-white text-gray-900 dark:border-dark-700 dark:bg-dark-900 dark:text-gray-100'"
            >
              <p v-if="message.content" class="whitespace-pre-wrap text-sm leading-6">{{ message.content }}</p>
              <div v-if="message.images?.length" class="mt-3 grid grid-cols-1 gap-3 sm:grid-cols-2">
                <figure
                  v-for="(image, imageIndex) in message.images"
                  :key="`${message.id}-${imageIndex}-${image.url}`"
                  class="overflow-hidden rounded-lg border border-gray-200 bg-gray-50 dark:border-dark-700 dark:bg-dark-800"
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
              <div v-if="message.role === 'assistant' && message.imageRequest" class="mt-3 flex justify-end">
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

        <form class="border-t border-gray-100 p-4 dark:border-dark-700" @submit.prevent="submit">
          <div v-if="errorMessage" class="mb-3 rounded-lg border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300">
            {{ errorMessage }}
          </div>
          <div class="flex gap-3">
            <textarea
              v-model="prompt"
              class="input min-h-[72px] flex-1 resize-none"
              :placeholder="activeMode === 'image' ? '描述你想生成的图片...' : '输入消息...'"
              @keydown.enter.exact.prevent="submit"
            />
            <button type="submit" class="btn btn-primary self-end" :disabled="submitting || !canSubmit">
              <Icon :name="submitting ? 'refresh' : 'arrowRight'" size="md" :class="submitting ? 'mr-2 animate-spin' : 'mr-2'" />
              {{ submitting ? '处理中' : activeMode === 'image' ? '生成' : '发送' }}
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
import { keysAPI } from '@/api'
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
  generateWebConsoleImage,
  isWebConsoleOpenAICompatibleEndpoint,
  sendWebConsoleChat,
  webConsoleErrorMessage,
} from '@/features/web-console/openaiClient'
import type {
  ChatResponseMode,
  ImageResponseMode,
  WebConsoleImage,
  WebConsoleImageOptions,
  WebConsoleImageRequest,
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
const chatModel = ref('gpt-5.4')
const imageModelValue = ref('gpt-image-2')
const chatMode = ref<ChatResponseMode>('auto')
const imageMode = ref<ImageResponseMode>('auto')
const activeMode = ref<WebConsoleMode>('chat')
const imageSize = ref('')
const imageQuality = ref('')
const imageBackground = ref('')
const imageOutputFormat = ref('png')
const imageCount = ref(1)
const prompt = ref('')
const submitting = ref(false)
const errorMessage = ref('')
const messagePanel = ref<HTMLElement | null>(null)

const publicSettings = computed(() => appStore.cachedPublicSettings)
const endpointOptions = computed<EndpointOption[]>(() => {
  const settings = publicSettings.value
  const items: EndpointOption[] = []
  const add = (name: string, endpoint: string, description?: string) => {
    const value = endpoint.trim()
    if (!value || !isWebConsoleOpenAICompatibleEndpoint(value) || items.some((item) => item.endpoint === value)) return
    items.push({ name, endpoint: value, description })
  }
  add('主端点', settings?.api_base_url || '')
  for (const endpoint of settings?.custom_endpoints || []) {
    add(endpoint.name || endpoint.endpoint, endpoint.endpoint, endpoint.description)
  }
  add('当前站点', window.location.origin)
  return items
})

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
const selectedKey = computed(() => compatibleApiKeys.value.find((key) => key.id === selectedKeyId.value) || null)
const keyCompatibilityMessage = computed(() => {
  if (!selectedEndpoint.value || compatibleApiKeys.value.length > 0) return ''
  const platforms = (compatibleEndpointPlatforms.value.length > 0 ? compatibleEndpointPlatforms.value : ['openai']).map(platformLabel).join(' / ')
  return `当前端点仅支持 ${platforms} 分组的 API Key，请切换端点或选择对应平台额度。`
})
const currentSession = computed(() => sessions.value.find((session) => session.id === currentSessionId.value) || null)
const model = computed({
  get: () => activeMode.value === 'image' ? imageModelValue.value : chatModel.value,
  set: (value: string) => {
    if (activeMode.value === 'image') {
      imageModelValue.value = value
    } else {
      chatModel.value = value
    }
  },
})
const canSubmit = computed(() => Boolean(prompt.value.trim() && selectedEndpoint.value && selectedKey.value && model.value.trim()))

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
  session.updated_at = new Date().toISOString()
  if (titlePrompt && session.messages.length <= 1) {
    session.title = titleFromPrompt(titlePrompt, session.mode === 'image' ? '新生图会话' : '新对话')
  }
  sessions.value = [
    session,
    ...sessions.value.filter((item) => item.id !== session.id),
  ]
  currentSessionId.value = session.id
  persistSessions()
}

function clearCurrentMessages(): void {
  const session = currentSession.value
  if (!session) return
  session.messages = []
  session.title = session.mode === 'image' ? '新生图会话' : '新对话'
  touchSession(session)
}

function clampImageCount(): void {
  imageCount.value = Math.min(Math.max(Math.trunc(Number(imageCount.value) || 1), 1), 4)
}

function currentImageOptions(): WebConsoleImageOptions {
  clampImageCount()
  return {
    size: imageSize.value,
    quality: imageQuality.value,
    background: imageBackground.value,
    outputFormat: imageOutputFormat.value,
    count: imageCount.value,
  }
}

function createImageRequest(input: string): WebConsoleImageRequest {
  return {
    prompt: input,
    model: imageModelValue.value.trim(),
    mode: imageMode.value,
    options: currentImageOptions(),
  }
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

function assistantImageContent(result: { images: WebConsoleImage[]; usedMode: string; fallbackUsed: boolean; text?: string }): string {
  return result.text || `已生成 ${result.images.length} 张图片（${result.usedMode}${result.fallbackUsed ? ' fallback' : ''}）`
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

async function regenerateImage(message: WebConsoleMessage): Promise<void> {
  if (submitting.value || !selectedKey.value || !message.imageRequest) return
  errorMessage.value = ''
  submitting.value = true

  try {
    const request = message.imageRequest
    const result = await generateWebConsoleImage(
      {
        endpoint: selectedEndpoint.value,
        apiKey: selectedKey.value.key,
        model: request.model,
        prompt: request.prompt,
        history: currentSession.value?.messages.filter((item) => item.id !== message.id) || [],
        imageOptions: request.options,
      },
      request.mode,
    )
    message.content = assistantImageContent(result)
    message.images = result.images
    message.created_at = new Date().toISOString()
    const session = currentSession.value
    if (session) {
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
  if (!canSubmit.value || submitting.value || !selectedKey.value) return
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
      const result = await generateWebConsoleImage(
        {
          endpoint: selectedEndpoint.value,
          apiKey: selectedKey.value.key,
          model: imageRequest.model,
          prompt: input,
          history: session.messages.slice(0, -1),
          imageOptions: imageRequest.options,
        },
        imageRequest.mode,
      )
      session.messages.push({
        id: createWebConsoleMessageId(),
        role: 'assistant',
        content: assistantImageContent(result),
        images: result.images,
        imageRequest,
        created_at: new Date().toISOString(),
      })
    } else {
      const result = await sendWebConsoleChat(
        {
          endpoint: selectedEndpoint.value,
          apiKey: selectedKey.value.key,
          model: model.value.trim(),
          prompt: input,
          history: session.messages.slice(0, -1),
        },
        chatMode.value,
      )
      session.messages.push({
        id: createWebConsoleMessageId(),
        role: 'assistant',
        content: result.text,
        created_at: new Date().toISOString(),
      })
    }
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
    window.location.origin
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
})
</script>
