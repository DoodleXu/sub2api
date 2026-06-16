import { describe, expect, it } from 'vitest'

import en from '../locales/en'
import zh from '../locales/zh'

describe('quota reset locale messages', () => {
  it('uses Vue i18n named placeholders instead of template literals', () => {
    expect(zh.keys.resetQuotaConfirmMessage).toContain('{used}')
    expect(zh.keys.resetQuotaConfirmMessage).not.toContain('${used}')
    expect(en.keys.resetQuotaConfirmMessage).toContain('{used}')
    expect(en.keys.resetQuotaConfirmMessage).not.toContain('${used}')
  })
})
