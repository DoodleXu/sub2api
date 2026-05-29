import { describe, expect, it } from 'vitest'
import {
  applyRouteSEO,
  isRouteIndexable,
  resolveCustomPageSEO,
  resolveDocumentTitle,
  resolveLegalDocumentSEO,
  resolvePageDescription,
} from '@/router/title'

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

describe('resolveCustomPageSEO', () => {
  it('公开自定义页面使用菜单名称作为标题并允许索引', () => {
    const seo = resolveCustomPageSEO({
      id: 'docs',
      label: '开发文档',
      icon_svg: '',
      url: 'md:docs',
      visibility: 'user',
      sort_order: 0,
    }, 'My Site', 'Site subtitle')

    expect(seo.title).toBe('开发文档 - My Site')
    expect(seo.description).toBe('Site subtitle')
    expect(seo.indexable).toBe(true)
  })

  it('后台自定义页面保持 noindex', () => {
    const seo = resolveCustomPageSEO({
      id: 'ops',
      label: '内部运维',
      icon_svg: '',
      url: 'md:ops',
      visibility: 'admin',
      sort_order: 0,
    }, 'My Site', 'Site subtitle')

    expect(seo.title).toBe('内部运维 - My Site')
    expect(seo.indexable).toBe(false)
  })
})

describe('resolveLegalDocumentSEO', () => {
  it('条款文档使用文档标题作为 SEO 标题', () => {
    const seo = resolveLegalDocumentSEO({
      id: 'privacy',
      title: '隐私政策',
      content_md: '# 隐私政策',
    }, 'My Site', 'Site subtitle')

    expect(seo.title).toBe('隐私政策 - My Site')
    expect(seo.description).toBe('Site subtitle')
    expect(seo.indexable).toBe(true)
  })
})

describe('applyRouteSEO', () => {
  it('自定义公开页面可通过 indexable 覆盖进入索引范围', () => {
    document.head.innerHTML = `
      <meta name="description" content="">
      <meta name="robots" content="noindex, nofollow">
      <meta property="og:site_name" content="">
      <meta property="og:title" content="">
      <meta property="og:description" content="">
      <meta property="og:image" content="">
      <meta name="twitter:title" content="">
      <meta name="twitter:description" content="">
      <link rel="canonical" href="/">
    `

    applyRouteSEO({
      path: '/custom/docs',
      title: '开发文档 - My Site',
      description: 'Site subtitle',
      siteName: 'My Site',
      indexable: true,
    })

    expect(document.title).toBe('开发文档 - My Site')
    expect(document.head.querySelector<HTMLMetaElement>('meta[name="robots"]')?.content).toBe('index, follow')
    expect(document.head.querySelector<HTMLLinkElement>('link[rel="canonical"]')?.href).toBe('http://localhost:3000/custom/docs')
  })
})
