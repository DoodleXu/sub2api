import { beforeEach, describe, expect, it } from 'vitest'
import { resolveFaviconType, updateFavicon } from '@/utils/favicon'

describe('resolveFaviconType', () => {
  it.each([
    ['/logo.png', 'image/png'],
    ['https://cdn.example.com/logo.svg?v=2', 'image/svg+xml'],
    ['/logo.JPG#cache', 'image/jpeg'],
    ['/logo.webp', 'image/webp'],
    ['/favicon.ico', 'image/x-icon'],
    ['data:image/svg+xml;base64,PHN2Zy8+', 'image/svg+xml'],
    ['data:image/jpeg;base64,/9j/4AAQ', 'image/jpeg'],
  ])('resolves %s as %s', (href, expected) => {
    expect(resolveFaviconType(href)).toBe(expected)
  })
})

describe('updateFavicon', () => {
  beforeEach(() => {
    document.head.innerHTML = ''
  })

  function favicon() {
    return document.head.querySelector<HTMLLinkElement>('link[rel="icon"]')
  }

  it('replaces regular favicon links with the configured site logo', () => {
    document.head.innerHTML = `
      <link rel="icon" type="image/png" href="/logo.png">
      <link rel="shortcut icon" type="image/x-icon" href="/favicon.ico">
      <link rel="apple-touch-icon" href="/apple-touch-icon.png">
    `

    updateFavicon('https://cdn.example.com/brand.svg?v=2')

    expect(document.head.querySelectorAll<HTMLLinkElement>('link[rel="icon"]')).toHaveLength(1)
    expect(favicon()?.href).toBe('https://cdn.example.com/brand.svg?v=2')
    expect(favicon()?.type).toBe('image/svg+xml')
    expect(document.head.querySelector('link[rel="shortcut icon"]')).toBeNull()
    expect(document.head.querySelector('link[rel="apple-touch-icon"]')).not.toBeNull()
  })

  it('can restore the default favicon after a custom logo was cleared', () => {
    updateFavicon('https://cdn.example.com/brand.svg')
    updateFavicon('/logo.png')

    expect(document.head.querySelectorAll<HTMLLinkElement>('link[rel="icon"]')).toHaveLength(1)
    expect(favicon()?.getAttribute('href')).toBe('/logo.png')
    expect(favicon()?.type).toBe('image/png')
  })
})
