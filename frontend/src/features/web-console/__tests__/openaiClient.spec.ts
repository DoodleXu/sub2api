import { beforeEach, describe, expect, it, vi } from 'vitest'
import { generateWebConsoleImage, sendWebConsoleChat } from '../openaiClient'

function jsonResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  })
}

describe('web console openai client', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
  })

  it('OpenAI 在线对话优先请求 /v1/responses 并使用 Responses 输入格式', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({
      output_text: '你好，有什么可以帮你？',
    }))
    vi.stubGlobal('fetch', fetchMock)

    const result = await sendWebConsoleChat({
      endpoint: 'https://api.example.com',
      apiKey: 'sk-test',
      model: 'gpt-5.4',
      prompt: '你好',
      history: [{
        id: 'message-1',
        role: 'assistant',
        content: '上一轮回复',
        created_at: '2026-05-29T00:00:00.000Z',
      }],
    })

    expect(result.text).toBe('你好，有什么可以帮你？')
    expect(result.usedMode).toBe('responses')
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://api.example.com/v1/responses')
    expect(init.headers).toEqual(expect.objectContaining({
      Authorization: 'Bearer sk-test',
      'Content-Type': 'application/json',
    }))
    expect(JSON.parse(String(init.body))).toEqual({
      model: 'gpt-5.4',
      input: [
        {
          role: 'assistant',
          content: [{ type: 'output_text', text: '上一轮回复' }],
        },
        {
          role: 'user',
          content: [{ type: 'input_text', text: '你好' }],
        },
      ],
    })
  })

  it('OpenAI 在线对话不会把 Gemini native 端点拼成 /v1beta/v1/responses', async () => {
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)

    await expect(sendWebConsoleChat({
      endpoint: 'https://api.example.com/v1beta',
      apiKey: 'sk-test',
      model: 'gpt-5.4',
      prompt: '你好',
      history: [],
    })).rejects.toThrow('网页工作台当前只支持 OpenAI-compatible /v1 端点')

    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('对话带 tools 时强制使用 Responses 并透传 tool_choice', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({
      output_text: '已搜索到结果',
    }))
    vi.stubGlobal('fetch', fetchMock)

    const result = await sendWebConsoleChat({
      endpoint: 'https://api.example.com',
      apiKey: 'sk-test',
      model: 'gpt-5.4',
      prompt: '查一下今天的新闻',
      history: [],
      tools: [{ type: 'web_search' }],
      toolChoice: 'required',
    })

    expect(result.text).toBe('已搜索到结果')
    expect(result.usedMode).toBe('responses')
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://api.example.com/v1/responses')
    expect(JSON.parse(String(init.body))).toEqual({
      model: 'gpt-5.4',
      input: [
        {
          role: 'user',
          content: [{ type: 'input_text', text: '查一下今天的新闻' }],
        },
      ],
      tools: [{ type: 'web_search' }],
      tool_choice: 'required',
    })
  })

  it('对话模式能收集 Responses tools 返回的图片', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({
      output: [{ type: 'image_generation_call', result: 'ZmFrZS1pbWFnZQ==' }],
    }))
    vi.stubGlobal('fetch', fetchMock)

    const result = await sendWebConsoleChat({
      endpoint: 'https://api.example.com',
      apiKey: 'sk-test',
      model: 'gpt-5.4',
      prompt: '画一只猫',
      history: [],
      tools: [{ type: 'image_generation' }],
    })

    expect(result.text).toBe('已生成 1 张图片。')
    expect(result.images).toEqual([{
      url: 'data:image/png;base64,ZmFrZS1pbWFnZQ==',
      alt: '画一只猫',
    }])
  })

  it('生图模式强制走 Responses image_generation tool 并按张数合并展示结果', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({
        output: [{ type: 'image_generation_call', result: 'ZmFrZS0x' }],
      }))
      .mockResolvedValueOnce(jsonResponse({
        output: [{ type: 'image_generation_call', result: 'ZmFrZS0y' }],
      }))
    vi.stubGlobal('fetch', fetchMock)

    const result = await generateWebConsoleImage({
      endpoint: 'https://api.example.com',
      apiKey: 'sk-test',
      model: 'gpt-5.4',
      prompt: '画两张海报',
      history: [],
      imageOptions: {
        size: '1024x1024',
        quality: 'high',
        background: 'transparent',
        outputFormat: 'webp',
        count: 2,
      },
    })

    expect(result.usedMode).toBe('responses')
    expect(result.images).toHaveLength(2)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      'https://api.example.com/v1/responses',
      'https://api.example.com/v1/responses',
    ])

    const [, firstResponsesInit] = fetchMock.mock.calls[0] as [string, RequestInit]
    const [, secondResponsesInit] = fetchMock.mock.calls[1] as [string, RequestInit]
    const expectedResponsesBody = {
      model: 'gpt-5.4',
      input: '画两张海报',
      tools: [{
        type: 'image_generation',
        model: 'gpt-image-2',
        size: '1024x1024',
        quality: 'high',
        background: 'transparent',
        output_format: 'webp',
      }],
      tool_choice: { type: 'image_generation' },
    }
    expect(JSON.parse(String(firstResponsesInit.body))).toEqual(expectedResponsesBody)
    expect(JSON.parse(String(secondResponsesInit.body))).toEqual(expectedResponsesBody)
  })
})
