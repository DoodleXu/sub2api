import { beforeEach, describe, expect, it } from 'vitest'
import { loadWebConsoleSessions, saveWebConsoleSessions } from '../storage'
import type { WebConsoleSession } from '../types'

function imageSession(): WebConsoleSession {
  return {
    id: 'session-image',
    title: '海报会话',
    mode: 'image',
    messages: [{
      id: 'message-image',
      role: 'assistant',
      content: '生图任务已提交，正在生成图片。',
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
          data_url: 'data:image/png;base64,c291cmNlLWltYWdl',
        }],
        maskImage: {
          name: 'mask.png',
          data_url: 'data:image/png;base64,bWFzay1pbWFnZQ',
        },
      },
      status: 'pending',
      created_at: '2026-06-21T00:00:00.000Z',
    }],
    created_at: '2026-06-21T00:00:00.000Z',
    updated_at: '2026-06-21T00:00:00.000Z',
  }
}

describe('web console storage', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('保存生图编辑会话时不持久化参考图和蒙版 data URL', () => {
    saveWebConsoleSessions([imageSession()])

    const raw = localStorage.getItem('sub2api-web-console-sessions-v1') || ''
    expect(raw).not.toContain('c291cmNlLWltYWdl')
    expect(raw).not.toContain('bWFzay1pbWFnZQ')

    const [restored] = loadWebConsoleSessions()
    const imageRequest = restored.messages[0].imageRequest
    expect(imageRequest?.mode).toBe('edit')
    expect(imageRequest?.referenceImages).toEqual([])
    expect(imageRequest?.maskImage).toBeNull()
  })
})
