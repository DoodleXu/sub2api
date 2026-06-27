import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import WebConsoleView from '../WebConsoleView.vue'
import { loadWebConsoleSessions, saveWebConsoleSessions } from '@/features/web-console/storage'
import type { WebConsoleSession } from '@/features/web-console/types'

const keysListMock = vi.hoisted(() => vi.fn())
const fetchPublicSettingsMock = vi.hoisted(() => vi.fn())
const sendWebConsoleChatMock = vi.hoisted(() => vi.fn())
const createImageTaskMock = vi.hoisted(() => vi.fn())
const getImageTaskMock = vi.hoisted(() => vi.fn())
const deleteImageTaskSessionMock = vi.hoisted(() => vi.fn())
const appStore = vi.hoisted(() => ({
  cachedPublicSettings: {
    api_base_url: 'https://api.example.com',
    custom_endpoints: [],
    web_console_default_endpoint: '',
  },
  fetchPublicSettings: fetchPublicSettingsMock,
}))

vi.mock('@/features/web-console/openaiClient', () => ({
  sendWebConsoleChat: sendWebConsoleChatMock,
  isWebConsoleOpenAICompatibleEndpoint: (endpoint: string) => {
    const path = new URL(endpoint, 'https://app.example.com').pathname.replace(/\/+$/, '').toLowerCase()
    return !(path.endsWith('/v1beta') || path.includes('/v1beta/') || path.endsWith('/antigravity/v1') || path.includes('/antigravity/v1/'))
  },
  webConsoleErrorMessage: (error: unknown) => error instanceof Error ? error.message : '请求失败，请稍后重试。',
}))

