import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import WebConsoleView from '../WebConsoleView.vue'
import { saveWebConsoleSessions } from '@/features/web-console/storage'
import type { WebConsoleSession } from '@/features/web-console/types'

const keysListMock = vi.hoisted(() => vi.fn())
const fetchPublicSettingsMock = vi.hoisted(() => vi.fn())
const sendWebConsoleChatMock = vi.hoisted(() => vi.fn())
const generateWebConsoleImageMock = vi.hoisted(() => vi.fn())
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
  generateWebConsoleImage: generateWebConsoleImageMock,
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
    generateWebConsoleImageMock.mockReset()
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

  it('移动端提供会话切换、新建和清空入口', async () => {
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

    const sessionSelect = wrapper.get('select[aria-label="切换会话"]')
    expect(sessionSelect.text()).toContain('海报会话')
    expect(sessionSelect.text()).toContain('旧对话')
    expect(wrapper.get('button[aria-label="新建会话"]').exists()).toBe(true)
    expect(wrapper.get('button[aria-label="清空当前会话"]').exists()).toBe(true)

    await sessionSelect.setValue('session-image')
    await flushPromises()
    expect(wrapper.text()).toContain('尺寸')

    const optionCountBefore = wrapper.findAll('select[aria-label="切换会话"] option').length
    await wrapper.get('button[aria-label="新建会话"]').trigger('click')
    await flushPromises()
    expect(wrapper.findAll('select[aria-label="切换会话"] option')).toHaveLength(optionCountBefore + 1)

    await wrapper.get('button[aria-label="清空当前会话"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).not.toContain('旧消息')
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

    const keySelect = wrapper.get('select[aria-label="API Key / 额度"]')
    const endpointSelect = wrapper.get('select[aria-label="API 端点"]')

    expect(endpointSelect.text()).toContain('OpenAI v1')
    expect(endpointSelect.text()).not.toContain('Gemini')
    expect((keySelect.element as HTMLSelectElement).value).toBe('1')
    expect(keySelect.text()).toContain('OpenAI Key')
    expect(keySelect.text()).not.toContain('Anthropic Key')
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

    const keySelect = wrapper.get('select[aria-label="API Key / 额度"]')
    expect((keySelect.element as HTMLSelectElement).value).toBe('0')
    expect(wrapper.text()).toContain('当前端点仅支持 OpenAI 分组的 API Key')
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeDefined()

    await wrapper.get('textarea').setValue('你好')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(wrapper.text()).toContain('当前端点仅支持 OpenAI 分组的 API Key')
    expect(sendWebConsoleChatMock).not.toHaveBeenCalled()
    expect(generateWebConsoleImageMock).not.toHaveBeenCalled()
  })

  it('OpenAI 在线对话提交时使用选中的 OpenAI key 和 endpoint', async () => {
    sendWebConsoleChatMock.mockResolvedValue({
      text: '你好，有什么可以帮你？',
      usedMode: 'responses',
      fallbackUsed: false,
    })

    const wrapper = mount(WebConsoleView)
    await flushPromises()

    await wrapper.get('textarea').setValue('你好')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(sendWebConsoleChatMock).toHaveBeenCalledTimes(1)
    expect(sendWebConsoleChatMock).toHaveBeenCalledWith(
      expect.objectContaining({
        endpoint: 'https://api.example.com',
        apiKey: 'sk-test',
        model: 'gpt-5.4',
        prompt: '你好',
      }),
      'auto',
    )
    expect(wrapper.text()).toContain('你好，有什么可以帮你？')
  })
})
