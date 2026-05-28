import { describe, expect, it } from 'vitest'
import { isRouteIndexable, resolveDocumentTitle, resolvePageDescription } from '@/router/title'

describe('resolveDocumentTitle', () => {
  it('路由存在标题时，使用“路由标题 - 站点名”格式', () => {
    expect(resolveDocumentTitle('Usage Records', 'My Site')).toBe('Usage Records - My Site')
  })

  it('路由无标题时，回退到站点名', () => {
    expect(resolveDocumentTitle(undefined, 'My Site')).toBe('My Site')
  })

  it('站点名为空时，回退默认站点名', () => {
    expect(resolveDocumentTitle('Dashboard', '')).toBe('Dashboard - Sub2API')
    expect(resolveDocumentTitle(undefined, '   ')).toBe('Sub2API')
  })

  it('站点名变更时仅影响后续路由标题计算', () => {
    const before = resolveDocumentTitle('Admin Dashboard', 'Alpha')
    const after = resolveDocumentTitle('Admin Dashboard', 'Beta')

    expect(before).toBe('Admin Dashboard - Alpha')
    expect(after).toBe('Admin Dashboard - Beta')
  })
})

describe('resolvePageDescription', () => {
  it('存在 fallback 时优先使用 fallback', () => {
    expect(resolvePageDescription(undefined, 'Custom site subtitle')).toBe('Custom site subtitle')
  })

  it('无描述时回退到默认 SEO 描述', () => {
    expect(resolvePageDescription()).toContain('Sub2API is an AI API gateway')
  })
})

describe('isRouteIndexable', () => {
  it('公开内容页允许索引', () => {
    expect(isRouteIndexable('/')).toBe(true)
    expect(isRouteIndexable('/home')).toBe(true)
    expect(isRouteIndexable('/legal/privacy')).toBe(true)
  })

  it('后台、登录和用户区默认不允许索引', () => {
    expect(isRouteIndexable('/login')).toBe(false)
    expect(isRouteIndexable('/dashboard')).toBe(false)
    expect(isRouteIndexable('/admin/dashboard')).toBe(false)
    expect(isRouteIndexable('/payment/result')).toBe(false)
  })
})
