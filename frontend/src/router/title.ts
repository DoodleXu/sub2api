import { i18n } from '@/i18n'
import type { CustomMenuItem, LoginAgreementDocument } from '@/types'

const DEFAULT_DESCRIPTION =
  'Sub2API is an AI API gateway for unified model access, account routing, usage billing, and API key management.'

/**
 * 统一生成页面标题，避免多处写入 document.title 产生覆盖冲突。
 * 优先使用 titleKey 通过 i18n 翻译，fallback 到静态 routeTitle。
 */
export function resolveDocumentTitle(routeTitle: unknown, siteName?: string, titleKey?: string): string {
  const normalizedSiteName = typeof siteName === 'string' && siteName.trim() ? siteName.trim() : 'Sub2API'

  if (typeof titleKey === 'string' && titleKey.trim()) {
    const translated = i18n.global.t(titleKey)
    if (translated && translated !== titleKey) {
      return `${translated} - ${normalizedSiteName}`
    }
  }

  if (typeof routeTitle === 'string' && routeTitle.trim()) {
    return `${routeTitle.trim()} - ${normalizedSiteName}`
  }

  return normalizedSiteName
}

export function resolvePageDescription(descriptionKey?: string, fallback?: string): string {
  if (typeof descriptionKey === 'string' && descriptionKey.trim()) {
    const translated = i18n.global.t(descriptionKey)
    if (translated && translated !== descriptionKey) {
      return translated
    }
  }

  if (typeof fallback === 'string' && fallback.trim()) {
    return fallback.trim()
  }

  return DEFAULT_DESCRIPTION
}

export function isRouteIndexable(path: string): boolean {
  const normalized = path.replace(/\/+$/, '')
  return normalized === '' || normalized === '/home' || normalized.startsWith('/legal/')
}

function normalizeSiteName(siteName?: string): string {
  return typeof siteName === 'string' && siteName.trim() ? siteName.trim() : 'Sub2API'
}

export function resolveCustomPageSEO(
  item: CustomMenuItem | null | undefined,
  siteName?: string,
  fallbackDescription?: string
): { title: string; description: string; indexable: boolean } {
  const normalizedSiteName = normalizeSiteName(siteName)
  const label = typeof item?.label === 'string' ? item.label.trim() : ''
  return {
    title: label ? `${label} - ${normalizedSiteName}` : resolveDocumentTitle('Custom Page', normalizedSiteName),
    description: resolvePageDescription(undefined, fallbackDescription),
    indexable: Boolean(label && item?.visibility !== 'admin'),
  }
}

export function resolveLegalDocumentSEO(
  document: LoginAgreementDocument | null | undefined,
  siteName?: string,
  fallbackDescription?: string
): { title: string; description: string; indexable: boolean } {
  const normalizedSiteName = normalizeSiteName(siteName)
  const title = typeof document?.title === 'string' ? document.title.trim() : ''
  return {
    title: title ? `${title} - ${normalizedSiteName}` : resolveDocumentTitle('Legal Document', normalizedSiteName),
    description: resolvePageDescription(undefined, fallbackDescription),
    indexable: true,
  }
}

function setMetaContent(selector: string, content: string): void {
  const element = document.head.querySelector<HTMLMetaElement>(selector)
  if (element) {
    element.content = content
  }
}

function setCanonical(href: string): void {
  const element = document.head.querySelector<HTMLLinkElement>('link[rel="canonical"]')
  if (element) {
    element.href = href
  }
}

export function applyRouteSEO(options: {
  path: string
  title: string
  description: string
  siteName: string
  image?: string
  indexable?: boolean
}): void {
  const { path, title, description, siteName } = options
  const image = options.image || '/logo.png'
  const canonical = `${window.location.origin}${path === '/' ? '/home' : path}`
  const robots = (options.indexable ?? isRouteIndexable(path)) ? 'index, follow' : 'noindex, nofollow'

  document.title = title
  setMetaContent('meta[name="description"]', description)
  setMetaContent('meta[name="robots"]', robots)
  setMetaContent('meta[property="og:site_name"]', siteName)
  setMetaContent('meta[property="og:title"]', title)
  setMetaContent('meta[property="og:description"]', description)
  setMetaContent('meta[property="og:image"]', image)
  setMetaContent('meta[name="twitter:title"]', title)
  setMetaContent('meta[name="twitter:description"]', description)
  setCanonical(canonical)
}
