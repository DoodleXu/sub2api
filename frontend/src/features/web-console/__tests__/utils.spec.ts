import { describe, expect, it } from 'vitest'
import { isWebConsoleOpenAICompatibleEndpoint, webConsoleErrorMessage } from '../utils'

describe('web console utils', () => {
  it('只接受 OpenAI-compatible 端点', () => {
    expect(isWebConsoleOpenAICompatibleEndpoint('/')).toBe(true)
    expect(isWebConsoleOpenAICompatibleEndpoint('https://api.example.com/v1')).toBe(true)
    expect(isWebConsoleOpenAICompatibleEndpoint('https://api.example.com/v1beta')).toBe(false)
    expect(isWebConsoleOpenAICompatibleEndpoint('https://api.example.com/antigravity/v1')).toBe(false)
  })

  it('将额度耗尽错误转成中文提示', () => {
    expect(webConsoleErrorMessage(new Error('The quota has been exceeded.'))).toBe('当前额度已用尽，请切换 API Key 或稍后再试。')
    expect(webConsoleErrorMessage(new Error('insufficient_quota'))).toBe('当前额度已用尽，请切换 API Key 或稍后再试。')
  })
})
