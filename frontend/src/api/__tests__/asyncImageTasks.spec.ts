import { afterEach, describe, expect, it, vi } from 'vitest'

import { create, get, taskAssets } from '../asyncImageTasks'

describe('asyncImageTasksAPI', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('submits generation jobs to the upstream async image endpoint', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      id: 'imgtask_1', task_id: 'imgtask_1', status: 'processing',
    }), { status: 202, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)

    const { task } = await create({
      endpoint: 'https://api.example.com', api_key: 'sk-test', model: 'gpt-image-2', prompt: 'cat',
      options: { size: '1024x1024', quality: 'high', background: '', outputFormat: 'png', count: 1 },
    })

    expect(task.task_id).toBe('imgtask_1')
    expect(fetchMock).toHaveBeenCalledWith('https://api.example.com/v1/images/generations/async', expect.objectContaining({
      method: 'POST', headers: expect.objectContaining({ Authorization: 'Bearer sk-test' }),
    }))
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(JSON.parse(String(init.body))).toEqual(expect.objectContaining({ size: '1024x1024' }))
  })

  it('maps console ratios to sizes supported by the upstream Images API', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      id: 'imgtask_2', task_id: 'imgtask_2', status: 'processing',
    }), { status: 202, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)

    await create({
      endpoint: 'https://api.example.com/v1', api_key: 'sk-test', model: 'gpt-image-2', prompt: 'poster',
      options: { size: '', ratio: '16:9', quality: '', background: '', outputFormat: 'png', count: 1 },
    })

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(JSON.parse(String(init.body))).toEqual(expect.objectContaining({ size: '1536x1024' }))
  })

  it('polls with the same key and maps stored result URLs to console assets', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      id: 'imgtask_1', task_id: 'imgtask_1', status: 'completed',
      result: { data: [{ url: 'https://bucket.example/images/imgtask_1-0.png' }] },
    }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)

    const task = await get('https://api.example.com/v1', 'sk-test', 'imgtask_1')
    expect(fetchMock).toHaveBeenCalledWith('https://api.example.com/v1/images/tasks/imgtask_1', expect.objectContaining({
      headers: { Authorization: 'Bearer sk-test' }, cache: 'no-store',
    }))
    expect(taskAssets(task)).toEqual([expect.objectContaining({
      id: 'imgtask_1-0', url: 'https://bucket.example/images/imgtask_1-0.png',
    })])
  })
})
