import { describe, expect, it } from 'vitest'

import { mergeLocaleMessages } from '../index'
import legacyEn from '../locales/en'
import modularEn from '../locales/en/index'
import legacyZh from '../locales/zh'
import modularZh from '../locales/zh/index'

type Messages = Record<string, unknown>

function flatten(messages: Messages, prefix = '', output = new Map<string, unknown>()) {
  for (const [key, value] of Object.entries(messages)) {
    const path = prefix ? `${prefix}.${key}` : key
    if (value && typeof value === 'object' && !Array.isArray(value)) {
      flatten(value as Messages, path, output)
    } else {
      output.set(path, value)
    }
  }
  return output
}

describe('runtime locale coverage', () => {
  const en = flatten(mergeLocaleMessages(legacyEn, modularEn))
  const zh = flatten(mergeLocaleMessages(legacyZh, modularZh))

  it('keeps English and Chinese runtime keys symmetric', () => {
    expect([...en.keys()].filter(key => !zh.has(key))).toEqual([])
    expect([...zh.keys()].filter(key => !en.has(key))).toEqual([])
  })

  it('includes upstream billing translations used by the accounts table', () => {
    expect(en.get('admin.accounts.columns.upstreamBillingRate')).toBe('Upstream Declared Rate')
    expect(zh.get('admin.accounts.columns.upstreamBillingRate')).toBe('上游声明倍率')
    expect(en.get('admin.accounts.upstreamBilling.notProbed')).toBe('Not probed')
    expect(zh.get('admin.accounts.upstreamBilling.notProbed')).toBe('未探测')
    expect(en.get('admin.accounts.upstreamBilling.autoProbeSettings')).toBe('Automatic rate probing')
    expect(zh.get('admin.accounts.upstreamBilling.autoProbeSettings')).toBe('自动倍率探测')
    expect(en.get('admin.accounts.upstreamBilling.intervalMinutes')).toBe('Interval (minutes)')
    expect(zh.get('admin.accounts.upstreamBilling.intervalMinutes')).toBe('探测间隔（分钟）')
  })
})
