import { describe, expect, it } from 'vitest'

import en from '../locales/en'
import zh from '../locales/zh'

describe('usage service tier locale keys', () => {
  it('contains zh labels for user usage cache stats and CSV headers', () => {
    expect(zh.usage.cacheHit).toBe('缓存命中')
    expect(zh.usage.cacheCreate).toBe('缓存创建')
    expect(zh.usage.cacheHitRate).toBe('缓存命中率')
    expect(zh.usage.csvHeaders.cacheReadTokens).toBe('缓存读取 Token')
    expect(zh.usage.csvHeaders.cacheCreationTokens).toBe('缓存创建 Token')
  })

  it('contains en labels for user usage cache stats and CSV headers', () => {
    expect(en.usage.cacheHit).toBe('Cache hit')
    expect(en.usage.cacheCreate).toBe('Cache create')
    expect(en.usage.cacheHitRate).toBe('Cache hit rate')
    expect(en.usage.csvHeaders.cacheReadTokens).toBe('Cache Read Tokens')
    expect(en.usage.csvHeaders.cacheCreationTokens).toBe('Cache Creation Tokens')
  })

  it('contains zh labels for service tier tooltip', () => {
    expect(zh.usage.serviceTier).toBe('服务档位')
    expect(zh.usage.serviceTierPriority).toBe('Fast')
    expect(zh.usage.serviceTierFlex).toBe('Flex')
    expect(zh.usage.serviceTierStandard).toBe('Standard')
  })

  it('contains en labels for service tier tooltip', () => {
    expect(en.usage.serviceTier).toBe('Service tier')
    expect(en.usage.serviceTierPriority).toBe('Fast')
    expect(en.usage.serviceTierFlex).toBe('Flex')
    expect(en.usage.serviceTierStandard).toBe('Standard')
  })
})
