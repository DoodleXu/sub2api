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

    const result = await sendWebConsoleChat(
      {
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
      },
      'auto',
    )

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

    await expect(sendWebConsoleChat(
      {
        endpoint: 'https://api.example.com/v1beta',
        apiKey: 'sk-test',
        model: 'gpt-5.4',
        prompt: '你好',
        history: [],
      },
      'responses',
    )).rejects.toThrow('网页工作台当前只支持 OpenAI-compatible /v1 端点')

    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('发送 Images 生图参数并保留默认模型兼容路径', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({
      data: [{ b64_json: 'ZmFrZS1pbWFnZQ==' }],
    }))
    vi.stubGlobal('fetch', fetchMock)

    const result = await generateWebConsoleImage(
      {
        endpoint: 'https://api.example.com',
        apiKey: 'sk-test',
        model: 'gpt-image-2',
        prompt: '画一张霓虹城市',
        history: [],
        imageOptions: {
          size: '1536x1024',
          quality: 'high',
          background: 'transparent',
          outputFormat: 'webp',
          count: 3,
        },
      },
      'images',
    )

    expect(result.images).toHaveLength(1)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://api.example.com/v1/images/generations')
    expect(init.headers).toEqual(expect.objectContaining({
      Authorization: 'Bearer sk-test',
      'Content-Type': 'application/json',
    }))
    expect(JSON.parse(String(init.body))).toEqual({
      prompt: '画一张霓虹城市',
      n: 3,
      response_format: 'b64_json',
      size: '1536x1024',
      quality: 'high',
      background: 'transparent',
      output_format: 'webp',
    })
  })

  it('限制张数范围并在非默认图片模型时显式传 model', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({
      data: [{ url: 'https://cdn.example.com/out.png' }],
    }))
    vi.stubGlobal('fetch', fetchMock)

    await generateWebConsoleImage(
      {
        endpoint: 'https://api.example.com/v1',
        apiKey: 'sk-test',
        model: 'gpt-image-1.5',
        prompt: '一只猫',
        history: [],
        imageOptions: {
          size: '',
          quality: '',
          background: '',
          outputFormat: 'png',
          count: 99,
        },
      },
      'images',
    )

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(JSON.parse(String(init.body))).toEqual({
      prompt: '一只猫',
      n: 4,
      response_format: 'b64_json',
      model: 'gpt-image-1.5',
    })
  })

  it('自动 fallback 到 Responses 生图时按张数发起请求并合并展示结果', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        error: { message: 'Images endpoint unavailable' },
      }), {
        status: 500,
        headers: { 'Content-Type': 'application/json' },
      }))
      .mockResolvedValueOnce(jsonResponse({
        output: [{ type: 'image_generation_call', result: 'ZmFrZS0x' }],
      }))
      .mockResolvedValueOnce(jsonResponse({
        output: [{ type: 'image_generation_call', result: 'ZmFrZS0y' }],
      }))
    vi.stubGlobal('fetch', fetchMock)

    const result = await generateWebConsoleImage(
      {
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
      },
      'auto',
    )

    expect(result.usedMode).toBe('responses')
    expect(result.fallbackUsed).toBe(true)
    expect(result.images).toHaveLength(2)
    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      'https://api.example.com/v1/images/generations',
      'https://api.example.com/v1/responses',
      'https://api.example.com/v1/responses',
    ])

    const [, firstResponsesInit] = fetchMock.mock.calls[1] as [string, RequestInit]
    const [, secondResponsesInit] = fetchMock.mock.calls[2] as [string, RequestInit]
    const expectedResponsesBody = {
      model: 'gpt-5.4',
      input: '画两张海报',
      tools: [{
        type: 'image_generation',
        size: '1024x1024',
        quality: 'high',
        background: 'transparent',
        output_format: 'webp',
      }],
    }
    expect(JSON.parse(String(firstResponsesInit.body))).toEqual(expectedResponsesBody)
    expect(JSON.parse(String(secondResponsesInit.body))).toEqual(expectedResponsesBody)
  })
})
