import { createI18n } from 'vue-i18n'

type LocaleCode = 'en' | 'zh'

type LocaleMessages = Record<string, any>

const LOCALE_KEY = 'sub2api_locale'
const DEFAULT_LOCALE: LocaleCode = 'en'

const localeLoaders: Record<LocaleCode, () => Promise<{ default: LocaleMessages }>> = {
  en: () => import('./locales/en'),
  zh: () => import('./locales/zh')
}

function isLocaleCode(value: string): value is LocaleCode {
  return value === 'en' || value === 'zh'
}

function getDefaultLocale(): LocaleCode {
  const saved = localStorage.getItem(LOCALE_KEY)
  if (saved && isLocaleCode(saved)) {
    return saved
  }

  const browserLang = navigator.language.toLowerCase()
  if (browserLang.startsWith('zh')) {
    return 'zh'
  }

  return DEFAULT_LOCALE
}

export const i18n = createI18n({
  legacy: false,
  locale: getDefaultLocale(),
  fallbackLocale: DEFAULT_LOCALE,
  messages: {},
  // 禁用 HTML 消息警告 - 引导步骤使用富文本内容（driver.js 支持 HTML）
  // 这些内容是内部定义的，不存在 XSS 风险
  warnHtmlMessage: false
})

const loadedLocales = new Set<LocaleCode>()

export async function loadLocaleMessages(locale: LocaleCode): Promise<void> {
  if (loadedLocales.has(locale)) {
    return
  }

  const loader = localeLoaders[locale]
  const module = await loader()
  i18n.global.setLocaleMessage(locale, module.default)
  loadedLocales.add(locale)
}

export async function initI18n(): Promise<void> {
  const current = getLocale()
  await loadLocaleMessages(current)
  document.documentElement.setAttribute('lang', current)
}

export async function setLocale(locale: string): Promise<void> {
  if (!isLocaleCode(locale)) {
    return
  }

  await loadLocaleMessages(locale)
  i18n.global.locale.value = locale
  localStorage.setItem(LOCALE_KEY, locale)
  document.documentElement.setAttribute('lang', locale)

  // 同步更新浏览器页签标题和 SEO 元信息，使其跟随语言切换
  const {
    applyRouteSEO,
    resolveCustomPageSEO,
    resolveDocumentTitle,
    resolveLegalDocumentSEO,
    resolvePageDescription,
  } = await import('@/router/title')
  const { default: router } = await import('@/router')
  const { useAppStore } = await import('@/stores/app')
  const route = router.currentRoute.value
  const appStore = useAppStore()
  const siteName = appStore.siteName || 'Sub2API'
  const siteSubtitle = appStore.cachedPublicSettings?.site_subtitle
  let title = resolveDocumentTitle(route.meta.title, appStore.siteName, route.meta.titleKey as string)
  let description = resolvePageDescription(route.meta.descriptionKey as string | undefined, siteSubtitle)
  let indexable: boolean | undefined

  if (route.name === 'CustomPage') {
    const id = route.params.id as string
    const item = appStore.cachedPublicSettings?.custom_menu_items?.find((menuItem) => menuItem.id === id)
    const seo = resolveCustomPageSEO(item, siteName, siteSubtitle)
    title = seo.title
    description = seo.description
    indexable = seo.indexable
  } else if (route.name === 'LegalDocument') {
    const id = route.params.documentId as string
    const document = appStore.cachedPublicSettings?.login_agreement_documents?.find((doc) => doc.id === id)
    const seo = resolveLegalDocumentSEO(document, siteName, siteSubtitle)
    title = seo.title
    description = seo.description
    indexable = seo.indexable
  }

  applyRouteSEO({
    path: route.path,
    title,
    description,
    siteName,
    image: appStore.siteLogo || '/logo.png',
    indexable,
  })
}

export function getLocale(): LocaleCode {
  const current = i18n.global.locale.value
  return isLocaleCode(current) ? current : DEFAULT_LOCALE
}

export const availableLocales = [
  { code: 'en', name: 'English', flag: '🇺🇸' },
  { code: 'zh', name: '中文', flag: '🇨🇳' }
] as const

export default i18n
