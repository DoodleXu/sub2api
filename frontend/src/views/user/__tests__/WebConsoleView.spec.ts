import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import WebConsoleView from '../WebConsoleView.vue'
import { saveWebConsoleSessions } from '@/features/web-console/storage'
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
    expect(remainingSessions).not.toContain('新生图会话')
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