vi.mock('@/api', () => ({
  keysAPI: {
    list: keysListMock,
  },
  webConsoleImageTasksAPI: {
    create: createImageTaskMock,
    get: getImageTaskMock,
    deleteSession: deleteImageTaskSessionMock,
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => appStore,
}))

vi.mock('@/components/layout/AppLayout.vue', () => ({
  default: {
    template: '<main><slot /></main>',
  },
}))

vi.mock('@/components/icons/Icon.vue', () => ({
  default: {
    props: ['name'],
    template: '<span data-testid="icon">{{ name }}</span>',
  },
}))

vi.mock('@/components/common/Select.vue', () => ({
  default: {
    props: ['modelValue', 'options', 'ariaLabel', 'disabled'],
    emits: ['update:modelValue', 'change'],
    data() {
      return { isOpen: false }
    },
    computed: {
      selectedLabel() {
        const selected = this.options.find((option: { value: unknown }) => String(option.value) === String(this.modelValue))
        return selected?.label || this.options[0]?.label || ''
      },
    },
    methods: {
      selectOption(option: { value: unknown }) {
        this.$emit('update:modelValue', option.value)
        this.$emit('change', option.value)
        this.isOpen = false
      },
    },
    template: `
      <div>
        <button type="button" :aria-label="ariaLabel || 'Select option'" :disabled="disabled" @click="isOpen = !isOpen">
          {{ selectedLabel }}
        </button>
        <Teleport to="body">
          <div v-if="isOpen">
            <button
              v-for="option in options"
              :key="String(option.value)"
              type="button"
              role="option"
              @click="selectOption(option)"
            >
              {{ option.label }}
            </button>
          </div>
        </Teleport>
      </div>
    `,
  },
}))

function session(overrides: Partial<WebConsoleSession>): WebConsoleSession {
  return {
    id: 'session-chat',
    title: '旧对话',
    mode: 'chat',
    messages: [{
      id: 'message-1',
      role: 'user',
      content: '旧消息',
      created_at: '2026-05-28T00:00:00.000Z',
    }],
    created_at: '2026-05-28T00:00:00.000Z',
    updated_at: '2026-05-28T00:00:00.000Z',
    ...overrides,
  }
}

async function openSelect(wrapper: ReturnType<typeof mount>, ariaLabel: string): Promise<void> {
  await wrapper.get(`button[aria-label="${ariaLabel}"]`).trigger('click')
  await flushPromises()
}

function selectOptionTexts(): string[] {
  return Array.from(document.body.querySelectorAll('[role="option"]')).map((option) => option.textContent?.trim() || '')
}

describe('WebConsoleView', () => {
  beforeEach(() => {
    localStorage.clear()
    appStore.cachedPublicSettings = {
      api_base_url: 'https://api.example.com',
      custom_endpoints: [],
      web_console_default_endpoint: '',
    }
    fetchPublicSettingsMock.mockResolvedValue(appStore.cachedPublicSettings)
    sendWebConsoleChatMock.mockReset()
    createImageTaskMock.mockReset()
    getImageTaskMock.mockReset()
    deleteImageTaskSessionMock.mockReset()
    deleteImageTaskSessionMock.mockResolvedValue({ deleted: 0 })
    keysListMock.mockResolvedValue({
      items: [{
        id: 1,
        name: '测试 Key',
        key: 'sk-test',
        status: 'active',
        quota: 10,
        quota_used: 0,
        group: {
          name: '默认组',
          platform: 'openai',
          subscription_type: 'balance',
        },
      }],
    })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('移动端提供会话切换、新建和删除入口', async () => {
    saveWebConsoleSessions([
      session({ id: 'session-chat', title: '旧对话', mode: 'chat' }),
      session({
        id: 'session-image',
        title: '海报会话',
        mode: 'image',
        messages: [],
        updated_at: '2026-05-28T00:01:00.000Z',
      }),
    ])

    const wrapper = mount(WebConsoleView)
    await flushPromises()

    const sessionSelect = wrapper.get('button[aria-label="切换会话"]')
    expect(sessionSelect.text()).toContain('海报会话')
    expect(wrapper.get('button[aria-label="新建会话"]').exists()).toBe(true)
    expect(wrapper.get('button[aria-label="删除当前会话"]').exists()).toBe(true)

    await openSelect(wrapper, '切换会话')
    expect(selectOptionTexts().join('\n')).toContain('海报会话')
    expect(selectOptionTexts().join('\n')).toContain('旧对话')
    await wrapper.get('button[aria-label="切换会话"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('尺寸')

    await openSelect(wrapper, '切换会话')
    const optionCountBefore = document.body.querySelectorAll('[role="option"]').length
    await wrapper.get('button[aria-label="切换会话"]').trigger('click')
    await wrapper.get('button[aria-label="新建会话"]').trigger('click')
    await flushPromises()
    await openSelect(wrapper, '切换会话')
    expect(document.body.querySelectorAll('[role="option"]')).toHaveLength(optionCountBefore + 1)
    await wrapper.get('button[aria-label="切换会话"]').trigger('click')

    await wrapper.get('button[aria-label="删除当前会话"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).not.toContain('旧消息')
    expect(deleteImageTaskSessionMock).toHaveBeenCalledTimes(1)
    expect(deleteImageTaskSessionMock).toHaveBeenCalledWith(expect.stringMatching(/^session-/))

    await openSelect(wrapper, '切换会话')
    const remainingSessions = selectOptionTexts().join('\n')
    expect(remainingSessions).not.toContain('创建新会话')
    expect(remainingSessions).toContain('海报会话')
    await wrapper.get('button[aria-label="切换会话"]').trigger('click')
  })

  it('删除生图会话时一并删除后端恢复态并移除本地 session', async () => {
    saveWebConsoleSessions([
      session({
        id: 'session-image',
        title: '海报会话',
        mode: 'image',
        messages: [{
          id: 'message-image',
          role: 'assistant',
          content: '已生成 1 张图片。',
          images: [],
          imageTaskId: 101,
          imageRequest: {
            prompt: '画一张海报',
            model: 'gpt-5.5',
            options: {
              size: '',
              quality: '',
              background: '',
              outputFormat: 'png',
              count: 1,
            },
          },
          status: 'completed',
          created_at: '2026-05-28T00:00:00.000Z',
        }],
        updated_at: '2026-05-28T00:01:00.000Z',
      }),
      session({ id: 'session-chat', title: '旧对话', mode: 'chat' }),
    ])

    const wrapper = mount(WebConsoleView)
    await flushPromises()

    await wrapper.get('button[aria-label="删除当前会话"]').trigger('click')
    await flushPromises()

    expect(deleteImageTaskSessionMock).toHaveBeenCalledWith('session-image')
    const stored = JSON.parse(localStorage.getItem('sub2api-web-console-sessions-v1') || '[]') as WebConsoleSession[]
    expect(stored.some((item) => item.id === 'session-image')).toBe(false)
    expect(stored.some((item) => item.id === 'session-chat')).toBe(true)
  })

  it('删除普通对话会话时只移除本地 session', async () => {
    saveWebConsoleSessions([
      session({ id: 'session-chat', title: '旧对话', mode: 'chat' }),
      session({ id: 'session-image', title: '海报会话', mode: 'image' }),
    ])

    const wrapper = mount(WebConsoleView)
    await flushPromises()

    await wrapper.get('button[aria-label="删除当前会话"]').trigger('click')
    await flushPromises()

    expect(deleteImageTaskSessionMock).not.toHaveBeenCalled()
    const stored = JSON.parse(localStorage.getItem('sub2api-web-console-sessions-v1') || '[]') as WebConsoleSession[]
    expect(stored.some((item) => item.id === 'session-chat')).toBe(false)
    expect(stored.some((item) => item.id === 'session-image')).toBe(true)
  })

  it('只展示 OpenAI-compatible 端点和 OpenAI 分组 API Key', async () => {
    appStore.cachedPublicSettings = {
      api_base_url: 'https://api.example.com',
      custom_endpoints: [
        {
          name: 'Gemini',
          endpoint: 'https://api.example.com/v1beta',
          description: '',
        },
        {
          name: 'OpenAI v1',
          endpoint: 'https://openai.example.com/v1',
          description: '',
        },
      ],
      web_console_default_endpoint: '',
    }
    keysListMock.mockResolvedValue({
      items: [
        {
          id: 1,
          name: 'OpenAI Key',
          key: 'sk-openai',
          status: 'active',
          quota: 10,
          quota_used: 0,
          group: {
            name: 'OpenAI 组',
            platform: 'openai',
            subscription_type: 'balance',
          },
        },
        {
          id: 2,
          name: 'Anthropic Key',
          key: 'sk-anthropic',
          status: 'active',
          quota: 10,
          quota_used: 0,
          group: {
            name: 'Anthropic 组',
            platform: 'anthropic',
            subscription_type: 'balance',
          },
        },
      ],
    })

    const wrapper = mount(WebConsoleView)
    await flushPromises()

    await openSelect(wrapper, 'API 端点')
    const endpointOptions = selectOptionTexts().join('\n')
    expect(endpointOptions).toContain('OpenAI v1')
    expect(endpointOptions).not.toContain('Gemini')
    await wrapper.get('button[aria-label="API 端点"]').trigger('click')

    const keySelect = wrapper.get('button[aria-label="API Key / 额度"]')
    expect(keySelect.text()).toContain('OpenAI Key')
    expect(keySelect.text()).not.toContain('Anthropic Key')

    await openSelect(wrapper, '模型')
    const modelOptions = selectOptionTexts()
    expect(modelOptions).toEqual(['gpt-5.5', 'gpt-5.4'])
    await wrapper.get('button[aria-label="模型"]').trigger('click')
  })

  it('没有 OpenAI 分组 API Key 时展示明确提示且不发起对话请求', async () => {
    appStore.cachedPublicSettings = {
      api_base_url: 'https://api.example.com',
      custom_endpoints: [],
      web_console_default_endpoint: '',
    }
    keysListMock.mockResolvedValue({
      items: [{
        id: 1,
        name: 'Anthropic Key',
        key: 'sk-anthropic',
        status: 'active',
        quota: 10,
        quota_used: 0,
        group: {
          name: 'Anthropic 组',
          platform: 'anthropic',
          subscription_type: 'balance',
        },
      }],
    })

    const wrapper = mount(WebConsoleView)
    await flushPromises()

    const keySelect = wrapper.get('button[aria-label="API Key / 额度"]')
    expect(keySelect.text()).toContain('当前端点无可用 API Key')
    expect(wrapper.text()).toContain('当前端点仅支持 OpenAI 分组的 API Key')
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeDefined()

    await wrapper.get('textarea').setValue('你好')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(wrapper.text()).toContain('当前端点仅支持 OpenAI 分组的 API Key')
    expect(sendWebConsoleChatMock).not.toHaveBeenCalled()
    expect(createImageTaskMock).not.toHaveBeenCalled()
  })

  it('OpenAI 在线对话提交时使用选中的 OpenAI key 和 endpoint', async () => {
    sendWebConsoleChatMock.mockResolvedValue({
      text: '你好，有什么可以帮你？',
      usedMode: 'responses',
    })

    const wrapper = mount(WebConsoleView)
    await flushPromises()

    await wrapper.get('textarea').setValue('你好')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(sendWebConsoleChatMock).toHaveBeenCalledTimes(1)
    expect(sendWebConsoleChatMock).toHaveBeenCalledWith(expect.objectContaining({
      endpoint: 'https://api.example.com',
      apiKey: 'sk-test',
      model: 'gpt-5.5',
      prompt: '你好',
      tools: [{ type: 'web_search' }, { type: 'image_generation' }],
      toolChoice: 'auto',
    }))
    expect(wrapper.text()).toContain('你好，有什么可以帮你？')
  })

  it('生图模式通过任务接口提交图片生成请求', async () => {
    createImageTaskMock.mockResolvedValue({
      task: {
        id: 101,
        status: 'completed',
        assets: [{ url: 'data:image/png;base64,ZmFrZQ==', asset_index: 0 }],
      },
    })
    getImageTaskMock.mockResolvedValue({
      id: 101,
      status: 'completed',
      assets: [{ url: 'data:image/png;base64,ZmFrZQ==', asset_index: 0 }],
    })

    const wrapper = mount(WebConsoleView)
    await flushPromises()

    const imageModeButton = wrapper.findAll('button').find((button) => button.text() === '生图')
    expect(imageModeButton).toBeTruthy()
    await imageModeButton!.trigger('click')
    await flushPromises()

    expect(wrapper.text()).not.toContain('Images 原生接口')
    expect(wrapper.text()).not.toContain('响应模式')
    await wrapper.get('textarea').setValue('画一只猫')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(createImageTaskMock).toHaveBeenCalledTimes(1)
    expect(createImageTaskMock).toHaveBeenCalledWith(expect.objectContaining({
      api_key_id: 1,
      endpoint: 'https://api.example.com',
      model: 'gpt-5.5',
      prompt: '画一只猫',
    }))
    expect(wrapper.find('a[title="打开图片"]').exists()).toBe(false)
  })

  it('生图完成后只拉取一次服务器资产并写入浏览器图片缓存', async () => {
    const cachePut = vi.fn()
    const cacheMatch = vi.fn().mockResolvedValue(null)
    vi.stubGlobal('caches', {
      open: vi.fn().mockResolvedValue({
        match: cacheMatch,
        put: cachePut,
      }),
    })
    vi.stubGlobal('fetch', vi.fn(async () => new Response(new Blob(['png'], { type: 'image/png' }), {
      status: 200,
      headers: { 'Content-Type': 'image/png' },
    })))
    const originalCreateObjectURL = URL.createObjectURL
    const originalRevokeObjectURL = URL.revokeObjectURL
    const createObjectURL = vi.fn(() => 'blob:web-console-image-1')
    const revokeObjectURL = vi.fn()
    Object.defineProperty(URL, 'createObjectURL', { value: createObjectURL, configurable: true })
    Object.defineProperty(URL, 'revokeObjectURL', { value: revokeObjectURL, configurable: true })
    localStorage.setItem('auth_token', 'user-token')
    createImageTaskMock.mockResolvedValue({
      task: {
        id: 104,
        status: 'pending',
        assets: [],
      },
    })
    getImageTaskMock.mockResolvedValue({
      id: 104,
      status: 'completed',
      assets: [{
        id: 7,
        record_id: 42,
        asset_index: 0,
        mime_type: 'image/png',
        extension: '.png',
        bytes: 3,
        sha256: 'hash',
        url: '/api/v1/web-console/image-tasks/assets/7',
      }],
    })

    const wrapper = mount(WebConsoleView)
    await flushPromises()

    await wrapper.findAll('button').find((button) => button.text() === '生图')!.trigger('click')
    await wrapper.get('textarea').setValue('画一张海报')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    await flushPromises()

    expect(fetch).toHaveBeenCalledWith('/api/v1/web-console/image-tasks/assets/7', {
      credentials: 'include',
      headers: { Authorization: 'Bearer user-token' },
    })
    expect(cachePut).toHaveBeenCalledWith('/__sub2api_web_console_image_cache__/7?v=hash', expect.any(Response))
    expect(wrapper.find('img').attributes('src')).toBe('blob:web-console-image-1')
    const stored = JSON.parse(localStorage.getItem('sub2api-web-console-sessions-v1') || '[]') as WebConsoleSession[]
    const assistant = stored[0].messages.find((message) => message.role === 'assistant')
    expect(assistant?.images?.[0]).toEqual(expect.objectContaining({
      url: '',
      cacheKey: '/__sub2api_web_console_image_cache__/7?v=hash',
      assetId: 7,
    }))
    expect(localStorage.getItem('sub2api-web-console-sessions-v1')).not.toContain('/api/v1/web-console/image-tasks/assets/7')

    wrapper.unmount()
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:web-console-image-1')
    Object.defineProperty(URL, 'createObjectURL', { value: originalCreateObjectURL, configurable: true })
    Object.defineProperty(URL, 'revokeObjectURL', { value: originalRevokeObjectURL, configurable: true })
  })

  it('重新进入创作台时从浏览器图片缓存恢复本地图片', async () => {
    const cachedResponse = () => new Response(new Blob(['cached-png'], { type: 'image/png' }), {
      status: 200,
      headers: { 'Content-Type': 'image/png' },
    })
    const cacheMatch = vi.fn().mockImplementation(() => Promise.resolve(cachedResponse()))
    vi.stubGlobal('caches', {
      open: vi.fn().mockResolvedValue({
        match: cacheMatch,
        put: vi.fn(),
      }),
    })
    vi.stubGlobal('fetch', vi.fn())
    const originalCreateObjectURL = URL.createObjectURL
    const originalRevokeObjectURL = URL.revokeObjectURL
    const createObjectURL = vi.fn(() => 'blob:web-console-cached-image')
    const revokeObjectURL = vi.fn()
    Object.defineProperty(URL, 'createObjectURL', { value: createObjectURL, configurable: true })
    Object.defineProperty(URL, 'revokeObjectURL', { value: revokeObjectURL, configurable: true })
    saveWebConsoleSessions([
      session({
        id: 'session-image',
        title: '海报会话',
        mode: 'image',
        messages: [{
          id: 'message-image',
          role: 'assistant',
          content: '已生成 1 张图片。',
          created_at: '2026-05-28T00:00:00.000Z',
          status: 'completed',
          imageRequest: {
            prompt: '画一张海报',
            model: 'gpt-5.5',
            options: {
              size: '',
              quality: '',
              background: '',
              outputFormat: 'png',
              count: 1,
            },
          },
          images: [{
            url: '',
            alt: '画一张海报',
            assetId: 7,
            cacheKey: '/__sub2api_web_console_image_cache__/7?v=hash',
            sha256: 'hash',
            mimeType: 'image/png',
            extension: '.png',
          }],
        }],
      }),
    ])

    const wrapper = mount(WebConsoleView)
    await flushPromises()
    await flushPromises()

    expect(cacheMatch).toHaveBeenCalledWith('/__sub2api_web_console_image_cache__/7?v=hash')
    expect(fetch).not.toHaveBeenCalled()
    expect(getImageTaskMock).not.toHaveBeenCalled()
    expect(wrapper.find('img').attributes('src')).toBe('blob:web-console-cached-image')
    expect(wrapper.text()).toContain('已生成 1 张图片。')

    wrapper.unmount()
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:web-console-cached-image')
    Object.defineProperty(URL, 'createObjectURL', { value: originalCreateObjectURL, configurable: true })
    Object.defineProperty(URL, 'revokeObjectURL', { value: originalRevokeObjectURL, configurable: true })
  })

  it('进入创作台时只恢复当前会话图片且切回不重复创建 blob URL', async () => {
    const cachedResponse = () => new Response(new Blob(['cached-png'], { type: 'image/png' }), {
      status: 200,
      headers: { 'Content-Type': 'image/png' },
    })
    const cacheMatch = vi.fn().mockImplementation((key: string) => Promise.resolve(
      key.includes('/7?') ? cachedResponse() : null
    ))
    vi.stubGlobal('caches', {
      open: vi.fn().mockResolvedValue({
        match: cacheMatch,
        put: vi.fn(),
      }),
    })
    const originalCreateObjectURL = URL.createObjectURL
    const originalRevokeObjectURL = URL.revokeObjectURL
    const createObjectURL = vi.fn(() => 'blob:web-console-current-image')
    const revokeObjectURL = vi.fn()
    Object.defineProperty(URL, 'createObjectURL', { value: createObjectURL, configurable: true })
    Object.defineProperty(URL, 'revokeObjectURL', { value: revokeObjectURL, configurable: true })
    saveWebConsoleSessions([
      session({
        id: 'session-current',
        title: '当前海报',
        mode: 'image',
        updated_at: '2026-05-28T00:02:00.000Z',
        messages: [{
          id: 'message-current',
          role: 'assistant',
          content: '已生成 1 张图片。',
          created_at: '2026-05-28T00:00:00.000Z',
          status: 'completed',
          imageRequest: {
            prompt: '当前图片',
            model: 'gpt-5.5',
            options: {
              size: '',
              quality: '',
              background: '',
              outputFormat: 'png',
              count: 1,
            },
          },
          images: [{
            url: '',
            alt: '当前图片',
            assetId: 7,
            cacheKey: '/__sub2api_web_console_image_cache__/7?v=hash',
          }],
        }],
      }),
      session({
        id: 'session-old',
        title: '旧海报',
        mode: 'image',
        updated_at: '2026-05-28T00:01:00.000Z',
        messages: [{
          id: 'message-old',
          role: 'assistant',
          content: '已生成 1 张图片。',
          created_at: '2026-05-28T00:00:00.000Z',
          status: 'completed',
          imageRequest: {
            prompt: '旧图片',
            model: 'gpt-5.5',
            options: {
              size: '',
              quality: '',
              background: '',
              outputFormat: 'png',
              count: 1,
            },
          },
          images: [{
            url: '',
            alt: '旧图片',
            assetId: 8,
            cacheKey: '/__sub2api_web_console_image_cache__/8?v=hash',
          }],
        }],
      }),
    ])

    const wrapper = mount(WebConsoleView)
    await flushPromises()
    await flushPromises()

    expect(cacheMatch).toHaveBeenCalledWith('/__sub2api_web_console_image_cache__/7?v=hash')
    expect(cacheMatch).not.toHaveBeenCalledWith('/__sub2api_web_console_image_cache__/8?v=hash')
    expect(createObjectURL).toHaveBeenCalledTimes(1)
    expect(wrapper.find('img').attributes('src')).toBe('blob:web-console-current-image')

    await openSelect(wrapper, '切换会话')
    Array.from(document.body.querySelectorAll('[role="option"]'))
      .find((option) => option.textContent?.trim() === '旧海报')
      ?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await flushPromises()

    await openSelect(wrapper, '切换会话')
    Array.from(document.body.querySelectorAll('[role="option"]'))
      .find((option) => option.textContent?.trim() === '当前海报')
      ?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await flushPromises()
    await flushPromises()

    expect(createObjectURL).toHaveBeenCalledTimes(1)

    wrapper.unmount()
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:web-console-current-image')
    Object.defineProperty(URL, 'createObjectURL', { value: originalCreateObjectURL, configurable: true })
    Object.defineProperty(URL, 'revokeObjectURL', { value: originalRevokeObjectURL, configurable: true })
  })

  it('浏览器图片缓存写入失败时仍展示当前生成结果', async () => {
    const cachePut = vi.fn().mockRejectedValue(new Error('quota exceeded'))
    vi.stubGlobal('caches', {
      open: vi.fn().mockResolvedValue({
        match: vi.fn().mockResolvedValue(null),
        put: cachePut,
      }),
    })
    vi.stubGlobal('fetch', vi.fn(async () => new Response(new Blob(['png'], { type: 'image/png' }), {
      status: 200,
      headers: { 'Content-Type': 'image/png' },
    })))
    const originalCreateObjectURL = URL.createObjectURL
    const originalRevokeObjectURL = URL.revokeObjectURL
    Object.defineProperty(URL, 'createObjectURL', { value: vi.fn(() => 'blob:web-console-image-fail-open'), configurable: true })
    Object.defineProperty(URL, 'revokeObjectURL', { value: vi.fn(), configurable: true })
    localStorage.setItem('auth_token', 'user-token')
    createImageTaskMock.mockResolvedValue({
      task: {
        id: 106,
        status: 'pending',
        assets: [],
      },
    })
    getImageTaskMock.mockResolvedValue({
      id: 106,
      status: 'completed',
      assets: [{
        id: 8,
        record_id: 43,
        asset_index: 0,
        mime_type: 'image/png',
        extension: '.png',
        bytes: 3,
        sha256: 'hash-2',
        url: '/api/v1/web-console/image-tasks/assets/8',
      }],
    })

    const wrapper = mount(WebConsoleView)
    await flushPromises()

    await wrapper.findAll('button').find((button) => button.text() === '生图')!.trigger('click')
    await wrapper.get('textarea').setValue('画一张海报')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    await flushPromises()

    expect(cachePut).toHaveBeenCalled()
    expect(wrapper.find('img').attributes('src')).toBe('blob:web-console-image-fail-open')
    expect(wrapper.text()).toContain('已生成 1 张图片。')
    const stored = JSON.parse(localStorage.getItem('sub2api-web-console-sessions-v1') || '[]') as WebConsoleSession[]
    const assistant = stored[0].messages.find((message) => message.role === 'assistant')
    expect(assistant?.images?.[0]).toEqual(expect.objectContaining({
      url: '',
      cacheKey: '/__sub2api_web_console_image_cache__/8?v=hash-2',
      unavailable: false,
    }))

    wrapper.unmount()
    Object.defineProperty(URL, 'createObjectURL', { value: originalCreateObjectURL, configurable: true })
    Object.defineProperty(URL, 'revokeObjectURL', { value: originalRevokeObjectURL, configurable: true })
  })

  it('编辑模式上传参考图和蒙版后通过 Responses 任务接口提交 reference_images 与 mask_image', async () => {
    const cachePut = vi.fn()
    vi.stubGlobal('caches', {
      open: vi.fn().mockResolvedValue({
        match: vi.fn().mockResolvedValue(null),
        put: cachePut,
      }),
    })
    createImageTaskMock.mockResolvedValue({
      task: {
        id: 101,
        status: 'completed',
        assets: [{ url: 'data:image/png;base64,ZmFrZQ==', asset_index: 0 }],
      },
    })
    getImageTaskMock.mockResolvedValue({
      id: 101,
      status: 'completed',
      assets: [{ url: 'data:image/png;base64,ZmFrZQ==', asset_index: 0 }],
    })

    const wrapper = mount(WebConsoleView)
    await flushPromises()

    await wrapper.findAll('button').find((button) => button.text() === '生图')!.trigger('click')
    await wrapper.findAll('button').find((button) => button.text() === '编辑')!.trigger('click')
    const fileInputs = wrapper.findAll('input[type="file"]')
    const sourceFile = new File(['source-image'], 'source.png', { type: 'image/png' })
    Object.defineProperty(fileInputs[0].element, 'files', { value: [sourceFile], configurable: true })
    await fileInputs[0].trigger('change')
    await new Promise((resolve) => setTimeout(resolve, 0))
    await flushPromises()

    const firstMaskFile = new File(['mask-image-old'], 'mask-old.png', { type: 'image/png' })
    Object.defineProperty(fileInputs[1].element, 'files', { value: [firstMaskFile], configurable: true })
    await fileInputs[1].trigger('change')
    await new Promise((resolve) => setTimeout(resolve, 0))
    await flushPromises()
    expect(wrapper.text()).toContain('mask-old.png')

    await wrapper.get('button[title="移除蒙版"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).not.toContain('mask-old.png')

    const maskFile = new File(['mask-image'], 'mask.png', { type: 'image/png' })
    Object.defineProperty(fileInputs[1].element, 'files', { value: [maskFile], configurable: true })
    await fileInputs[1].trigger('change')
    await new Promise((resolve) => setTimeout(resolve, 0))
    await flushPromises()

    await wrapper.get('textarea').setValue('把背景换成海边')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(createImageTaskMock).toHaveBeenCalledWith(expect.objectContaining({
      mode: 'edit',
      prompt: '把背景换成海边',
      reference_images: [expect.objectContaining({
        name: 'source.png',
        data_url: expect.stringMatching(/^data:image\/png;base64,/),
      })],
      mask_image: expect.objectContaining({
        name: 'mask.png',
        data_url: expect.stringMatching(/^data:image\/png;base64,/),
      }),
      options: expect.objectContaining({
        outputFormat: 'png',
        outputCompression: null,
      }),
    }))
    const saved = loadWebConsoleSessions()
    const savedAssistant = saved[0].messages.find((message) => message.role === 'assistant')
    expect(savedAssistant?.imageRequest?.mode).toBe('edit')
    expect(savedAssistant?.imageRequest?.referenceImages).toEqual([expect.objectContaining({
      name: 'source.png',
      data_url: '',
      cacheKey: expect.stringContaining('/edit-references/'),
    })])
    expect(savedAssistant?.imageRequest?.maskImage).toEqual(expect.objectContaining({
      name: 'mask.png',
      data_url: '',
      cacheKey: expect.stringContaining('/edit-references/'),
    }))
    expect(cachePut).toHaveBeenCalledWith(expect.stringContaining('/edit-references/'), expect.any(Response))
    expect(localStorage.getItem('sub2api-web-console-sessions-v1')).not.toContain('c291cmNlLWltYWdl')
    expect(localStorage.getItem('sub2api-web-console-sessions-v1')).not.toContain('bWFzay1pbWFnZQ')
  })

  it('刷新后可从浏览器缓存恢复编辑参考图并重新生成', async () => {
    vi.stubGlobal('caches', {
      open: vi.fn().mockResolvedValue({
        match: vi.fn(async (cacheKey: string) => {
          if (cacheKey.includes('/reference/')) {
            return new Response('source-image', { headers: { 'Content-Type': 'image/png' } })
          }
          if (cacheKey.includes('/mask/')) {
            return new Response('mask-image', { headers: { 'Content-Type': 'image/png' } })
          }
          return null
        }),
        put: vi.fn(),
      }),
    })
    saveWebConsoleSessions([
      session({
        id: 'session-image',
        title: '海报会话',
        mode: 'image',
        messages: [{
          id: 'message-refresh-retry',
          role: 'assistant',
          content: '生图失败：上游临时不可用',
          images: [],
          imageRequest: {
            prompt: '把背景换成海边',
            mode: 'edit',
            model: 'gpt-5.5',
            options: {
              size: '',
              quality: '',
              background: '',
              outputFormat: 'png',
              count: 1,
            },
            referenceImages: [{
              name: 'source.png',
              data_url: '',
              cacheKey: '/__sub2api_web_console_image_cache__/edit-references/session-image/message-refresh-retry/reference/0?name=source.png',
            }],
            maskImage: {
              name: 'mask.png',
              data_url: '',
              cacheKey: '/__sub2api_web_console_image_cache__/edit-references/session-image/message-refresh-retry/mask/0?name=mask.png',
            },
          },
          status: 'failed',
          created_at: '2026-05-28T00:00:00.000Z',
        }],
      }),
    ])
    createImageTaskMock.mockResolvedValue({
      task: {
        id: 109,
        status: 'completed',
        assets: [{ url: 'data:image/png;base64,ZmFrZS0z', asset_index: 0 }],
      },
    })
    getImageTaskMock.mockResolvedValue({
      id: 109,
      status: 'completed',
      assets: [{ url: 'data:image/png;base64,ZmFrZS0z', asset_index: 0 }],
    })

    const wrapper = mount(WebConsoleView)
    await flushPromises()

    await wrapper.findAll('button').find((button) => button.text().includes('重新生成'))!.trigger('click')
    await new Promise((resolve) => setTimeout(resolve, 0))
    await flushPromises()
    await flushPromises()

    await vi.waitFor(() => {
      expect(createImageTaskMock).toHaveBeenCalledWith(expect.objectContaining({
        mode: 'edit',
        prompt: '把背景换成海边',
        reference_images: [expect.objectContaining({
          name: 'source.png',
          data_url: 'data:image/png;base64,c291cmNlLWltYWdl',
        })],
        mask_image: expect.objectContaining({
          name: 'mask.png',
          data_url: 'data:image/png;base64,bWFzay1pbWFnZQ==',
        }),
      }))
    })
    expect(localStorage.getItem('sub2api-web-console-sessions-v1')).not.toContain('c291cmNlLWltYWdl')
    expect(localStorage.getItem('sub2api-web-console-sessions-v1')).not.toContain('bWFzay1pbWFnZQ')
  })

  it('刷新后编辑参考图缓存失效时不创建重新生成任务', async () => {
    vi.stubGlobal('caches', {
      open: vi.fn().mockResolvedValue({
        match: vi.fn().mockResolvedValue(null),
        put: vi.fn(),
      }),
    })
    saveWebConsoleSessions([
      session({
        id: 'session-image',
        title: '海报会话',
        mode: 'image',
        messages: [{
          id: 'message-cache-miss',
          role: 'assistant',
          content: '生图失败：上游临时不可用',
          images: [],
          imageRequest: {
            prompt: '把背景换成海边',
            mode: 'edit',
            model: 'gpt-5.5',
            options: {
              size: '',
              quality: '',
              background: '',
              outputFormat: 'png',
              count: 1,
            },
            referenceImages: [{
              name: 'source.png',
              data_url: '',
              cacheKey: '/__sub2api_web_console_image_cache__/edit-references/session-image/message-cache-miss/reference/0?name=source.png',
            }],
            maskImage: null,
          },
          status: 'failed',
          created_at: '2026-05-28T00:00:00.000Z',
        }],
      }),
    ])

    const wrapper = mount(WebConsoleView)
    await flushPromises()

    await wrapper.findAll('button').find((button) => button.text().includes('重新生成'))!.trigger('click')
    await new Promise((resolve) => setTimeout(resolve, 0))
    await flushPromises()

    expect(createImageTaskMock).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('该编辑请求的参考图缓存已失效，请重新添加参考图后再编辑。')
    expect(wrapper.text()).toContain('生图失败：上游临时不可用')
  })

  it('编辑模式没有参考图时阻止提交', async () => {
    const wrapper = mount(WebConsoleView)
    await flushPromises()

    await wrapper.findAll('button').find((button) => button.text() === '生图')!.trigger('click')
    await wrapper.findAll('button').find((button) => button.text() === '编辑')!.trigger('click')
    await wrapper.get('textarea').setValue('只上传蒙版不应提交')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(wrapper.text()).toContain('编辑模式需要至少添加一张参考图。')
    expect(createImageTaskMock).not.toHaveBeenCalled()
  })

  it('生图任务失败时展示上游失败提示', async () => {
    createImageTaskMock.mockResolvedValue({
      task: {
        id: 103,
        status: 'pending',
        assets: [],
      },
    })
    getImageTaskMock.mockResolvedValue({
      id: 103,
      status: 'failed',
      assets: [],
      error_message: '上游生图服务暂时不可用：upstream_error: policy rejected',
    })

    const wrapper = mount(WebConsoleView)
    await flushPromises()

    await wrapper.findAll('button').find((button) => button.text() === '生图')!.trigger('click')
    await wrapper.get('textarea').setValue('画一张海报')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(wrapper.text()).toContain('生图失败：上游生图服务暂时不可用：upstream_error: policy rejected')
  })

  it('本地失败但后端已归档完成时会恢复图片结果', async () => {
    saveWebConsoleSessions([
      session({
        id: 'session-image',
        title: '海报会话',
        mode: 'image',
        messages: [{
          id: 'message-image',
          role: 'assistant',
          content: '生图失败：临时拉取失败',
          images: [],
          imageTaskId: 105,
          imageRequest: {
            prompt: '画一张海报',
            model: 'gpt-5.5',
            options: {
              size: '',
              quality: '',
              background: '',
              outputFormat: 'png',
              count: 1,
            },
          },
          status: 'failed',
          created_at: '2026-05-28T00:00:00.000Z',
        }],
      }),
    ])
    getImageTaskMock.mockResolvedValue({
      id: 105,
      status: 'completed',
      assets: [{ url: 'data:image/png;base64,ZmFrZQ==', asset_index: 0 }],
    })

    const wrapper = mount(WebConsoleView)
    await flushPromises()
    await flushPromises()

    expect(getImageTaskMock).toHaveBeenCalledWith(105)
    expect(wrapper.text()).toContain('已生成 1 张图片。')
    expect(wrapper.find('img').attributes('src')).toBe('data:image/png;base64,ZmFrZQ==')
  })

  it('浏览器图片缓存丢失时会通过后端任务重新拉取归档资产', async () => {
    const cachePut = vi.fn()
    vi.stubGlobal('caches', {
      open: vi.fn().mockResolvedValue({
        match: vi.fn().mockResolvedValue(null),
        put: cachePut,
      }),
    })
    vi.stubGlobal('fetch', vi.fn(async () => new Response(new Blob(['png'], { type: 'image/png' }), {
      status: 200,
      headers: { 'Content-Type': 'image/png' },
    })))
    const originalCreateObjectURL = URL.createObjectURL
    const originalRevokeObjectURL = URL.revokeObjectURL
    Object.defineProperty(URL, 'createObjectURL', { value: vi.fn(() => 'blob:web-console-image-restored'), configurable: true })
    Object.defineProperty(URL, 'revokeObjectURL', { value: vi.fn(), configurable: true })
    localStorage.setItem('auth_token', 'user-token')
    saveWebConsoleSessions([
      session({
        id: 'session-image',
        title: '海报会话',
        mode: 'image',
        messages: [{
          id: 'message-image',
          role: 'assistant',
          content: '已生成 1 张图片。',
          images: [{
            url: '',
            cacheKey: '/__sub2api_web_console_image_cache__/9?v=old-hash',
            assetId: 9,
            sha256: 'old-hash',
            mimeType: 'image/png',
            extension: '.png',
            unavailable: false,
          }],
          imageTaskId: 107,
          imageRequest: {
            prompt: '画一张海报',
            model: 'gpt-5.5',
            options: {
              size: '',
              quality: '',
              background: '',
              outputFormat: 'png',
              count: 1,
            },
          },
          status: 'completed',
          created_at: '2026-05-28T00:00:00.000Z',
        }],
      }),
    ])
    getImageTaskMock.mockResolvedValue({
      id: 107,
      status: 'completed',
      assets: [{
        id: 9,
        record_id: 44,
        asset_index: 0,
        mime_type: 'image/png',
        extension: '.png',
        bytes: 3,
        sha256: 'new-hash',
        url: '/api/v1/web-console/image-tasks/assets/9',
      }],
    })

    const wrapper = mount(WebConsoleView)
    await flushPromises()
    await flushPromises()

    expect(getImageTaskMock).toHaveBeenCalledWith(107)
    expect(fetch).toHaveBeenCalledWith('/api/v1/web-console/image-tasks/assets/9', {
      credentials: 'include',
      headers: { Authorization: 'Bearer user-token' },
    })
    expect(cachePut).toHaveBeenCalledWith('/__sub2api_web_console_image_cache__/9?v=new-hash', expect.any(Response))
    expect(wrapper.find('img').attributes('src')).toBe('blob:web-console-image-restored')
    expect(wrapper.text()).not.toContain('本地图片缓存不可用')

    wrapper.unmount()
    Object.defineProperty(URL, 'createObjectURL', { value: originalCreateObjectURL, configurable: true })
    Object.defineProperty(URL, 'revokeObjectURL', { value: originalRevokeObjectURL, configurable: true })
  })

  it('旧版签名图片 URL 失效后会通过后端任务恢复归档资产', async () => {
    const cachePut = vi.fn()
    vi.stubGlobal('caches', {
      open: vi.fn().mockResolvedValue({
        match: vi.fn().mockResolvedValue(null),
        put: cachePut,
      }),
    })
    vi.stubGlobal('fetch', vi.fn(async () => new Response(new Blob(['png'], { type: 'image/png' }), {
      status: 200,
      headers: { 'Content-Type': 'image/png' },
    })))
    const originalCreateObjectURL = URL.createObjectURL
    const originalRevokeObjectURL = URL.revokeObjectURL
    Object.defineProperty(URL, 'createObjectURL', { value: vi.fn(() => 'blob:web-console-image-legacy-restored'), configurable: true })
    Object.defineProperty(URL, 'revokeObjectURL', { value: vi.fn(), configurable: true })
    localStorage.setItem('auth_token', 'user-token')
    saveWebConsoleSessions([
      session({
        id: 'session-image',
        title: '海报会话',
        mode: 'image',
        messages: [{
          id: 'message-image',
          role: 'assistant',
          content: '已生成 1 张图片。',
          images: [{
            url: '/api/v1/image-assets/9?v=old-hash&expires=1800000000&scope=web-console-image-task&sig=old',
            alt: '画一张海报',
          }],
          imageTaskId: 108,
          imageRequest: {
            prompt: '画一张海报',
            model: 'gpt-5.5',
            options: {
              size: '',
              quality: '',
              background: '',
              outputFormat: 'png',
              count: 1,
            },
          },
          status: 'completed',
          created_at: '2026-05-28T00:00:00.000Z',
        }],
      }),
    ])
    getImageTaskMock.mockResolvedValue({
      id: 108,
      status: 'completed',
      assets: [{
        id: 9,
        record_id: 44,
        asset_index: 0,
        mime_type: 'image/png',
        extension: '.png',
        bytes: 3,
        sha256: 'new-hash',
        url: '/api/v1/web-console/image-tasks/assets/9',
      }],
    })

    const wrapper = mount(WebConsoleView)
    await flushPromises()
    await flushPromises()

    expect(getImageTaskMock).toHaveBeenCalledWith(108)
    expect(fetch).toHaveBeenCalledWith('/api/v1/web-console/image-tasks/assets/9', {
      credentials: 'include',
      headers: { Authorization: 'Bearer user-token' },
    })
    expect(cachePut).toHaveBeenCalledWith('/__sub2api_web_console_image_cache__/9?v=new-hash', expect.any(Response))
    expect(wrapper.find('img').attributes('src')).toBe('blob:web-console-image-legacy-restored')
    expect(localStorage.getItem('sub2api-web-console-sessions-v1')).not.toContain('/api/v1/image-assets/9')

    wrapper.unmount()
    Object.defineProperty(URL, 'createObjectURL', { value: originalCreateObjectURL, configurable: true })
    Object.defineProperty(URL, 'revokeObjectURL', { value: originalRevokeObjectURL, configurable: true })
  })

  it('生成结果可以作为下一次编辑的参考图', async () => {
    saveWebConsoleSessions([
      session({
        id: 'session-image',
        title: '海报会话',
        mode: 'image',
        messages: [{
          id: 'message-image',
          role: 'assistant',
          content: '已生成 1 张图片。',
          images: [{ url: 'data:image/png;base64,ZmFrZS1pbWFnZQ==', alt: '旧图' }],
          imageRequest: {
            prompt: '旧图',
            model: 'gpt-5.5',
            options: {
              size: '',
              quality: '',
              background: '',
              outputFormat: 'png',
              count: 1,
            },
          },
          status: 'completed',
          created_at: '2026-05-28T00:00:00.000Z',
        }],
      }),
    ])
    createImageTaskMock.mockResolvedValue({
      task: {
        id: 102,
        status: 'completed',
        assets: [{ url: 'data:image/png;base64,ZmFrZS0y', asset_index: 0 }],
      },
    })
    getImageTaskMock.mockResolvedValue({
      id: 102,
      status: 'completed',
      assets: [{ url: 'data:image/png;base64,ZmFrZS0y', asset_index: 0 }],
    })

    const wrapper = mount(WebConsoleView)
    await flushPromises()

    await wrapper.get('button[title="用作参考图"]').trigger('click')
    await flushPromises()
    await wrapper.get('textarea').setValue('改成夜景')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(createImageTaskMock).toHaveBeenCalledWith(expect.objectContaining({
      mode: 'edit',
      prompt: '改成夜景',
      reference_images: [expect.objectContaining({
        data_url: 'data:image/png;base64,ZmFrZS1pbWFnZQ==',
      })],
    }))
  })

  it('生图任务完成前只展示居中动画且不展示重新生成按钮', async () => {
    saveWebConsoleSessions([
      session({
        id: 'session-image',
        title: '海报会话',
        mode: 'image',
        messages: [{
          id: 'message-image',
          role: 'assistant',
          content: '生图任务已提交，正在生成图片。',
          images: [],
          imageRequest: {
            prompt: '画一只猫',
            model: 'gpt-5.5',
            options: {
              size: '',
              quality: '',
              background: '',
              outputFormat: 'png',
              count: 1,
            },
          },
          status: 'running',
          created_at: '2026-05-28T00:00:00.000Z',
        }],
      }),
    ])

    const wrapper = mount(WebConsoleView)
    await flushPromises()

    expect(wrapper.get('[role="status"]').text()).toContain('生图任务已提交，正在生成图片。')
    expect(wrapper.findAll('button').some((button) => button.text().includes('重新生成'))).toBe(false)
    expect(createImageTaskMock).not.toHaveBeenCalled()
  })

  it('对话模式不暴露调试选项并默认启用 tools', async () => {
    sendWebConsoleChatMock.mockResolvedValue({
      text: '已联网搜索。',
      usedMode: 'responses',
    })

    const wrapper = mount(WebConsoleView)
    await flushPromises()

    expect(wrapper.text()).not.toContain('响应模式')
    expect(wrapper.text()).not.toContain('tool_choice')
    expect(wrapper.text()).not.toContain('不使用工具')
    expect(wrapper.text()).not.toContain('Web Search')
    expect(wrapper.text()).not.toContain('Imagegen')

    await wrapper.get('textarea').setValue('查一下最新消息')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(sendWebConsoleChatMock).toHaveBeenCalledWith(expect.objectContaining({
      tools: [{ type: 'web_search' }, { type: 'image_generation' }],
      toolChoice: 'auto',
    }))
    expect(wrapper.text()).toContain('已联网搜索。')
  })
})
