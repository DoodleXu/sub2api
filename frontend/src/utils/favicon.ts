export function resolveFaviconType(logoUrl: string): string {
  const normalized = logoUrl.trim().toLowerCase()
  const path = normalized.split(/[?#]/, 1)[0]
  if (normalized.startsWith('data:image/svg+xml') || path.endsWith('.svg')) return 'image/svg+xml'
  if (
    normalized.startsWith('data:image/jpeg') ||
    normalized.startsWith('data:image/jpg') ||
    path.endsWith('.jpg') ||
    path.endsWith('.jpeg')
  ) {
    return 'image/jpeg'
  }
  if (normalized.startsWith('data:image/webp') || path.endsWith('.webp')) return 'image/webp'
  if (
    normalized.startsWith('data:image/x-icon') ||
    normalized.startsWith('data:image/vnd.microsoft.icon') ||
    path.endsWith('.ico')
  ) {
    return 'image/x-icon'
  }
  return 'image/png'
}

export function updateFavicon(logoUrl: string): void {
  document.head
    .querySelectorAll<HTMLLinkElement>('link[rel="icon"], link[rel="shortcut icon"]')
    .forEach((node) => node.remove())
  const link = document.createElement('link')
  link.rel = 'icon'
  link.type = resolveFaviconType(logoUrl)
  link.href = logoUrl
  document.head.appendChild(link)
}
